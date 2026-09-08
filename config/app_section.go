package config

import "fmt"

// maxAppNameBytes bounds app.name because the messaging layer stamps it as the
// AMQP app_id property of every publish, and app_id is a wire shortstr: a value
// over 255 bytes cannot be written into the content-header frame, and amqp091
// answers a frame-write failure by tearing down the Connection every publisher
// in the process shares. The 255 is messaging's constant (maxShortStrBytes in
// messaging/publish_destination.go), restated here rather than imported so
// config keeps no dependency on messaging. Restating it is safe in practice: a
// service name is a handful of bytes, so the bound is a backstop against a
// mis-set APP_NAME, not a limit any real deployment approaches.
const maxAppNameBytes = 255

// normalizeApp fills the startup timeout defaults.
func normalizeApp(cfg *AppConfig) error {
	return applyStartupDefaults(&cfg.Startup)
}

// checkApp rejects a missing Name or Version, a Name too long for the AMQP
// app_id shortstr it becomes (see maxAppNameBytes), an Env outside envFormat
// (see its docs for the policy), and negative rate limits.
func checkApp(cfg *AppConfig) error {
	if cfg.Name == "" {
		return NewMissingFieldError("app.name", "APP_NAME", "app.name")
	}

	if len(cfg.Name) > maxAppNameBytes {
		return NewInvalidFieldError(
			"app.name",
			fmt.Sprintf("is %d bytes, limit is %d", len(cfg.Name), maxAppNameBytes),
			nil,
		)
	}

	if cfg.Version == "" {
		return NewMissingFieldError("app.version", "APP_VERSION", "app.version")
	}

	if !envFormat.MatchString(cfg.Env) {
		return NewInvalidFieldError(
			fieldAppEnv,
			fmt.Sprintf("'%s' must be 1-32 lowercase alphanumeric or hyphen, starting with a letter", cfg.Env),
			nil,
		)
	}

	if cfg.Rate.Limit < 0 {
		return NewValidationError(fieldAppRateLimit, errMustBeNonNegative)
	}

	if cfg.Rate.Burst < 0 {
		return NewValidationError("app.rate.burst", errMustBeNonNegative)
	}

	return nil
}

// applyStartupDefaults sets production-safe defaults for startup configuration.
//
// Fallback hierarchy for component timeouts:
//  1. Explicit component value (preserved if set)
//  2. Global Timeout (used when component is 0 and Timeout was explicitly set)
//  3. Per-component default (used when both component and original Timeout are 0)
//
// Default values:
// - Timeout: 10s, Database: 10s, Messaging: 10s, Cache: 5s, Observability: 15s
//
// Returns an error when any value is negative; otherwise returns nil.
func applyStartupDefaults(cfg *StartupConfig) error {
	// Capture whether global timeout was originally set (non-zero)
	globalWasSet := cfg.Timeout != 0

	// Validate and default the global timeout first
	if cfg.Timeout < 0 {
		return NewValidationError("app.startup.timeout", errMustBeNonNegative)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultStartupTimeout
	}

	// Apply defaults to each component using helper
	if err := applyTimeoutDefault(&cfg.Database, "app.startup.database",
		globalWasSet, cfg.Timeout, defaultStartupDatabaseTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Messaging, "app.startup.messaging",
		globalWasSet, cfg.Timeout, defaultStartupMessagingTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Cache, "app.startup.cache",
		globalWasSet, cfg.Timeout, defaultStartupCacheTimeout); err != nil {
		return err
	}
	if err := applyTimeoutDefault(&cfg.Observability, "app.startup.observability",
		globalWasSet, cfg.Timeout, defaultStartupObservabilityTimeout); err != nil {
		return err
	}

	return nil
}
