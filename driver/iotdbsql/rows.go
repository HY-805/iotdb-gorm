package iotdbsql

import (
	"context"
	"database/sql/driver"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/HY-805/iotdb-gorm/internal/backend"
)

type fullPathColumnsContextKey struct{}

// WithFullPathColumns marks a wildcard query so result columns retain complete server paths.
func WithFullPathColumns(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, fullPathColumnsContextKey{}, true)
}

// rows adapts the official result set and owns its checked-out session.
type rows struct {
	ctx         context.Context
	result      backend.ResultSet
	columns     []string
	columnTypes []string
}

// newRows creates database/sql rows with GORM-friendly TreeModel column labels.
func newRows(ctx context.Context, result backend.ResultSet) *rows {
	serverColumns := result.ColumnNames()
	columns := make([]string, len(serverColumns))
	preserveFullPaths := false
	if ctx != nil {
		preserveFullPaths, _ = ctx.Value(fullPathColumnsContextKey{}).(bool)
	}
	for i, column := range serverColumns {
		columns[i] = normalizeColumnName(column, preserveFullPaths)
	}
	return &rows{
		ctx:         ctx,
		result:      result,
		columns:     columns,
		columnTypes: result.ColumnTypes(),
	}
}

// Columns returns short labels for fixed-device scans or complete labels for wildcard scans.
func (r *rows) Columns() []string {
	return append([]string(nil), r.columns...)
}

// Close releases the official result set and session.
func (r *rows) Close() error {
	return r.result.Close()
}

// Next decodes one row without converting read errors into zero values.
func (r *rows) Next(destination []driver.Value) error {
	select {
	case <-r.ctx.Done():
		return r.ctx.Err()
	default:
	}
	if len(destination) != len(r.columns) {
		return fmt.Errorf("iotdb: destination has %d columns, server returned %d", len(destination), len(r.columns))
	}
	next, err := r.result.Next()
	if err != nil {
		return fmt.Errorf("iotdb: advance result set: %w", err)
	}
	if !next {
		return io.EOF
	}
	for i := range destination {
		value, err := r.result.Value(i)
		if err != nil {
			return fmt.Errorf("iotdb: read column %q: %w", r.columns[i], err)
		}
		destination[i] = value
	}
	return nil
}

// ColumnTypeDatabaseTypeName returns the server's logical type name.
func (r *rows) ColumnTypeDatabaseTypeName(index int) string {
	if index < 0 || index >= len(r.columnTypes) {
		return ""
	}
	return strings.ToUpper(r.columnTypes[index])
}

// ColumnTypeScanType returns a useful database/sql scan type for each IoTDB type.
func (r *rows) ColumnTypeScanType(index int) reflect.Type {
	// IoTDB 1.3.1 reports the TreeModel Time column as INT64 metadata while
	// the official client returns its runtime value as time.Time.
	if index >= 0 && index < len(r.columns) && strings.EqualFold(r.columns[index], "time") {
		return reflect.TypeOf(time.Time{})
	}
	switch r.ColumnTypeDatabaseTypeName(index) {
	case "BOOLEAN":
		return reflect.TypeOf(false)
	case "INT32":
		return reflect.TypeOf(int32(0))
	case "INT64":
		return reflect.TypeOf(int64(0))
	case "FLOAT":
		return reflect.TypeOf(float32(0))
	case "DOUBLE":
		return reflect.TypeOf(float64(0))
	case "TIMESTAMP", "DATE":
		return reflect.TypeOf(time.Time{})
	case "BLOB":
		return reflect.TypeOf([]byte(nil))
	default:
		return reflect.TypeOf("")
	}
}

// normalizeColumnName strips a TreeModel prefix for fixed-device scans and preserves it for wildcard scans.
func normalizeColumnName(column string, preserveFullPath bool) string {
	trimmed := strings.Trim(column, "`\"")
	if strings.EqualFold(trimmed, "Time") {
		return "time"
	}
	if preserveFullPath {
		return trimmed
	}
	if dot := strings.LastIndexByte(trimmed, '.'); dot >= 0 && dot+1 < len(trimmed) {
		return trimmed[dot+1:]
	}
	return trimmed
}

var _ driver.Rows = (*rows)(nil)
var _ driver.RowsColumnTypeDatabaseTypeName = (*rows)(nil)
var _ driver.RowsColumnTypeScanType = (*rows)(nil)
