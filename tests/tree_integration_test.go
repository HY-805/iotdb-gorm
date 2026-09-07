package tests

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type treeIntegrationTelemetry struct {
	Time        time.Time `gorm:"column:time" iotdb:"time"`
	DevicePath  string    `gorm:"column:device_path" iotdb:"device"`
	Temperature float64   `gorm:"column:temperature"`
	Online      bool      `gorm:"column:online"`
	Counter     int64     `gorm:"column:counter"`
	Note        *string   `gorm:"column:note"`
}

// TestTreeIntegrationAlignedCRUD verifies migration, Tablet insert, NULL, and GORM query on IoTDB 1.3.1.
func TestTreeIntegrationAlignedCRUD(t *testing.T) {
	db, database := openTreeIntegrationDB(t, true, "IOTDB_TREE_DATABASE")
	device := integrationDevice("aligned")
	defer cleanupTreeDevice(t, db, database+"."+device)

	target := db.Table(device)
	if err := target.AutoMigrate(&treeIntegrationTelemetry{}); err != nil {
		t.Fatalf("AutoMigrate aligned device: %v", err)
	}
	note := "normal"
	base := time.Now().UTC().Truncate(time.Millisecond)
	rows := []treeIntegrationTelemetry{
		{Time: base.Add(2 * time.Millisecond), Temperature: 22.5, Online: true, Counter: 2, Note: &note},
		{Time: base, Temperature: 20.5, Online: false, Counter: 0, Note: nil},
		{Time: base.Add(time.Millisecond), Temperature: 21.5, Online: true, Counter: 1, Note: &note},
	}
	result := target.CreateInBatches(&rows, 128)
	if result.Error != nil {
		t.Fatalf("CreateInBatches aligned rows: %v", result.Error)
	}
	if result.RowsAffected != int64(len(rows)) {
		t.Fatalf("expected %d affected rows, got %d", len(rows), result.RowsAffected)
	}

	var found []treeIntegrationTelemetry
	if err := target.Where("time >= ? AND time <= ?", base, base.Add(2*time.Millisecond)).Order("time asc").Find(&found).Error; err != nil {
		t.Fatalf("query aligned rows: %v", err)
	}
	if len(found) != 3 || found[0].Counter != 0 || found[1].Counter != 1 || found[2].Counter != 2 {
		t.Fatalf("unexpected ordered rows: %+v", found)
	}
	if found[0].Note != nil || found[1].Note == nil || *found[1].Note != note {
		t.Fatalf("unexpected NULL/text values: %+v", found)
	}
}

