// Package logger provides filtering capabilities for sensitive data in log output.
package logger

import (
	"net/url"
	"reflect"
	"strings"

	"github.com/rs/zerolog"
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
		MaskValue: "***",
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
		config.MaskValue = "***"
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

// FilterValue filters sensitive data from interface{} values
func (f *SensitiveDataFilter) FilterValue(key string, value interface{}) interface{} {
	if f.isSensitiveField(key) {
		return f.config.MaskValue
	}

	// Handle map[string]interface{} recursively
	if m, ok := value.(map[string]interface{}); ok {
		filtered := make(map[string]interface{})
		for k, v := range m {
			filtered[k] = f.FilterValue(k, v)
		}
		return filtered
	}

	// Handle struct recursively using reflection
	if reflect.TypeOf(value).Kind() == reflect.Struct {
		return f.filterStruct(value)
	}

	return value
}

// FilterFields filters a map of fields for sensitive data
func (f *SensitiveDataFilter) FilterFields(fields map[string]interface{}) map[string]interface{} {
	filtered := make(map[string]interface{})
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
		if password, hasPassword := parsed.User.Password(); hasPassword {
			// Simple and elegant: just replace the actual password with mask
			return strings.Replace(urlStr, ":"+password+"@", ":"+f.config.MaskValue+"@", 1)
		}
	}

	// No password to mask, return original URL
	return urlStr
}

// filterStruct filters sensitive fields in struct values using reflection
func (f *SensitiveDataFilter) filterStruct(value interface{}) interface{} {
	val := reflect.ValueOf(value)
	typ := reflect.TypeOf(value)

	if typ.Kind() == reflect.Ptr {
		if val.IsNil() {
			return value
		}
		val = val.Elem()
		typ = typ.Elem()
	}

	if typ.Kind() != reflect.Struct {
		return value
	}

	// Create a map representation of the struct with filtered values
	result := make(map[string]interface{})
	for i := 0; i < val.NumField(); i++ {
		field := typ.Field(i)
		fieldValue := val.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		fieldName := field.Name
		// Use json tag if available
		if tag := field.Tag.Get("json"); tag != "" && tag != "-" {
			if idx := strings.Index(tag, ","); idx != -1 {
				fieldName = tag[:idx]
			} else {
				fieldName = tag
			}
		}

		if fieldValue.CanInterface() {
			result[fieldName] = f.FilterValue(fieldName, fieldValue.Interface())
		}
	}

	return result
}
