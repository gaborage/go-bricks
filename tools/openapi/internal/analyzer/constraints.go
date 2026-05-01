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

const (
	// OpenAPI constraint property names
	constraintFormat    = "format"
	constraintMinLength = "minLength"
	constraintMaxLength = "maxLength"
	constraintMinimum   = "minimum"
	constraintMaximum   = "maximum"
	constraintEnum      = "enum"

	// Validator tag values
	validatorOneOf = "oneof"

	// OpenAPI format values
	formatEmail = "email"
	formatUUID  = "uuid"
	formatDate  = "date"

	// Go primitive type names referenced for type discrimination
	goTypeInt64   = "int64"
	goTypeFloat64 = "float64"
)

// MapConstraintToOpenAPI converts validation constraints to OpenAPI schema properties
// Takes the field type and constraints map, returns OpenAPI-compatible constraints
func MapConstraintToOpenAPI(fieldType string, constraints map[string]string) []OpenAPIConstraint {
	var result []OpenAPIConstraint

	// Determine base type (strip pointer prefix if present)
	baseType := strings.TrimPrefix(fieldType, "*")

	for key, value := range constraints {
		// Skip required - handled at schema level
		if key == constraintRequired {
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
		formatEmail: formatEmail,
		"url":       "uri",
		"uri":       "uri",
		formatUUID:  formatUUID,
		"uuid4":     formatUUID,
		formatDate:  formatDate,
		"datetime":  "date-time",
	}

	if format, ok := formatMap[key]; ok {
		return []OpenAPIConstraint{{Name: constraintFormat, Value: format}}
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
			return []OpenAPIConstraint{{Name: constraintMinLength, Value: length}}
		}
	} else if isNumericType(baseType) {
		//nolint:S8148 // NOSONAR: Parse errors intentionally ignored - invalid validation tag values are silently skipped
		if minVal, err := parseNumeric(value); err == nil {
			return []OpenAPIConstraint{{Name: constraintMinimum, Value: minVal}}
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
			return []OpenAPIConstraint{{Name: constraintMaxLength, Value: length}}
		}
	} else if isNumericType(baseType) {
		//nolint:S8148 // NOSONAR: Parse errors intentionally ignored - invalid validation tag values are silently skipped
		if maxVal, err := parseNumeric(value); err == nil {
			return []OpenAPIConstraint{{Name: constraintMaximum, Value: maxVal}}
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
		{Name: constraintMinLength, Value: length},
		{Name: constraintMaxLength, Value: length},
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
			{Name: constraintMinimum, Value: numVal},
			{Name: "exclusiveMinimum", Value: true},
		}
	case "gte":
		return []OpenAPIConstraint{{Name: constraintMinimum, Value: numVal}}
	case "lt":
		// lt (less than) becomes maximum with exclusiveMaximum
		return []OpenAPIConstraint{
			{Name: constraintMaximum, Value: numVal},
			{Name: "exclusiveMaximum", Value: true},
		}
	case "lte":
		return []OpenAPIConstraint{{Name: constraintMaximum, Value: numVal}}
	}

	return nil
}

// handleEnumConstraint maps 'oneof' constraint to OpenAPI enum array
// For numeric types, parses values as numbers; otherwise treats as strings
func handleEnumConstraint(key, value, baseType string) []OpenAPIConstraint {
	if key != validatorOneOf {
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

	return []OpenAPIConstraint{{Name: constraintEnum, Value: enumArray}}
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
	return typeName == goTypeString
}

// isNumericType checks if the type is a numeric type
func isNumericType(typeName string) bool {
	numericTypes := []string{
		"int", "int8", "int16", "int32", goTypeInt64,
		"uint", "uint8", "uint16", "uint32", "uint64",
		"float32", goTypeFloat64,
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
