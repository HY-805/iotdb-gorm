package dialector

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/HY-805/iotdb-gorm/internal/model"
	"github.com/apache/iotdb-client-go/v2/client"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
	"gorm.io/gorm/schema"
)

// tabletColumn contains the official type and role for one persisted field.
type tabletColumn struct {
	column   model.Column
	dataType client.TSDataType
	category client.ColumnCategory
}

// preparedRow is a converted row ready for deterministic batching.
type preparedRow struct {
	timestamp int64
	values    []any
	bytes     int
}

// tabletGroup contains rows sharing one device or relational table.
type tabletGroup struct {
	target string
	rows   []preparedRow
}

// createCallback routes runtime writes directly to the official Tablet APIs.
func (d *Dialector) createCallback() func(*gorm.DB) {
	dryRunCreate := callbacks.Create(&callbacks.Config{})
	return func(db *gorm.DB) {
		if db.Error != nil {
			return
		}
		if db.DryRun {
			dryRunCreate(db)
			return
		}
		if d.backend == nil {
			if d.resolved.AllowSQLFallback {
				dryRunCreate(db)
				return
			}
			_ = db.AddError(fmt.Errorf("iotdb: Tablet backend is unavailable and SQL fallback is disabled"))
			return
		}

		tablets, rowCount, err := d.buildTablets(db.Statement)
		if err != nil {
			_ = db.AddError(err)
			return
		}
		if err := d.backend.InsertTablets(db.Statement.Context, tablets, d.resolved.AlignedValue); err != nil {
			_ = db.AddError(fmt.Errorf("iotdb: create %d row(s) in %d tablet(s): %w", rowCount, len(tablets), err))
			return
		}
		db.RowsAffected = int64(rowCount)
	}
}

// buildTablets converts one GORM Create destination to bounded official Tablets.
func (d *Dialector) buildTablets(statement *gorm.Statement) ([]*client.Tablet, int, error) {
	if statement.Schema == nil {
		return nil, 0, fmt.Errorf("iotdb: Create requires a struct schema")
	}
	rows, err := reflectRows(statement)
	if err != nil {
		return nil, 0, err
	}
	timeField, deviceField, columns, err := d.writeColumns(statement)
	if err != nil {
		return nil, 0, err
	}
	groups, order, err := d.prepareGroups(statement, rows, timeField, deviceField, columns)
	if err != nil {
		return nil, 0, err
	}

	tablets := make([]*client.Tablet, 0, len(groups))
	for _, target := range order {
		group := groups[target]
		sort.SliceStable(group.rows, func(i, j int) bool {
			return group.rows[i].timestamp < group.rows[j].timestamp
		})
		for i := 1; i < len(group.rows); i++ {
			if group.rows[i-1].timestamp == group.rows[i].timestamp {
				return nil, 0, fmt.Errorf("iotdb: duplicate timestamp %d in target %s", group.rows[i].timestamp, target)
			}
		}
		batches, err := d.tabletsForGroup(group, columns)
		if err != nil {
			return nil, 0, err
		}
		tablets = append(tablets, batches...)
	}
	return tablets, len(rows), nil
}

