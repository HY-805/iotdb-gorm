package dialector

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/HY-805/iotdb-gorm/internal/backend"
	"github.com/HY-805/iotdb-gorm/internal/model"
	"github.com/apache/iotdb-client-go/v2/client"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// Migrator implements only idempotent, non-destructive IoTDB migrations.
type Migrator struct {
	db        *gorm.DB
	dialector *Dialector
	backend   backend.Backend
}

// AutoMigrate creates a missing device/table and adds only compatible columns.
func (m Migrator) AutoMigrate(dst ...interface{}) error {
	for _, destination := range dst {
		if m.dialector.resolved.ModelMode == TreeModel {
			if err := m.migrateTree(destination); err != nil {
				return err
			}
			continue
		}
		if err := m.migrateTable(destination); err != nil {
			return err
		}
	}
	return nil
}

// CurrentDatabase returns the configured logical database.
func (m Migrator) CurrentDatabase() string {
	return m.dialector.resolved.Database
}

// FullDataTypeOf returns one IoTDB type expression.
func (m Migrator) FullDataTypeOf(field *schema.Field) clause.Expr {
	return clause.Expr{SQL: m.dialector.DataTypeOf(field)}
}

// GetTypeAliases returns the narrow set of accepted server aliases.
func (m Migrator) GetTypeAliases(databaseTypeName string) []string {
	switch strings.ToUpper(databaseTypeName) {
	case "TEXT", "STRING":
		return []string{"TEXT", "STRING"}
	case "INT32", "INTEGER":
		return []string{"INT32", "INTEGER"}
	case "INT64", "LONG":
		return []string{"INT64", "LONG"}
	default:
		return []string{strings.ToUpper(databaseTypeName)}
	}
}

// CreateTable creates TreeModel measurements or one TableModel table.
func (m Migrator) CreateTable(dst ...interface{}) error {
	for _, destination := range dst {
		statement, table, err := m.parse(destination)
		if err != nil {
			return err
		}
		if m.dialector.resolved.ModelMode == TreeModel {
			measurements, measurementErr := treeMeasurements(statement.Schema)
			if measurementErr != nil {
				return measurementErr
			}
			if m.backend == nil {
				return fmt.Errorf("iotdb: TreeMigrator requires an owned official backend")
			}
			if err := m.backend.EnsureTreeSchema(m.context(), table, measurements, m.dialector.resolved.AlignedValue); err != nil {
				return fmt.Errorf("iotdb: migrate TreeModel device %s: %w", table, err)
			}
			continue
		}
		if err := m.createTableModelTable(statement.Schema, table); err != nil {
			return err
		}
	}
	return nil
}

// DropTable rejects destructive schema removal.
func (m Migrator) DropTable(...interface{}) error {
	return fmt.Errorf("%w: DropTable", ErrUnsupportedOperation)
}

// HasTable reports whether a TreeModel device or TableModel table exists.
func (m Migrator) HasTable(dst interface{}) bool {
	_, table, err := m.parse(dst)
	if err != nil || m.backend == nil {
		return false
	}
	if m.dialector.resolved.ModelMode == TreeModel {
		device, inspectErr := m.backend.InspectTreeDevice(m.context(), table)
		return inspectErr == nil && device.Exists
	}
	tables, listErr := m.GetTables()
	if listErr != nil {
		return false
	}
	for _, existing := range tables {
		if strings.EqualFold(existing, table) {
			return true
		}
	}
	return false
}

// RenameTable rejects path and table renames.
func (m Migrator) RenameTable(interface{}, interface{}) error {
	return fmt.Errorf("%w: RenameTable", ErrUnsupportedOperation)
}

