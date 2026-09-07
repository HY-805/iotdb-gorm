package iotdbsql

import (
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// TimePrecision controls conversion of time.Time bind values to IoTDB integers.
type TimePrecision uint8

const (
	// Milliseconds is IoTDB's common default timestamp precision.
	Milliseconds TimePrecision = iota
	// Microseconds converts timestamps to Unix microseconds.
	Microseconds
	// Nanoseconds converts timestamps to Unix nanoseconds.
	Nanoseconds
)

// Binder safely renders value literals because the official client exposes SQL strings.
type Binder struct {
	TimePrecision TimePrecision
}

// Bind replaces positional placeholders outside quoted strings and comments.
func (b Binder) Bind(query string, args []driver.NamedValue) (string, error) {
	literals := make([]string, len(args))
	for i := range args {
		if args[i].Name != "" {
			return "", fmt.Errorf("iotdb: named bind %q is not supported", args[i].Name)
		}
		literal, err := b.literal(args[i].Value)
		if err != nil {
			return "", fmt.Errorf("iotdb: bind argument %d: %w", i+1, err)
		}
		literals[i] = literal
	}

	var output strings.Builder
	output.Grow(len(query) + len(args)*8)
	argument := 0
	state := bindStateNormal
	for i := 0; i < len(query); i++ {
		current := query[i]
		next := byte(0)
		if i+1 < len(query) {
			next = query[i+1]
		}

		switch state {
		case bindStateNormal:
			switch {
			case current == '\'':
				state = bindStateSingleQuote
			case current == '"':
				state = bindStateDoubleQuote
			case current == '`':
				state = bindStateBacktick
			case current == '-' && next == '-':
				state = bindStateLineComment
			case current == '/' && next == '*':
				state = bindStateBlockComment
			case current == '?':
				if argument >= len(literals) {
					return "", errors.New("iotdb: more placeholders than bind arguments")
				}
				output.WriteString(literals[argument])
				argument++
				continue
			}
		case bindStateSingleQuote:
			if current == '\'' {
				if next == '\'' {
					output.WriteByte(current)
					i++
					output.WriteByte(next)
					continue
				}
				state = bindStateNormal
			}
		case bindStateDoubleQuote:
			if current == '"' {
				state = bindStateNormal
			}
		case bindStateBacktick:
			if current == '`' {
				state = bindStateNormal
			}
		case bindStateLineComment:
			if current == '\n' {
				state = bindStateNormal
			}
		case bindStateBlockComment:
			if current == '*' && next == '/' {
				output.WriteByte(current)
				i++
				output.WriteByte(next)
				state = bindStateNormal
				continue
			}
		}
		output.WriteByte(current)
	}
	if argument != len(literals) {
		return "", fmt.Errorf("iotdb: %d bind argument(s) were not consumed", len(literals)-argument)
	}
	return output.String(), nil
}

// literal converts one Go value into a validated IoTDB SQL literal.
func (b Binder) literal(value any) (string, error) {
	if value == nil {
		return "NULL", nil
	}
	if valuer, ok := value.(driver.Valuer); ok {
		converted, err := valuer.Value()
		if err != nil {
			return "", err
		}
		return b.literal(converted)
	}

	switch typed := value.(type) {
	case string:
		return "'" + strings.ReplaceAll(typed, "'", "''") + "'", nil
	case []byte:
		return "X'" + strings.ToUpper(hex.EncodeToString(typed)) + "'", nil
	case bool:
		return strconv.FormatBool(typed), nil
	case time.Time:
		return strconv.FormatInt(b.timestamp(typed), 10), nil
	case int:
		return strconv.FormatInt(int64(typed), 10), nil
	case int8:
		return strconv.FormatInt(int64(typed), 10), nil
	case int16:
		return strconv.FormatInt(int64(typed), 10), nil
	case int32:
		return strconv.FormatInt(int64(typed), 10), nil
	case int64:
		return strconv.FormatInt(typed, 10), nil
	case uint:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), 10), nil
	case uint64:
		return strconv.FormatUint(typed, 10), nil
	case float32:
		return formatFloat(float64(typed), 32)
	case float64:
		return formatFloat(typed, 64)
	}

	reflected := reflect.ValueOf(value)
	if reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return "NULL", nil
		}
		return b.literal(reflected.Elem().Interface())
	}
	return "", fmt.Errorf("unsupported value type %T", value)
}

// timestamp converts a time according to the configured server precision.
func (b Binder) timestamp(value time.Time) int64 {
	switch b.TimePrecision {
	case Microseconds:
		return value.UnixMicro()
	case Nanoseconds:
		return value.UnixNano()
	default:
		return value.UnixMilli()
	}
}

// formatFloat rejects non-finite values that IoTDB cannot persist portably.
func formatFloat(value float64, bits int) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", errors.New("non-finite float is not supported")
	}
	return strconv.FormatFloat(value, 'g', -1, bits), nil
}

// bindState tracks syntax where question marks are not placeholders.
type bindState uint8

const (
	bindStateNormal bindState = iota
	bindStateSingleQuote
	bindStateDoubleQuote
	bindStateBacktick
	bindStateLineComment
	bindStateBlockComment
)
