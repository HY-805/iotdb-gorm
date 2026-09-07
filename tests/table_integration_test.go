package tests

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type tableIntegrationTelemetry struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	Region      string    `gorm:"column:region" iotdb:"tag"`
	DeviceID    string    `gorm:"column:device_id" iotdb:"tag"`
	Description string    `gorm:"column:description" iotdb:"attribute"`
	Temperature float64   `gorm:"column:temperature" iotdb:"field"`
	Counter     int64     `gorm:"column:counter" iotdb:"field"`
	Note        *string   `gorm:"column:note" iotdb:"field"`
}

// TestTableIntegrationCRUD verifies IoTDB 2.0.x DDL, relational Tablet, NULL, and GORM query.
func TestTableIntegrationCRUD(t *testing.T) {
	if os.Getenv("IOTDB_TABLE_INTEGRATION") != "1" {
		t.Skip("set IOTDB_TABLE_INTEGRATION=1 to run against a real IoTDB 2.0.x TableModel server")
	}
	nodeURL := requiredIntegrationEnv(t, "IOTDB_TABLE_NODE_URL")
	database := requiredIntegrationEnv(t, "IOTDB_TABLE_DATABASE")
	username := os.Getenv("IOTDB_TABLE_USERNAME")
	if username == "" {
		username = "root"
	}
	password := os.Getenv("IOTDB_TABLE_PASSWORD")
	if password == "" {
		password = "root"
	}
	db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
		ModelMode:              gormiotdb.TableModel,
		NodeURLs:               []string{nodeURL},
		Username:               username,
		Password:               password,
		Database:               database,
		PoolSize:               4,
		BatchSize:              256,
		TableInsertConcurrency: 2,
	}), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open TableModel integration database: %v", err)
	}
	t.Cleanup(func() {
		if err := gormiotdb.Close(db); err != nil {
			t.Errorf("close TableModel integration database: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := gormiotdb.Ping(ctx, db); err != nil {
		t.Fatalf("ping TableModel integration database: %v", err)
	}

	table := "gorm_table_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	defer func() {
		if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			t.Errorf("cleanup table %s: %v", table, err)
		}
	}()
	target := db.Table(table)
	if err := target.AutoMigrate(&tableIntegrationTelemetry{}); err != nil {
		t.Fatalf("AutoMigrate TableModel: %v", err)
	}
	note := "stable"
	base := time.Now().UTC().Truncate(time.Millisecond)
	rows := []tableIntegrationTelemetry{
		{Time: base.Add(time.Millisecond), Region: "cn", DeviceID: "d1", Description: "sensor", Temperature: 21, Counter: 1, Note: &note},
		{Time: base, Region: "cn", DeviceID: "d1", Description: "sensor", Temperature: 20, Counter: 0, Note: nil},
	}
	result := target.CreateInBatches(&rows, 128)
	if result.Error != nil {
		t.Fatalf("CreateInBatches TableModel: %v", result.Error)
	}
	if result.RowsAffected != 2 {
		t.Fatalf("expected 2 affected rows, got %d", result.RowsAffected)
	}
	var found []tableIntegrationTelemetry
	if err := target.Where("region = ? AND device_id = ? AND time >= ?", "cn", "d1", base).Order("time asc").Find(&found).Error; err != nil {
		t.Fatalf("query TableModel rows: %v", err)
	}
	if len(found) != 2 || found[0].Counter != 0 || found[0].Note != nil || found[1].Note == nil {
		t.Fatalf("unexpected TableModel rows: %+v", found)
	}
}
