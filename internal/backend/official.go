package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	treeclient "github.com/apache/iotdb-client-go/client"
	"github.com/apache/iotdb-client-go/v2/client"
)

// Official implements Backend with official v2 write/Table pools and a v1 TreeModel query pool.
type Official struct {
	config        Config
	treePool      *client.SessionPool
	treeQueryPool *treeclient.SessionPool
	tablePool     *client.TableSessionPool

	lifecycleMu sync.Mutex
	inFlight    sync.WaitGroup
	closed      atomic.Bool
	closeOnce   sync.Once
}

// NewOfficial creates the official pools required by the configured model compatibility boundary.
func NewOfficial(config Config) (*Official, error) {
	if len(config.NodeURLs) == 0 {
		return nil, errors.New("iotdb: at least one NodeURL is required")
	}
	if config.Mode != TreeModel && config.Mode != TableModel {
		return nil, fmt.Errorf("iotdb: unsupported model mode %d", config.Mode)
	}

	poolConfig := &client.PoolConfig{
		NodeUrls:        append([]string(nil), config.NodeURLs...),
		UserName:        config.Username,
		Password:        config.Password,
		FetchSize:       config.FetchSize,
		TimeZone:        config.TimeZone,
		ConnectRetryMax: config.ConnectRetryMax,
	}

	connectionTimeout := durationMilliseconds(config.ConnectTimeout)
	acquireTimeout := durationMilliseconds(config.AcquireTimeout)
	official := &Official{config: config}
	if config.Mode == TreeModel {
		pool := client.NewSessionPool(poolConfig, config.PoolSize, connectionTimeout, acquireTimeout, config.EnableRPCCompression)
		official.treePool = &pool
		queryPool := treeclient.NewSessionPool(&treeclient.PoolConfig{
			NodeUrls:        append([]string(nil), config.NodeURLs...),
			UserName:        config.Username,
			Password:        config.Password,
			FetchSize:       config.FetchSize,
			TimeZone:        config.TimeZone,
			ConnectRetryMax: config.ConnectRetryMax,
		}, config.PoolSize, connectionTimeout, acquireTimeout, config.EnableRPCCompression)
		official.treeQueryPool = &queryPool
		return official, nil
	}

	// Database is intentionally selected after checkout. This lets AutoMigrate
	// create a missing table-model database while retaining a single pool.
	pool := client.NewTableSessionPool(poolConfig, config.PoolSize, connectionTimeout, acquireTimeout, config.EnableRPCCompression)
	official.tablePool = &pool
	return official, nil
}

// Mode reports the configured server model.
func (o *Official) Mode() ModelMode {
	return o.config.Mode
}

// Ping verifies that a session can execute a small server-side query.
func (o *Official) Ping(ctx context.Context) error {
	query := "SHOW VERSION"
	if o.config.Mode == TableModel {
		query = "SHOW DATABASES"
	}
	result, err := o.Query(ctx, query)
	if err != nil {
		return fmt.Errorf("iotdb ping: %w", err)
	}
	return result.Close()
}

// Exec executes one non-query statement through the selected official pool.
func (o *Official) Exec(ctx context.Context, statement string) error {
	if err := o.begin(ctx); err != nil {
		return err
	}
	defer o.end()

	if o.config.Mode == TreeModel {
		session, err := o.treePool.GetSession()
		if err != nil {
			return fmt.Errorf("acquire TreeModel session: %w", err)
		}
		defer o.treePool.PutBack(session)
		if err := session.ExecuteNonQueryStatement(statement); err != nil {
			return fmt.Errorf("execute TreeModel statement: %w", err)
		}
		return contextResult(ctx)
	}

	session, err := o.tablePool.GetSession()
	if err != nil {
		return fmt.Errorf("acquire TableModel session: %w", err)
	}
	defer session.Close()
	if needsTableDatabase(statement) {
		if err := o.useTableDatabase(session); err != nil {
			return err
		}
	}
	if err := session.ExecuteNonQueryStatement(statement); err != nil {
		return fmt.Errorf("execute TableModel statement: %w", err)
	}
	return contextResult(ctx)
}

