package config

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/knadh/koanf/v2"

	"github.com/gaborage/go-bricks/logger"
)

const (
	// Error message constants for getter methods
	errMsgRequiredKeyMissing   = "required configuration key '%s' is missing"
	errMsgConfigNotInitialized = "configuration not initialized"
	errMsgRequiredKeyInvalid   = "required configuration key '%s' is invalid: %w"
)

// Failure classes reported by the lenient getters. A key is either present and
// empty, or present and holding something no converter accepts.
const (
	classEmpty       = "empty"
	classUnparseable = "unparseable"
)

// warnedKeys remembers which keys already produced a warning, so a getter on a
// hot path reports each unusable key once per process rather than once per call.
// It is also what keeps warnUnusable's logger construction off that hot path.
var warnedKeys sync.Map

// warnUnusable reports a key whose value the caller asked for as kind but no
// converter accepted, once per key per process. The logger is built from the
// config's own Log section on this cold path.
//
// SECURITY: the value itself is never rendered — neither directly nor through
// err.Error(), because strconv quotes its input in the parse error it returns.
// Only the key, the requested type and the failure class are logged.
func (c *Config) warnUnusable(key, kind string, err error) {
	if _, alreadyWarned := warnedKeys.LoadOrStore(key, struct{}{}); alreadyWarned {
		return
	}

	class := classUnparseable
	if errors.Is(err, errEmptyString) {
		class = classEmpty
	}

	// Built here rather than held on the Config: the once-per-key guard above means
	// this runs at most once per unusable key, so it costs nothing on a hot getter,
	// it honors a hand-built Config's own log settings, and Load pays nothing for a
	// process that never reads an unusable key.
	//
	// An unset level is normalized rather than passed through: zerolog parses "" to
	// NoLevel WITHOUT an error, so logger.New keeps it and the warning would be
	// dropped instead of reported. An unparseable level does error there and already
	// degrades to info, which reports.
	level := strings.TrimSpace(c.Log.Level)
	if level == "" {
		level = logger.LevelInfo
	}

	logger.New(level, c.Log.Pretty).Warn().
		Str("key", key).
		Str("type", kind).
		Str("class", class).
		Msg("Configuration key is present but unusable; returning the default")
}

// String retrieves a string value from the configuration or the provided default.
func (c *Config) String(key string, defaultVal ...string) string {
	if c == nil || c.k == nil || !c.k.Exists(key) {
		if len(defaultVal) > 0 {
			return defaultVal[0]
		}
		return ""
	}
	return c.k.String(key)
}

// getLenient is the shared body of the lenient typed getters: an absent key
// returns the default silently, a present-but-unusable one returns the default
// and warns once. It is a free function because a method cannot introduce its
// own type parameter.
func getLenient[T any](c *Config, key, kind string, convert func(any) (T, error), defaultVal ...T) T {
	var zero T

	val, ok := c.rawValue(key)
	if !ok {
		return optionalDefault(zero, defaultVal...)
	}

	converted, err := convert(val)
	if err != nil {
		c.warnUnusable(key, kind, err)
		return optionalDefault(zero, defaultVal...)
	}
	return converted
}

// Int retrieves an int value from the configuration or the provided default.
// See getLenient for the absent / unusable contract; RequiredInt is the
// error-returning door.
func (c *Config) Int(key string, defaultVal ...int) int {
	return getLenient(c, key, "int", toInt, defaultVal...)
}

// Int64 retrieves an int64 value from the configuration or the provided default.
// See getLenient for the absent / unusable contract; RequiredInt64 is the
// error-returning door.
func (c *Config) Int64(key string, defaultVal ...int64) int64 {
	return getLenient(c, key, "int64", toInt64, defaultVal...)
}

// Float64 retrieves a float64 value from the configuration or the provided default.
// See getLenient for the absent / unusable contract; RequiredFloat64 is the
// error-returning door.
func (c *Config) Float64(key string, defaultVal ...float64) float64 {
	return getLenient(c, key, "float64", toFloat64, defaultVal...)
}

// Bool retrieves a bool value from the configuration or the provided default.
// See getLenient for the absent / unusable contract; RequiredBool is the
// error-returning door.
func (c *Config) Bool(key string, defaultVal ...bool) bool {
	return getLenient(c, key, "bool", toBool, defaultVal...)
}

// RequiredString retrieves a required string value from the configuration.
func (c *Config) RequiredString(key string) (string, error) {
	if c == nil || c.k == nil || !c.k.Exists(key) {
		return "", fmt.Errorf(errMsgRequiredKeyMissing, key)
	}

	val := strings.TrimSpace(c.k.String(key))
	if val == "" {
		return "", fmt.Errorf("required configuration key '%s' is empty", key)
	}
	return val, nil
}

// RequiredInt retrieves a required int value from the configuration.
func (c *Config) RequiredInt(key string) (int, error) {
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

// RequiredInt64 retrieves a required int64 value from the configuration.
func (c *Config) RequiredInt64(key string) (int64, error) {
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

// RequiredFloat64 retrieves a required float64 value from the configuration.
func (c *Config) RequiredFloat64(key string) (float64, error) {
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

// RequiredBool retrieves a required bool value from the configuration.
func (c *Config) RequiredBool(key string) (bool, error) {
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
		return errors.New(errMsgConfigNotInitialized)
	}
	// UnmarshalWithConf with our decoder chain so bare numeric time.Duration fields are
	// rejected here too; empty Tag keeps koanf's "koanf" TagName (field-name fallback).
	// unmarshalDecoderConfig (no slice hook) preserves koanf's default string -> []string
	// single-element wrap on this public seam.
	return c.k.UnmarshalWithConf(key, out, koanf.UnmarshalConf{DecoderConfig: unmarshalDecoderConfig()})
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
