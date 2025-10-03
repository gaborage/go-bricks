package config

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors for common configuration states
var (
	// ErrNotConfigured indicates a feature is intentionally not configured (not an error state)
	ErrNotConfigured = errors.New("not configured")
)

// ConfigError represents a configuration error with actionable guidance.
// All error messages are lowercase following Go conventions.
//
//nolint:revive // ConfigError is intentionally named for clarity in external API usage
type ConfigError struct {
	Category string   // error category: "missing", "invalid", "not_configured", "connection"
	Field    string   // config field path (e.g., "database.host", "messaging.broker.url")
	Message  string   // user-friendly error message (lowercase)
	Action   string   // actionable instruction (lowercase)
	Details  []string // additional details or examples
}

// Error implements the error interface with lowercase formatting.
func (e *ConfigError) Error() string {
	var parts []string

	// Add category prefix
	if e.Category != "" {
		parts = append(parts, fmt.Sprintf("config_%s:", e.Category))
	}

	// Add field if present
	if e.Field != "" {
		parts = append(parts, e.Field)
	}

	// Add message
	if e.Message != "" {
		parts = append(parts, e.Message)
	}

	// Add action if present
	if e.Action != "" {
		parts = append(parts, e.Action)
	}

	// Add details if present
	if len(e.Details) > 0 {
		parts = append(parts, strings.Join(e.Details, "; "))
	}

	return strings.Join(parts, " ")
}

// Unwrap returns nil to maintain compatibility with error wrapping.
// ConfigError is a leaf error that contains all necessary context.
func (e *ConfigError) Unwrap() error {
	return nil
}

// NewMissingFieldError creates an error for a required missing configuration field.
func NewMissingFieldError(field, envVar, yamlPath string) *ConfigError {
	action := fmt.Sprintf("set %s env var or add %s to config.yaml", envVar, yamlPath)
	return &ConfigError{
		Category: "missing",
		Field:    field,
		Message:  "required",
		Action:   action,
	}
}

// NewInvalidFieldError creates an error for an invalid configuration value.
func NewInvalidFieldError(field, message string, validOptions []string) *ConfigError {
	err := &ConfigError{
		Category: "invalid",
		Field:    field,
		Message:  message,
	}

	if len(validOptions) > 0 {
		err.Action = fmt.Sprintf("must be one of: %s", strings.Join(validOptions, ", "))
	}

	return err
}

// NewNotConfiguredError creates an informational error for optional features.
// This indicates the feature is intentionally not configured, not an error state.
func NewNotConfiguredError(feature, envVar, yamlPath string) *ConfigError {
	action := fmt.Sprintf("to enable: set %s env var or add %s to config.yaml", envVar, yamlPath)
	return &ConfigError{
		Category: "not_configured",
		Field:    feature,
		Message:  "(optional)",
		Action:   action,
	}
}

// NewConnectionError creates an error for connection failures with configured resources.
func NewConnectionError(resource, message string, troubleshooting []string) *ConfigError {
	return &ConfigError{
		Category: "connection",
		Field:    resource,
		Message:  message,
		Details:  troubleshooting,
	}
}

// IsNotConfigured checks if an error indicates a feature is not configured.
// Returns true for ConfigError with category "not_configured" or errors wrapping ErrNotConfigured.
func IsNotConfigured(err error) bool {
	if err == nil {
		return false
	}

	// Check for ErrNotConfigured sentinel
	if errors.Is(err, ErrNotConfigured) {
		return true
	}

	// Check for ConfigError with not_configured category
	var configErr *ConfigError
	if errors.As(err, &configErr) {
		return configErr.Category == "not_configured"
	}

	return false
}

// NewMultiTenantError creates an error specific to multi-tenant configuration.
func NewMultiTenantError(tenantID, field, message, action string) *ConfigError {
	fullField := fmt.Sprintf("tenant '%s' %s", tenantID, field)
	return &ConfigError{
		Category: "missing",
		Field:    fullField,
		Message:  message,
		Action:   action,
	}
}

// NewValidationError creates a general validation error with custom message.
func NewValidationError(field, message string) *ConfigError {
	return &ConfigError{
		Category: "invalid",
		Field:    field,
		Message:  message,
	}
}