// Query executes one query and keeps its official session checked out until Close.
func (o *Official) Query(ctx context.Context, statement string) (ResultSet, error) {
	if err := o.begin(ctx); err != nil {
		return nil, err
	}

	timeout := o.queryTimeout(ctx)
	if o.config.Mode == TreeModel {
		session, err := o.treeQueryPool.GetSession()
		if err != nil {
			o.end()
			return nil, fmt.Errorf("acquire TreeModel session: %w", err)
		}
		dataSet, err := session.ExecuteQueryStatement(statement, &timeout)
		if err != nil {
			o.treeQueryPool.PutBack(session)
			o.end()
			return nil, fmt.Errorf("query TreeModel: %w", err)
		}
		return newTreeV1ResultSet(dataSet, func() {
			o.treeQueryPool.PutBack(session)
			o.end()
		}), nil
	}

	session, err := o.tablePool.GetSession()
	if err != nil {
		o.end()
		return nil, fmt.Errorf("acquire TableModel session: %w", err)
	}
	if err := o.useTableDatabase(session); err != nil {
		_ = session.Close()
		o.end()
		return nil, err
	}
	dataSet, err := session.ExecuteQueryStatement(statement, &timeout)
	if err != nil {
		_ = session.Close()
		o.end()
		return nil, fmt.Errorf("query TableModel: %w", err)
	}
	return newOfficialResultSet(dataSet, func() {
		_ = session.Close()
		o.end()
	}), nil
}

// InsertTablets inserts prebuilt official tablets without SQL row fallback.
func (o *Official) InsertTablets(ctx context.Context, tablets []*client.Tablet, aligned bool) error {
	if len(tablets) == 0 {
		return nil
	}
	if err := o.begin(ctx); err != nil {
		return err
	}
	defer o.end()

	if o.config.Mode == TreeModel {
		return o.insertTreeTablets(ctx, tablets, aligned)
	}
	return o.insertRelationalTablets(ctx, tablets)
}

// InspectTreeDevice returns existing alignment and measurement type metadata.
func (o *Official) InspectTreeDevice(ctx context.Context, device string) (TreeDeviceSchema, error) {
	if o.config.Mode != TreeModel {
		return TreeDeviceSchema{}, errors.New("iotdb: tree schema inspection requires TreeModel")
	}
	if err := o.begin(ctx); err != nil {
		return TreeDeviceSchema{}, err
	}
	defer o.end()

	session, err := o.treePool.GetSession()
	if err != nil {
		return TreeDeviceSchema{}, fmt.Errorf("acquire TreeModel session: %w", err)
	}
	defer o.treePool.PutBack(session)
	return o.inspectTreeDeviceWithSession(ctx, &session, device)
}

// EnsureTreeSchema creates only missing measurements and rejects incompatible schema.
func (o *Official) EnsureTreeSchema(ctx context.Context, device string, measurements []Measurement, aligned bool) error {
	if o.config.Mode != TreeModel {
		return errors.New("iotdb: tree schema migration requires TreeModel")
	}
	if len(measurements) == 0 {
		return errors.New("iotdb: no TreeModel measurements to migrate")
	}
	if err := o.begin(ctx); err != nil {
		return err
	}
	defer o.end()

	session, err := o.treePool.GetSession()
	if err != nil {
		return fmt.Errorf("acquire TreeModel session: %w", err)
	}
	defer o.treePool.PutBack(session)

	current, err := o.inspectTreeDeviceWithSession(ctx, &session, device)
	if err != nil {
		return err
	}
	missing, err := validateTreeSchema(current, measurements, aligned)
	if err != nil || len(missing) == 0 {
		return err
	}
	if err := createTreeMeasurements(&session, device, missing, aligned); err == nil {
		return contextResult(ctx)
	} else {
		// A concurrent migrator may have created the same measurements. Re-read
		// before surfacing the create error so AutoMigrate stays idempotent.
		after, inspectErr := o.inspectTreeDeviceWithSession(ctx, &session, device)
		if inspectErr == nil {
			if remaining, validateErr := validateTreeSchema(after, measurements, aligned); validateErr == nil && len(remaining) == 0 {
				return nil
			}
		}
		return fmt.Errorf("create TreeModel schema for %s: %w", device, err)
	}
}

// Close waits for checked-out result sets and then closes all owned official pools.
func (o *Official) Close() error {
	o.closeOnce.Do(func() {
		o.lifecycleMu.Lock()
		o.closed.Store(true)
		o.lifecycleMu.Unlock()
		o.inFlight.Wait()
		if o.treePool != nil {
			o.treePool.Close()
		}
		if o.treeQueryPool != nil {
			o.treeQueryPool.Close()
		}
		if o.tablePool != nil {
			o.tablePool.Close()
		}
	})
	return nil
}

// begin reserves lifecycle ownership for one operation.
func (o *Official) begin(ctx context.Context) error {
	if err := contextResult(ctx); err != nil {
		return err
	}
	o.lifecycleMu.Lock()
	defer o.lifecycleMu.Unlock()
	if o.closed.Load() {
		return ErrClosed
	}
	o.inFlight.Add(1)
	return nil
}