// GetTables lists devices below the TreeModel root or tables in the TableModel database.
func (m Migrator) GetTables() ([]string, error) {
	if m.backend == nil {
		return nil, fmt.Errorf("iotdb: table listing requires an owned official backend")
	}
	query := "SHOW TABLES"
	if m.dialector.resolved.ModelMode == TreeModel {
		query = "SHOW DEVICES " + m.dialector.resolved.Database + ".**"
	}
	result, err := m.backend.Query(m.context(), query)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	var tables []string
	for {
		next, nextErr := result.Next()
		if nextErr != nil {
			return nil, nextErr
		}
		if !next {
			return tables, nil
		}
		value, valueErr := result.Value(0)
		if valueErr != nil {
			return nil, valueErr
		}
		if name, ok := value.(string); ok {
			tables = append(tables, name)
		}
	}
}

// TableType reports that IoTDB table/device metadata cannot be represented by
// GORM's relational TableType contract.
func (m Migrator) TableType(interface{}) (gorm.TableType, error) {
	return nil, fmt.Errorf("%w: TableType", ErrUnsupportedOperation)
}

// AddColumn adds only the requested missing Tree measurement or table column.
func (m Migrator) AddColumn(dst interface{}, fieldName string) error {
	statement, table, err := m.parse(dst)
	if err != nil {
		return err
	}
	field, err := model.FindField(statement.Schema, fieldName)
	if err != nil {
		return err
	}
	column := model.ParseField(field)
	if column.Role == model.ColumnRoleTime || column.Role == model.ColumnRoleDevice {
		return fmt.Errorf("iotdb: %s is not a physical measurement column", fieldName)
	}
	if m.dialector.resolved.ModelMode == TreeModel {
		if column.Role == model.ColumnRoleTag || column.Role == model.ColumnRoleAttribute {
			return fmt.Errorf("iotdb: TreeModel does not map %s to a measurement", roleName(column.Role))
		}
		measurement, err := treeMeasurement(column)
		if err != nil {
			return err
		}
		return m.backend.EnsureTreeSchema(m.context(), table, []backend.Measurement{measurement}, m.dialector.resolved.AlignedValue)
	}
	definition, err := tableColumnDefinition(column)
	if err != nil {
		return err
	}
	return m.db.Exec("ALTER TABLE " + table + " ADD COLUMN " + definition).Error
}

// DropColumn rejects destructive column removal.
func (m Migrator) DropColumn(interface{}, string) error {
	return fmt.Errorf("%w: DropColumn", ErrUnsupportedOperation)
}

// AlterColumn rejects in-place type changes.
func (m Migrator) AlterColumn(interface{}, string) error {
	return fmt.Errorf("%w: AlterColumn", ErrUnsupportedOperation)
}

// MigrateColumn accepts matching types and rejects incompatible changes.
func (m Migrator) MigrateColumn(dst interface{}, field *schema.Field, columnType gorm.ColumnType) error {
	requested := strings.ToUpper(m.dialector.DataTypeOf(field))
	existing := strings.ToUpper(columnType.DatabaseTypeName())
	for _, alias := range m.GetTypeAliases(existing) {
		if alias == requested {
			return nil
		}
	}
	return fmt.Errorf("iotdb: column %s type conflict: server=%s requested=%s", field.DBName, existing, requested)
}

// MigrateColumnUnique keeps GORM 1.26.1 AutoMigrate compatible while leaving
// relational uniqueness unsupported for both IoTDB models.
func (m Migrator) MigrateColumnUnique(_ interface{}, _ *schema.Field, columnType gorm.ColumnType) error {
	if _, ok := columnType.Unique(); !ok {
		return nil
	}
	return fmt.Errorf("%w: unique constraints", ErrUnsupportedOperation)
}

// HasColumn reports whether a measurement or table column exists.
func (m Migrator) HasColumn(dst interface{}, fieldName string) bool {
	types, err := m.ColumnTypes(dst)
	if err != nil {
		return false
	}
	statement, _, parseErr := m.parse(dst)
	if parseErr != nil {
		return false
	}
	field, fieldErr := model.FindField(statement.Schema, fieldName)
	if fieldErr != nil {
		return false
	}
	for _, column := range types {
		if strings.EqualFold(column.Name(), field.DBName) {
			return true
		}
	}
	return false
}

// RenameColumn rejects measurement and table-column renames.
func (m Migrator) RenameColumn(interface{}, string, string) error {
	return fmt.Errorf("%w: RenameColumn", ErrUnsupportedOperation)
}

