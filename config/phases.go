package config

import (
	"errors"
	"fmt"
)

// errNilConfig is Validate's answer to a nil *Config: the same wording
// app.Builder.WithConfig uses, so a direct caller and a builder caller read
// one message.
var errNilConfig = errors.New("configuration required")

// Validate turns cfg into the shape the framework consumes and rejects what it
// cannot accept: normalize, then check. It mutates cfg and is idempotent —
// every construction path calls it (ADR-064), some more than once. A nil cfg
// is an error, not a panic.
func Validate(cfg *Config) error {
	if cfg == nil {
		return errNilConfig
	}
	if err := normalize(cfg); err != nil {
		return err
	}
	return check(cfg)
}

// normalize shapes cfg: infers what can be inferred, fills documented
// defaults, and rejects only what it cannot shape — a contradiction, a value a
// consumer would silently drop, or (for a fill step) a negative where zero
// means "use the default". Presence rejections precede shaping.
func normalize(cfg *Config) error {
	// Presence rejections precede shaping: a section delivered with only empty
	// identity keys cannot be shaped, and its error must not be replaced by the
	// generic "incomplete" one that shaping would raise (ADR-051).
	if err := validateNoDeliveredEmptyDatabase(cfg); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	// Same step, same reason: a list key delivered empty cannot be shaped either, and its
	// empty value disables a control rather than relaxing one (ADR-078).
	if err := validateNoDeliveredEmptyList(cfg); err != nil {
		return err
	}

	if err := normalizeApp(&cfg.App); err != nil {
		return fmt.Errorf("app config: %w", err)
	}

	if err := normalizeServer(&cfg.Server); err != nil {
		return fmt.Errorf("server config: %w", err)
	}

	if err := normalizeScheduler(&cfg.Scheduler); err != nil {
		return fmt.Errorf("scheduler config: %w", err)
	}

	normalizeKeyStore(&cfg.KeyStore)

	if err := normalizeMultitenant(&cfg.Multitenant, &cfg.Source); err != nil {
		return fmt.Errorf("multitenant config: %w", err)
	}

	if err := normalizeDatabaseSection(&cfg.Database, rootDatabaseSection()); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	// Named/tenant DBs share the primary DbManager (see normalizeDatabaseSection), so manager defaults apply only here.
	if err := applyDatabaseManagerDefaults(&cfg.Database.Manager, cfg.Multitenant.Enabled); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	if err := normalizeNamedDatabases(cfg.Databases); err != nil {
		return fmt.Errorf("databases config: %w", err)
	}

	if err := normalizeCache(&cfg.Cache, cfg.Multitenant.Enabled); err != nil {
		return fmt.Errorf("cache config: %w", err)
	}

	if err := normalizeMessaging(&cfg.Messaging, cfg.Multitenant.Enabled); err != nil {
		return fmt.Errorf("messaging config: %w", err)
	}

	return nil
}

// check rejects a normalized cfg without changing it: required identity that
// is still missing, enumerations, and rules that span fields or sections. It
// assumes normalize ran first; callers other than Validate are tests.
func check(cfg *Config) error {
	if err := checkApp(&cfg.App); err != nil {
		return fmt.Errorf("app config: %w", err)
	}

	if err := checkServer(&cfg.Server); err != nil {
		return fmt.Errorf("server config: %w", err)
	}

	if err := checkScheduler(&cfg.Scheduler); err != nil {
		return fmt.Errorf("scheduler config: %w", err)
	}

	if err := checkMultitenant(&cfg.Multitenant, &cfg.Database, &cfg.Messaging, &cfg.Source); err != nil {
		return fmt.Errorf("multitenant config: %w", err)
	}

	if err := checkNamedDatabases(cfg.Databases, &cfg.Multitenant); err != nil {
		return fmt.Errorf("databases config: %w", err)
	}

	if err := checkLog(&cfg.Log); err != nil {
		return fmt.Errorf("log config: %w", err)
	}

	if err := checkCache(&cfg.Cache); err != nil {
		return fmt.Errorf("cache config: %w", err)
	}

	if err := checkMessaging(&cfg.Messaging, cfg.Multitenant.Enabled); err != nil {
		return fmt.Errorf("messaging config: %w", err)
	}

	if err := checkKeyStore(&cfg.KeyStore); err != nil {
		return fmt.Errorf("keystore config: %w", err)
	}

	if err := checkDebug(&cfg.Debug); err != nil {
		return fmt.Errorf("debug config: %w", err)
	}

	return nil
}
