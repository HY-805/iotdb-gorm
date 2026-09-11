// Package dialector implements a dual-model GORM dialector for Apache IoTDB.
package dialector

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/HY-805/iotdb-gorm/driver/iotdbsql"
	"github.com/HY-805/iotdb-gorm/internal/backend"
	"github.com/HY-805/iotdb-gorm/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

// ErrUnsupportedOperation marks GORM semantics that IoTDB cannot safely emulate.
var ErrUnsupportedOperation = errors.New("iotdb: unsupported GORM operation")

// Dialector implements gorm.Dialector and owns one official-client backend.
type Dialector struct {
	config   Config
	resolved resolvedConfig
	backend  backend.Backend
	sqlDB    *sql.DB

	lifecycleMu sync.Mutex
	closed      bool
}

// Open creates a TreeModel-by-default dialector from an iotdb:// DSN.
func Open(dsn string) gorm.Dialector {
	return New(Config{DSN: dsn})
}

// New creates a dialector from structured configuration.
func New(config Config) gorm.Dialector {
	return &Dialector{config: config}
}

// Name returns the GORM dialector name.
func (d *Dialector) Name() string {
	return "iotdb"
}

// Initialize builds one official pool and registers explicit GORM behavior.
func (d *Dialector) Initialize(db *gorm.DB) error {
	if !db.Config.SkipDefaultTransaction {
		return fmt.Errorf("%w: set gorm.Config.SkipDefaultTransaction=true", iotdbsql.ErrTransactionsUnsupported)
	}
	resolved, err := resolveConfig(d.config)
	if err != nil {
		return err
	}
	d.resolved = resolved

	if resolved.Conn != nil {
		db.ConnPool = resolved.Conn
	} else {
		if d.backend == nil {
			runtime, backendErr := backend.NewOfficial(resolved.backendConfig())
			if backendErr != nil {
				return backendErr
			}
			d.backend = runtime
		}
		connector := iotdbsql.NewConnector(d.backend, iotdbsql.Binder{TimePrecision: resolved.TimePrecision})
		d.sqlDB = sql.OpenDB(connector)
		d.sqlDB.SetMaxOpenConns(resolved.PoolSize)
		d.sqlDB.SetMaxIdleConns(resolved.PoolSize)
		db.ConnPool = d.sqlDB
	}

	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{})
	if err := d.registerCallbacks(db); err != nil {
		_ = d.Close()
		return err
	}
	db.ClauseBuilders["LIMIT"] = buildLimit
	return nil
}