// TestTreeIntegrationNonAlignedTablet verifies the explicit non-aligned write path.
func TestTreeIntegrationNonAlignedTablet(t *testing.T) {
	db, database := openTreeIntegrationDB(t, false, "IOTDB_TREE_DATABASE")
	device := integrationDevice("nonaligned")
	defer cleanupTreeDevice(t, db, database+"."+device)

	target := db.Table(device)
	if err := target.AutoMigrate(&treeIntegrationTelemetry{}); err != nil {
		t.Fatalf("AutoMigrate non-aligned device: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	rows := []treeIntegrationTelemetry{
		{Time: base, Temperature: 10, Online: true, Counter: 10},
		{Time: base.Add(time.Millisecond), Temperature: 11, Online: true, Counter: 11},
	}
	if err := target.Create(&rows).Error; err != nil {
		t.Fatalf("insert non-aligned Tablet: %v", err)
	}
	var found []treeIntegrationTelemetry
	if err := target.Select("temperature, online, counter").Where("time >= ?", base).Order("time asc").Find(&found).Error; err != nil {
		t.Fatalf("query non-aligned rows: %v", err)
	}
	if len(found) != 2 || found[0].Counter != 10 || found[1].Counter != 11 {
		t.Fatalf("unexpected non-aligned rows: %+v", found)
	}
}

// TestTreeIntegrationMultiDeviceBatch verifies one GORM batch becomes multiple official device Tablets.
func TestTreeIntegrationMultiDeviceBatch(t *testing.T) {
	db, database := openTreeIntegrationDB(t, true, "IOTDB_TREE_SECOND_DATABASE")
	deviceA := integrationDevice("multi_a")
	deviceB := integrationDevice("multi_b")
	fullA := database + "." + deviceA
	fullB := database + "." + deviceB
	defer cleanupTreeDevice(t, db, fullA)
	defer cleanupTreeDevice(t, db, fullB)
	if err := db.Table(deviceA).AutoMigrate(&treeIntegrationTelemetry{}); err != nil {
		t.Fatalf("migrate first device: %v", err)
	}
	if err := db.Table(deviceB).AutoMigrate(&treeIntegrationTelemetry{}); err != nil {
		t.Fatalf("migrate second device: %v", err)
	}

	base := time.Now().UTC().Truncate(time.Millisecond)
	rows := []treeIntegrationTelemetry{
		{Time: base.Add(time.Millisecond), DevicePath: deviceA, Temperature: 1, Counter: 1},
		{Time: base, DevicePath: fullB, Temperature: 2, Counter: 2},
		{Time: base, DevicePath: deviceA, Temperature: 3, Counter: 3},
	}
	if err := db.Table("logical_batch").Create(&rows).Error; err != nil {
		t.Fatalf("insert multi-device batch: %v", err)
	}
	for _, device := range []string{deviceA, deviceB} {
		var found []treeIntegrationTelemetry
		if err := db.Table(device).Select("temperature, online, counter").Where("time >= ?", base).Order("time asc").Find(&found).Error; err != nil {
			t.Fatalf("query device %s: %v", device, err)
		}
		if len(found) == 0 {
			t.Fatalf("expected data for device %s", device)
		}
	}
}

// openTreeIntegrationDB opens an opt-in real IoTDB connection and registers cleanup.
func openTreeIntegrationDB(t *testing.T, aligned bool, databaseEnv string) (*gorm.DB, string) {
	t.Helper()
	if os.Getenv("IOTDB_TREE_INTEGRATION") != "1" {
		t.Skip("set IOTDB_TREE_INTEGRATION=1 to run against a real IoTDB 1.3.1 server")
	}
	nodeURL := requiredIntegrationEnv(t, "IOTDB_TREE_NODE_URL")
	database := os.Getenv(databaseEnv)
	if database == "" && databaseEnv == "IOTDB_TREE_SECOND_DATABASE" {
		database = os.Getenv("IOTDB_TREE_DATABASE")
	}
	if database == "" {
		t.Fatalf("%s is required", databaseEnv)
	}
	username := os.Getenv("IOTDB_TREE_USERNAME")
	if username == "" {
		username = "root"
	}
	password := os.Getenv("IOTDB_TREE_PASSWORD")
	if password == "" {
		password = "root"
	}
	db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
		ModelMode:    gormiotdb.TreeModel,
		NodeURLs:     []string{nodeURL},
		Username:     username,
		Password:     password,
		Database:     database,
		Aligned:      gormiotdb.Bool(aligned),
		PoolSize:     4,
		BatchSize:    256,
		QueryTimeout: 15 * time.Second,
	}), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatalf("open TreeModel integration database: %v", err)
	}
	t.Cleanup(func() {
		if err := gormiotdb.Close(db); err != nil {
			t.Errorf("close TreeModel integration database: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := gormiotdb.Ping(ctx, db); err != nil {
		t.Fatalf("ping TreeModel integration database: %v", err)
	}
	return db, database
}

// cleanupTreeDevice deletes only the unique measurements created by one test.
func cleanupTreeDevice(t *testing.T, db *gorm.DB, fullDevice string) {
	t.Helper()
	if err := db.Exec("DELETE TIMESERIES " + fullDevice + ".*").Error; err != nil {
		t.Errorf("cleanup %s: %v", fullDevice, err)
	}
}

// integrationDevice generates an identifier accepted by the strict TreeModel path validator.
func integrationDevice(prefix string) string {
	return "gorm_" + prefix + "_" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// requiredIntegrationEnv returns a required integration setting.
func requiredIntegrationEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}

// String keeps fmt imported in old Go versions where test diagnostics are optimized.
func (value treeIntegrationTelemetry) String() string {
	return fmt.Sprintf("%s:%f", value.Time.Format(time.RFC3339Nano), value.Temperature)
}
