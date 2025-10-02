package config

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// InjectInto populates a struct with configuration values based on struct tags.
// It supports the following struct tags:
//   - `config:"key.path"` - specifies the configuration key to use
//   - `required:"true"` - marks the field as required (default: false)
//   - `default:"value"` - provides a default value if the config key is missing
//
// Supported field types: string, int, int64, float64, bool, time.Duration
func (c *Config) InjectInto(target any) error {
	if c == nil || c.k == nil {
		return &ConfigError{
			Category: "invalid",
			Field:    "config",
			Message:  "not initialized",
			Action:   "ensure config is loaded before injection",
		}
	}

	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return &ConfigError{
			Category: "invalid",
			Field:    "injection target",
			Message:  fmt.Sprintf("must be pointer to struct, got %T", target),
			Action:   "pass &yourStruct to InjectInto()",
		}
	}

	rv = rv.Elem() // Dereference pointer to get struct value
	rt := rv.Type()

	for i := 0; i < rv.NumField(); i++ {
		field := rv.Field(i)
		fieldType := rt.Field(i)

		// Skip unexported fields
		if !field.CanSet() {
			continue
		}

		// Get struct tags
		configKey := fieldType.Tag.Get("config")
		if configKey == "" {
			continue // Skip fields without config tag
		}

		required := fieldType.Tag.Get("required") == "true"
		defaultValue, hasDefault := fieldType.Tag.Lookup("default")

		// Set field value based on config
		if err := c.setFieldValue(field, configKey, required, defaultValue, hasDefault); err != nil {
			// Enhance error with field name if not already present
			if cerr, ok := err.(*ConfigError); ok && cerr.Field == configKey {
				return err
			}
			return fmt.Errorf("field %s: %w", fieldType.Name, err)
		}
	}

	return nil
}

// setFieldValue sets a struct field value from configuration
func (c *Config) setFieldValue(field reflect.Value, configKey string, required bool, defaultValue string, hasDefault bool) error {
	value, shouldSet, err := c.resolveFieldValue(configKey, required, defaultValue, hasDefault)
	if err != nil {
		return err
	}
	if !shouldSet {
		return nil
	}

	// Handle special types first
	if field.Type() == reflect.TypeOf(time.Duration(0)) {
		durationVal, err := c.convertToDuration(value, configKey)
		if err != nil {
			return err
		}
		field.Set(reflect.ValueOf(durationVal))
		return nil
	}

	return c.assignFieldValue(field, configKey, required, value)
}

func (c *Config) resolveFieldValue(configKey string, required bool, defaultValue string, hasDefault bool) (value any, shouldSet bool, err error) {
	if c.k.Exists(configKey) {
		return c.k.Get(configKey), true, nil
	}

	if required {
		// Convert config key to env var format (e.g., "custom.api.key" -> "CUSTOM_API_KEY")
		envVar := strings.ToUpper(strings.ReplaceAll(configKey, ".", "_"))
		return nil, false, &ConfigError{
			Category: "missing",
			Field:    configKey,
			Message:  "required",
			Action:   fmt.Sprintf("set %s env var or add '%s' to config.yaml", envVar, configKey),
		}
	}

	if hasDefault {
		return defaultValue, true, nil
	}

	return nil, false, nil
}

func (c *Config) assignFieldValue(field reflect.Value, configKey string, required bool, value any) error {
	switch field.Kind() {
	case reflect.String:
		return c.assignStringField(field, configKey, required, value)
	case reflect.Int, reflect.Int64:
		return c.assignIntegerField(field, configKey, value)
	case reflect.Float64:
		return c.assignFloatField(field, configKey, value)
	case reflect.Bool:
		return c.assignBoolField(field, configKey, value)
	default:
		return &ConfigError{
			Category: "invalid",
			Field:    configKey,
			Message:  fmt.Sprintf("unsupported type %s", field.Type()),
			Action:   "use string, int, int64, float64, bool, or time.Duration",
		}
	}
}

// Type-specific field assignment methods to reduce cognitive complexity

func (c *Config) assignStringField(field reflect.Value, configKey string, required bool, value any) error {
	strVal, err := c.convertToString(value, configKey, required)
	if err != nil {
		return err
	}
	field.SetString(strVal)
	return nil
}

func (c *Config) assignIntegerField(field reflect.Value, configKey string, value any) error {
	intVal, err := c.convertToInt64(value, configKey)
	if err != nil {
		return err
	}
	if field.Kind() == reflect.Int {
		if intVal > int64(maxInt) || intVal < int64(minInt) {
			return &ConfigError{
				Category: "invalid",
				Field:    configKey,
				Message:  fmt.Sprintf("value %d overflows int", intVal),
				Action:   "use int64 type or reduce value",
			}
		}
	}
	field.SetInt(intVal)
	return nil
}

func (c *Config) assignFloatField(field reflect.Value, configKey string, value any) error {
	floatVal, err := c.convertToFloat64(value, configKey)
	if err != nil {
		return err
	}
	field.SetFloat(floatVal)
	return nil
}

func (c *Config) assignBoolField(field reflect.Value, configKey string, value any) error {
	boolVal, err := c.convertToBool(value, configKey)
	if err != nil {
		return err
	}
	field.SetBool(boolVal)
	return nil
}

// Helper conversion methods with better error messages

func (c *Config) convertToString(value any, key string, required bool) (string, error) {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if required && trimmed == "" {
			envVar := strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
			return "", &ConfigError{
				Category: "missing",
				Field:    key,
				Message:  "cannot be empty",
				Action:   fmt.Sprintf("set %s env var or add '%s' to config.yaml", envVar, key),
			}
		}
		return trimmed, nil
	default:
		return fmt.Sprintf("%v", value), nil
	}
}

func (c *Config) convertToInt64(value any, key string) (int64, error) {
	result, err := toInt64(value)
	if err != nil {
		return 0, &ConfigError{
			Category: "invalid",
			Field:    key,
			Message:  fmt.Sprintf("'%v' is not a valid integer", value),
			Action:   "provide integer value",
		}
	}
	return result, nil
}

func (c *Config) convertToFloat64(value any, key string) (float64, error) {
	result, err := toFloat64(value)
	if err != nil {
		return 0, &ConfigError{
			Category: "invalid",
			Field:    key,
			Message:  fmt.Sprintf("'%v' is not a valid float", value),
			Action:   "provide numeric value",
		}
	}
	return result, nil
}

func (c *Config) convertToBool(value any, key string) (bool, error) {
	result, err := toBool(value)
	if err != nil {
		return false, &ConfigError{
			Category: "invalid",
			Field:    key,
			Message:  fmt.Sprintf("'%v' is not a valid boolean", value),
			Action:   "use true/false, 1/0, yes/no",
		}
	}
	return result, nil
}

func (c *Config) convertToDuration(value any, key string) (time.Duration, error) {
	switch v := value.(type) {
	case string:
		duration, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return 0, &ConfigError{
				Category: "invalid",
				Field:    key,
				Message:  fmt.Sprintf("'%v' is not a valid duration", value),
				Action:   "use format like '30s', '5m', '2h'",
			}
		}
		return duration, nil
	case time.Duration:
		return v, nil
	default:
		return 0, &ConfigError{
			Category: "invalid",
			Field:    key,
			Message:  fmt.Sprintf("unsupported type %T for duration", value),
			Action:   "provide string duration like '30s', '5m', '2h'",
		}
	}
}