// end releases lifecycle ownership for one operation.
func (o *Official) end() {
	o.inFlight.Done()
}

// queryTimeout resolves the smallest configured or context deadline timeout.
func (o *Official) queryTimeout(ctx context.Context) int64 {
	timeout := o.config.QueryTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if timeout <= 0 || remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return int64(timeout / time.Millisecond)
}

// useTableDatabase selects the configured database on a checked-out table session.
func (o *Official) useTableDatabase(session client.ITableSession) error {
	if o.config.Database == "" {
		return nil
	}
	if err := session.ExecuteNonQueryStatement("USE " + o.config.Database); err != nil {
		return fmt.Errorf("select TableModel database %s: %w", o.config.Database, err)
	}
	return nil
}

// insertTreeTablets uses the official single- or multi-device Tablet API.
func (o *Official) insertTreeTablets(ctx context.Context, tablets []*client.Tablet, aligned bool) error {
	session, err := o.treePool.GetSession()
	if err != nil {
		return fmt.Errorf("acquire TreeModel session: %w", err)
	}
	defer o.treePool.PutBack(session)
	if err := contextResult(ctx); err != nil {
		return err
	}
	if len(tablets) == 1 {
		if aligned {
			err = session.InsertAlignedTablet(tablets[0], true)
		} else {
			err = session.InsertTablet(tablets[0], true)
		}
	} else if aligned {
		err = session.InsertAlignedTablets(tablets, true)
	} else {
		err = session.InsertTablets(tablets, true)
	}
	if err != nil {
		return fmt.Errorf("insert %d TreeModel tablet(s): %w", len(tablets), err)
	}
	return contextResult(ctx)
}

// insertRelationalTablets executes bounded concurrent inserts for independent tables.
func (o *Official) insertRelationalTablets(ctx context.Context, tablets []*client.Tablet) error {
	workerCount := o.config.TableInsertConcurrency
	if workerCount <= 0 {
		workerCount = 1
	}
	if workerCount > len(tablets) {
		workerCount = len(tablets)
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan *client.Tablet)
	errCh := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer workers.Done()
			for tablet := range jobs {
				if err := o.insertRelationalTablet(workCtx, tablet); err != nil {
					select {
					case errCh <- err:
						cancel()
					default:
					}
					return
				}
			}
		}()
	}

sendLoop:
	for _, tablet := range tablets {
		select {
		case jobs <- tablet:
		case <-workCtx.Done():
			break sendLoop
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return contextResult(ctx)
	}
}

// insertRelationalTablet inserts one official relational Tablet.
func (o *Official) insertRelationalTablet(ctx context.Context, tablet *client.Tablet) error {
	if err := contextResult(ctx); err != nil {
		return err
	}
	session, err := o.tablePool.GetSession()
	if err != nil {
		return fmt.Errorf("acquire TableModel session: %w", err)
	}
	defer session.Close()
	if err := o.useTableDatabase(session); err != nil {
		return err
	}
	if err := session.Insert(tablet); err != nil {
		return fmt.Errorf("insert TableModel tablet with %d row(s): %w", tablet.RowSize, err)
	}
	return contextResult(ctx)
}

// inspectTreeDeviceWithSession reads device alignment and measurement types.
func (o *Official) inspectTreeDeviceWithSession(ctx context.Context, session *client.Session, device string) (TreeDeviceSchema, error) {
	result := TreeDeviceSchema{Measurements: make(map[string]client.TSDataType)}
	timeout := o.queryTimeout(ctx)
	devices, err := session.ExecuteQueryStatement("SHOW DEVICES "+device, &timeout)
	if err != nil {
		return result, fmt.Errorf("inspect TreeModel device %s: %w", device, err)
	}
	if err := readDeviceMetadata(devices, &result); err != nil {
		return result, err
	}

	series, err := session.ExecuteQueryStatement("SHOW TIMESERIES "+device+".*", &timeout)
	if err != nil {
		return result, fmt.Errorf("inspect TreeModel measurements for %s: %w", device, err)
	}
	if err := readTimeseriesMetadata(series, device, &result); err != nil {
		return result, err
	}
	return result, contextResult(ctx)
}

// readDeviceMetadata decodes SHOW DEVICES without relying on column order.
func readDeviceMetadata(dataSet *client.SessionDataSet, target *TreeDeviceSchema) error {
	defer dataSet.Close()
	for {
		next, err := dataSet.Next()
		if err != nil {
			return fmt.Errorf("read SHOW DEVICES: %w", err)
		}
		if !next {
			return nil
		}
		target.Exists = true
		if name := findColumn(dataSet.GetColumnNames(), "IsAligned"); name != "" {
			rawValue, valueErr := dataSet.GetString(name)
			if valueErr != nil {
				return fmt.Errorf("read IsAligned: %w", valueErr)
			}
			value, valueErr := parseMetadataBoolean(name, rawValue)
			if valueErr != nil {
				return valueErr
			}
			target.Aligned = value
		}
	}
}