// ColumnTypes returns TreeModel or TableModel type metadata.
func (m Migrator) ColumnTypes(dst interface{}) ([]gorm.ColumnType, error) {
	_, table, err := m.parse(dst)
	if err != nil {
		return nil, err
	}
	if m.backend == nil {
		return nil, fmt.Errorf("iotdb: column inspection requires an owned official backend")
	}
	if m.dialector.resolved.ModelMode == TreeModel {
		device, inspectErr := m.backend.InspectTreeDevice(m.context(), table)
		if inspectErr != nil {
			return nil, inspectErr
		}
		columns := make([]gorm.ColumnType, 0, len(device.Measurements)+1)
		columns = append(columns, columnType{name: "time", databaseType: "TIMESTAMP", nullable: false})
		for name, dataType := range device.Measurements {
			columns = append(columns, columnType{name: name, databaseType: dataTypeString(dataType), nullable: true})
		}
		return columns, nil
	}
	return m.describeTable(table)
}

// CreateView rejects unsupported views.
func (m Migrator) CreateView(string, gorm.ViewOption) error {
	return fmt.Errorf("%w: CreateView", ErrUnsupportedOperation)
}

// DropView rejects unsupported views.
func (m Migrator) DropView(string) error {
	return fmt.Errorf("%w: DropView", ErrUnsupportedOperation)
}

// CreateConstraint rejects relational constraints.
func (m Migrator) CreateConstraint(interface{}, string) error {
	return fmt.Errorf("%w: CreateConstraint", ErrUnsupportedOperation)
}

// DropConstraint rejects relational constraints.
func (m Migrator) DropConstraint(interface{}, string) error {
	return fmt.Errorf("%w: DropConstraint", ErrUnsupportedOperation)
}

// HasConstraint always reports false because constraints are unsupported.
func (m Migrator) HasConstraint(interface{}, string) bool {
	return false
}

// CreateIndex rejects relational indexes.
func (m Migrator) CreateIndex(interface{}, string) error {
	return fmt.Errorf("%w: CreateIndex", ErrUnsupportedOperation)
}

// DropIndex rejects relational indexes.
func (m Migrator) DropIndex(interface{}, string) error {
	return fmt.Errorf("%w: DropIndex", ErrUnsupportedOperation)
}

// HasIndex always reports false because relational indexes are unsupported.
func (m Migrator) HasIndex(interface{}, string) bool {
	return false
}

// RenameIndex rejects relational index renames.
func (m Migrator) RenameIndex(interface{}, string, string) error {
	return fmt.Errorf("%w: RenameIndex", ErrUnsupportedOperation)
}

// GetIndexes reports that IoTDB does not expose relational index metadata.
func (m Migrator) GetIndexes(interface{}) ([]gorm.Index, error) {
	return nil, fmt.Errorf("%w: GetIndexes", ErrUnsupportedOperation)
}

// migrateTree delegates idempotent device schema reconciliation to the backend.
func (m Migrator) migrateTree(destination interface{}) error {
	return m.CreateTable(destination)
}

// migrateTable creates a table then checks and adds compatible missing columns.
func (m Migrator) migrateTable(destination interface{}) error {
	statement, table, err := m.parse(destination)
	if err != nil {
		return err
	}
	if !m.HasTable(destination) {
		return m.createTableModelTable(statement.Schema, table)
	}
	existing, err := m.describeTable(table)
	if err != nil {
		return err
	}
	existingByName := make(map[string]gorm.ColumnType, len(existing))
	for _, column := range existing {
		existingByName[strings.ToLower(column.Name())] = column
	}
	for _, column := range model.ParseColumns(statement.Schema) {
		if column.Role == model.ColumnRoleTime || column.Role == model.ColumnRoleDevice || column.Field.DBName == "" || column.Field.IgnoreMigration {
			continue
		}
		if current, ok := existingByName[strings.ToLower(column.Field.DBName)]; ok {
			if err := m.MigrateColumn(destination, column.Field, current); err != nil {
				return err
			}
			continue
		}
		if err := m.AddColumn(destination, column.Field.DBName); err != nil {
			return err
		}
	}
	return nil
}

