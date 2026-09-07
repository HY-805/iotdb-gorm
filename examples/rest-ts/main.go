package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	gormiotdb "github.com/HY-805/iotdb-gorm"
	"gorm.io/gorm"
)

type TelemetryRequest struct {
	Time     time.Time `json:"time"`
	Region   string    `json:"region"`
	DeviceID string    `json:"device_id"`
	Temp     float64   `json:"temp"`
}

type Telemetry struct {
	Time       time.Time `gorm:"column:time" iotdb:"time"`
	DevicePath string    `gorm:"column:device_path" iotdb:"device"`
	Region     string    `gorm:"column:region"`
	Temp       float64   `gorm:"column:temp"`
}

// @Summary ingest telemetry
// @Accept json
// @Produce json
// @Param payload body TelemetryRequest true "telemetry payload"
// @Success 202 {object} map[string]string
// @Router /telemetry [post]
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

	http.HandleFunc("/telemetry", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload TelemetryRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		record := Telemetry{Time: payload.Time, DevicePath: payload.DeviceID, Region: payload.Region, Temp: payload.Temp}
		if err := db.Table("telemetry").Create(&record).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
