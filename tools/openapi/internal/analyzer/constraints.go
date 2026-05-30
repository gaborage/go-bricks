package analyzer

import (
	"regexp"
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
	constraintFormat           = "format"
	constraintMinLength        = "minLength"
	constraintMaxLength        = "maxLength"
	constraintMinimum          = "minimum"
	constraintMaximum          = "maximum"
	constraintMinItems         = "minItems"
	constraintMaxItems         = "maxItems"
	constraintEnum             = "enum"
	constraintPattern          = "pattern"
	constraintExclusiveMinimum = "exclusiveMinimum"
	constraintExclusiveMaximum = "exclusiveMaximum"

	// Validator tag values
	validatorOneOf    = "oneof"
	validatorDatetime = "datetime"

	// OpenAPI format values
	formatEmail    = "email"
	formatUUID     = "uuid"
	formatDate     = "date"
	formatDateTime = "date-time"
	formatByte     = "byte"
	formatIPv4     = "ipv4"

	// OpenAPI 3-way kind values (also the values of FieldInfo.UnderlyingKind).
	kindInteger = "integer"
	kindNumber  = "number"

	// Go primitive type names referenced for type discrimination
	goTypeInt64   = "int64"
	goTypeFloat32 = "float32"
	goTypeFloat64 = "float64"
)

// MapConstraintToOpenAPI converts validation constraints to OpenAPI schema properties
// Takes the field type and constraints map, returns OpenAPI-compatible constraints
func MapConstraintToOpenAPI(fieldType, underlyingKind string, constraints map[string]string) []OpenAPIConstraint {
	var result []OpenAPIConstraint

	baseType := strings.TrimPrefix(fieldType, "*")
	// []byte/[]uint8 are well-known base64 string types, not arrays — treat them as
	// scalars (the well-known mapper already types them string/binary), not slices.
	// Their min/max are byte counts, which do NOT equal the base64-encoded character
	// length, so we deliberately drop length bounds on them rather than emit a wrong
	// minLength/maxLength. Maps likewise carry no cardinality here (effectiveKind is
	// "" for a map type, so min/max are dropped); minProperties/maxProperties support
	// is tracked as a follow-up.
	isSlice := isSliceType(fieldType) && !isByteSlice(baseType)
	effKind := effectiveKind(baseType, underlyingKind)

	for key, value := range constraints {
		if key == constraintRequired {
			continue // handled at schema level
		}

		// Slice fields: only cardinality (min/max/len -> minItems/maxItems) applies
		// to the array itself; element rules arrive via ElementConstraints + dive.
		if isSlice {
			if h := handleSliceCardinality(key, value); h != nil {
				result = append(result, h...)
			}
			continue
		}

		handled := handleFormatConstraint(key, value)
		if handled == nil {
			handled = handlePatternFormatConstraint(key, value)
		}
		if handled == nil {
			handled = handleMinConstraint(key, value, effKind)
		}
		if handled == nil {
			handled = handleMaxConstraint(key, value, effKind)
		}
		if handled == nil {
			handled = handleLenConstraint(key, value, effKind)
		}
		if handled == nil {
			handled = handleNumericComparison(key, value, effKind)
		}
		if handled == nil {
			handled = handleEnumConstraint(key, value, effKind)
		}
		if handled == nil {
			handled = handleEqConstraint(key, value, effKind)
		}
		if handled == nil {
			handled = handlePatternConstraint(key, value)
		}

		result = append(result, handled...)
	}

	return result
}

// effectiveKind resolves the OpenAPI 3-way kind to drive string-vs-numeric
// decisions: the analyzer-resolved UnderlyingKind (for named scalars like
// `type Cents int64` / time.Duration) wins; otherwise it is derived from the
// builtin base type. Empty when the type is neither string nor numeric.
func effectiveKind(baseType, underlyingKind string) string {
	if underlyingKind != "" {
		return underlyingKind
	}
	switch {
	case isStringType(baseType):
		return goTypeString
	case isIntegerType(baseType):
		return kindInteger
	case isFloatType(baseType):
		return kindNumber
	}
	return ""
}

func isEffectiveString(k string) bool  { return k == goTypeString }
func isEffectiveNumeric(k string) bool { return k == kindInteger || k == kindNumber }

// isByteSlice reports whether t is []byte/[]uint8 (a base64 string, not an array).
func isByteSlice(t string) bool { return t == "[]byte" || t == "[]uint8" }

// isSliceType reports whether t denotes a slice (after an optional leading
// pointer). Twin of generator.isSliceType — keep in sync.
func isSliceType(t string) bool {
	return strings.HasPrefix(strings.TrimPrefix(t, "*"), "[]")
}

// formatTagMap maps boolean validator format tags to their OpenAPI `format`.
var formatTagMap = map[string]string{
	formatEmail: formatEmail,
	"url":       "uri",
	"uri":       "uri",
	formatUUID:  formatUUID,
	"uuid4":     formatUUID,
	formatDate:  formatDate,
	formatIPv4:  formatIPv4,
	"ipv6":      "ipv6",
	"hostname":  "hostname",
	"base64":    formatByte,
}

