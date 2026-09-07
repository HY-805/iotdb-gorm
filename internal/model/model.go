// Package model maps GORM schemas to explicit IoTDB model roles.
package model

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"gorm.io/gorm/schema"
)

// ColumnRole describes how a field participates in an IoTDB model.
type ColumnRole uint8

const (
	// ColumnRoleField marks a measurement or table FIELD.
	ColumnRoleField ColumnRole = iota
	// ColumnRoleTag marks a TableModel TAG and is rejected as implicit TreeModel metadata.
	ColumnRoleTag
	// ColumnRoleAttribute marks a TableModel ATTRIBUTE.
	ColumnRoleAttribute
	// ColumnRoleTime marks the row timestamp stored through Tablet.SetTimestamp.
	ColumnRoleTime
	// ColumnRoleDevice marks a per-row TreeModel device path selector.
	ColumnRoleDevice
)

// Column describes one parsed GORM field and its IoTDB role.
type Column struct {
	Field   *schema.Field
	Role    ColumnRole
	Options map[string]string
}

// ParseColumns extracts IoTDB roles from either `iotdb` or legacy GORM tag settings.
func ParseColumns(s *schema.Schema) []Column {
	columns := make([]Column, 0, len(s.Fields))
	for _, field := range s.Fields {
		columns = append(columns, ParseField(field))
	}
	return columns
}

// ParseField extracts one field's IoTDB role and options.
func ParseField(field *schema.Field) Column {
	options := parseOptions(field)
	return Column{Field: field, Role: parseRole(field, options), Options: options}
}

// TagValueMap extracts TableModel tag values for compatibility with the imported API.
func TagValueMap(s *schema.Schema, value reflect.Value) map[string]any {
	tags := make(map[string]any)
	for _, column := range ParseColumns(s) {
		if column.Role != ColumnRoleTag {
			continue
		}
		v, zero := column.Field.ValueOf(context.Background(), value)
		if !zero {
			tags[column.Field.DBName] = v
		}
	}
	return tags
}

// FindField resolves a field by Go name or database name.
func FindField(s *schema.Schema, name string) (*schema.Field, error) {
	if field := s.LookUpField(name); field != nil {
		return field, nil
	}
	for _, field := range s.Fields {
		if strings.EqualFold(field.DBName, name) || strings.EqualFold(field.Name, name) {
			return field, nil
		}
	}
	return nil, fmt.Errorf("iotdb: field %q not found on schema %s", name, s.Name)
}

// parseOptions normalizes dedicated and legacy IoTDB tag forms.
func parseOptions(field *schema.Field) map[string]string {
	raw := field.StructField.Tag.Get("iotdb")
	if legacy := field.TagSettings["IOTDB"]; legacy != "" {
		if raw != "" {
			raw += ";"
		}
		raw += legacy
	}
	options := make(map[string]string)
	for _, token := range strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ',' }) {
		parts := strings.SplitN(strings.TrimSpace(token), "=", 2)
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		if key == "" {
			continue
		}
		value := "true"
		if len(parts) == 2 {
			value = strings.TrimSpace(parts[1])
		}
		options[key] = value
	}
	return options
}

// parseRole resolves explicit roles before applying the conventional time name.
func parseRole(field *schema.Field, options map[string]string) ColumnRole {
	for _, role := range []struct {
		name string
		role ColumnRole
	}{
		{name: "device", role: ColumnRoleDevice},
		{name: "time", role: ColumnRoleTime},
		{name: "tag", role: ColumnRoleTag},
		{name: "attribute", role: ColumnRoleAttribute},
		{name: "field", role: ColumnRoleField},
	} {
		if _, ok := options[role.name]; ok {
			return role.role
		}
	}
	if strings.EqualFold(field.DBName, "time") || strings.EqualFold(field.Name, "Time") {
		return ColumnRoleTime
	}
	return ColumnRoleField
}
