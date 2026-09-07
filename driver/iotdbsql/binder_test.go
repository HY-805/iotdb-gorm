package iotdbsql

import (
	"database/sql"
	"database/sql/driver"
	"math"
	"strings"
	"testing"
	"time"
)

// TestBinderSkipsQuotedAndCommentQuestionMarks verifies syntax-aware replacement.
func TestBinderSkipsQuotedAndCommentQuestionMarks(t *testing.T) {
	query := "SELECT '?' AS literal FROM root.sg.d1 WHERE note = ? -- ?\nAND count > ? /* ? */"
	bound, err := (Binder{}).Bind(query, []driver.NamedValue{{Ordinal: 1, Value: "O'Reilly"}, {Ordinal: 2, Value: int64(2)}})
	if err != nil {
		t.Fatal(err)
	}
	wanted := "note = 'O''Reilly'"
	if !strings.Contains(bound, wanted) || !strings.Contains(bound, "count > 2") || !strings.Contains(bound, "-- ?") || !strings.Contains(bound, "/* ? */") {
		t.Fatalf("unexpected bound SQL: %s", bound)
	}
}

// TestBinderTimePrecisionAndNull verifies deterministic timestamp and NULL literals.
func TestBinderTimePrecisionAndNull(t *testing.T) {
	value := time.Unix(1, 234_567_890)
	bound, err := (Binder{TimePrecision: Microseconds}).Bind("SELECT * WHERE time = ? AND note = ?", []driver.NamedValue{
		{Ordinal: 1, Value: value},
		{Ordinal: 2, Value: sql.NullString{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if bound != "SELECT * WHERE time = 1234567 AND note = NULL" {
		t.Fatalf("unexpected bound SQL: %s", bound)
	}
}

// TestBinderRejectsMismatches verifies arguments cannot be silently dropped.
func TestBinderRejectsMismatches(t *testing.T) {
	if _, err := (Binder{}).Bind("SELECT ?", nil); err == nil {
		t.Fatal("expected missing argument error")
	}
	if _, err := (Binder{}).Bind("SELECT ?", []driver.NamedValue{{Value: 1}, {Value: 2}}); err == nil {
		t.Fatal("expected unused argument error")
	}
	if _, err := (Binder{}).Bind("SELECT ? + ?", []driver.NamedValue{{Value: 1}}); err == nil {
		t.Fatal("expected missing argument error")
	}
}

// TestBinderRejectsNamedAndNonFiniteValues verifies unsupported values fail closed.
func TestBinderRejectsNamedAndNonFiniteValues(t *testing.T) {
	if _, err := (Binder{}).Bind("SELECT @value", []driver.NamedValue{{Name: "value", Value: 1}}); err == nil {
		t.Fatal("expected named parameter error")
	}
	if _, err := (Binder{}).Bind("SELECT ?", []driver.NamedValue{{Value: math.NaN()}}); err == nil {
		t.Fatal("expected non-finite float error")
	}
}
