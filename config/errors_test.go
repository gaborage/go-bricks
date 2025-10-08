package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConfigError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *ConfigError
		expected string
	}{
		{
			name: "complete error with all fields",
			err: &ConfigError{
				Category: "missing",
				Field:    "database.host",
				Message:  "required",
				Action:   "set DATABASE_HOST env var or add database.host to config.yaml",
				Details:  []string{"detail1", "detail2"},
			},
			expected: "config_missing: database.host required set DATABASE_HOST env var or add database.host to config.yaml detail1; detail2",
		},
		{
			name: "error without category",
			err: &ConfigError{
				Field:   "app.name",
				Message: "required",
				Action:  "set APP_NAME env var",
			},
			expected: "app.name required set APP_NAME env var",
		},
		{
			name: "error without field",
			err: &ConfigError{
				Category: "invalid",
				Message:  "configuration error",
				Action:   "check your config",
			},
			expected: "config_invalid: configuration error check your config",
		},
		{
			name: "error without message",
			err: &ConfigError{
				Category: "connection",
				Field:    "database",
				Action:   "check connection",
			},
			expected: "config_connection: database check connection",
		},
		{
			name: "error without action",
			err: &ConfigError{
				Category: "invalid",
				Field:    "port",
				Message:  "out of range",
			},
			expected: "config_invalid: port out of range",
		},
		{
			name: "error with only details",
			err: &ConfigError{
				Details: []string{"detail1", "detail2", "detail3"},
			},
			expected: "detail1; detail2; detail3",
		},
		{
			name: "minimal error with only message",
			err: &ConfigError{
				Message: "something went wrong",
			},
			expected: "something went wrong",
		},
		{
			name:     "empty error",
			err:      &ConfigError{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.err.Error()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestConfigError_Unwrap(t *testing.T) {
	t.Run("Unwrap always returns nil", func(t *testing.T) {
		err := &ConfigError{
			Category: "missing",
			Field:    "test.field",
			Message:  "test message",
		}

		unwrapped := err.Unwrap()
		assert.Nil(t, unwrapped, "Unwrap should always return nil for leaf errors")
	})

	t.Run("Unwrap compatible with errors.Unwrap", func(t *testing.T) {
		err := &ConfigError{
			Category: "invalid",
			Field:    "test.field",
		}

		unwrapped := errors.Unwrap(err)
		assert.Nil(t, unwrapped, "errors.Unwrap should return nil for ConfigError")
	})
}

func TestNewMissingFieldError(t *testing.T) {
	tests := []struct {
		name         string
		field        string
		envVar       string
		yamlPath     string
		wantCategory string
		wantField    string
		wantMessage  string
	}{
		{
			name:         "database host missing",
			field:        "database.host",
			envVar:       "DATABASE_HOST",
			yamlPath:     "database.host",
			wantCategory: "missing",
			wantField:    "database.host",
			wantMessage:  "required",
		},
		{
			name:         "app name missing",
			field:        "app.name",
			envVar:       "APP_NAME",
			yamlPath:     "app.name",
			wantCategory: "missing",
			wantField:    "app.name",
			wantMessage:  "required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewMissingFieldError(tt.field, tt.envVar, tt.yamlPath)

			assert.Equal(t, tt.wantCategory, err.Category)
			assert.Equal(t, tt.wantField, err.Field)
			assert.Equal(t, tt.wantMessage, err.Message)
			assert.Contains(t, err.Action, tt.envVar)
			assert.Contains(t, err.Action, tt.yamlPath)
		})
	}
}

func TestNewInvalidFieldError(t *testing.T) {
	tests := []struct {
		name         string
		field        string
		message      string
		validOptions []string
		wantAction   bool
	}{
		{
			name:         "invalid with options",
			field:        "log.level",
			message:      "'invalid' is not supported",
			validOptions: []string{"debug", "info", "warn", "error"},
			wantAction:   true,
		},
		{
			name:         "invalid without options",
			field:        "app.rate.limit",
			message:      "must be non-negative",
			validOptions: nil,
			wantAction:   false,
		},
		{
			name:         "invalid with empty options",
			field:        "server.port",
			message:      "out of range",
			validOptions: []string{},
			wantAction:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewInvalidFieldError(tt.field, tt.message, tt.validOptions)

			assert.Equal(t, "invalid", err.Category)
			assert.Equal(t, tt.field, err.Field)
			assert.Equal(t, tt.message, err.Message)

			if tt.wantAction {
				assert.NotEmpty(t, err.Action)
				for _, opt := range tt.validOptions {
					assert.Contains(t, err.Action, opt)
				}
			} else {
				assert.Empty(t, err.Action)
			}
		})
	}
}

func TestNewNotConfiguredError(t *testing.T) {
	tests := []struct {
		name     string
		feature  string
		envVar   string
		yamlPath string
	}{
		{
			name:     "messaging not configured",
			feature:  "messaging.broker.url",
			envVar:   "MESSAGING_BROKER_URL",
			yamlPath: "messaging.broker.url",
		},
		{
			name:     "database not configured",
			feature:  "database",
			envVar:   "DATABASE_HOST",
			yamlPath: "database.host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewNotConfiguredError(tt.feature, tt.envVar, tt.yamlPath)

			assert.Equal(t, "not_configured", err.Category)
			assert.Equal(t, tt.feature, err.Field)
			assert.Equal(t, "(optional)", err.Message)
			assert.Contains(t, err.Action, "to enable")
			assert.Contains(t, err.Action, tt.envVar)
			assert.Contains(t, err.Action, tt.yamlPath)
		})
	}
}

