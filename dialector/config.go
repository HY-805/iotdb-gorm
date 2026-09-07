package dialector

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/HY-805/iotdb-gorm/driver/iotdbsql"
	"github.com/HY-805/iotdb-gorm/internal/backend"
	"gorm.io/gorm"
)

// ModelMode selects the IoTDB data model.
type ModelMode = backend.ModelMode

const (
	// TreeModel targets IoTDB 1.3.1 hierarchical paths and is the default.
	TreeModel = backend.TreeModel
	// TableModel targets IoTDB 2.0.10 relational tables.
	TableModel = backend.TableModel
)

// TimePrecision controls time.Time conversion for SQL predicates and Tablets.
type TimePrecision = iotdbsql.TimePrecision

const (
	// Milliseconds is the default IoTDB timestamp precision.
	Milliseconds = iotdbsql.Milliseconds
	// Microseconds selects microsecond timestamp conversion.
	Microseconds = iotdbsql.Microseconds
	// Nanoseconds selects nanosecond timestamp conversion.
	Nanoseconds = iotdbsql.Nanoseconds
)

// DevicePathFunc resolves an optional per-row TreeModel device path.
type DevicePathFunc func(logicalTable string, row any) (string, error)

// Config configures the IoTDB GORM dialector and its one official client pool.
type Config struct {
	DSN                    string
	ModelMode              ModelMode
	NodeURLs               []string
	Username               string
	Password               string
	Database               string
	Aligned                *bool
	PoolSize               int
	ConnectTimeout         time.Duration
	AcquireTimeout         time.Duration
	QueryTimeout           time.Duration
	FetchSize              int32
	TimeZone               string
	ConnectRetryMax        int
	EnableRPCCompression   bool
	BatchSize              int
	MaxBatchBytes          int
	TableInsertConcurrency int
	TimePrecision          TimePrecision
	DevicePathField        string
	DevicePathFunc         DevicePathFunc
	AllowSQLFallback       bool
	Conn                   gorm.ConnPool
}

// Bool returns a pointer useful for explicit boolean configuration values.
func Bool(value bool) *bool {
	return &value
}

// resolvedConfig contains defaults and parsed DSN values used at runtime.
type resolvedConfig struct {
	Config
	AlignedValue bool
}

