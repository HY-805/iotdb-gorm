package dialector

import (
	"strconv"
	"testing"
	"time"

	"github.com/HY-805/iotdb-gorm/internal/backend"
	"github.com/apache/iotdb-client-go/v2/client"
	"gorm.io/gorm"
)

// BenchmarkCreateBatchTablet measures GORM-to-Tablet conversion without network I/O.
func BenchmarkCreateBatchTablet(b *testing.B) {
	for _, rowCount := range []int{1, 100, 1000, 5000} {
		b.Run(strconv.Itoa(rowCount), func(b *testing.B) {
			runtime := &mockBackend{mode: backend.TreeModel}
			d := &Dialector{config: Config{
				NodeURLs: []string{"127.0.0.1:6667"}, Database: "root.benchmark", BatchSize: 1000,
			}, backend: runtime}
			db, err := gorm.Open(d, &gorm.Config{SkipDefaultTransaction: true, DisableAutomaticPing: true})
			if err != nil {
				b.Fatal(err)
			}
			rows := benchmarkRows(rowCount)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				runtime.tablets = runtime.tablets[:0]
				if err := db.Table("device001").Create(&rows).Error; err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkOfficialTabletBuild measures direct official Tablet construction as a local baseline.
func BenchmarkOfficialTabletBuild(b *testing.B) {
	for _, rowCount := range []int{1, 100, 1000, 5000} {
		b.Run(strconv.Itoa(rowCount), func(b *testing.B) {
			rows := benchmarkRows(rowCount)
			schemas := []*client.MeasurementSchema{
				{Measurement: "temp", DataType: client.DOUBLE},
				{Measurement: "count", DataType: client.INT64},
				{Measurement: "note", DataType: client.TEXT},
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				tablet, err := client.NewTablet("root.benchmark.device001", schemas, rowCount)
				if err != nil {
					b.Fatal(err)
				}
				for rowIndex, row := range rows {
					tablet.SetTimestamp(row.Time.UnixMilli(), rowIndex)
					if err := tablet.SetValueAt(row.Temp, 0, rowIndex); err != nil {
						b.Fatal(err)
					}
					if err := tablet.SetValueAt(row.Count, 1, rowIndex); err != nil {
						b.Fatal(err)
					}
					if err := tablet.SetValueAt(*row.Note, 2, rowIndex); err != nil {
						b.Fatal(err)
					}
					tablet.RowSize++
				}
			}
		})
	}
}

// benchmarkRows creates deterministic, already-sorted telemetry input.
func benchmarkRows(count int) []treeTelemetry {
	rows := make([]treeTelemetry, count)
	base := time.UnixMilli(1_700_000_000_000)
	note := "benchmark"
	for i := range rows {
		rows[i] = treeTelemetry{
			Time: base.Add(time.Duration(i) * time.Millisecond), DevicePath: "device001",
			Temp: float64(i), Count: int64(i), Note: &note,
		}
	}
	return rows
}
