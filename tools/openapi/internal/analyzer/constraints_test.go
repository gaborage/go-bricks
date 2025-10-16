package analyzer

import (
	"testing"
)

func TestMapConstraintToOpenAPI(t *testing.T) {
	tests := []struct {
		name        string
		fieldType   string
		constraints map[string]string
		expected    []OpenAPIConstraint
		description string
	}{
		{
			name:        "email format",
			fieldType:   "string",
			constraints: map[string]string{"email": "true"},
			expected:    []OpenAPIConstraint{{Name: "format", Value: "email"}},
			description: "should map email to format constraint",
		},
		{
			name:        "url format",
			fieldType:   "string",
			constraints: map[string]string{"url": "true"},
			expected:    []OpenAPIConstraint{{Name: "format", Value: "uri"}},
			description: "should map url to uri format",
		},
		{
			name:        "uuid format",
			fieldType:   "string",
			constraints: map[string]string{"uuid": "true"},
			expected:    []OpenAPIConstraint{{Name: "format", Value: "uuid"}},
			description: "should map uuid to format constraint",
		},
		{
			name:        "date format",
			fieldType:   "string",
			constraints: map[string]string{"date": "true"},
			expected:    []OpenAPIConstraint{{Name: "format", Value: "date"}},
			description: "should map date to format constraint",
		},
		{
			name:        "datetime format",
			fieldType:   "string",
			constraints: map[string]string{"datetime": "true"},
			expected:    []OpenAPIConstraint{{Name: "format", Value: "date-time"}},
			description: "should map datetime to date-time format",
		},
		{
			name:        "string min length",
			fieldType:   "string",
			constraints: map[string]string{"min": "5"},
			expected:    []OpenAPIConstraint{{Name: "minLength", Value: 5}},
			description: "should map min to minLength for strings",
		},
		{
			name:        "string max length",
			fieldType:   "string",
			constraints: map[string]string{"max": "100"},
			expected:    []OpenAPIConstraint{{Name: "maxLength", Value: 100}},
			description: "should map max to maxLength for strings",
		},
		{
			name:        "string exact length",
			fieldType:   "string",
			constraints: map[string]string{"len": "10"},
			expected: []OpenAPIConstraint{
				{Name: "minLength", Value: 10},
				{Name: "maxLength", Value: 10},
			},
			description: "should map len to both minLength and maxLength",
		},
		{
			name:        "integer minimum",
			fieldType:   "int",
			constraints: map[string]string{"min": "18"},
			expected:    []OpenAPIConstraint{{Name: "minimum", Value: int64(18)}},
			description: "should map min to minimum for integers",
		},
		{
			name:        "integer maximum",
			fieldType:   "int",
			constraints: map[string]string{"max": "120"},
			expected:    []OpenAPIConstraint{{Name: "maximum", Value: int64(120)}},
			description: "should map max to maximum for integers",
		},
		{
			name:        "int64 minimum",
			fieldType:   "int64",
			constraints: map[string]string{"min": "1000"},
			expected:    []OpenAPIConstraint{{Name: "minimum", Value: int64(1000)}},
			description: "should handle int64 type",
		},
		{
			name:        "float minimum",
			fieldType:   "float64",
			constraints: map[string]string{"min": "0.5"},
			expected:    []OpenAPIConstraint{{Name: "minimum", Value: 0.5}},
			description: "should map min to minimum for floats",
		},
		{
			name:        "float maximum",
			fieldType:   "float64",
			constraints: map[string]string{"max": "99.9"},
			expected:    []OpenAPIConstraint{{Name: "maximum", Value: 99.9}},
			description: "should map max to maximum for floats",
		},
		{
			name:        "greater than (exclusive minimum)",
			fieldType:   "int",
			constraints: map[string]string{"gt": "0"},
			expected: []OpenAPIConstraint{
				{Name: "minimum", Value: int64(0)},
				{Name: "exclusiveMinimum", Value: true},
			},
			description: "should map gt to exclusive minimum",
		},
		{
			name:        "greater than or equal",
			fieldType:   "int",
			constraints: map[string]string{"gte": "1"},
			expected:    []OpenAPIConstraint{{Name: "minimum", Value: int64(1)}},
			description: "should map gte to minimum",
		},
		{
			name:        "less than (exclusive maximum)",
			fieldType:   "int",
			constraints: map[string]string{"lt": "100"},
			expected: []OpenAPIConstraint{
				{Name: "maximum", Value: int64(100)},
				{Name: "exclusiveMaximum", Value: true},
			},
			description: "should map lt to exclusive maximum",
		},
		{
			name:        "less than or equal",
			fieldType:   "int",
			constraints: map[string]string{"lte": "99"},
			expected:    []OpenAPIConstraint{{Name: "maximum", Value: int64(99)}},
			description: "should map lte to maximum",
		},
		{
			name:        "oneof enum",
			fieldType:   "string",
			constraints: map[string]string{"oneof": "red green blue"},
			expected:    []OpenAPIConstraint{{Name: "enum", Value: []any{"red", "green", "blue"}}},
			description: "should map oneof to enum array",
		},
		{
			name:        "regexp pattern",
			fieldType:   "string",
			constraints: map[string]string{"regexp": "^[A-Z]+$"},
			expected:    []OpenAPIConstraint{{Name: "pattern", Value: "^[A-Z]+$"}},
			description: "should map regexp to pattern",
		},
		{
			name:      "required constraint skipped",
			fieldType: "string",
			constraints: map[string]string{
				"required": "true",
				"email":    "true",
			},
			expected:    []OpenAPIConstraint{{Name: "format", Value: "email"}},
			description: "should skip required constraint (handled at schema level)",
		},
		{
			name:      "multiple string constraints",
			fieldType: "string",
			constraints: map[string]string{
				"required": "true",
				"email":    "true",
				"min":      "5",
				"max":      "100",
			},
			expected: []OpenAPIConstraint{
				{Name: "format", Value: "email"},
				{Name: "minLength", Value: 5},
				{Name: "maxLength", Value: 100},
			},
			description: "should map multiple string constraints",
		},
		{
			name:      "multiple integer constraints",
			fieldType: "int",
			constraints: map[string]string{
				"required": "true",
				"min":      "1",
				"max":      "1000",
			},
			expected: []OpenAPIConstraint{
				{Name: "minimum", Value: int64(1)},
				{Name: "maximum", Value: int64(1000)},
			},
			description: "should map multiple integer constraints",
		},
		{
			name:        "pointer type stripped",
			fieldType:   "*string",
			constraints: map[string]string{"min": "5"},
			expected:    []OpenAPIConstraint{{Name: "minLength", Value: 5}},
			description: "should strip pointer prefix from type",
		},
		{
			name:        "empty constraints",
			fieldType:   "string",
			constraints: map[string]string{},
			expected:    []OpenAPIConstraint{},
			description: "should return empty array for no constraints",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MapConstraintToOpenAPI(tt.fieldType, tt.constraints)

			if len(result) != len(tt.expected) {
				t.Errorf("%s: expected %d constraints, got %d", tt.description, len(tt.expected), len(result))
				t.Logf("Expected: %+v", tt.expected)
				t.Logf("Got: %+v", result)
				return
			}

			// Create a map for easier comparison
			resultMap := make(map[string]any)
			for _, c := range result {
				resultMap[c.Name] = c.Value
			}

			for _, expected := range tt.expected {
				gotValue, ok := resultMap[expected.Name]
				if !ok {
					t.Errorf("%s: missing constraint %q", tt.description, expected.Name)
					continue
				}

				// Special handling for enum arrays
				if expected.Name == "enum" {
					expectedArr, ok1 := expected.Value.([]any)
					gotArr, ok2 := gotValue.([]any)
					if !ok1 || !ok2 {
						t.Errorf("%s: enum values are not arrays", tt.description)
						continue
					}
					if len(expectedArr) != len(gotArr) {
						t.Errorf("%s: enum array length mismatch: expected %d, got %d", tt.description, len(expectedArr), len(gotArr))
						continue
					}
					for i := range expectedArr {
						if expectedArr[i] != gotArr[i] {
							t.Errorf("%s: enum[%d]: expected %v, got %v", tt.description, i, expectedArr[i], gotArr[i])
						}
					}
				} else if gotValue != expected.Value {
					t.Errorf("%s: constraint %q: expected %v (type %T), got %v (type %T)",
						tt.description, expected.Name, expected.Value, expected.Value, gotValue, gotValue)
				}
			}
		})
	}
}

