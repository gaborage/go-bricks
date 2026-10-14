package config

import "time"

const fieldSchedulerSummaryWindow = "scheduler.summary.window"

// normalizeScheduler normalizes the scheduler section:
//   - Timeout.Shutdown / Timeout.SlowJob / Summary.Window: zero applies the default, negative is invalid.
//   - Timezone: default "UTC", "-" opt-out for host-local, IANA validation — an
//     invalid zone or the literal "Local" fails fast at startup.
func normalizeScheduler(cfg *SchedulerConfig) error {
	if err := applyNonNegativeDefault(&cfg.Timeout.Shutdown, defaultSchedulerShutdownTimeout, "scheduler.timeout.shutdown"); err != nil {
		return err
	}

	if err := applyNonNegativeDefault(&cfg.Timeout.SlowJob, defaultSchedulerSlowJobThreshold, "scheduler.timeout.slowjob"); err != nil {
		return err
	}

	if err := applyNonNegativeDefault(&cfg.Summary.Window, defaultSchedulerSummaryWindow, fieldSchedulerSummaryWindow); err != nil {
		return err
	}

	normalized, err := normalizeIANATimezone("scheduler.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}

// checkScheduler rejects an entirely unparseable CIDR allowlist or
// trusted-proxy list, and a summary window under 1m or one that does not divide
// 24h (a zero window is one normalize never filled; the module refuses it).
func checkScheduler(cfg *SchedulerConfig) error {
	if w := cfg.Summary.Window; w != 0 && (w < time.Minute || (24*time.Hour)%w != 0) {
		return NewValidationError(fieldSchedulerSummaryWindow, "must be at least 1m and divide 24h evenly")
	}
	if err := validateCIDRList("scheduler.security.cidrallowlist", cfg.Security.CIDRAllowlist); err != nil {
		return err
	}
	return validateTrustedProxyList(fieldSchedulerTrustedProxies, cfg.Security.TrustedProxies)
}
