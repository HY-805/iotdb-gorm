package dialector

import (
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/HY-805/iotdb-gorm/internal/backend"
	"github.com/HY-805/iotdb-gorm/internal/model"
	"github.com/apache/iotdb-client-go/v2/client"
	"gorm.io/gorm/schema"
)

// dataTypeName maps one parsed column to an IoTDB type name.
func dataTypeName(column model.Column, mode ModelMode) string {
	if explicit := explicitDataType(column); explicit != "" {
		return explicit
	}
	if column.Role == model.ColumnRoleTime {
		return "TIMESTAMP"
	}
	if mode == TableModel && (column.Role == model.ColumnRoleTag || column.Role == model.ColumnRoleAttribute) {
		return "STRING"
	}

	field := column.Field
	switch field.DataType {
	case schema.Bool:
		return "BOOLEAN"
	case schema.Int, schema.Uint:
		if field.Size > 0 && field.Size <= 32 {
			return "INT32"
		}
		return "INT64"
	case schema.Float:
		if field.Size > 0 && field.Size <= 32 {
			return "FLOAT"
		}
		return "DOUBLE"
	case schema.Bytes:
		return "BLOB"
	case schema.Time:
		return "TIMESTAMP"
	case schema.String:
		if mode == TableModel && column.Role != model.ColumnRoleField {
			return "STRING"
		}
		return "TEXT"
	default:
		return "TEXT"
	}
}

// explicitDataType returns a normalized type from dedicated or GORM tags.
func explicitDataType(column model.Column) string {
	if value := strings.TrimSpace(column.Options["type"]); value != "" {
		return strings.ToUpper(value)
	}
	if value := strings.TrimSpace(column.Field.TagSettings["TYPE"]); value != "" {
		return strings.ToUpper(value)
	}
	return ""
}

// clientDataType validates and converts the mapped type.
func clientDataType(column model.Column, mode ModelMode) (client.TSDataType, error) {
	name := dataTypeName(column, mode)
	dataType, err := client.GetDataTypeByStr(name)
	if err != nil {
		return client.UNKNOWN, fmt.Errorf("iotdb: field %s has unsupported type %s: %w", column.Field.Name, name, err)
	}
	return dataType, nil
}

// treeMeasurement converts one field into official schema metadata.
func treeMeasurement(column model.Column) (backend.Measurement, error) {
	dataType, err := clientDataType(column, TreeModel)
	if err != nil {
		return backend.Measurement{}, err
	}
	encoding, err := measurementEncoding(column.Options["encoding"], dataType)
	if err != nil {
		return backend.Measurement{}, fmt.Errorf("iotdb: field %s: %w", column.Field.Name, err)
	}
	compression, err := measurementCompression(firstNonEmpty(column.Options["compression"], column.Options["compressor"]))
	if err != nil {
		return backend.Measurement{}, fmt.Errorf("iotdb: field %s: %w", column.Field.Name, err)
	}
	return backend.Measurement{
		Name:        column.Field.DBName,
		DataType:    dataType,
		Encoding:    encoding,
		Compression: compression,
	}, nil
}

// measurementEncoding applies portable defaults and accepts explicit encodings.
func measurementEncoding(raw string, dataType client.TSDataType) (client.TSEncoding, error) {
	if raw == "" {
		switch dataType {
		case client.INT32, client.INT64, client.TIMESTAMP:
			return client.TS_2DIFF, nil
		case client.FLOAT, client.DOUBLE:
			return client.GORILLA, nil
		default:
			return client.PLAIN, nil
		}
	}
	encodings := map[string]client.TSEncoding{
		"PLAIN": client.PLAIN, "DICTIONARY": client.DICTIONARY, "RLE": client.RLE,
		"DIFF": client.DIFF, "TS_2DIFF": client.TS_2DIFF, "BITMAP": client.BITMAP,
		"GORILLA": client.GORILLA, "ZIGZAG": client.ZIGZAG, "CHIMP": client.CHIMP,
		"SPRINTZ": client.SPRINTZ, "RLBE": client.RLBE,
	}
	value, ok := encodings[strings.ToUpper(strings.TrimSpace(raw))]
	if !ok {
		return client.PLAIN, fmt.Errorf("unsupported encoding %q", raw)
	}
	return value, nil
}

// measurementCompression applies SNAPPY unless explicitly configured.
func measurementCompression(raw string) (client.TSCompressionType, error) {
	if raw == "" {
		return client.SNAPPY, nil
	}
	compressions := map[string]client.TSCompressionType{
		"UNCOMPRESSED": client.UNCOMPRESSED, "SNAPPY": client.SNAPPY,
		"GZIP": client.GZIP, "LZ4": client.LZ4, "ZSTD": client.ZSTD,
		"LZMA2": client.LZMA2,
	}
	value, ok := compressions[strings.ToUpper(strings.TrimSpace(raw))]
	if !ok {
		return client.SNAPPY, fmt.Errorf("unsupported compression %q", raw)
	}
	return value, nil
}