var (
	treePathNodePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
	tableNamePattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// resolveConfig merges defaults, DSN values, and explicit structured settings.
func resolveConfig(input Config) (resolvedConfig, error) {
	defaults := Config{
		ModelMode:              TreeModel,
		Username:               "root",
		Password:               "root",
		PoolSize:               8,
		ConnectTimeout:         10 * time.Second,
		AcquireTimeout:         10 * time.Second,
		QueryTimeout:           30 * time.Second,
		FetchSize:              1024,
		TimeZone:               "Asia/Shanghai",
		ConnectRetryMax:        3,
		BatchSize:              1000,
		MaxBatchBytes:          4 << 20,
		TableInsertConcurrency: 4,
		TimePrecision:          Milliseconds,
	}
	defaultAligned := true
	defaults.Aligned = &defaultAligned

	if input.DSN != "" {
		parsed, err := parseDSN(input.DSN, defaults)
		if err != nil {
			return resolvedConfig{}, err
		}
		defaults = parsed
	}
	applyExplicitConfig(&defaults, input)
	resolved := resolvedConfig{Config: defaults, AlignedValue: defaults.Aligned == nil || *defaults.Aligned}
	if err := validateResolvedConfig(resolved); err != nil {
		return resolvedConfig{}, err
	}
	return resolved, nil
}

// applyExplicitConfig overlays non-zero structured settings on parsed defaults.
func applyExplicitConfig(target *Config, input Config) {
	target.DSN = input.DSN
	if input.ModelMode == TableModel {
		target.ModelMode = TableModel
	}
	if len(input.NodeURLs) > 0 {
		target.NodeURLs = append([]string(nil), input.NodeURLs...)
	}
	if input.Username != "" {
		target.Username = input.Username
	}
	if input.Password != "" {
		target.Password = input.Password
	}
	if input.Database != "" {
		target.Database = input.Database
	}
	if input.Aligned != nil {
		target.Aligned = input.Aligned
	}
	if input.PoolSize > 0 {
		target.PoolSize = input.PoolSize
	}
	if input.ConnectTimeout > 0 {
		target.ConnectTimeout = input.ConnectTimeout
	}
	if input.AcquireTimeout > 0 {
		target.AcquireTimeout = input.AcquireTimeout
	}
	if input.QueryTimeout > 0 {
		target.QueryTimeout = input.QueryTimeout
	}
	if input.FetchSize > 0 {
		target.FetchSize = input.FetchSize
	}
	if input.TimeZone != "" {
		target.TimeZone = input.TimeZone
	}
	if input.ConnectRetryMax > 0 {
		target.ConnectRetryMax = input.ConnectRetryMax
	}
	if input.BatchSize > 0 {
		target.BatchSize = input.BatchSize
	}
	if input.MaxBatchBytes > 0 {
		target.MaxBatchBytes = input.MaxBatchBytes
	}
	if input.TableInsertConcurrency > 0 {
		target.TableInsertConcurrency = input.TableInsertConcurrency
	}
	if input.TimePrecision != Milliseconds {
		target.TimePrecision = input.TimePrecision
	}
	target.EnableRPCCompression = input.EnableRPCCompression || target.EnableRPCCompression
	target.DevicePathField = input.DevicePathField
	target.DevicePathFunc = input.DevicePathFunc
	target.AllowSQLFallback = input.AllowSQLFallback
	target.Conn = input.Conn
}

// parseDSN parses the supported iotdb:// URL settings.
func parseDSN(raw string, base Config) (Config, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return Config{}, fmt.Errorf("iotdb: parse DSN: %w", err)
	}
	if !strings.EqualFold(parsed.Scheme, "iotdb") {
		return Config{}, fmt.Errorf("iotdb: DSN scheme must be iotdb, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return Config{}, fmt.Errorf("iotdb: DSN has no NodeURL")
	}
	base.DSN = raw
	base.NodeURLs = strings.Split(parsed.Host, ",")
	if parsed.User != nil {
		base.Username = parsed.User.Username()
		if password, ok := parsed.User.Password(); ok {
			base.Password = password
		}
	}
	if database := strings.Trim(parsed.EscapedPath(), "/"); database != "" {
		decoded, decodeErr := url.PathUnescape(database)
		if decodeErr != nil {
			return Config{}, fmt.Errorf("iotdb: decode DSN database: %w", decodeErr)
		}
		base.Database = decoded
	}

	query := parsed.Query()
	if value := query.Get("username"); value != "" {
		base.Username = value
	}
	if value := query.Get("password"); value != "" {
		base.Password = value
	}
	if value := query.Get("database"); value != "" {
		base.Database = value
	}
	if value := query.Get("model"); value != "" {
		mode, modeErr := parseModelMode(value)
		if modeErr != nil {
			return Config{}, modeErr
		}
		base.ModelMode = mode
	}
	if value := query.Get("model_mode"); value != "" {
		mode, modeErr := parseModelMode(value)
		if modeErr != nil {
			return Config{}, modeErr
		}
		base.ModelMode = mode
	}
	if value := query.Get("aligned"); value != "" {
		aligned, boolErr := strconv.ParseBool(value)
		if boolErr != nil {
			return Config{}, fmt.Errorf("iotdb: parse aligned: %w", boolErr)
		}
		base.Aligned = Bool(aligned)
	}
	if err := applyDSNNumericAndDuration(query, &base); err != nil {
		return Config{}, err
	}
	if value := query.Get("time_precision"); value != "" {
		precision, precisionErr := parseTimePrecision(value)
		if precisionErr != nil {
			return Config{}, precisionErr
		}
		base.TimePrecision = precision
	}
	if value := query.Get("time_zone"); value != "" {
		base.TimeZone = value
	}
	if value := query.Get("rpc_compression"); value != "" {
		enabled, boolErr := strconv.ParseBool(value)
		if boolErr != nil {
			return Config{}, fmt.Errorf("iotdb: parse rpc_compression: %w", boolErr)
		}
		base.EnableRPCCompression = enabled
	}
	return base, nil
}

// applyDSNNumericAndDuration parses bounded numeric and duration DSN options.
func applyDSNNumericAndDuration(values url.Values, target *Config) error {
	integers := []struct {
		key string
		set func(int)
	}{
		{key: "pool_size", set: func(value int) { target.PoolSize = value }},
		{key: "fetch_size", set: func(value int) { target.FetchSize = int32(value) }},
		{key: "connect_retry_max", set: func(value int) { target.ConnectRetryMax = value }},
		{key: "batch_size", set: func(value int) { target.BatchSize = value }},
		{key: "max_batch_bytes", set: func(value int) { target.MaxBatchBytes = value }},
		{key: "table_insert_concurrency", set: func(value int) { target.TableInsertConcurrency = value }},
	}
	for _, item := range integers {
		if raw := values.Get(item.key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value <= 0 {
				return fmt.Errorf("iotdb: %s must be a positive integer", item.key)
			}
			item.set(value)
		}
	}
	durations := []struct {
		key string
		set func(time.Duration)
	}{
		{key: "connect_timeout", set: func(value time.Duration) { target.ConnectTimeout = value }},
		{key: "acquire_timeout", set: func(value time.Duration) { target.AcquireTimeout = value }},
		{key: "query_timeout", set: func(value time.Duration) { target.QueryTimeout = value }},
	}
	for _, item := range durations {
		if raw := values.Get(item.key); raw != "" {
			value, err := time.ParseDuration(raw)
			if err != nil || value <= 0 {
				return fmt.Errorf("iotdb: %s must be a positive duration", item.key)
			}
			item.set(value)
		}
	}
	return nil
}

// parseModelMode parses user-facing mode names.
func parseModelMode(value string) (ModelMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tree", "treemodel":
		return TreeModel, nil
	case "table", "tablemodel":
		return TableModel, nil
	default:
		return TreeModel, fmt.Errorf("iotdb: invalid model mode %q", value)
	}
}

