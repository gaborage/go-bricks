package config

import "fmt"

// Validate turns cfg into the shape the framework consumes and rejects what it
// cannot accept: normalize, then check. It mutates cfg and is idempotent —
// every construction path calls it (ADR-064), some more than once.
func Validate(cfg *Config) error {
	if err := normalize(cfg); err != nil {
		return err
	}
	return check(cfg)
}

// normalize shapes cfg: infers what can be inferred, fills documented
// defaults, and rejects only what it cannot shape — a contradiction, a value a
// consumer would silently drop, or (for a fill step) a negative where zero
// means "use the default".
func normalize(cfg *Config) error {
	if err := normalizeApp(&cfg.App); err != nil {
		return fmt.Errorf("app config: %w", err)
	}

	if err := normalizeScheduler(&cfg.Scheduler); err != nil {
		return fmt.Errorf("scheduler config: %w", err)
	}

	// The remaining validate* steps still interleave shaping and checks; PR2/PR3 split or move them.
	if err := validateNoDeliveredEmptyDatabase(cfg); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	if err := validateMultitenant(&cfg.Multitenant, &cfg.Database, &cfg.Messaging, &cfg.Source); err != nil {
		return fmt.Errorf("multitenant config: %w", err)
	}

	if err := normalizeDatabaseSection(&cfg.Database, rootDatabaseSection()); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	// Named/tenant DBs share the primary DbManager (see normalizeDatabaseSection), so manager defaults apply only here.
	if err := applyDatabaseManagerDefaults(&cfg.Database.Manager, cfg.Multitenant.Enabled); err != nil {
		return fmt.Errorf("database config: %w", err)
	}

	if err := validateNamedDatabases(cfg.Databases, &cfg.Multitenant); err != nil {
		return fmt.Errorf("databases config: %w", err)
	}

	if err := validateCache(&cfg.Cache, cfg.Multitenant.Enabled); err != nil {
		return fmt.Errorf("cache config: %w", err)
	}

	if err := validateMessaging(&cfg.Messaging, cfg.Multitenant.Enabled); err != nil {
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

	if err := checkLog(&cfg.Log); err != nil {
		return fmt.Errorf("log config: %w", err)
	}

	if err := checkKeyStore(&cfg.KeyStore); err != nil {
		return fmt.Errorf("keystore config: %w", err)
	}

	if err := checkDebug(&cfg.Debug); err != nil {
		return fmt.Errorf("debug config: %w", err)
	}

	return nil
}