// createTableModelTable creates the database and one relational table.
func (m Migrator) createTableModelTable(schemaValue *schema.Schema, table string) error {
	if err := m.db.Exec("CREATE DATABASE IF NOT EXISTS " + m.dialector.resolved.Database).Error; err != nil {
		return fmt.Errorf("iotdb: create TableModel database: %w", err)
	}
	definitions := make([]string, 0, len(schemaValue.Fields))
	for _, column := range model.ParseColumns(schemaValue) {
		if column.Role == model.ColumnRoleTime {
			continue
		}
		if column.Role == model.ColumnRoleDevice {
			return fmt.Errorf("iotdb: device role is invalid in TableModel")
		}
		if column.Field.DBName == "" || column.Field.IgnoreMigration {
			continue
		}
		definition, err := tableColumnDefinition(column)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	if len(definitions) == 0 {
		return fmt.Errorf("iotdb: table %s has no columns", table)
	}
	query := "CREATE TABLE IF NOT EXISTS " + table + " (" + strings.Join(definitions, ", ") + ")"
	if err := m.db.Exec(query).Error; err != nil {
		return fmt.Errorf("iotdb: create TableModel table %s: %w", table, err)
	}
	return nil
}

// describeTable decodes table metadata without assuming a fixed column order.
func (m Migrator) describeTable(table string) ([]gorm.ColumnType, error) {
	result, err := m.backend.Query(m.context(), "DESCRIBE "+table)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	names := result.ColumnNames()
	nameIndex := findMetadataColumn(names, "ColumnName", "column_name", "Field")
	typeIndex := findMetadataColumn(names, "DataType", "data_type", "Type")
	if nameIndex < 0 || typeIndex < 0 {
		return nil, fmt.Errorf("iotdb: unrecognized DESCRIBE columns %v", names)
	}
	var columns []gorm.ColumnType
	for {
		next, nextErr := result.Next()
		if nextErr != nil {
			return nil, nextErr
		}
		if !next {
			return columns, nil
		}
		name, nameErr := result.Value(nameIndex)
		if nameErr != nil {
			return nil, nameErr
		}
		dataType, typeErr := result.Value(typeIndex)
		if typeErr != nil {
			return nil, typeErr
		}
		columns = append(columns, columnType{name: fmt.Sprint(name), databaseType: strings.ToUpper(fmt.Sprint(dataType)), nullable: true})
	}
}

// parse resolves a GORM schema and its safe physical target.
func (m Migrator) parse(destination interface{}) (*gorm.Statement, string, error) {
	statement := &gorm.Statement{DB: m.db}
	if m.db.Statement != nil && m.db.Statement.Table != "" {
		statement.Table = m.db.Statement.Table
	}
	if err := statement.Parse(destination); err != nil {
		return nil, "", err
	}
	table := statement.Table
	if table == "" {
		table = statement.Schema.Table
	}
	resolved, err := m.dialector.resolveTable(table)
	if err != nil {
		return nil, "", err
	}
	return statement, resolved, nil
}

// context returns the statement context or a safe background context.
func (m Migrator) context() context.Context {
	if m.db.Statement != nil && m.db.Statement.Context != nil {
		return m.db.Statement.Context
	}
	return context.Background()
}

// treeMeasurements validates and maps all migratable TreeModel fields.
func treeMeasurements(schemaValue *schema.Schema) ([]backend.Measurement, error) {
	measurements := make([]backend.Measurement, 0, len(schemaValue.Fields))
	for _, column := range model.ParseColumns(schemaValue) {
		switch column.Role {
		case model.ColumnRoleTime, model.ColumnRoleDevice:
			continue
		case model.ColumnRoleTag, model.ColumnRoleAttribute:
			return nil, fmt.Errorf("iotdb: %s role on %s is not an implicit TreeModel measurement", roleName(column.Role), column.Field.Name)
		}
		if column.Field.DBName == "" || column.Field.IgnoreMigration {
			continue
		}
		if !treePathNodePattern.MatchString(column.Field.DBName) {
			return nil, fmt.Errorf("iotdb: invalid measurement name %q", column.Field.DBName)
		}
		measurement, err := treeMeasurement(column)
		if err != nil {
			return nil, err
		}
		measurements = append(measurements, measurement)
	}
	if len(measurements) == 0 {
		return nil, fmt.Errorf("iotdb: model %s has no TreeModel measurements", schemaValue.Name)
	}
	return measurements, nil
}

// tableColumnDefinition renders one validated TableModel column.
func tableColumnDefinition(column model.Column) (string, error) {
	if !tableNamePattern.MatchString(column.Field.DBName) {
		return "", fmt.Errorf("iotdb: invalid TableModel column %q", column.Field.DBName)
	}
	if column.Role == model.ColumnRoleTime || column.Role == model.ColumnRoleDevice {
		return "", fmt.Errorf("iotdb: %s cannot be emitted as a table column", roleName(column.Role))
	}
	typeName := dataTypeName(column, TableModel)
	if _, err := clientDataType(column, TableModel); err != nil {
		return "", err
	}
	return column.Field.DBName + " " + typeName + " " + roleName(column.Role), nil
}

// findMetadataColumn returns the first case-insensitive matching column index.
func findMetadataColumn(columns []string, candidates ...string) int {
	for index, column := range columns {
		for _, candidate := range candidates {
			if strings.EqualFold(column, candidate) {
				return index
			}
		}
	}
	return -1
}

// dataTypeString returns the official enum's stable SQL name.
func dataTypeString(dataType client.TSDataType) string {
	switch dataType {
	case client.BOOLEAN:
		return "BOOLEAN"
	case client.INT32:
		return "INT32"
	case client.INT64:
		return "INT64"
	case client.FLOAT:
		return "FLOAT"
	case client.DOUBLE:
		return "DOUBLE"
	case client.TEXT:
		return "TEXT"
	case client.TIMESTAMP:
		return "TIMESTAMP"
	case client.DATE:
		return "DATE"
	case client.BLOB:
		return "BLOB"
	case client.STRING:
		return "STRING"
	default:
		return "UNKNOWN"
	}
}

// columnType is minimal GORM column metadata.
type columnType struct {
	name         string
	databaseType string
	nullable     bool
}

// Name returns the normalized column name.
func (c columnType) Name() string { return c.name }

// DatabaseTypeName returns the IoTDB type.
func (c columnType) DatabaseTypeName() string { return c.databaseType }

// ColumnType returns the IoTDB type expression.
func (c columnType) ColumnType() (string, bool) { return c.databaseType, true }

// PrimaryKey reports unsupported relational primary keys.
func (c columnType) PrimaryKey() (bool, bool) { return false, false }

// AutoIncrement reports unsupported auto-increment columns.
func (c columnType) AutoIncrement() (bool, bool) { return false, false }

// Length reports no relational length metadata.
func (c columnType) Length() (int64, bool) { return 0, false }

// DecimalSize reports no relational precision metadata.
func (c columnType) DecimalSize() (int64, int64, bool) { return 0, 0, false }

// Nullable reports whether the column may contain NULL.
func (c columnType) Nullable() (bool, bool) { return c.nullable, true }

// Unique reports unsupported relational uniqueness.
func (c columnType) Unique() (bool, bool) { return false, false }

// ScanType returns a conservative Go scan type.
func (c columnType) ScanType() reflect.Type {
	switch c.databaseType {
	case "BOOLEAN":
		return reflect.TypeOf(false)
	case "INT32":
		return reflect.TypeOf(int32(0))
	case "INT64":
		return reflect.TypeOf(int64(0))
	default:
		return reflect.TypeOf("")
	}
}

// Comment reports no column comment metadata.
func (c columnType) Comment() (string, bool) { return "", false }

// DefaultValue reports no IoTDB default metadata.
func (c columnType) DefaultValue() (string, bool) { return "", false }

var _ gorm.Migrator = (*Migrator)(nil)
