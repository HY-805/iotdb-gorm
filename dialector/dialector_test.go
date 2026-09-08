package dialector

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HY-805/iotdb-gorm/internal/backend"
	"github.com/apache/iotdb-client-go/v2/client"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

type treeTelemetry struct {
	Time       time.Time `gorm:"column:time" iotdb:"time"`
	DevicePath string    `gorm:"column:device_path" iotdb:"device"`
	Temp       float64   `gorm:"column:temp"`
	Count      int64     `gorm:"column:count"`
	Note       *string   `gorm:"column:note"`
}

type tableTelemetry struct {
	Time     time.Time `gorm:"column:time" iotdb:"time"`
	Region   string    `gorm:"column:region" iotdb:"tag"`
	DeviceID string    `gorm:"column:device_id" iotdb:"tag"`
	Temp     float64   `gorm:"column:temp" iotdb:"field"`
}

type noopConnPool struct{}

// PrepareContext implements the dry-run GORM connection contract.
func (noopConnPool) PrepareContext(context.Context, string) (*sql.Stmt, error) { return nil, nil }

// ExecContext implements the dry-run GORM connection contract.
func (noopConnPool) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

// QueryContext implements the dry-run GORM connection contract.
func (noopConnPool) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, nil
}

// QueryRowContext implements the dry-run GORM connection contract.
func (noopConnPool) QueryRowContext(context.Context, string, ...interface{}) *sql.Row { return nil }

type recordingConnPool struct {
	noopConnPool
	query string
}

// QueryContext records the statement so Raw callback behavior can be asserted without a live server.
func (c *recordingConnPool) QueryContext(_ context.Context, query string, _ ...interface{}) (*sql.Rows, error) {
	c.query = query
	return nil, nil
}

type mockBackend struct {
	mode         backend.ModelMode
	tablets      []*client.Tablet
	aligned      bool
	ensureDevice string
	ensureSchema []backend.Measurement
	ensureAlign  bool
	closed       bool
}

// Mode returns the configured mock mode.
func (m *mockBackend) Mode() backend.ModelMode { return m.mode }

// Ping returns success without network access.
func (m *mockBackend) Ping(context.Context) error { return nil }

// Exec records no statements because these tests focus on Tablet paths.
func (m *mockBackend) Exec(context.Context, string) error { return nil }

// Query is intentionally unsupported by the write-path mock.
func (m *mockBackend) Query(context.Context, string) (backend.ResultSet, error) {
	return nil, errors.New("unexpected query")
}

// InsertTablets captures official Tablets for assertions.
func (m *mockBackend) InsertTablets(_ context.Context, tablets []*client.Tablet, aligned bool) error {
	m.tablets = append(m.tablets, tablets...)
	m.aligned = aligned
	return nil
}

// InspectTreeDevice reports an absent device.
func (m *mockBackend) InspectTreeDevice(context.Context, string) (backend.TreeDeviceSchema, error) {
	return backend.TreeDeviceSchema{Measurements: map[string]client.TSDataType{}}, nil
}

// EnsureTreeSchema captures non-destructive migration input.
func (m *mockBackend) EnsureTreeSchema(_ context.Context, device string, schema []backend.Measurement, aligned bool) error {
	m.ensureDevice = device
	m.ensureSchema = append([]backend.Measurement(nil), schema...)
	m.ensureAlign = aligned
	return nil
}

// Close marks the mock as closed.
func (m *mockBackend) Close() error {
	m.closed = true
	return nil
}

// TestResolveConfigDefaults verifies TreeModel and aligned defaults.
func TestResolveConfigDefaults(t *testing.T) {
	config, err := resolveConfig(Config{NodeURLs: []string{"127.0.0.1:6667"}, Database: "root.datacenter_compatible"})
	if err != nil {
		t.Fatal(err)
	}
	if config.ModelMode != TreeModel || !config.AlignedValue {
		t.Fatalf("expected default TreeModel aligned=true, got mode=%d aligned=%t", config.ModelMode, config.AlignedValue)
	}
	if config.BatchSize != 1000 || config.TimePrecision != Milliseconds {
		t.Fatalf("unexpected defaults: batch=%d precision=%d", config.BatchSize, config.TimePrecision)
	}
}

// TestParseTableDSN verifies fixed table-model DSN parsing.
func TestParseTableDSN(t *testing.T) {
	config, err := resolveConfig(Config{DSN: "iotdb://user:pass@127.0.0.1:6667/iotdb_test?model=table&batch_size=200"})
	if err != nil {
		t.Fatal(err)
	}
	if config.ModelMode != TableModel || config.Database != "iotdb_test" || config.Username != "user" || config.Password != "pass" {
		t.Fatalf("unexpected parsed config: %+v", config.Config)
	}
	if config.BatchSize != 200 {
		t.Fatalf("expected batch size 200, got %d", config.BatchSize)
	}
}

