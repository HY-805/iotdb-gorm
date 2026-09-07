package tests

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type noopConnPool struct{}

type telemetry struct {
	Time     time.Time `gorm:"column:time;iotdb:time"`
	Region   string    `gorm:"column:region"`
	DeviceID string    `gorm:"column:device_id"`
	Temp     float64   `gorm:"column:temp"`
}

func (noopConnPool) PrepareContext(_ context.Context, _ string) (*sql.Stmt, error) { return nil, nil }
func (noopConnPool) ExecContext(_ context.Context, _ string, _ ...interface{}) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}
func (noopConnPool) QueryContext(_ context.Context, _ string, _ ...interface{}) (*sql.Rows, error) {
	return nil, nil
}
func (noopConnPool) QueryRowContext(_ context.Context, _ string, _ ...interface{}) *sql.Row {
	return nil
}

func TestOpenDryRun(t *testing.T) {
	db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{Conn: noopConnPool{}, Database: "root.datacenter_compatible"}), &gorm.Config{SkipDefaultTransaction: true, DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}

	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Table("device001").Create(&telemetry{Time: time.Unix(0, 0), Region: "cn", DeviceID: "d1", Temp: 20.5})
	})
	if sql == "" {
		t.Fatal("expected generated SQL")
	}
}