// registerCallbacks replaces unsafe relational semantics and adds path resolution.
func (d *Dialector) registerCallbacks(db *gorm.DB) error {
	if err := db.Callback().Create().Replace("gorm:create", d.createCallback()); err != nil {
		return fmt.Errorf("register IoTDB create callback: %w", err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("iotdb:resolve_table", d.resolveStatementTable); err != nil {
		return fmt.Errorf("register IoTDB query path callback: %w", err)
	}
	if err := db.Callback().Query().Before("gorm:query").Register("iotdb:reject_joins", rejectJoins); err != nil {
		return fmt.Errorf("register IoTDB join guard callback: %w", err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("iotdb:resolve_row_table", d.resolveStatementTable); err != nil {
		return fmt.Errorf("register IoTDB row path callback: %w", err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("iotdb:reject_row_joins", rejectJoins); err != nil {
		return fmt.Errorf("register IoTDB row join guard callback: %w", err)
	}
	if err := db.Callback().Query().Replace("gorm:preload", rejectPreload); err != nil {
		return fmt.Errorf("register IoTDB preload callback: %w", err)
	}
	if err := db.Callback().Update().Replace("gorm:update", rejectUpdate); err != nil {
		return fmt.Errorf("register IoTDB update callback: %w", err)
	}
	if err := db.Callback().Delete().Replace("gorm:delete", rejectDelete); err != nil {
		return fmt.Errorf("register IoTDB delete callback: %w", err)
	}
	return nil
}

// Migrator returns a non-destructive model-specific migrator.
func (d *Dialector) Migrator(db *gorm.DB) gorm.Migrator {
	return Migrator{db: db, dialector: d, backend: d.backend}
}

// DataTypeOf returns the IoTDB type corresponding to one GORM field.
func (d *Dialector) DataTypeOf(field *schema.Field) string {
	column := model.ParseField(field)
	return dataTypeName(column, d.resolved.ModelMode)
}

// DefaultValueOf reports the configured default expression for DryRun SQL only.
func (d *Dialector) DefaultValueOf(field *schema.Field) clause.Expression {
	if field.DefaultValue != "" {
		return clause.Expr{SQL: field.DefaultValue}
	}
	return clause.Expr{SQL: "DEFAULT"}
}

// BindVarTo writes one positional placeholder for the database/sql binder.
func (d *Dialector) BindVarTo(writer clause.Writer, _ *gorm.Statement, _ interface{}) {
	_ = writer.WriteByte('?')
}

// QuoteTo writes validated IoTDB identifiers without treating a Tree path as one name.
func (d *Dialector) QuoteTo(writer clause.Writer, identifier string) {
	if identifier == "*" {
		_, _ = writer.WriteString(identifier)
		return
	}
	parts := strings.Split(identifier, ".")
	for i, part := range parts {
		if i > 0 {
			_ = writer.WriteByte('.')
		}
		if part == "*" || part == "**" || treePathNodePattern.MatchString(part) || tableNamePattern.MatchString(part) {
			_, _ = writer.WriteString(part)
			continue
		}
		_ = writer.WriteByte('`')
		_, _ = writer.WriteString(strings.ReplaceAll(part, "`", "``"))
		_ = writer.WriteByte('`')
	}
}

// Explain renders SQL for GORM logs and DryRun output.
func (d *Dialector) Explain(query string, vars ...interface{}) string {
	return logger.ExplainSQL(query, nil, "'", vars...)
}

// SavePoint rejects savepoint emulation.
func (d *Dialector) SavePoint(*gorm.DB, string) error {
	return fmt.Errorf("%w: savepoints", ErrUnsupportedOperation)
}

// RollbackTo rejects savepoint emulation.
func (d *Dialector) RollbackTo(*gorm.DB, string) error {
	return fmt.Errorf("%w: rollback to savepoint", ErrUnsupportedOperation)
}

// Ping performs a real backend round trip.
func (d *Dialector) Ping(ctx context.Context) error {
	if d.backend == nil {
		return errors.New("iotdb: no owned backend is available")
	}
	return d.backend.Ping(ctx)
}

// Close closes database/sql wrappers and then the owned official pool.
func (d *Dialector) Close() error {
	d.lifecycleMu.Lock()
	if d.closed {
		d.lifecycleMu.Unlock()
		return nil
	}
	d.closed = true
	d.lifecycleMu.Unlock()

	var closeErrors []error
	if d.sqlDB != nil {
		if err := d.sqlDB.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	if d.backend != nil {
		if err := d.backend.Close(); err != nil {
			closeErrors = append(closeErrors, err)
		}
	}
	return errors.Join(closeErrors...)
}

// resolveStatementTable validates and expands the current logical table.
func (d *Dialector) resolveStatementTable(db *gorm.DB) {
	if db.Error != nil {
		return
	}
	// Raw already contains a complete statement and therefore has no logical table to resolve.
	if db.Statement.SQL.Len() > 0 {
		return
	}
	table := db.Statement.Table
	if table == "" && db.Statement.Schema != nil {
		table = db.Statement.Schema.Table
	}
	resolved, wildcard, err := d.resolveQueryTable(table)
	if err != nil {
		_ = db.AddError(err)
		return
	}
	if wildcard {
		db.Statement.Context = iotdbsql.WithFullPathColumns(db.Statement.Context)
	}
	db.Statement.Table = resolved
	if db.Statement.TableExpr != nil {
		if len(db.Statement.TableExpr.Vars) > 0 {
			_ = db.AddError(fmt.Errorf("%w: dynamic table expressions", ErrUnsupportedOperation))
			return
		}
		db.Statement.TableExpr = &clause.Expr{SQL: resolved}
	}
}

// resolveQueryTable allows complete-node wildcards only for TreeModel read paths.
func (d *Dialector) resolveQueryTable(table string) (string, bool, error) {
	if d.resolved.ModelMode == TableModel {
		resolved, err := d.resolveTable(table)
		return resolved, false, err
	}

	table = strings.Trim(strings.TrimSpace(table), "`\"")
	if table == "" {
		return "", false, errors.New("iotdb: empty table or device name")
	}
	if strings.ContainsAny(table, " \t\r\n,()") {
		return "", false, fmt.Errorf("iotdb: table aliases and expressions are not supported: %q", table)
	}

	path := table
	if !strings.HasPrefix(path, "root.") {
		if strings.Contains(path, ".") {
			return "", false, fmt.Errorf("iotdb: relative TreeModel query path must be one path node: %q", table)
		}
		path = d.resolved.Database + "." + path
	}
	wildcard, err := validateTreeQueryPath(path)
	if err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(path, d.resolved.Database+".") {
		return "", false, fmt.Errorf("iotdb: query path %q is outside configured database %q", path, d.resolved.Database)
	}
	return path, wildcard, nil
}

// resolveTable maps a simple TreeModel table to its configured device root.
func (d *Dialector) resolveTable(table string) (string, error) {
	table = strings.Trim(strings.TrimSpace(table), "`\"")
	if table == "" {
		return "", errors.New("iotdb: empty table or device name")
	}
	if strings.ContainsAny(table, " \t\r\n,()") {
		return "", fmt.Errorf("iotdb: table aliases and expressions are not supported: %q", table)
	}
	if d.resolved.ModelMode == TableModel {
		if !tableNamePattern.MatchString(table) {
			return "", fmt.Errorf("iotdb: invalid TableModel table %q", table)
		}
		return table, nil
	}

	device := table
	if !strings.HasPrefix(device, "root.") {
		if strings.Contains(device, ".") {
			return "", fmt.Errorf("iotdb: relative TreeModel device must be one path node: %q", table)
		}
		device = d.resolved.Database + "." + device
	}
	if err := validateTreePath(device); err != nil {
		return "", err
	}
	if !strings.HasPrefix(device, d.resolved.Database+".") {
		return "", fmt.Errorf("iotdb: device %q is outside configured database %q", device, d.resolved.Database)
	}
	return device, nil
}

// validateTreePath validates every node in a complete TreeModel path.
func validateTreePath(path string) error {
	parts := strings.Split(path, ".")
	if len(parts) < 3 || parts[0] != "root" {
		return fmt.Errorf("iotdb: device must be a complete root.database.device path")
	}
	for _, part := range parts {
		if !treePathNodePattern.MatchString(part) {
			return fmt.Errorf("iotdb: invalid TreeModel path node %q", part)
		}
	}
	return nil
}

// validateTreeQueryPath accepts only full-node TreeModel wildcards and reports whether one is present.
func validateTreeQueryPath(path string) (bool, error) {
	parts := strings.Split(path, ".")
	if len(parts) < 3 || parts[0] != "root" {
		return false, fmt.Errorf("iotdb: query path must be a complete root.database.device path")
	}
	wildcard := false
	for _, part := range parts {
		if part == "*" || part == "**" {
			wildcard = true
			continue
		}
		if !treePathNodePattern.MatchString(part) {
			return false, fmt.Errorf("iotdb: invalid TreeModel query path node %q", part)
		}
	}
	return wildcard, nil
}

// rejectPreload reports unsupported association preloading only when requested.
func rejectPreload(db *gorm.DB) {
	if len(db.Statement.Preloads) > 0 {
		_ = db.AddError(fmt.Errorf("%w: Preload", ErrUnsupportedOperation))
	}
}

// rejectJoins rejects relationship and SQL joins before GORM builds a query.
func rejectJoins(db *gorm.DB) {
	if len(db.Statement.Joins) > 0 {
		_ = db.AddError(fmt.Errorf("%w: Join", ErrUnsupportedOperation))
		return
	}
	if from, ok := db.Statement.Clauses["FROM"].Expression.(clause.From); ok && len(from.Joins) > 0 {
		_ = db.AddError(fmt.Errorf("%w: Join", ErrUnsupportedOperation))
	}
}

// rejectUpdate rejects Updates and the update branch of Save.
func rejectUpdate(db *gorm.DB) {
	_ = db.AddError(fmt.Errorf("%w: Updates and Save", ErrUnsupportedOperation))
}

// rejectDelete rejects GORM Delete while Raw/Exec remains explicitly available.
func rejectDelete(db *gorm.DB) {
	_ = db.AddError(fmt.Errorf("%w: Delete; use an explicit IoTDB delete statement", ErrUnsupportedOperation))
}

// buildLimit renders IoTDB LIMIT and OFFSET clauses.
func buildLimit(c clause.Clause, builder clause.Builder) {
	limit, ok := c.Expression.(clause.Limit)
	if !ok {
		c.Build(builder)
		return
	}
	if limit.Limit != nil && *limit.Limit > 0 {
		_, _ = builder.WriteString("LIMIT ")
		builder.AddVar(builder, *limit.Limit)
	}
	if limit.Offset > 0 {
		_, _ = builder.WriteString(" OFFSET ")
		builder.AddVar(builder, limit.Offset)
	}
}

var _ gorm.Dialector = (*Dialector)(nil)
var _ gorm.SavePointerDialectorInterface = (*Dialector)(nil)
