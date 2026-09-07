package main

import (
	"log"
	"os"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type Telemetry struct {
	Time     time.Time `gorm:"column:time;iotdb:time"`
	Region   string    `gorm:"column:region"`
	DeviceID string    `gorm:"column:device_id"`
	Temp     float64   `gorm:"column:temp"`
}

// main demonstrates TreeModel migration and one-row Tablet insertion.
func main() {
	dsn := os.Getenv("IOTDB_DSN")
	if dsn == "" {
		dsn = "iotdb://root:root@127.0.0.1:6667/root.example"
	}

	db, err := gorm.Open(gormiotdb.Open(dsn), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		log.Fatal(err)
	}
	defer gormiotdb.Close(db)
	device := db.Table("device001")
	if err := device.AutoMigrate(&Telemetry{}); err != nil {
		log.Fatal(err)
	}
	if err := device.Create(&Telemetry{Time: time.Now(), Region: "cn", DeviceID: "d1", Temp: 20.5}).Error; err != nil {
		log.Fatal(err)
	}
}