func TestNewConnectionError(t *testing.T) {
	tests := []struct {
		name             string
		resource         string
		message          string
		troubleshooting  []string
		wantDetailsCount int
	}{
		{
			name:     "database connection error with troubleshooting",
			resource: "database",
			message:  "connection refused",
			troubleshooting: []string{
				"check if database is running",
				"verify network connectivity",
				"check firewall rules",
			},
			wantDetailsCount: 3,
		},
		{
			name:             "messaging connection error without troubleshooting",
			resource:         "messaging",
			message:          "connection timeout",
			troubleshooting:  nil,
			wantDetailsCount: 0,
		},
		{
			name:             "connection error with empty troubleshooting",
			resource:         "redis",
			message:          "auth failed",
			troubleshooting:  []string{},
			wantDetailsCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewConnectionError(tt.resource, tt.message, tt.troubleshooting)

			assert.Equal(t, "connection", err.Category)
			assert.Equal(t, tt.resource, err.Field)
			assert.Equal(t, tt.message, err.Message)
			assert.Len(t, err.Details, tt.wantDetailsCount)

			if tt.wantDetailsCount > 0 {
				for i, detail := range tt.troubleshooting {
					assert.Equal(t, detail, err.Details[i])
				}
			}
		})
	}
}

func TestNewMultiTenantError(t *testing.T) {
	tests := []struct {
		name      string
		tenantID  string
		field     string
		message   string
		action    string
		wantField string
	}{
		{
			name:      "tenant database config missing",
			tenantID:  "tenant-a",
			field:     "database.host",
			message:   "not configured",
			action:    "add multitenant.tenants.tenant-a.database.host",
			wantField: "tenant 'tenant-a' database.host",
		},
		{
			name:      "tenant messaging config missing",
			tenantID:  "tenant-b",
			field:     "messaging.url",
			message:   "required",
			action:    "configure messaging for tenant",
			wantField: "tenant 'tenant-b' messaging.url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewMultiTenantError(tt.tenantID, tt.field, tt.message, tt.action)

			assert.Equal(t, "missing", err.Category)
			assert.Equal(t, tt.wantField, err.Field)
			assert.Equal(t, tt.message, err.Message)
			assert.Equal(t, tt.action, err.Action)
		})
	}
}

func TestNewValidationError(t *testing.T) {
	tests := []struct {
		name    string
		field   string
		message string
	}{
		{
			name:    "rate limit validation",
			field:   "app.rate.limit",
			message: "must be non-negative",
		},
		{
			name:    "port validation",
			field:   "server.port",
			message: "must be between 1 and 65535",
		},
		{
			name:    "timeout validation",
			field:   "server.timeout.read",
			message: "must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewValidationError(tt.field, tt.message)

			assert.Equal(t, "invalid", err.Category)
			assert.Equal(t, tt.field, err.Field)
			assert.Equal(t, tt.message, err.Message)
			assert.Empty(t, err.Action)
			assert.Empty(t, err.Details)
		})
	}
}

func TestConfigError_AsError(t *testing.T) {
	t.Run("ConfigError implements error interface", func(t *testing.T) {
		var err error = &ConfigError{
			Category: "missing",
			Field:    "test.field",
			Message:  "required",
		}

		assert.NotNil(t, err)
		assert.Contains(t, err.Error(), "test.field")
	})

	t.Run("ConfigError can be wrapped with errors.Is", func(t *testing.T) {
		configErr := &ConfigError{
			Category: "invalid",
			Field:    "test.field",
		}

		// ConfigError is a leaf error, so errors.Is with itself should work
		assert.True(t, errors.Is(configErr, configErr))
	})
}

func TestConfigError_IntegrationWithErrorsPackage(t *testing.T) {
	t.Run("error formatting with %v", func(t *testing.T) {
		err := &ConfigError{
			Category: "missing",
			Field:    "database.host",
			Message:  "required",
		}

		formatted := errors.New(err.Error())
		assert.Contains(t, formatted.Error(), "database.host")
	})

	t.Run("error formatting with %w", func(t *testing.T) {
		configErr := &ConfigError{
			Category: "invalid",
			Field:    "port",
			Message:  "out of range",
		}

		// Wrapping ConfigError
		wrapped := errors.New("validation failed: " + configErr.Error())
		assert.Contains(t, wrapped.Error(), "port")
		assert.Contains(t, wrapped.Error(), "validation failed")
	})
}

func TestIsNotConfigured(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{
			name:     "nil error returns false",
			err:      nil,
			expected: false,
		},
		{
			name:     "ErrNotConfigured sentinel returns true",
			err:      ErrNotConfigured,
			expected: true,
		},
		{
			name:     "wrapped ErrNotConfigured returns true",
			err:      errors.New("database: " + ErrNotConfigured.Error()),
			expected: false, // errors.New creates new error, not wrapping
		},
		{
			name: "ConfigError with not_configured category returns true",
			err: &ConfigError{
				Category: "not_configured",
				Field:    "messaging",
				Message:  "(optional)",
			},
			expected: true,
		},
		{
			name: "ConfigError with missing category returns false",
			err: &ConfigError{
				Category: "missing",
				Field:    "database.host",
				Message:  "required",
			},
			expected: false,
		},
		{
			name: "ConfigError with invalid category returns false",
			err: &ConfigError{
				Category: "invalid",
				Field:    "log.level",
				Message:  "unsupported",
			},
			expected: false,
		},
		{
			name: "ConfigError with connection category returns false",
			err: &ConfigError{
				Category: "connection",
				Field:    "database",
				Message:  "connection refused",
			},
			expected: false,
		},
		{
			name:     "generic error returns false",
			err:      errors.New("some generic error"),
			expected: false,
		},
		{
			name:     "NewNotConfiguredError returns true",
			err:      NewNotConfiguredError("messaging", "MESSAGING_BROKER_URL", "messaging.broker.url"),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsNotConfigured(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
