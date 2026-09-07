package main

import (
	"log"
	"os"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type Telemetry struct {
	Time       time.Time `gorm:"column:time;iotdb:time"`
	DevicePath string    `gorm:"column:device_path" iotdb:"device"`
	Temp       float64   `gorm:"column:temp"`
}

// main demonstrates multi-device CreateInBatches through InsertAlignedTablets.
func main() {
	dsn := os.Getenv("IOTDB_DSN")
	if dsn == "" {
		dsn = "iotdb://root:root@127.0.0.1:6667/root.example"
	}

	db, err := gorm.Open(gormiotdb.New(gormiotdb.Config{
		DSN: dsn,
	}), &gorm.Config{SkipDefaultTransaction: true, CreateBatchSize: 100})
	if err != nil {
		log.Fatal(err)
	}
	defer gormiotdb.Close(db)

	batch := []Telemetry{
		{Time: time.Now(), DevicePath: "device_cn", Temp: 20.5},
		{Time: time.Now(), DevicePath: "device_us", Temp: 21.5},
	}
	if err := db.Table("logical_batch").CreateInBatches(&batch, 100).Error; err != nil {
		log.Fatal(err)
	}
}
