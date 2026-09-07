package model

import (
	"reflect"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm/schema"
)

type telemetry struct {
	Time     time.Time `gorm:"column:time;iotdb:time"`
	Region   string    `gorm:"column:region;iotdb:tag"`
	DeviceID string    `gorm:"column:device_id;iotdb:tag"`
	Temp     float64   `gorm:"column:temp"`
	Path     string    `gorm:"column:path" iotdb:"device"`
}

func TestParseColumns(t *testing.T) {
	var cache sync.Map
	s, err := schema.Parse(&telemetry{}, &cache, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}

	columns := ParseColumns(s)
	if len(columns) != 5 {
		t.Fatalf("expected 5 columns, got %d", len(columns))
	}
	if columns[0].Role != ColumnRoleTime || columns[1].Role != ColumnRoleTag || columns[4].Role != ColumnRoleDevice {
		t.Fatalf("unexpected roles: %+v", columns)
	}
}

func TestTagValueMap(t *testing.T) {
	var cache sync.Map
	s, err := schema.Parse(&telemetry{}, &cache, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}

	values := TagValueMap(s, reflect.ValueOf(telemetry{Region: "cn", DeviceID: "d1"}))
	if values["region"] != "cn" {
		t.Fatalf("expected region cn, got %v", values["region"])
	}
}
