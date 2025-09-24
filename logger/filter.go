// Package logger provides filtering capabilities for sensitive data in log output.
package logger

import (
	"net/url"
	"reflect"
	"strings"
	"unsafe"

	"github.com/rs/zerolog"
)

const (
	// DefaultMaxDepth is the default maximum recursion depth for filtering
	DefaultMaxDepth = 8
)

// FilterConfig defines the configuration for sensitive data filtering
type FilterConfig struct {
	// SensitiveFields contains field names that should be masked in logs
	SensitiveFields []string
	// MaskValue is the value used to replace sensitive data (default: "***")
	MaskValue string
}

// DefaultFilterConfig returns a default configuration with common sensitive field names
func DefaultFilterConfig() *FilterConfig {
	return &FilterConfig{
		SensitiveFields: []string{
			"password", "passwd", "pwd",
			"secret", "key", "api_key", "apikey",
			"token", "access_token", "refresh_token",
			"auth", "authorization",
			"credential", "credentials",
			"broker_url", "database_url", "db_url",
		},
		MaskValue: DefaultMaskValue,
	}
}

// SensitiveDataFilter implements zerolog.Hook to filter sensitive data from logs
type SensitiveDataFilter struct {
	config *FilterConfig
}

// NewSensitiveDataFilter creates a new filter with the given configuration
func NewSensitiveDataFilter(config *FilterConfig) *SensitiveDataFilter {
	if config == nil {
		config = DefaultFilterConfig()
	}
	if config.MaskValue == "" {
		config.MaskValue = DefaultMaskValue
	}
	return &SensitiveDataFilter{config: config}
}

// Run implements zerolog.Hook interface to filter sensitive data
func (f *SensitiveDataFilter) Run(_ *zerolog.Event, _ zerolog.Level, _ string) {
	// Note: zerolog's Event doesn't expose its internal fields directly,
	// so we need to work with the event through its methods.
	// The filtering will be applied when fields are added to the event.

	// This hook serves as a placeholder for the filtering mechanism.
	// The actual filtering happens in the FilterString and FilterValue methods
	// that are called by the enhanced logger methods.
}

// FilterString filters sensitive data from string values
func (f *SensitiveDataFilter) FilterString(key, value string) string {
	if f.isSensitiveField(key) {
		return f.maskString(value)
	}
	return value
}

// FilterValue filters sensitive data from any values
func (f *SensitiveDataFilter) FilterValue(key string, value any) any {
	visited := make(map[uintptr]struct{})
	return f.filterValueWithProtection(key, value, visited, DefaultMaxDepth)
}

// filterValueWithProtection performs filtering with cycle detection and depth limiting
func (f *SensitiveDataFilter) filterValueWithProtection(key string, value any, visited map[uintptr]struct{}, maxDepth int) any {
	if f.isSensitiveField(key) {
		return f.config.MaskValue
	}

	if value == nil {
		return nil
	}

	// Check depth limit
	if maxDepth <= 0 {
		return value
	}

	return f.filterByTypeWithProtection(key, value, visited, maxDepth)
}

// filterByTypeWithProtection dispatches to appropriate handler with cycle detection
func (f *SensitiveDataFilter) filterByTypeWithProtection(key string, value any, visited map[uintptr]struct{}, maxDepth int) any {
	// Handle typed map first (most common case)
	if m, ok := value.(map[string]any); ok {
		return f.filterStringMapWithProtection(m, visited, maxDepth)
	}

	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		return f.filterSliceOrArrayWithProtection(key, rv, visited, maxDepth)
	case reflect.Struct:
		return f.filterStructWithProtection(value, visited, maxDepth)
	case reflect.Pointer:
		if !rv.IsNil() && rv.Type().Elem().Kind() == reflect.Struct {
			return f.filterStructWithProtection(value, visited, maxDepth)
		}
		return value
	default:
		// All other types pass through unchanged
		return value
	}
}

// filterStringMapWithProtection handles map[string]any filtering with cycle detection
func (f *SensitiveDataFilter) filterStringMapWithProtection(m map[string]any, visited map[uintptr]struct{}, maxDepth int) map[string]any {
	filtered := make(map[string]any, len(m))
	for k, v := range m {
		filtered[k] = f.filterValueWithProtection(k, v, visited, maxDepth-1)
	}
	return filtered
}

