package config

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"
)

const (
	maxInt = int(^uint(0) >> 1)
	minInt = -maxInt - 1

	// Error message constants to avoid duplication while preserving context
	errMsgRequiredKeyInvalid      = "required configuration key '%s' is invalid: %w"
	errMsgUnsupportedType         = "unsupported type %T"
	errMsgUnsupportedSignedType   = "unsupported signed int type %T"
	errMsgUnsupportedUnsignedType = "unsupported unsigned int type %T"
)

var (
	maxInt64ExactFloat = math.Nextafter(float64(math.MaxInt64), math.Inf(-1))
	minInt64ExactFloat = float64(math.MinInt64)

	// Error variables for simple messages without format specifiers
	errEmptyString = errors.New("empty string")
)

// GetString retrieves a string value from the configuration or the provided default.
func (c *Config) GetString(key string, defaultVal ...string) string {
	if c == nil || c.k == nil || !c.k.Exists(key) {
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return ""
	}
	return c.k.String(key)
}

// GetInt retrieves an int value from the configuration or the provided default.
func (c *Config) GetInt(key string, defaultVal ...int) int {
	val, ok := c.rawValue(key)
	if !ok {
		return optionalDefault(0, defaultVal...)
	}

	n, err := toInt(val)
	if err != nil {
		return optionalDefault(0, defaultVal...)
	}
	return n
}

// GetInt64 retrieves an int64 value from the configuration or the provided default.
func (c *Config) GetInt64(key string, defaultVal ...int64) int64 {
	val, ok := c.rawValue(key)
	if !ok {
		return optionalDefault(int64(0), defaultVal...)
	}

	n, err := toInt64(val)
	if err != nil {
		return optionalDefault(int64(0), defaultVal...)
	}
	return n
}

// GetFloat64 retrieves a float64 value from the configuration or the provided default.
func (c *Config) GetFloat64(key string, defaultVal ...float64) float64 {
	val, ok := c.rawValue(key)
	if !ok {
		return optionalDefault(float64(0), defaultVal...)
	}

	f, err := toFloat64(val)
	if err != nil {
		return optionalDefault(float64(0), defaultVal...)
	}
	return f
}

// GetBool retrieves a bool value from the configuration or the provided default.
func (c *Config) GetBool(key string, defaultVal ...bool) bool {
	val, ok := c.rawValue(key)
	if !ok {
		return optionalDefault(false, defaultVal...)
	}

	b, err := toBool(val)
	if err != nil {
		return optionalDefault(false, defaultVal...)
	}
	return b
}

// GetRequiredString retrieves a required string value from the configuration.
func (c *Config) GetRequiredString(key string) (string, error) {
	if c == nil || c.k == nil || !c.k.Exists(key) {
		return "", fmt.Errorf("required configuration key '%s' is missing", key)
	}

	val := strings.TrimSpace(c.k.String(key))
	if val == "" {
		return "", fmt.Errorf("required configuration key '%s' is empty", key)
	}
	return val, nil
}

// GetRequiredInt retrieves a required int value from the configuration.
func (c *Config) GetRequiredInt(key string) (int, error) {
	val, err := c.rawRequiredValue(key)
	if err != nil {
		return 0, err
	}

	n, err := toInt(val)
	if err != nil {
		return 0, fmt.Errorf(errMsgRequiredKeyInvalid, key, err)
	}
	return n, nil
}

// GetRequiredInt64 retrieves a required int64 value from the configuration.
func (c *Config) GetRequiredInt64(key string) (int64, error) {
	val, err := c.rawRequiredValue(key)
	if err != nil {
		return 0, err
	}

	n, err := toInt64(val)
	if err != nil {
		return 0, fmt.Errorf(errMsgRequiredKeyInvalid, key, err)
	}
	return n, nil
}

// GetRequiredFloat64 retrieves a required float64 value from the configuration.
func (c *Config) GetRequiredFloat64(key string) (float64, error) {
	val, err := c.rawRequiredValue(key)
	if err != nil {
		return 0, err
	}

	f, err := toFloat64(val)
	if err != nil {
		return 0, fmt.Errorf(errMsgRequiredKeyInvalid, key, err)
	}
	return f, nil
}

