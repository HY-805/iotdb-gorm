package main

import (
	"fmt"
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

// main demonstrates a bounded TreeModel query using regular GORM clauses.
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

	var rows []Telemetry
	if err := db.Table("device001").Where("time >= ?", time.Now().Add(-time.Hour)).Order("time desc").Limit(10).Find(&rows).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(rows))
}