// filterSliceOrArrayWithProtection handles slice and array filtering with cycle detection
func (f *SensitiveDataFilter) filterSliceOrArrayWithProtection(key string, rv reflect.Value, visited map[uintptr]struct{}, maxDepth int) any {
	// Check if we can get a pointer to track this slice/array for cycles
	if rv.CanAddr() {
		ptr := uintptr(unsafe.Pointer(rv.UnsafeAddr()))
		if _, exists := visited[ptr]; exists {
			return rv.Interface() // Return original if cycle detected
		}
		visited[ptr] = struct{}{}
		defer delete(visited, ptr)
	}

	length := rv.Len()
	filtered := make([]any, length)
	hasChanges := false

	for i := range length {
		elemVal := rv.Index(i)
		elem := elemVal.Interface()

		var filteredElem any
		if f.isStructType(elemVal.Type()) {
			filteredElem = f.filterStructWithProtection(elem, visited, maxDepth-1)
			hasChanges = true // Struct filtering always creates a map
		} else {
			filteredElem = f.filterValueWithProtection(key, elem, visited, maxDepth-1)
			if filteredElem != elem {
				hasChanges = true
			}
		}
		filtered[i] = filteredElem
	}

	// If no changes were made, return the original slice to preserve type
	if !hasChanges {
		return rv.Interface()
	}

	return filtered
}

// isStructType checks if a type is a struct or pointer to struct
func (f *SensitiveDataFilter) isStructType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct || (t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct)
}

// FilterFields filters a map of fields for sensitive data
func (f *SensitiveDataFilter) FilterFields(fields map[string]any) map[string]any {
	filtered := make(map[string]any, len(fields))
	for key, value := range fields {
		filtered[key] = f.FilterValue(key, value)
	}
	return filtered
}

// isSensitiveField checks if a field name is considered sensitive
func (f *SensitiveDataFilter) isSensitiveField(fieldName string) bool {
	lowerFieldName := strings.ToLower(fieldName)
	for _, sensitiveField := range f.config.SensitiveFields {
		if strings.Contains(lowerFieldName, strings.ToLower(sensitiveField)) {
			return true
		}
	}
	return false
}

// maskString masks sensitive string values
func (f *SensitiveDataFilter) maskString(value string) string {
	if value == "" {
		return value
	}

	// Special handling for URLs to preserve structure while masking sensitive parts
	if f.isURL(value) {
		return f.maskURL(value)
	}

	// For all other sensitive strings, completely mask the value
	// No partial disclosure for security reasons
	return f.config.MaskValue
}

// isURL checks if a string appears to be a URL
func (f *SensitiveDataFilter) isURL(value string) bool {
	return strings.HasPrefix(value, "http://") ||
		strings.HasPrefix(value, "https://") ||
		strings.HasPrefix(value, "amqp://") ||
		strings.HasPrefix(value, "amqps://")
}

// maskURL masks sensitive information in URLs (like passwords) while preserving structure
func (f *SensitiveDataFilter) maskURL(urlStr string) string {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		// If parsing fails, fallback to generic masking
		return f.config.MaskValue
	}

	// Mask password in user info
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			username := parsed.User.Username()
			return f.buildMaskedURL(parsed, username)
		}
	}

	// No password to mask, return original URL
	return urlStr
}

// buildMaskedURL constructs a URL with masked password while preserving structure
func (f *SensitiveDataFilter) buildMaskedURL(parsed *url.URL, username string) string {
	var b strings.Builder

	// Scheme and authority
	b.WriteString(parsed.Scheme)
	b.WriteString("://")

	// User info with masked password
	b.WriteString(username)
	b.WriteByte(':')
	b.WriteString(f.config.MaskValue)
	b.WriteByte('@')
	b.WriteString(parsed.Host)

	// Preserve encoded path, query and fragment parts
	if p := parsed.EscapedPath(); p != "" {
		b.WriteString(p)
	}
	if q := parsed.RawQuery; q != "" {
		b.WriteByte('?')
		b.WriteString(q)
	}
	if frag := parsed.Fragment; frag != "" {
		b.WriteByte('#')
		b.WriteString(frag)
	}

	return b.String()
}

