package backend

import (
	"strings"
	"testing"
)

// TestParseMetadataBoolean verifies metadata compatibility across IoTDB BOOLEAN and TEXT encodings.
func TestParseMetadataBoolean(t *testing.T) {
	tests := []struct {
		name     string
		rawValue string
		want     bool
	}{
		{name: "lowercase true", rawValue: "true", want: true},
		{name: "uppercase true", rawValue: "TRUE", want: true},
		{name: "mixed case false", rawValue: "False", want: false},
		{name: "surrounding spaces", rawValue: "  false\t", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMetadataBoolean("IsAligned", tt.rawValue)
			if err != nil {
				t.Fatalf("parse %q: %v", tt.rawValue, err)
			}
			if got != tt.want {
				t.Fatalf("parse %q: expected %t, got %t", tt.rawValue, tt.want, got)
			}
		})
	}
}

// TestParseMetadataBooleanRejectsInvalidValue verifies malformed server metadata is not silently treated as false.
func TestParseMetadataBooleanRejectsInvalidValue(t *testing.T) {
	_, err := parseMetadataBoolean("IsAligned", "not-a-boolean")
	if err == nil {
		t.Fatal("expected invalid boolean metadata to fail")
	}
	if !strings.Contains(err.Error(), "IsAligned") || !strings.Contains(err.Error(), "not-a-boolean") {
		t.Fatalf("expected column and raw value in error, got %v", err)
	}
}
