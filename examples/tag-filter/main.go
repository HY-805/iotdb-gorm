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
	Time     time.Time `gorm:"column:time" iotdb:"time"`
	Region   string    `gorm:"column:region" iotdb:"tag"`
	DeviceID string    `gorm:"column:device_id" iotdb:"tag"`
	Temp     float64   `gorm:"column:temp" iotdb:"field"`
}

// main demonstrates explicit TableModel TAG predicates.
func main() {
	dsn := os.Getenv("IOTDB_DSN")
	if dsn == "" {
		dsn = "iotdb://root:root@127.0.0.1:6667/example?model=table"
	}

	db, err := gorm.Open(gormiotdb.Open(dsn), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		log.Fatal(err)
	}
	defer gormiotdb.Close(db)

	var rows []Telemetry
	if err := db.Table("telemetry").Where("region = ?", "cn").Where("device_id = ?", "d1").Find(&rows).Error; err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(rows))
}