// parseTimePrecision parses supported server timestamp units.
func parseTimePrecision(value string) (TimePrecision, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ms", "millisecond", "milliseconds":
		return Milliseconds, nil
	case "us", "microsecond", "microseconds":
		return Microseconds, nil
	case "ns", "nanosecond", "nanoseconds":
		return Nanoseconds, nil
	default:
		return Milliseconds, fmt.Errorf("iotdb: invalid time precision %q", value)
	}
}

// validateResolvedConfig rejects unsafe paths and unusable pool settings.
func validateResolvedConfig(config resolvedConfig) error {
	if config.DevicePathField != "" && config.DevicePathFunc != nil {
		return fmt.Errorf("iotdb: configure either DevicePathField or DevicePathFunc, not both")
	}
	if config.Conn == nil && len(config.NodeURLs) == 0 {
		return fmt.Errorf("iotdb: at least one NodeURL is required")
	}
	for _, nodeURL := range config.NodeURLs {
		if _, _, err := net.SplitHostPort(nodeURL); err != nil {
			return fmt.Errorf("iotdb: invalid NodeURL %q: %w", nodeURL, err)
		}
	}
	if config.Database == "" {
		return fmt.Errorf("iotdb: database is required")
	}
	if config.ModelMode == TreeModel {
		if err := validateTreeDatabase(config.Database); err != nil {
			return err
		}
	} else if config.ModelMode == TableModel {
		if !tableNamePattern.MatchString(config.Database) {
			return fmt.Errorf("iotdb: invalid TableModel database %q", config.Database)
		}
	} else {
		return fmt.Errorf("iotdb: unsupported model mode %d", config.ModelMode)
	}
	return nil
}

// validateTreeDatabase enforces a controlled root.xxx hierarchy.
func validateTreeDatabase(database string) error {
	parts := strings.Split(database, ".")
	if len(parts) < 2 || parts[0] != "root" {
		return fmt.Errorf("iotdb: TreeModel database must start with root. and contain a child path")
	}
	for _, part := range parts {
		if !treePathNodePattern.MatchString(part) {
			return fmt.Errorf("iotdb: invalid TreeModel path node %q", part)
		}
	}
	return nil
}

// backendConfig projects public configuration onto the official client boundary.
func (c resolvedConfig) backendConfig() backend.Config {
	return backend.Config{
		Mode:                   c.ModelMode,
		NodeURLs:               append([]string(nil), c.NodeURLs...),
		Username:               c.Username,
		Password:               c.Password,
		Database:               c.Database,
		PoolSize:               c.PoolSize,
		ConnectTimeout:         c.ConnectTimeout,
		AcquireTimeout:         c.AcquireTimeout,
		QueryTimeout:           c.QueryTimeout,
		FetchSize:              c.FetchSize,
		TimeZone:               c.TimeZone,
		ConnectRetryMax:        c.ConnectRetryMax,
		EnableRPCCompression:   c.EnableRPCCompression,
		TableInsertConcurrency: c.TableInsertConcurrency,
	}
}
