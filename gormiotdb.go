// Package gormiotdb exposes a GORM adapter backed by Apache's official IoTDB Go client.
package gormiotdb

import (
	"context"
	"fmt"

	"github.com/HY-805/iotdb-gorm/dialector"
	"gorm.io/gorm"
)

// Config configures the IoTDB dialector.
type Config = dialector.Config

// Dialector is the concrete dual-model GORM dialector.
type Dialector = dialector.Dialector

// Migrator is the non-destructive IoTDB migrator.
type Migrator = dialector.Migrator

// ModelMode selects TreeModel or TableModel.
type ModelMode = dialector.ModelMode

// TimePrecision controls time.Time conversion.
type TimePrecision = dialector.TimePrecision

// DevicePathFunc resolves a per-row TreeModel device path.
type DevicePathFunc = dialector.DevicePathFunc

const (
	// TreeModel targets IoTDB 1.3.1 hierarchical paths and is the default.
	TreeModel = dialector.TreeModel
	// TableModel targets IoTDB 2.0.10 relational tables.
	TableModel = dialector.TableModel
	// Milliseconds is the default IoTDB timestamp precision.
	Milliseconds = dialector.Milliseconds
	// Microseconds selects microsecond timestamps.
	Microseconds = dialector.Microseconds
	// Nanoseconds selects nanosecond timestamps.
	Nanoseconds = dialector.Nanoseconds
)

// ErrUnsupportedOperation marks GORM behavior that cannot be represented safely.
var ErrUnsupportedOperation = dialector.ErrUnsupportedOperation

// Bool returns a pointer useful for explicit boolean configuration values.
func Bool(value bool) *bool {
	return dialector.Bool(value)
}

// Open returns a TreeModel-by-default GORM dialector for an iotdb:// DSN.
func Open(dsn string) gorm.Dialector {
	return dialector.Open(dsn)
}

// New returns a GORM dialector using structured configuration.
func New(config Config) gorm.Dialector {
	return dialector.New(config)
}

// Ping performs a real server round trip through the adapter's official pool.
func Ping(ctx context.Context, db *gorm.DB) error {
	runtime, ok := db.Dialector.(interface{ Ping(context.Context) error })
	if !ok {
		return fmt.Errorf("iotdb: GORM database does not use this dialector")
	}
	return runtime.Ping(ctx)
}

// Close closes both the database/sql bridge and the owned official client pool.
func Close(db *gorm.DB) error {
	runtime, ok := db.Dialector.(interface{ Close() error })
	if !ok {
		return fmt.Errorf("iotdb: GORM database does not use this dialector")
	}
	return runtime.Close()
}