// TestDataTypeOf verifies pinned GORM schema mapping.
func TestDataTypeOf(t *testing.T) {
	var cache sync.Map
	s, err := schema.Parse(&treeTelemetry{}, &cache, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	d := &Dialector{}
	if got := d.DataTypeOf(s.LookUpField("Temp")); got != "DOUBLE" {
		t.Fatalf("expected DOUBLE, got %s", got)
	}
	if got := d.DataTypeOf(s.LookUpField("Time")); got != "TIMESTAMP" {
		t.Fatalf("expected TIMESTAMP, got %s", got)
	}
}

// TestResolveTreeDeviceBoundary verifies simple expansion and root isolation.
func TestResolveTreeDeviceBoundary(t *testing.T) {
	d := &Dialector{resolved: resolvedConfig{Config: Config{ModelMode: TreeModel, Database: "root.datacenter_compatible"}}}
	resolved, err := d.resolveTable("device001")
	if err != nil || resolved != "root.datacenter_compatible.device001" {
		t.Fatalf("unexpected resolved path %q: %v", resolved, err)
	}
	if _, err := d.resolveTable("root.other.device001"); err == nil {
		t.Fatal("expected path outside configured root to fail")
	}
}

// TestCreateUsesAlignedTablets verifies grouping, sorting, NULLs, and no SQL fallback.
func TestCreateUsesAlignedTablets(t *testing.T) {
	runtime := &mockBackend{mode: backend.TreeModel}
	d := &Dialector{
		config: Config{
			NodeURLs:      []string{"127.0.0.1:6667"},
			Database:      "root.datacenter_compatible",
			BatchSize:     10,
			MaxBatchBytes: 1024,
		},
		backend: runtime,
	}
	db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	note := "ok"
	base := time.UnixMilli(1_700_000_000_000)
	rows := []treeTelemetry{
		{Time: base.Add(2 * time.Millisecond), DevicePath: "device_a", Temp: 22, Count: 2, Note: &note},
		{Time: base, DevicePath: "device_b", Temp: 30, Count: 3, Note: nil},
		{Time: base.Add(time.Millisecond), DevicePath: "device_a", Temp: 21, Count: 1, Note: nil},
	}
	result := db.Table("logical").Create(&rows)
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if result.RowsAffected != 3 || len(runtime.tablets) != 2 || !runtime.aligned {
		t.Fatalf("unexpected write: rows=%d tablets=%d aligned=%t", result.RowsAffected, len(runtime.tablets), runtime.aligned)
	}
	firstTemp, err := runtime.tablets[0].GetValueAt(0, 0)
	if err != nil || firstTemp != float64(21) {
		t.Fatalf("expected sorted first device value 21, got %v: %v", firstTemp, err)
	}
	nullNote, err := runtime.tablets[0].GetValueAt(2, 0)
	if err != nil || nullNote != nil {
		t.Fatalf("expected NULL note, got %v: %v", nullNote, err)
	}
}

// TestCreateHonorsBatchSize verifies adapter-side Tablet boundaries.
func TestCreateHonorsBatchSize(t *testing.T) {
	runtime := &mockBackend{mode: backend.TreeModel}
	d := &Dialector{config: Config{
		NodeURLs: []string{"127.0.0.1:6667"}, Database: "root.datacenter_compatible", BatchSize: 1,
	}, backend: runtime}
	db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	base := time.UnixMilli(1_700_000_000_000)
	rows := []treeTelemetry{{Time: base, DevicePath: "device_a"}, {Time: base.Add(time.Millisecond), DevicePath: "device_a"}}
	if err := db.Table("logical").Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(runtime.tablets) != 2 {
		t.Fatalf("expected two tablets, got %d", len(runtime.tablets))
	}
}

// TestEmptyDevicePathFallsBackToTable verifies optional per-row routing keeps the logical table target.
func TestEmptyDevicePathFallsBackToTable(t *testing.T) {
	var cache sync.Map
	schemaValue, err := schema.Parse(&treeTelemetry{}, &cache, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	d := &Dialector{resolved: resolvedConfig{Config: Config{ModelMode: TreeModel, Database: "root.datacenter_compatible"}}}
	target, err := d.rowTarget(&gorm.Statement{
		Context: context.Background(),
		Schema:  schemaValue,
		Table:   "device001",
	}, reflect.ValueOf(treeTelemetry{}), schemaValue.LookUpField("DevicePath"))
	if err != nil {
		t.Fatal(err)
	}
	if target != "root.datacenter_compatible.device001" {
		t.Fatalf("expected logical table fallback, got %s", target)
	}
}

// TestTreeMigratorUsesOfficialSchemaAPI verifies CREATE TABLE SQL is not used.
func TestTreeMigratorUsesOfficialSchemaAPI(t *testing.T) {
	runtime := &mockBackend{mode: backend.TreeModel}
	d := &Dialector{config: Config{NodeURLs: []string{"127.0.0.1:6667"}, Database: "root.datacenter_compatible"}, backend: runtime}
	db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Table("device001").AutoMigrate(&treeTelemetry{}); err != nil {
		t.Fatal(err)
	}
	if runtime.ensureDevice != "root.datacenter_compatible.device001" || len(runtime.ensureSchema) != 3 || !runtime.ensureAlign {
		t.Fatalf("unexpected migration target=%s schema=%d aligned=%t", runtime.ensureDevice, len(runtime.ensureSchema), runtime.ensureAlign)
	}
}

// TestDryRunResolvesTreePath verifies the query callback expands only table names.
func TestDryRunResolvesTreePath(t *testing.T) {
	db, err := gorm.Open(New(Config{Conn: noopConnPool{}, Database: "root.datacenter_compatible"}), &gorm.Config{
		SkipDefaultTransaction: true,
		DryRun:                 true,
		DisableAutomaticPing:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	query := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Table("device001").Where("time >= ?", int64(10)).Order("time desc").Limit(2).Find(&[]treeTelemetry{})
	})
	if !strings.Contains(query, "FROM root.datacenter_compatible.device001") || !strings.Contains(query, "LIMIT 2") {
		t.Fatalf("unexpected TreeModel query: %s", query)
	}
}

// TestRawRowsSkipsTreePathResolution verifies complete Raw SQL does not require a placeholder Table call.
func TestRawRowsSkipsTreePathResolution(t *testing.T) {
	conn := &recordingConnPool{}
	db, err := gorm.Open(New(Config{Conn: conn, Database: "root.datacenter_compatible"}), &gorm.Config{
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	rows, err := db.Raw("SHOW STORAGE GROUP").Rows()
	if err != nil {
		t.Fatalf("execute Raw rows without Table: %v", err)
	}
	if rows != nil {
		_ = rows.Close()
	}
	if conn.query != "SHOW STORAGE GROUP" {
		t.Fatalf("expected Raw query to reach connection unchanged, got %q", conn.query)
	}

	var found []treeTelemetry
	err = db.Table("root.other.device001").Find(&found).Error
	if err == nil || !strings.Contains(err.Error(), "outside configured database") {
		t.Fatalf("expected ordinary GORM query path validation to remain active, got %v", err)
	}
}

// TestDefaultTransactionsAreRejected verifies fake transaction success cannot recur.
func TestDefaultTransactionsAreRejected(t *testing.T) {
	_, err := gorm.Open(New(Config{Conn: noopConnPool{}, Database: "root.datacenter_compatible"}), &gorm.Config{DisableAutomaticPing: true})
	if !errors.Is(err, ErrUnsupportedOperation) && (err == nil || !strings.Contains(err.Error(), "SkipDefaultTransaction")) {
		t.Fatalf("expected explicit transaction configuration error, got %v", err)
	}
}

// TestTreeRejectsTableTags verifies TDengine-style tags are never silently remapped.
func TestTreeRejectsTableTags(t *testing.T) {
	runtime := &mockBackend{mode: backend.TreeModel}
	d := &Dialector{config: Config{NodeURLs: []string{"127.0.0.1:6667"}, Database: "root.datacenter_compatible"}, backend: runtime}
	db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	err = db.Table("device001").Create(&tableTelemetry{Time: time.Now(), Region: "cn", DeviceID: "d1", Temp: 1}).Error
	if err == nil || !strings.Contains(err.Error(), "TableModel-only") {
		t.Fatalf("expected explicit TreeModel tag error, got %v", err)
	}
}

// TestRejectsJoins verifies Join never reaches IoTDB as a relational query.
func TestRejectsJoins(t *testing.T) {
	runtime := &mockBackend{mode: backend.TreeModel}
	d := &Dialector{config: Config{NodeURLs: []string{"127.0.0.1:6667"}, Database: "root.datacenter_compatible"}, backend: runtime}
	db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	var rows []treeTelemetry
	err = db.Table("device001").Joins("other_device").Find(&rows).Error
	if err == nil || !strings.Contains(err.Error(), "Join") {
		t.Fatalf("expected explicit Join rejection, got %v", err)
	}
	if len(runtime.tablets) != 0 {
		t.Fatalf("join rejection must not write tablets, got %d", len(runtime.tablets))
	}
}

// TestCreateTableModelRelationalTablet verifies explicit TAG/FIELD mapping reaches a relational Tablet.
func TestCreateTableModelRelationalTablet(t *testing.T) {
	runtime := &mockBackend{mode: backend.TableModel}
	d := &Dialector{config: Config{
		ModelMode: TableModel, NodeURLs: []string{"127.0.0.1:6667"}, Database: "iotdb_test",
	}, backend: runtime}
	db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	row := tableTelemetry{Time: time.UnixMilli(1_700_000_000_000), Region: "cn", DeviceID: "d1", Temp: 20.5}
	if err := db.Table("telemetry").Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if len(runtime.tablets) != 1 || runtime.tablets[0].RowSize != 1 {
		t.Fatalf("expected one relational Tablet row, got %d tablet(s)", len(runtime.tablets))
	}
	region, err := runtime.tablets[0].GetValueAt(0, 0)
	if err != nil || region != "cn" {
		t.Fatalf("expected TAG value cn, got %v: %v", region, err)
	}
}

var _ backend.Backend = (*mockBackend)(nil)
