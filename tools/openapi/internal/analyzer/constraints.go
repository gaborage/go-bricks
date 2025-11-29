package analyzer

import (
	"slices"
	"strconv"
	"strings"
)

// OpenAPIConstraint represents a single OpenAPI schema constraint
type OpenAPIConstraint struct {
	Name  string // OpenAPI property name (e.g., "minLength", "minimum", "format")
	Value any    // Constraint value (string, int, bool, []string for enum)
}

// MapConstraintToOpenAPI converts validation constraints to OpenAPI schema properties
// Takes the field type and constraints map, returns OpenAPI-compatible constraints
func MapConstraintToOpenAPI(fieldType string, constraints map[string]string) []OpenAPIConstraint {
	var result []OpenAPIConstraint

	// Determine base type (strip pointer prefix if present)
	baseType := strings.TrimPrefix(fieldType, "*")

	for key, value := range constraints {
		// Skip required - handled at schema level
		if key == "required" {
			continue
		}

		// Try each constraint handler in sequence
		var handled []OpenAPIConstraint

		handled = handleFormatConstraint(key)
		if handled == nil {
			handled = handleMinConstraint(key, value, baseType)
		}
		if handled == nil {
			handled = handleMaxConstraint(key, value, baseType)
		}
		if handled == nil {
			handled = handleLenConstraint(key, value, baseType)
		}
		if handled == nil {
			handled = handleNumericComparison(key, value, baseType)
		}
		if handled == nil {
			handled = handleEnumConstraint(key, value, baseType)
		}
		if handled == nil {
			handled = handlePatternConstraint(key, value)
		}

		result = append(result, handled...)
	}

	return result
}

// handleFormatConstraint maps format validation tags to OpenAPI format constraints
func handleFormatConstraint(key string) []OpenAPIConstraint {
	formatMap := map[string]string{
		"email":    "email",
		"url":      "uri",
		"uri":      "uri",
		"uuid":     "uuid",
		"uuid4":    "uuid",
		"date":     "date",
		"datetime": "date-time",
	}

	if format, ok := formatMap[key]; ok {
		return []OpenAPIConstraint{{Name: "format", Value: format}}
	}
	return nil
}

// handleMinConstraint maps 'min' constraint to minLength (strings) or minimum (numbers)
func handleMinConstraint(key, value, baseType string) []OpenAPIConstraint {
	if key != "min" {
		return nil
	}

	if isStringType(baseType) {
		if length, err := strconv.Atoi(value); err == nil {
			return []OpenAPIConstraint{{Name: "minLength", Value: length}}
		}
	} else if isNumericType(baseType) {
		//nolint:S8148 // NOSONAR: Parse errors intentionally ignored - invalid validation tag values are silently skipped
		if minVal, err := parseNumeric(value); err == nil {
			return []OpenAPIConstraint{{Name: "minimum", Value: minVal}}
		}
	}

	return nil
}

// handleMaxConstraint maps 'max' constraint to maxLength (strings) or maximum (numbers)
func handleMaxConstraint(key, value, baseType string) []OpenAPIConstraint {
	if key != "max" {
		return nil
	}

	if isStringType(baseType) {
		if length, err := strconv.Atoi(value); err == nil {
			return []OpenAPIConstraint{{Name: "maxLength", Value: length}}
		}
	} else if isNumericType(baseType) {
		//nolint:S8148 // NOSONAR: Parse errors intentionally ignored - invalid validation tag values are silently skipped
		if maxVal, err := parseNumeric(value); err == nil {
			return []OpenAPIConstraint{{Name: "maximum", Value: maxVal}}
		}
	}

	return nil
}

// handleLenConstraint maps 'len' constraint to exact length (minLength + maxLength)
func handleLenConstraint(key, value, baseType string) []OpenAPIConstraint {
	if key != "len" || !isStringType(baseType) {
		return nil
	}

	length, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}

	return []OpenAPIConstraint{
		{Name: "minLength", Value: length},
		{Name: "maxLength", Value: length},
	}
}

// handleNumericComparison maps gt/gte/lt/lte constraints to OpenAPI range constraints
func handleNumericComparison(key, value, baseType string) []OpenAPIConstraint {
	if !isNumericType(baseType) {
		return nil
	}

	numVal, err := parseNumeric(value)
	if err != nil {
		return nil
	}

	switch key {
	case "gt":
		// gt (greater than) becomes minimum with exclusiveMinimum
		return []OpenAPIConstraint{
			{Name: "minimum", Value: numVal},
			{Name: "exclusiveMinimum", Value: true},
		}
	case "gte":
		return []OpenAPIConstraint{{Name: "minimum", Value: numVal}}
	case "lt":
		// lt (less than) becomes maximum with exclusiveMaximum
		return []OpenAPIConstraint{
			{Name: "maximum", Value: numVal},
			{Name: "exclusiveMaximum", Value: true},
		}
	case "lte":
		return []OpenAPIConstraint{{Name: "maximum", Value: numVal}}
	}

	return nil
}

// handleEnumConstraint maps 'oneof' constraint to OpenAPI enum array
// For numeric types, parses values as numbers; otherwise treats as strings
func handleEnumConstraint(key, value, baseType string) []OpenAPIConstraint {
	if key != "oneof" {
		return nil
	}

	// Parse space-separated values into enum array
	enumValues := strings.Fields(value)
	if len(enumValues) == 0 {
		return nil
	}

	// Convert to []any; parse numerics when field is numeric
	enumArray := make([]any, len(enumValues))
	if isNumericType(baseType) {
		for i, v := range enumValues {
			//nolint:S8148 // NOSONAR: Parse errors intentionally fall back to string value
			if num, err := parseNumeric(v); err == nil {
				enumArray[i] = num
			} else {
				// Fallback to string if parsing fails
				enumArray[i] = v
			}
		}
	} else {
		for i, v := range enumValues {
			enumArray[i] = v
		}
	}

	return []OpenAPIConstraint{{Name: "enum", Value: enumArray}}
}

// handlePatternConstraint maps 'regexp' constraint to OpenAPI pattern
func handlePatternConstraint(key, value string) []OpenAPIConstraint {
	if key != "regexp" {
		return nil
	}
	return []OpenAPIConstraint{{Name: "pattern", Value: value}}
}

// isStringType checks if the type is a string type
func isStringType(typeName string) bool {
	return typeName == "string"
}

// isNumericType checks if the type is a numeric type
func isNumericType(typeName string) bool {
	numericTypes := []string{
		"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", "float64",
	}

	return slices.Contains(numericTypes, typeName)
}

// parseNumeric converts a string to a numeric value (int or float)
func parseNumeric(value string) (any, error) {
	// Try parsing as integer first
	//nolint:S8148 // NOSONAR: Parse error intentionally falls through to float parsing
	if intVal, err := strconv.ParseInt(value, 10, 64); err == nil {
		return intVal, nil
	}

	// Fall back to float
	return strconv.ParseFloat(value, 64)
}
