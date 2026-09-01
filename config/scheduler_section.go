package config

// normalizeScheduler normalizes the scheduler section:
//   - Timeout.Shutdown / Timeout.SlowJob: zero applies the default, negative is invalid.
//   - Timezone: default "UTC", "-" opt-out for host-local, IANA validation — an
//     invalid zone fails fast at startup.
func normalizeScheduler(cfg *SchedulerConfig) error {
	if err := applyNonNegativeDefault(&cfg.Timeout.Shutdown, defaultSchedulerShutdownTimeout, "scheduler.timeout.shutdown"); err != nil {
		return err
	}

	if err := applyNonNegativeDefault(&cfg.Timeout.SlowJob, defaultSchedulerSlowJobThreshold, "scheduler.timeout.slowjob"); err != nil {
		return err
	}

	normalized, err := normalizeIANATimezone("scheduler.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}

// checkScheduler rejects an entirely unparseable CIDR allowlist or
// trusted-proxy list.
func checkScheduler(cfg *SchedulerConfig) error {
	if err := validateCIDRList("scheduler.security.cidrallowlist", cfg.Security.CIDRAllowlist); err != nil {
		return err
	}
	return validateTrustedProxyList(fieldSchedulerTrustedProxies, cfg.Security.TrustedProxies)
}