// filterStruct filters sensitive fields in struct values using reflection
func (f *SensitiveDataFilter) filterStruct(value any) any {
	visited := make(map[uintptr]struct{})
	return f.filterStructWithProtection(value, visited, DefaultMaxDepth)
}

// filterStructWithProtection filters sensitive fields with cycle detection and depth limiting
func (f *SensitiveDataFilter) filterStructWithProtection(value any, visited map[uintptr]struct{}, maxDepth int) any {
	if value == nil {
		return nil
	}

	// Check depth limit
	if maxDepth <= 0 {
		return value
	}

	structVal, structType, ptr := f.extractStructValueWithPointer(value)
	if !structVal.IsValid() {
		return value
	}

	// Check for cycles using the pointer
	if ptr != 0 {
		if _, exists := visited[ptr]; exists {
			return value // Return original if cycle detected
		}
		visited[ptr] = struct{}{}
		defer delete(visited, ptr)
	}

	return f.buildFilteredStructMapWithProtection(structVal, structType, visited, maxDepth)
}

// extractStructValueWithPointer handles pointer dereferencing and returns tracking pointer
func (f *SensitiveDataFilter) extractStructValueWithPointer(value any) (reflect.Value, reflect.Type, uintptr) {
	val := reflect.ValueOf(value)
	typ := reflect.TypeOf(value)
	var trackingPtr uintptr

	// Handle pointer types and capture the first non-nil pointer for tracking
	for typ.Kind() == reflect.Pointer {
		if val.IsNil() {
			return reflect.Value{}, nil, 0
		}

		// Capture the pointer value for cycle detection on the first pointer
		if trackingPtr == 0 && val.CanAddr() {
			trackingPtr = val.Pointer()
		} else if trackingPtr == 0 {
			// If we can't get address, use the pointer value directly
			trackingPtr = val.Pointer()
		}

		val = val.Elem()
		typ = typ.Elem()
	}

	// If we have a struct value that can be addressed and we haven't captured a pointer yet
	if trackingPtr == 0 && val.CanAddr() {
		trackingPtr = uintptr(unsafe.Pointer(val.UnsafeAddr()))
	}

	// Validate it's a struct
	if typ.Kind() != reflect.Struct {
		return reflect.Value{}, nil, 0
	}

	return val, typ, trackingPtr
}

// buildFilteredStructMapWithProtection creates a map representation with cycle detection
func (f *SensitiveDataFilter) buildFilteredStructMapWithProtection(structVal reflect.Value, structType reflect.Type, visited map[uintptr]struct{}, maxDepth int) map[string]any {
	// Pre-allocate result map with capacity for all fields to reduce allocations
	result := make(map[string]any, structVal.NumField())

	for i := 0; i < structVal.NumField(); i++ {
		field := structType.Field(i)
		fieldValue := structVal.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Only process fields that can be converted to interface{}
		if !fieldValue.CanInterface() {
			continue
		}

		// Extract field name (empty string means skip)
		fieldName := f.extractFieldName(&field)
		if fieldName == "" {
			continue
		}

		result[fieldName] = f.filterValueWithProtection(fieldName, fieldValue.Interface(), visited, maxDepth-1)
	}

	return result
}

// extractFieldName determines the field name to use, preferring json tags
// Returns empty string to signal the field should be skipped
func (f *SensitiveDataFilter) extractFieldName(field *reflect.StructField) string {
	tag := field.Tag.Get("json")

	// Skip fields marked with json:"-"
	if tag == "-" {
		return ""
	}

	// Use struct field name if no json tag
	if tag == "" {
		return field.Name
	}

	// Handle comma-separated json tags (e.g., "name,omitempty")
	if idx := strings.Index(tag, ","); idx != -1 {
		fieldName := tag[:idx]
		// Use struct field name if tag part is empty (e.g., ",omitempty")
		if fieldName == "" {
			return field.Name
		}
		return fieldName
	}

	return tag
}