// GetRequiredBool retrieves a required bool value from the configuration.
func (c *Config) GetRequiredBool(key string) (bool, error) {
	val, err := c.rawRequiredValue(key)
	if err != nil {
		return false, err
	}

	b, err := toBool(val)
	if err != nil {
		return false, fmt.Errorf(errMsgRequiredKeyInvalid, key, err)
	}
	return b, nil
}

// Unmarshal unmarshals a configuration section into the provided struct.
func (c *Config) Unmarshal(key string, out any) error {
	if c == nil || c.k == nil {
		return fmt.Errorf("configuration not initialized")
	}
	return c.k.Unmarshal(key, out)
}

// Exists checks if a configuration key exists.
func (c *Config) Exists(key string) bool {
	if c == nil || c.k == nil {
		return false
	}
	return c.k.Exists(key)
}

// All returns all configuration as a flattened map.
func (c *Config) All() map[string]any {
	if c == nil || c.k == nil {
		return nil
	}
	return c.k.All()
}

// Custom returns the values under the `custom` namespace.
func (c *Config) Custom() map[string]any {
	if c == nil || c.k == nil {
		return nil
	}
	raw := c.k.Get("custom")
	if m, ok := raw.(map[string]any); ok {
		return m
	}
	return nil
}

func (c *Config) rawValue(key string) (any, bool) {
	if c == nil || c.k == nil || !c.k.Exists(key) {
		return nil, false
	}
	return c.k.Get(key), true
}

func (c *Config) rawRequiredValue(key string) (any, error) {
	if c == nil || c.k == nil {
		return nil, fmt.Errorf("configuration not initialized")
	}
	if !c.k.Exists(key) {
		return nil, fmt.Errorf("required configuration key '%s' is missing", key)
	}
	return c.k.Get(key), nil
}

func optionalDefault[T any](zero T, overrides ...T) T {
	if len(overrides) > 0 {
		return overrides[0]
	}
	return zero
}

func toInt(value any) (int, error) {
	n, err := toInt64(value)
	if err != nil {
		return 0, err
	}
	if n > int64(maxInt) || n < int64(minInt) {
		return 0, fmt.Errorf("value %d overflows int", n)
	}
	return int(n), nil
}

func toInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int, int8, int16, int32:
		return toInt64FromSignedInt(v)
	case uint, uint8, uint16, uint32, uint64:
		return toInt64FromUnsignedInt(v)
	case float32:
		return floatToInt64(float64(v))
	case float64:
		return floatToInt64(v)
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return 0, errEmptyString
		}
		return strconv.ParseInt(str, 10, strconv.IntSize)
	default:
		return 0, fmt.Errorf(errMsgUnsupportedType, value)
	}
}

// toInt64FromSignedInt handles conversion from signed integer types
func toInt64FromSignedInt(value any) (int64, error) {
	switch v := value.(type) {
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	default:
		return 0, fmt.Errorf(errMsgUnsupportedSignedType, value)
	}
}

// toInt64FromUnsignedInt handles conversion from unsigned integer types with overflow checks
func toInt64FromUnsignedInt(value any) (int64, error) {
	switch v := value.(type) {
	case uint8:
		return int64(v), nil
	case uint16:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	case uint:
		if uint64(v) > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("value %d overflows int64", v)
		}
		return int64(v), nil //#nosec G115 -- safe conversion after overflow check
	case uint64:
		if v > uint64(math.MaxInt64) {
			return 0, fmt.Errorf("value %d overflows int64", v)
		}
		return int64(v), nil //#nosec G115 -- safe conversion after overflow check
	default:
		return 0, fmt.Errorf(errMsgUnsupportedUnsignedType, value)
	}
}

func toFloat64(value any) (float64, error) {
	switch v := value.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int8:
		return float64(v), nil
	case int16:
		return float64(v), nil
	case int32:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint:
		return float64(v), nil
	case uint8:
		return float64(v), nil
	case uint16:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return 0, errEmptyString
		}
		return strconv.ParseFloat(str, 64)
	default:
		return 0, fmt.Errorf(errMsgUnsupportedType, value)
	}
}

func toBool(value any) (bool, error) {
	switch v := value.(type) {
	case bool:
		return v, nil
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return false, errEmptyString
		}
		b, err := strconv.ParseBool(str)
		if err != nil {
			return false, err
		}
		return b, nil
	case int, int8, int16, int32, int64:
		n, err := toInt64(v)
		if err != nil {
			return false, err
		}
		return n != 0, nil
	case uint, uint8, uint16, uint32, uint64:
		n, err := toInt64(v)
		if err != nil {
			return false, err
		}
		return n != 0, nil
	default:
		return false, fmt.Errorf(errMsgUnsupportedType, value)
	}
}