func TestIsStringType(t *testing.T) {
	tests := []struct {
		typeName string
		expected bool
	}{
		{"string", true},
		{"int", false},
		{"float64", false},
		{"bool", false},
		{"[]string", false},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			result := isStringType(tt.typeName)
			if result != tt.expected {
				t.Errorf("isStringType(%q): expected %v, got %v", tt.typeName, tt.expected, result)
			}
		})
	}
}

func TestIsNumericType(t *testing.T) {
	tests := []struct {
		typeName string
		expected bool
	}{
		{"int", true},
		{"int8", true},
		{"int16", true},
		{"int32", true},
		{"int64", true},
		{"uint", true},
		{"uint8", true},
		{"uint16", true},
		{"uint32", true},
		{"uint64", true},
		{"float32", true},
		{"float64", true},
		{"string", false},
		{"bool", false},
		{"[]int", false},
	}

	for _, tt := range tests {
		t.Run(tt.typeName, func(t *testing.T) {
			result := isNumericType(tt.typeName)
			if result != tt.expected {
				t.Errorf("isNumericType(%q): expected %v, got %v", tt.typeName, tt.expected, result)
			}
		})
	}
}

func TestParseNumeric(t *testing.T) {
	tests := []struct {
		value       string
		expected    any
		shouldError bool
		description string
	}{
		{
			value:       "42",
			expected:    int64(42),
			shouldError: false,
			description: "should parse integer",
		},
		{
			value:       "3.14",
			expected:    3.14,
			shouldError: false,
			description: "should parse float",
		},
		{
			value:       "0",
			expected:    int64(0),
			shouldError: false,
			description: "should parse zero",
		},
		{
			value:       "-10",
			expected:    int64(-10),
			shouldError: false,
			description: "should parse negative integer",
		},
		{
			value:       "-2.5",
			expected:    -2.5,
			shouldError: false,
			description: "should parse negative float",
		},
		{
			value:       "invalid",
			shouldError: true,
			description: "should error on invalid input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			result, err := parseNumeric(tt.value)

			if tt.shouldError {
				if err == nil {
					t.Errorf("%s: expected error, got nil", tt.description)
				}
			} else {
				if err != nil {
					t.Errorf("%s: unexpected error: %v", tt.description, err)
				}
				if result != tt.expected {
					t.Errorf("%s: expected %v (type %T), got %v (type %T)",
						tt.description, tt.expected, tt.expected, result, result)
				}
			}
		})
	}
}
