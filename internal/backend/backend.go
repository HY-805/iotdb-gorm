// Package backend contains the thin runtime boundary around Apache's official
// IoTDB Go client.
package backend

import (
	"context"
	"errors"
	"time"

	"github.com/apache/iotdb-client-go/v2/client"
)

// ModelMode selects the IoTDB data model used by a dialector.
type ModelMode uint8

const (
	// TreeModel targets IoTDB's hierarchical time-series model.
	TreeModel ModelMode = iota
	// TableModel targets IoTDB's relational table model.
	TableModel
)

// ErrClosed reports use of a backend after its lifecycle has ended.
var ErrClosed = errors.New("iotdb: backend is closed")

// Config contains the official-client settings needed by a backend.
type Config struct {
	Mode                   ModelMode
	NodeURLs               []string
	Username               string
	Password               string
	Database               string
	PoolSize               int
	ConnectTimeout         time.Duration
	AcquireTimeout         time.Duration
	QueryTimeout           time.Duration
	FetchSize              int32
	TimeZone               string
	ConnectRetryMax        int
	EnableRPCCompression   bool
	TableInsertConcurrency int
}

// Measurement describes one TreeModel time series.
type Measurement struct {
	Name        string
	DataType    client.TSDataType
	Encoding    client.TSEncoding
	Compression client.TSCompressionType
}

// TreeDeviceSchema is the server-side schema currently visible for a device.
type TreeDeviceSchema struct {
	Exists       bool
	Aligned      bool
	Measurements map[string]client.TSDataType
}

// ResultSet is the minimal result contract consumed by the database/sql bridge.
type ResultSet interface {
	ColumnNames() []string
	ColumnTypes() []string
	Next() (bool, error)
	Value(index int) (any, error)
	Close() error
}

// Backend is the model-independent runtime used by the GORM adapter.
type Backend interface {
	Mode() ModelMode
	Ping(context.Context) error
	Exec(context.Context, string) error
	Query(context.Context, string) (ResultSet, error)
	InsertTablets(context.Context, []*client.Tablet, bool) error
	InspectTreeDevice(context.Context, string) (TreeDeviceSchema, error)
	EnsureTreeSchema(context.Context, string, []Measurement, bool) error
	Close() error
}
