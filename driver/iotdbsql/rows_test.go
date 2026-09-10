package iotdbsql

import (
	"context"
	"reflect"
	"testing"
	"time"
)

type resultSetStub struct {
	columns []string
	types   []string
}

// ColumnNames returns the configured test labels.
func (r *resultSetStub) ColumnNames() []string { return r.columns }

// ColumnTypes returns the configured test types.
func (r *resultSetStub) ColumnTypes() []string { return r.types }

// Next reports an empty test result.
func (*resultSetStub) Next() (bool, error) { return false, nil }

// Value is unused because the test result has no rows.
func (*resultSetStub) Value(int) (any, error) { return nil, nil }

// Close releases the no-op test result.
func (*resultSetStub) Close() error { return nil }

// TestRowsNormalizeFixedDeviceColumns verifies existing struct-friendly labels remain unchanged.
func TestRowsNormalizeFixedDeviceColumns(t *testing.T) {
	result := &resultSetStub{
		columns: []string{"Time", "root.datacenter.device_a.product_data.pressure"},
		types:   []string{"INT64", "FLOAT"},
	}
	got := newRows(context.Background(), result).Columns()
	want := []string{"time", "pressure"}
	assertColumnsEqual(t, got, want)
}

// TestRowsPreserveWildcardFullPathColumns verifies each expanded time series keeps a unique label.
func TestRowsPreserveWildcardFullPathColumns(t *testing.T) {
	result := &resultSetStub{
		columns: []string{
			"Time",
			"root.datacenter.device_a.product_data.pressure",
			"root.datacenter.device_b.product_data.pressure",
		},
		types: []string{"INT64", "FLOAT", "FLOAT"},
	}
	got := newRows(WithFullPathColumns(context.Background()), result).Columns()
	want := []string{
		"time",
		"root.datacenter.device_a.product_data.pressure",
		"root.datacenter.device_b.product_data.pressure",
	}
	assertColumnsEqual(t, got, want)
}

// TestRowsTimeColumnScanType verifies the TreeModel Time column keeps its runtime time.Time type
// even when IoTDB 1.3.1 reports INT64 metadata for that column.
func TestRowsTimeColumnScanType(t *testing.T) {
	result := &resultSetStub{
		columns: []string{"Time", "root.datacenter.device_a.product_data.counter"},
		types:   []string{"INT64", "INT64"},
	}
	rows := newRows(context.Background(), result)
	if got, want := rows.ColumnTypeScanType(0), reflect.TypeOf(time.Time{}); got != want {
		t.Fatalf("time scan type mismatch: got=%v want=%v", got, want)
	}
	if got, want := rows.ColumnTypeScanType(1), reflect.TypeOf(int64(0)); got != want {
		t.Fatalf("ordinary INT64 scan type mismatch: got=%v want=%v", got, want)
	}
}

// assertColumnsEqual compares ordered database/sql column labels.
func assertColumnsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("column count mismatch: got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("column %d mismatch: got=%q want=%q", i, got[i], want[i])
		}
	}
}