// normalizeTabletValue converts a model value to the exact official Tablet type.
func normalizeTabletValue(value any, dataType client.TSDataType, precision TimePrecision) (any, error) {
	converted, err := unwrapValue(value)
	if err != nil || converted == nil {
		return converted, err
	}
	reflected := reflect.ValueOf(converted)

	switch dataType {
	case client.BOOLEAN:
		if reflected.Kind() != reflect.Bool {
			return nil, typeMismatch(dataType, converted)
		}
		return reflected.Bool(), nil
	case client.INT32:
		value, ok := signedInteger(reflected, 32)
		if !ok {
			return nil, typeMismatch(dataType, converted)
		}
		return int32(value), nil
	case client.INT64:
		value, ok := signedInteger(reflected, 64)
		if !ok {
			return nil, typeMismatch(dataType, converted)
		}
		return value, nil
	case client.FLOAT:
		value, ok := floatingPoint(reflected)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value > math.MaxFloat32 || value < -math.MaxFloat32 {
			return nil, typeMismatch(dataType, converted)
		}
		return float32(value), nil
	case client.DOUBLE:
		value, ok := floatingPoint(reflected)
		if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, typeMismatch(dataType, converted)
		}
		return value, nil
	case client.TEXT, client.STRING:
		switch value := converted.(type) {
		case string:
			return value, nil
		case []byte:
			return value, nil
		default:
			return nil, typeMismatch(dataType, converted)
		}
	case client.BLOB:
		value, ok := converted.([]byte)
		if !ok {
			return nil, typeMismatch(dataType, converted)
		}
		return value, nil
	case client.TIMESTAMP:
		if value, ok := converted.(time.Time); ok {
			return timestampFromTime(value, precision), nil
		}
		value, ok := signedInteger(reflected, 64)
		if !ok {
			return nil, typeMismatch(dataType, converted)
		}
		return value, nil
	case client.DATE:
		value, ok := converted.(time.Time)
		if !ok {
			return nil, typeMismatch(dataType, converted)
		}
		return value, nil
	default:
		return nil, fmt.Errorf("iotdb: unsupported Tablet type %v", dataType)
	}
}

// timestampValue validates a required row timestamp.
func timestampValue(value any, precision TimePrecision) (int64, error) {
	converted, err := unwrapValue(value)
	if err != nil {
		return 0, err
	}
	if converted == nil {
		return 0, fmt.Errorf("iotdb: time value is NULL")
	}
	if typed, ok := converted.(time.Time); ok {
		if typed.IsZero() {
			return 0, fmt.Errorf("iotdb: time value is zero")
		}
		return timestampFromTime(typed, precision), nil
	}
	value64, ok := signedInteger(reflect.ValueOf(converted), 64)
	if !ok {
		return 0, fmt.Errorf("iotdb: time value must be time.Time or an integer, got %T", converted)
	}
	return value64, nil
}

// timestampFromTime converts a time according to the configured server precision.
func timestampFromTime(value time.Time, precision TimePrecision) int64 {
	switch precision {
	case Microseconds:
		return value.UnixMicro()
	case Nanoseconds:
		return value.UnixNano()
	default:
		return value.UnixMilli()
	}
}

// unwrapValue resolves pointers, interfaces, and database/sql Valuer wrappers.
func unwrapValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	if valuer, ok := value.(driver.Valuer); ok {
		return valuer.Value()
	}
	reflected := reflect.ValueOf(value)
	for reflected.IsValid() && (reflected.Kind() == reflect.Pointer || reflected.Kind() == reflect.Interface) {
		if reflected.IsNil() {
			return nil, nil
		}
		reflected = reflected.Elem()
	}
	if !reflected.IsValid() {
		return nil, nil
	}
	return reflected.Interface(), nil
}

// signedInteger converts signed and bounded unsigned integer kinds.
func signedInteger(value reflect.Value, bits int) (int64, bool) {
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := value.Int()
		if bits == 32 && (integer < math.MinInt32 || integer > math.MaxInt32) {
			return 0, false
		}
		return integer, true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer := value.Uint()
		limit := uint64(math.MaxInt64)
		if bits == 32 {
			limit = math.MaxInt32
		}
		if integer > limit {
			return 0, false
		}
		return int64(integer), true
	default:
		return 0, false
	}
}

// floatingPoint converts float kinds without silently accepting integers.
func floatingPoint(value reflect.Value) (float64, bool) {
	switch value.Kind() {
	case reflect.Float32, reflect.Float64:
		return value.Float(), true
	default:
		return 0, false
	}
}

// typeMismatch formats a consistent model-to-Tablet conversion error.
func typeMismatch(dataType client.TSDataType, value any) error {
	return fmt.Errorf("iotdb: value %T is incompatible with Tablet type %v", value, dataType)
}

// firstNonEmpty returns the first configured option.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