// handleFormatConstraint maps boolean format tags to OpenAPI `format`. datetime is
// value-aware: a date-only layout maps to `date`, otherwise `date-time`.
func handleFormatConstraint(key, value string) []OpenAPIConstraint {
	if key == validatorDatetime {
		if value == "" || value == boolTrueString {
			return []OpenAPIConstraint{{Name: constraintFormat, Value: formatDateTime}}
		}
		return []OpenAPIConstraint{{Name: constraintFormat, Value: datetimeFormat(value)}}
	}
	if format, ok := formatTagMap[key]; ok {
		return []OpenAPIConstraint{{Name: constraintFormat, Value: format}}
	}
	return nil
}

// stringContentPatterns maps boolean string-content tags to a canonical anchored
// regex. JSON-Schema `pattern` is documentation-grade here (kin-openapi/redocly
// accept arbitrary regex), so these mirror validator/v10 semantics closely.
var stringContentPatterns = map[string]string{
	"alpha":    `^[a-zA-Z]+$`,
	"alphanum": `^[a-zA-Z0-9]+$`,
	"numeric":  `^[-+]?[0-9]+(?:\.[0-9]+)?$`,
	"hexcolor": `^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`,
	"e164":     `^\+[1-9]\d{1,14}$`,
}

// handlePatternFormatConstraint maps string-content tags to a `pattern`. Boolean
// tags (alpha/alphanum/numeric/hexcolor/e164) use a fixed anchored regex;
// value-bearing tags (contains/startswith/endswith) build an (un)anchored pattern
// from the QuoteMeta-escaped literal. Bare `ip` is intentionally left
// unconstrained: there is no single OpenAPI format for "IPv4 or IPv6" and a
// correct combined regex is huge/error-prone, so we document the type as string
// with no pattern rather than over- or mis-constrain it (covered by a test).
func handlePatternFormatConstraint(key, value string) []OpenAPIConstraint {
	if p, ok := stringContentPatterns[key]; ok {
		return []OpenAPIConstraint{{Name: constraintPattern, Value: p}}
	}
	if value == "" {
		return nil
	}
	switch key {
	case "contains":
		return []OpenAPIConstraint{{Name: constraintPattern, Value: regexp.QuoteMeta(value)}}
	case "startswith":
		return []OpenAPIConstraint{{Name: constraintPattern, Value: "^" + regexp.QuoteMeta(value)}}
	case "endswith":
		return []OpenAPIConstraint{{Name: constraintPattern, Value: regexp.QuoteMeta(value) + "$"}}
	}
	return nil
}

// handleMinConstraint maps 'min' to minLength (strings) or minimum (numbers).
func handleMinConstraint(key, value, effKind string) []OpenAPIConstraint {
	if key != "min" {
		return nil
	}
	if isEffectiveString(effKind) {
		if length, err := strconv.Atoi(value); err == nil {
			return []OpenAPIConstraint{{Name: constraintMinLength, Value: length}}
		}
	} else if isEffectiveNumeric(effKind) {
		//nolint:S8148 // NOSONAR: invalid validation tag values are silently skipped
		if minVal, err := parseNumeric(value); err == nil {
			return []OpenAPIConstraint{{Name: constraintMinimum, Value: minVal}}
		}
	}
	return nil
}

// handleMaxConstraint maps 'max' to maxLength (strings) or maximum (numbers).
func handleMaxConstraint(key, value, effKind string) []OpenAPIConstraint {
	if key != "max" {
		return nil
	}
	if isEffectiveString(effKind) {
		if length, err := strconv.Atoi(value); err == nil {
			return []OpenAPIConstraint{{Name: constraintMaxLength, Value: length}}
		}
	} else if isEffectiveNumeric(effKind) {
		//nolint:S8148 // NOSONAR: invalid validation tag values are silently skipped
		if maxVal, err := parseNumeric(value); err == nil {
			return []OpenAPIConstraint{{Name: constraintMaximum, Value: maxVal}}
		}
	}
	return nil
}