// parseMetadataBoolean normalizes metadata returned as BOOLEAN or TEXT by different IoTDB versions.
func parseMetadataBoolean(column, rawValue string) (bool, error) {
	value, err := strconv.ParseBool(strings.TrimSpace(rawValue))
	if err != nil {
		return false, fmt.Errorf("parse metadata column %s value %q as boolean: %w", column, rawValue, err)
	}
	return value, nil
}

// readTimeseriesMetadata decodes SHOW TIMESERIES paths and data types.
func readTimeseriesMetadata(dataSet *client.SessionDataSet, device string, target *TreeDeviceSchema) error {
	defer dataSet.Close()
	pathColumn := findColumn(dataSet.GetColumnNames(), "Timeseries")
	typeColumn := findColumn(dataSet.GetColumnNames(), "DataType")
	if pathColumn == "" || typeColumn == "" {
		return fmt.Errorf("SHOW TIMESERIES metadata columns not found: %v", dataSet.GetColumnNames())
	}
	for {
		next, err := dataSet.Next()
		if err != nil {
			return fmt.Errorf("read SHOW TIMESERIES: %w", err)
		}
		if !next {
			return nil
		}
		path, err := dataSet.GetString(pathColumn)
		if err != nil {
			return fmt.Errorf("read timeseries path: %w", err)
		}
		dataType, err := dataSet.GetString(typeColumn)
		if err != nil {
			return fmt.Errorf("read timeseries type: %w", err)
		}
		parsed, err := client.GetDataTypeByStr(strings.ToUpper(dataType))
		if err != nil {
			return fmt.Errorf("parse type for %s: %w", path, err)
		}
		measurement := strings.TrimPrefix(path, device+".")
		target.Measurements[measurement] = parsed
		target.Exists = true
	}
}

// validateTreeSchema returns missing measurements after checking immutable traits.
func validateTreeSchema(current TreeDeviceSchema, requested []Measurement, aligned bool) ([]Measurement, error) {
	if current.Exists && current.Aligned != aligned {
		return nil, fmt.Errorf("iotdb: device alignment conflict: server=%t requested=%t", current.Aligned, aligned)
	}
	missing := make([]Measurement, 0, len(requested))
	for _, measurement := range requested {
		serverType, ok := current.Measurements[measurement.Name]
		if !ok {
			missing = append(missing, measurement)
			continue
		}
		if serverType != measurement.DataType {
			return nil, fmt.Errorf("iotdb: measurement %s type conflict: server=%v requested=%v", measurement.Name, serverType, measurement.DataType)
		}
	}
	return missing, nil
}

// createTreeMeasurements invokes official schema APIs for one device.
func createTreeMeasurements(session *client.Session, device string, measurements []Measurement, aligned bool) error {
	names := make([]string, len(measurements))
	types := make([]client.TSDataType, len(measurements))
	encodings := make([]client.TSEncoding, len(measurements))
	compressions := make([]client.TSCompressionType, len(measurements))
	paths := make([]string, len(measurements))
	for i, measurement := range measurements {
		names[i] = measurement.Name
		types[i] = measurement.DataType
		encodings[i] = measurement.Encoding
		compressions[i] = measurement.Compression
		paths[i] = device + "." + measurement.Name
	}
	if aligned {
		return session.CreateAlignedTimeseries(device, names, types, encodings, compressions, nil)
	}
	return session.CreateMultiTimeseries(paths, types, encodings, compressions)
}

// findColumn performs a case-insensitive metadata column lookup.
func findColumn(columns []string, wanted string) string {
	for _, column := range columns {
		if strings.EqualFold(column, wanted) {
			return column
		}
	}
	return ""
}

// needsTableDatabase identifies statements that can run before USE database.
func needsTableDatabase(statement string) bool {
	upper := strings.ToUpper(strings.TrimSpace(statement))
	return !(strings.HasPrefix(upper, "CREATE DATABASE") ||
		strings.HasPrefix(upper, "DROP DATABASE") ||
		strings.HasPrefix(upper, "SHOW DATABASES") ||
		strings.HasPrefix(upper, "USE "))
}

// durationMilliseconds safely converts client timeout settings to int.
func durationMilliseconds(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int(value / time.Millisecond)
}