func floatToInt64(value float64) (int64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("invalid float value")
	}
	if math.Trunc(value) != value {
		return 0, fmt.Errorf("value %v is not an integer", value)
	}
	if value > maxInt64ExactFloat || value < minInt64ExactFloat {
		return 0, fmt.Errorf("value %v overflows int64", value)
	}
	result := int64(value)
	if float64(result) != value {
		return 0, fmt.Errorf("value %v cannot be represented exactly as int64", value)
	}
	return result, nil
}

// InjectInto populates a struct with configuration values based on struct tags.
// It supports the following struct tags:
//   - `config:"key.path"` - specifies the configuration key to use
//   - `required:"true"` - marks the field as required (default: false)
//   - `default:"value"` - provides a default value if the config key is missing
//
// Supported field types: string, int, int64, float64, bool, time.Duration
func (c *Config) InjectInto(target any) error {
	if c == nil || c.k == nil {
		return fmt.Errorf("configuration not initialized")
	}

	rv := reflect.ValueOf(target)
	if rv.Kind() != reflect.Ptr || rv.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("target must be a pointer to a struct, got %T", target)
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
			return fmt.Errorf("failed to set field %s: %w", fieldType.Name, err)
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
		return nil, false, fmt.Errorf("required configuration key '%s' is missing", configKey)
	}

	if hasDefault {
		return defaultValue, true, nil
	}

	return nil, false, nil
}

func (c *Config) assignFieldValue(field reflect.Value, configKey string, required bool, value any) error {
	switch field.Kind() { //nolint:exhaustive // injection supports a controlled subset of kinds
	case reflect.String:
		strVal, err := c.convertToString(value, configKey, required)
		if err != nil {
			return err
		}
		field.SetString(strVal)

	case reflect.Int, reflect.Int64:
		intVal, err := c.convertToInt64(value, configKey)
		if err != nil {
			return err
		}
		if field.Kind() == reflect.Int {
			if intVal > int64(maxInt) || intVal < int64(minInt) {
				return fmt.Errorf("value %d for key '%s' overflows int", intVal, configKey)
			}
			field.SetInt(intVal)
		} else {
			field.SetInt(intVal)
		}

	case reflect.Float64:
		floatVal, err := c.convertToFloat64(value, configKey)
		if err != nil {
			return err
		}
		field.SetFloat(floatVal)

	case reflect.Bool:
		boolVal, err := c.convertToBool(value, configKey)
		if err != nil {
			return err
		}
		field.SetBool(boolVal)

	default:
		return fmt.Errorf("unsupported field type %s for key '%s'", field.Type(), configKey)
	}

	return nil
}

// Helper conversion methods with better error messages

func (c *Config) convertToString(value any, key string, required bool) (string, error) {
	switch v := value.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if required && trimmed == "" {
			return "", fmt.Errorf("required configuration key '%s' is empty", key)
		}
		return trimmed, nil
	default:
		return fmt.Sprintf("%v", value), nil
	}
}

func (c *Config) convertToInt64(value any, key string) (int64, error) {
	result, err := toInt64(value)
	if err != nil {
		return 0, fmt.Errorf("invalid integer value for key '%s': %w", key, err)
	}
	return result, nil
}

func (c *Config) convertToFloat64(value any, key string) (float64, error) {
	result, err := toFloat64(value)
	if err != nil {
		return 0, fmt.Errorf("invalid float value for key '%s': %w", key, err)
	}
	return result, nil
}

func (c *Config) convertToBool(value any, key string) (bool, error) {
	result, err := toBool(value)
	if err != nil {
		return false, fmt.Errorf("invalid boolean value for key '%s': %w", key, err)
	}
	return result, nil
}

func (c *Config) convertToDuration(value any, key string) (time.Duration, error) {
	switch v := value.(type) {
	case string:
		duration, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return 0, fmt.Errorf("invalid duration value for key '%s': %w", key, err)
		}
		return duration, nil
	case time.Duration:
		return v, nil
	default:
		return 0, fmt.Errorf("unsupported duration type for key '%s': %T", key, value)
	}
}