// handleLenConstraint maps 'len' on a string to an exact length (minLength == maxLength).
func handleLenConstraint(key, value, effKind string) []OpenAPIConstraint {
	if key != "len" || !isEffectiveString(effKind) {
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

// handleNumericComparison maps gt/gte/lt/lte: range constraints for numerics, and
// minLength/maxLength for strings (a string comparison constrains its length).
func handleNumericComparison(key, value, effKind string) []OpenAPIConstraint {
	if isEffectiveString(effKind) {
		return handleStringLengthComparison(key, value)
	}
	if !isEffectiveNumeric(effKind) {
		return nil
	}
	numVal, err := parseNumeric(value)
	if err != nil {
		return nil
	}
	switch key {
	case "gt":
		return []OpenAPIConstraint{{Name: constraintMinimum, Value: numVal}, {Name: constraintExclusiveMinimum, Value: true}}
	case "gte":
		return []OpenAPIConstraint{{Name: constraintMinimum, Value: numVal}}
	case "lt":
		return []OpenAPIConstraint{{Name: constraintMaximum, Value: numVal}, {Name: constraintExclusiveMaximum, Value: true}}
	case "lte":
		return []OpenAPIConstraint{{Name: constraintMaximum, Value: numVal}}
	}
	return nil
}

// handleStringLengthComparison maps gt/gte/lt/lte on a string field to
// minLength/maxLength (gt=N -> minLength N+1, lt=N -> maxLength N-1), clamped to
// non-negative so the emitted bound stays valid OpenAPI.
func handleStringLengthComparison(key, value string) []OpenAPIConstraint {
	n, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	switch key {
	case "gt":
		return []OpenAPIConstraint{{Name: constraintMinLength, Value: clampNonNeg(n + 1)}}
	case "gte":
		return []OpenAPIConstraint{{Name: constraintMinLength, Value: clampNonNeg(n)}}
	case "lt":
		return []OpenAPIConstraint{{Name: constraintMaxLength, Value: clampNonNeg(n - 1)}}
	case "lte":
		return []OpenAPIConstraint{{Name: constraintMaxLength, Value: clampNonNeg(n)}}
	}
	return nil
}

func clampNonNeg(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// handleSliceCardinality maps min/max/len on a slice field to minItems/maxItems.
func handleSliceCardinality(key, value string) []OpenAPIConstraint {
	n, err := strconv.Atoi(value)
	if err != nil {
		return nil
	}
	switch key {
	case "min":
		return []OpenAPIConstraint{{Name: constraintMinItems, Value: n}}
	case "max":
		return []OpenAPIConstraint{{Name: constraintMaxItems, Value: n}}
	case "len":
		return []OpenAPIConstraint{{Name: constraintMinItems, Value: n}, {Name: constraintMaxItems, Value: n}}
	}
	return nil
}

// handleEnumConstraint maps 'oneof' to an enum array, numeric-coercing values for
// numeric fields. Tokenization is quote-aware so single-quoted multi-word values
// (oneof='New York' 'Los Angeles') stay intact.
func handleEnumConstraint(key, value, effKind string) []OpenAPIConstraint {
	if key != validatorOneOf {
		return nil
	}
	tokens := tokenizeOneOf(value)
	if len(tokens) == 0 {
		return nil
	}
	return []OpenAPIConstraint{{Name: constraintEnum, Value: coerceEnum(tokens, effKind)}}
}

// handleEqConstraint maps 'eq=<v>' to a single-element enum (the cleanest OpenAPI
// expression of equality). 'ne' has no clean scalar representation and is dropped.
func handleEqConstraint(key, value, effKind string) []OpenAPIConstraint {
	if key != "eq" {
		return nil
	}
	return []OpenAPIConstraint{{Name: constraintEnum, Value: coerceEnum([]string{value}, effKind)}}
}

// coerceEnum converts enum tokens to []any, parsing numerics for numeric fields.
func coerceEnum(tokens []string, effKind string) []any {
	out := make([]any, len(tokens))
	for i, v := range tokens {
		if isEffectiveNumeric(effKind) {
			//nolint:S8148 // NOSONAR: non-numeric tokens fall back to the string value
			if num, err := parseNumeric(v); err == nil {
				out[i] = num
				continue
			}
		}
		out[i] = v
	}
	return out
}

// tokenizeOneOf splits a oneof value on spaces outside single quotes, stripping the
// quotes, so "'New York' 'Los Angeles'" -> ["New York", "Los Angeles"] and
// "active pending" -> ["active", "pending"].
func tokenizeOneOf(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			inQuote = !inQuote
		case c == ' ' && !inQuote:
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return out
}

// datetimeFormat inspects a Go time layout: a layout with no clock/time tokens is
// date-only (-> "date"); anything with a clock/zone token is "date-time".
func datetimeFormat(layout string) string {
	for _, tok := range []string{"15", "03", "3:", "04", "05", ".000", "PM", "pm", "Z07", "-07", "-0700"} {
		if strings.Contains(layout, tok) {
			return formatDateTime
		}
	}
	return formatDate
}

// handlePatternConstraint maps the 'regexp' tag to an OpenAPI pattern.
func handlePatternConstraint(key, value string) []OpenAPIConstraint {
	if key != "regexp" {
		return nil
	}
	return []OpenAPIConstraint{{Name: constraintPattern, Value: value}}
}

// isStringType checks if the type is a string type
func isStringType(typeName string) bool {
	return typeName == goTypeString
}

// isIntegerType reports whether the Go type name is a signed/unsigned integer.
func isIntegerType(typeName string) bool {
	switch typeName {
	case "int", "int8", "int16", "int32", goTypeInt64,
		"uint", "uint8", "uint16", "uint32", "uint64":
		return true
	}
	return false
}

// isFloatType reports whether the Go type name is a floating-point type.
func isFloatType(typeName string) bool {
	return typeName == goTypeFloat32 || typeName == goTypeFloat64
}

// isNumericType checks if the type is a numeric type (integer or float).
func isNumericType(typeName string) bool {
	return isIntegerType(typeName) || isFloatType(typeName)
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