// contextResult exposes cancellation before or after non-context-aware client calls.
func contextResult(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// officialResultSet owns a SessionDataSet and its checked-out session release.
type officialResultSet struct {
	dataSet  *client.SessionDataSet
	release  func()
	closeErr error
	once     sync.Once
}

// newOfficialResultSet creates a result set with exactly-once release semantics.
func newOfficialResultSet(dataSet *client.SessionDataSet, release func()) *officialResultSet {
	return &officialResultSet{dataSet: dataSet, release: release}
}

// ColumnNames returns server-reported column labels.
func (r *officialResultSet) ColumnNames() []string {
	return append([]string(nil), r.dataSet.GetColumnNames()...)
}

// ColumnTypes returns server-reported IoTDB data types.
func (r *officialResultSet) ColumnTypes() []string {
	return append([]string(nil), r.dataSet.GetColumnTypes()...)
}

// Next advances to the next row.
func (r *officialResultSet) Next() (bool, error) {
	return r.dataSet.Next()
}

// Value returns a database/sql-compatible value at a zero-based index.
func (r *officialResultSet) Value(index int) (any, error) {
	serverIndex := int32(index + 1)
	isNull, err := r.dataSet.IsNullByIndex(serverIndex)
	if err != nil {
		return nil, err
	}
	if isNull {
		return nil, nil
	}
	return r.dataSet.GetObjectByIndex(serverIndex)
}

// Close closes the server result and returns its session exactly once.
func (r *officialResultSet) Close() error {
	r.once.Do(func() {
		r.closeErr = r.dataSet.Close()
		r.release()
	})
	return r.closeErr
}

// treeV1ResultSet reads IoTDB 1.3.1 TreeModel blocks through the matching official client.
type treeV1ResultSet struct {
	dataSet  *treeclient.SessionDataSet
	release  func()
	closeErr error
	once     sync.Once
}

// newTreeV1ResultSet creates a result set that releases its TreeModel session exactly once.
func newTreeV1ResultSet(dataSet *treeclient.SessionDataSet, release func()) *treeV1ResultSet {
	return &treeV1ResultSet{dataSet: dataSet, release: release}
}

// ColumnNames returns server-reported TreeModel labels.
func (r *treeV1ResultSet) ColumnNames() []string {
	return append([]string(nil), r.dataSet.GetColumnNames()...)
}

// ColumnTypes returns server-reported TreeModel types.
func (r *treeV1ResultSet) ColumnTypes() []string {
	return append([]string(nil), r.dataSet.GetColumnTypes()...)
}

// Next advances to the next TreeModel row.
func (r *treeV1ResultSet) Next() (bool, error) {
	next, err := r.dataSet.Next()
	if isTreeResultEOF(err) {
		return false, nil
	}
	return next, err
}

// Value converts official v1 values to database/sql-compatible values.
func (r *treeV1ResultSet) Value(index int) (any, error) {
	serverIndex := int32(index + 1)
	isNull, err := r.dataSet.IsNullByIndex(serverIndex)
	if err != nil {
		return nil, err
	}
	if isNull {
		return nil, nil
	}
	value, err := r.dataSet.GetObjectByIndex(serverIndex)
	if err != nil {
		return nil, err
	}
	binary, ok := value.(*treeclient.Binary)
	if !ok {
		return value, nil
	}
	dataType := treeColumnType(r.dataSet.GetColumnTypes(), index)
	if dataType == "BLOB" {
		return binary.GetValues(), nil
	}
	if len(binary.GetValues()) == 0 {
		return nil, nil
	}
	return binary.GetStringValue(), nil
}

// Close closes the official v1 result and returns its session exactly once.
func (r *treeV1ResultSet) Close() error {
	r.once.Do(func() {
		r.closeErr = r.dataSet.Close()
		if isTreeResultEOF(r.closeErr) {
			r.closeErr = nil
		}
		r.release()
	})
	return r.closeErr
}

// isTreeResultEOF handles both Go and Thrift EOF values returned after result completion.
func isTreeResultEOF(err error) bool {
	return errors.Is(err, io.EOF) || (err != nil && strings.EqualFold(strings.TrimSpace(err.Error()), "EOF"))
}

// treeColumnType returns a normalized column type when metadata contains the requested index.
func treeColumnType(columnTypes []string, index int) string {
	if index < 0 || index >= len(columnTypes) {
		return ""
	}
	return strings.ToUpper(columnTypes[index])
}

var _ Backend = (*Official)(nil)
var _ ResultSet = (*officialResultSet)(nil)
var _ ResultSet = (*treeV1ResultSet)(nil)