// reflectRows normalizes a struct or slice Create destination.
func reflectRows(statement *gorm.Statement) ([]reflect.Value, error) {
	value := indirectReflectValue(statement.ReflectValue)
	if !value.IsValid() {
		value = indirectReflectValue(reflect.ValueOf(statement.Dest))
	}
	if !value.IsValid() {
		return nil, fmt.Errorf("iotdb: invalid Create destination")
	}
	switch value.Kind() {
	case reflect.Struct:
		return []reflect.Value{value}, nil
	case reflect.Slice, reflect.Array:
		if value.Len() == 0 {
			return nil, gorm.ErrEmptySlice
		}
		rows := make([]reflect.Value, 0, value.Len())
		for i := 0; i < value.Len(); i++ {
			row := indirectReflectValue(value.Index(i))
			if !row.IsValid() || row.Kind() != reflect.Struct {
				return nil, fmt.Errorf("iotdb: Create row %d is not a struct", i)
			}
			rows = append(rows, row)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("iotdb: Create supports a struct or slice of structs, got %s", value.Kind())
	}
}

// writeColumns resolves time, device, and persisted columns after Select/Omit.
func (d *Dialector) writeColumns(statement *gorm.Statement) (*schema.Field, *schema.Field, []tabletColumn, error) {
	selected, restricted := statement.SelectAndOmitColumns(true, false)
	var timeField *schema.Field
	var deviceField *schema.Field
	columns := make([]tabletColumn, 0, len(statement.Schema.Fields))
	for _, parsed := range model.ParseColumns(statement.Schema) {
		field := parsed.Field
		role := parsed.Role
		if d.resolved.DevicePathField != "" &&
			(strings.EqualFold(field.Name, d.resolved.DevicePathField) || strings.EqualFold(field.DBName, d.resolved.DevicePathField)) {
			role = model.ColumnRoleDevice
			parsed.Role = role
		}
		switch role {
		case model.ColumnRoleTime:
			if timeField != nil && timeField != field {
				return nil, nil, nil, fmt.Errorf("iotdb: model %s has more than one time field", statement.Schema.Name)
			}
			if !isSelectedField(field.DBName, selected, restricted) {
				return nil, nil, nil, fmt.Errorf("iotdb: time field %s cannot be omitted", field.Name)
			}
			timeField = field
			continue
		case model.ColumnRoleDevice:
			if d.resolved.ModelMode != TreeModel {
				return nil, nil, nil, fmt.Errorf("iotdb: device fields are only supported in TreeModel")
			}
			if deviceField != nil && deviceField != field {
				return nil, nil, nil, fmt.Errorf("iotdb: model %s has more than one device field", statement.Schema.Name)
			}
			deviceField = field
			continue
		case model.ColumnRoleTag, model.ColumnRoleAttribute:
			if d.resolved.ModelMode == TreeModel {
				return nil, nil, nil, fmt.Errorf("iotdb: %s role on field %s is TableModel-only; TreeModel TAG metadata requires an explicit capability API", roleName(role), field.Name)
			}
		}
		if field.DBName == "" || !field.Creatable || !isSelectedField(field.DBName, selected, restricted) {
			continue
		}
		dataType, err := clientDataType(parsed, d.resolved.ModelMode)
		if err != nil {
			return nil, nil, nil, err
		}
		columns = append(columns, tabletColumn{
			column:   parsed,
			dataType: dataType,
			category: columnCategory(role),
		})
	}
	if timeField == nil {
		return nil, nil, nil, fmt.Errorf("iotdb: model %s requires one field tagged iotdb:\"time\" or named Time", statement.Schema.Name)
	}
	if len(columns) == 0 {
		return nil, nil, nil, fmt.Errorf("iotdb: model %s has no persisted measurements", statement.Schema.Name)
	}
	return timeField, deviceField, columns, nil
}

// prepareGroups converts values and groups TreeModel rows by device path.
func (d *Dialector) prepareGroups(statement *gorm.Statement, rows []reflect.Value, timeField, deviceField *schema.Field, columns []tabletColumn) (map[string]*tabletGroup, []string, error) {
	groups := make(map[string]*tabletGroup)
	order := make([]string, 0)
	for index, row := range rows {
		timeRaw, _ := timeField.ValueOf(statement.Context, row)
		timestamp, err := timestampValue(timeRaw, d.resolved.TimePrecision)
		if err != nil {
			return nil, nil, fmt.Errorf("iotdb: row %d field %s: %w", index, timeField.Name, err)
		}
		target, err := d.rowTarget(statement, row, deviceField)
		if err != nil {
			return nil, nil, fmt.Errorf("iotdb: row %d: %w", index, err)
		}

		prepared := preparedRow{timestamp: timestamp, values: make([]any, len(columns)), bytes: 8}
		for columnIndex, column := range columns {
			raw, _ := column.column.Field.ValueOf(statement.Context, row)
			value, valueErr := normalizeTabletValue(raw, column.dataType, d.resolved.TimePrecision)
			if valueErr != nil {
				return nil, nil, fmt.Errorf("iotdb: row %d field %s: %w", index, column.column.Field.Name, valueErr)
			}
			prepared.values[columnIndex] = value
			prepared.bytes += estimateValueBytes(value)
		}
		group, exists := groups[target]
		if !exists {
			group = &tabletGroup{target: target}
			groups[target] = group
			order = append(order, target)
		}
		group.rows = append(group.rows, prepared)
	}
	return groups, order, nil
}

// rowTarget resolves a table or a validated per-row TreeModel device.
func (d *Dialector) rowTarget(statement *gorm.Statement, row reflect.Value, deviceField *schema.Field) (string, error) {
	logicalTable := statement.Table
	if logicalTable == "" {
		logicalTable = statement.Schema.Table
	}
	target := logicalTable
	if d.resolved.DevicePathFunc != nil {
		resolved, err := d.resolved.DevicePathFunc(logicalTable, interfaceForRow(row))
		if err != nil {
			return "", fmt.Errorf("resolve device path: %w", err)
		}
		target = resolved
	} else if deviceField != nil {
		raw, _ := deviceField.ValueOf(statement.Context, row)
		value, err := unwrapValue(raw)
		if err != nil {
			return "", fmt.Errorf("read device field %s: %w", deviceField.Name, err)
		}
		path, ok := value.(string)
		if !ok || strings.TrimSpace(path) == "" {
			return "", fmt.Errorf("device field %s must be a non-empty string", deviceField.Name)
		}
		target = path
	}
	return d.resolveTable(target)
}

// tabletsForGroup applies row and byte boundaries to one sorted target group.
func (d *Dialector) tabletsForGroup(group *tabletGroup, columns []tabletColumn) ([]*client.Tablet, error) {
	var tablets []*client.Tablet
	for start := 0; start < len(group.rows); {
		end := start
		batchBytes := 0
		for end < len(group.rows) && end-start < d.resolved.BatchSize {
			rowBytes := group.rows[end].bytes
			if end > start && batchBytes+rowBytes > d.resolved.MaxBatchBytes {
				break
			}
			batchBytes += rowBytes
			end++
		}
		if end == start {
			end++
		}
		tablet, err := d.newTablet(group.target, group.rows[start:end], columns)
		if err != nil {
			return nil, err
		}
		tablets = append(tablets, tablet)
		start = end
	}
	return tablets, nil
}

// newTablet builds one official TreeModel or relational Tablet.
func (d *Dialector) newTablet(target string, rows []preparedRow, columns []tabletColumn) (*client.Tablet, error) {
	schemas := make([]*client.MeasurementSchema, len(columns))
	categories := make([]client.ColumnCategory, len(columns))
	for i, column := range columns {
		schemas[i] = &client.MeasurementSchema{Measurement: column.column.Field.DBName, DataType: column.dataType}
		categories[i] = column.category
	}
	var (
		tablet *client.Tablet
		err    error
	)
	if d.resolved.ModelMode == TreeModel {
		tablet, err = client.NewTablet(target, schemas, len(rows))
	} else {
		tablet, err = client.NewRelationalTablet(target, schemas, categories, len(rows))
	}
	if err != nil {
		return nil, fmt.Errorf("iotdb: create Tablet for %s: %w", target, err)
	}
	for rowIndex, row := range rows {
		tablet.SetTimestamp(row.timestamp, rowIndex)
		for columnIndex, value := range row.values {
			if err := tablet.SetValueAt(value, columnIndex, rowIndex); err != nil {
				return nil, fmt.Errorf("iotdb: fill Tablet %s row %d column %s: %w", target, rowIndex, schemas[columnIndex].Measurement, err)
			}
		}
		tablet.RowSize++
	}
	return tablet, nil
}

// isSelectedField applies GORM Select and Omit decisions to one column.
func isSelectedField(name string, selected map[string]bool, restricted bool) bool {
	if value, ok := selected[name]; ok {
		return value
	}
	return !restricted
}

// columnCategory maps parsed roles to relational Tablet categories.
func columnCategory(role model.ColumnRole) client.ColumnCategory {
	switch role {
	case model.ColumnRoleTag:
		return client.TAG
	case model.ColumnRoleAttribute:
		return client.ATTRIBUTE
	default:
		return client.FIELD
	}
}

// roleName returns a user-facing IoTDB role label.
func roleName(role model.ColumnRole) string {
	switch role {
	case model.ColumnRoleTag:
		return "TAG"
	case model.ColumnRoleAttribute:
		return "ATTRIBUTE"
	case model.ColumnRoleTime:
		return "TIME"
	case model.ColumnRoleDevice:
		return "DEVICE"
	default:
		return "FIELD"
	}
}

// estimateValueBytes provides a conservative batching estimate before serialization.
func estimateValueBytes(value any) int {
	if value == nil {
		return 1
	}
	switch typed := value.(type) {
	case string:
		return len(typed) + 4
	case []byte:
		return len(typed) + 4
	case bool:
		return 1
	case int32, float32:
		return 4
	case int64, float64:
		return 8
	default:
		return 16
	}
}

// interfaceForRow returns an addressable model value when possible.
func interfaceForRow(row reflect.Value) any {
	if row.CanAddr() {
		return row.Addr().Interface()
	}
	return row.Interface()
}

// indirectReflectValue unwraps pointers and interfaces without panicking on nil.
func indirectReflectValue(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}
