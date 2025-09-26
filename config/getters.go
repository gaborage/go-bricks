package config

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// Error message constants for getter methods
	errMsgRequiredKeyMissing   = "required configuration key '%s' is missing"
	errMsgConfigNotInitialized = "configuration not initialized"
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
		return "", fmt.Errorf(errMsgRequiredKeyMissing, key)
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
		return 0, fmt.Errorf("required configuration key '%s' is invalid: %w", key, err)
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
		return 0, fmt.Errorf("required configuration key '%s' is invalid: %w", key, err)
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
		return 0, fmt.Errorf("required configuration key '%s' is invalid: %w", key, err)
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
		return false, fmt.Errorf("required configuration key '%s' is invalid: %w", key, err)
	}
	return b, nil
}

// Unmarshal unmarshals a configuration section into the provided struct.
func (c *Config) Unmarshal(key string, out any) error {
	if c == nil || c.k == nil {
		return errors.New(errMsgConfigNotInitialized)
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

// rawValue retrieves a raw configuration value.
func (c *Config) rawValue(key string) (any, bool) {
	if c == nil || c.k == nil || !c.k.Exists(key) {
		return nil, false
	}
	return c.k.Get(key), true
}

// rawRequiredValue retrieves a raw configuration value for required fields.
func (c *Config) rawRequiredValue(key string) (any, error) {
	if c == nil || c.k == nil {
		return nil, errors.New(errMsgConfigNotInitialized)
	}
	if !c.k.Exists(key) {
		return nil, fmt.Errorf(errMsgRequiredKeyMissing, key)
	}
	return c.k.Get(key), nil
}

// optionalDefault returns the first override if provided, otherwise returns zero value.
func optionalDefault[T any](zero T, overrides ...T) T {
	if len(overrides) > 0 {
		return overrides[0]
	}
	return zero
}
