package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSchedulerTimezoneDefault(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		expectedTimezone string
	}{
		{name: "empty_defaults_to_utc", input: "", expectedTimezone: "UTC"},
		{name: "explicit_utc_preserved", input: "UTC", expectedTimezone: "UTC"},
		{name: "iana_name_preserved", input: "America/New_York", expectedTimezone: "America/New_York"},
		{name: "asia_iana_preserved", input: "Asia/Tokyo", expectedTimezone: "Asia/Tokyo"},
		{name: "europe_iana_preserved", input: "Europe/London", expectedTimezone: "Europe/London"},
		{name: "dash_sentinel_preserved", input: "-", expectedTimezone: "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Timezone: tt.input}
			err := normalizeScheduler(cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedTimezone, cfg.Timezone)
		})
	}
}

func TestValidateSchedulerTimezoneRejectsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unknown_iana_name", input: "Not/AZone"},
		{name: "garbage_string", input: "xyz"},
		{name: "numeric_offset_not_iana", input: "+05:30"},
		{name: "literal_local_rejected", input: "Local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Timezone: tt.input}
			err := normalizeScheduler(cfg)
			assertValidationError(t, err, "scheduler.timezone")
		})
	}
}

func TestValidateSchedulerTimezoneWiredIntoValidate(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Scheduler.Timezone = "Not/AZone"
	err := Validate(cfg)
	require.ErrorContains(t, err, "scheduler config:")
	assert.ErrorContains(t, err, "scheduler.timezone")
}

func TestNormalizeSchedulerTimeoutDefaults(t *testing.T) {
	tests := []struct {
		name             string
		shutdown         time.Duration
		slowJob          time.Duration
		expectedShutdown time.Duration
		expectedSlowJob  time.Duration
	}{
		{
			name:             "zero_fills_both_defaults",
			expectedShutdown: 30 * time.Second,
			expectedSlowJob:  25 * time.Second,
		},
		{
			name:             "explicit_values_preserved",
			shutdown:         90 * time.Second,
			slowJob:          2 * time.Second,
			expectedShutdown: 90 * time.Second,
			expectedSlowJob:  2 * time.Second,
		},
		{
			name:             "zero_fills_only_the_unset_key",
			slowJob:          2 * time.Second,
			expectedShutdown: 30 * time.Second,
			expectedSlowJob:  2 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Timeout: SchedulerTimeoutConfig{Shutdown: tt.shutdown, SlowJob: tt.slowJob}}

			require.NoError(t, normalizeScheduler(cfg))

			assert.Equal(t, tt.expectedShutdown, cfg.Timeout.Shutdown)
			assert.Equal(t, tt.expectedSlowJob, cfg.Timeout.SlowJob)
		})
	}
}

// A negative duration used to be absorbed by the scheduler module's use-time
// fallback. With that fallback gone it would reach the module verbatim, so
// normalization rejects it here instead.
func TestNormalizeSchedulerRejectsNegativeTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		timeout SchedulerTimeoutConfig
		field   string
	}{
		{
			name:    "negative_shutdown",
			timeout: SchedulerTimeoutConfig{Shutdown: -time.Second},
			field:   "scheduler.timeout.shutdown",
		},
		{
			name:    "negative_slowjob",
			timeout: SchedulerTimeoutConfig{SlowJob: -time.Second},
			field:   "scheduler.timeout.slowjob",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := normalizeScheduler(&SchedulerConfig{Timeout: tt.timeout})

			assertValidationError(t, err, tt.field)
		})
	}
}

// The keys reach hand-built configs through Validate (ADR-064), which is the
// only path that ever normalized them for a config koanf never loaded.
func TestValidateFillsSchedulerTimeoutsOnHandBuiltConfig(t *testing.T) {
	cfg := createValidFullConfig()
	cfg.Scheduler.Timeout = SchedulerTimeoutConfig{}

	require.NoError(t, Validate(cfg))

	assert.Equal(t, 30*time.Second, cfg.Scheduler.Timeout.Shutdown)
	assert.Equal(t, 25*time.Second, cfg.Scheduler.Timeout.SlowJob)
}

func TestValidateSchedulerCIDRListRejectsAllInvalid(t *testing.T) {
	tests := []struct {
		name      string
		security  SchedulerSecurityConfig
		wantField string
	}{
		{
			name:      "allowlist_all_invalid",
			security:  SchedulerSecurityConfig{CIDRAllowlist: []string{"not-a-cidr", "also-bad"}},
			wantField: "scheduler.security.cidrallowlist",
		},
		{
			name:      "trustedproxies_all_invalid",
			security:  SchedulerSecurityConfig{TrustedProxies: []string{"garbage"}},
			wantField: "scheduler.security.trustedproxies",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Security: tt.security}
			err := checkScheduler(cfg)
			assertValidationError(t, err, tt.wantField)
		})
	}
}

func TestValidateSchedulerCIDRListAcceptsValidCases(t *testing.T) {
	tests := []struct {
		name     string
		security SchedulerSecurityConfig
	}{
		{name: "empty_lists", security: SchedulerSecurityConfig{}},
		{name: "single_valid", security: SchedulerSecurityConfig{CIDRAllowlist: []string{"10.0.0.0/8"}}},
		{name: "valid_multi", security: SchedulerSecurityConfig{CIDRAllowlist: []string{"10.0.0.0/8", "192.168.0.0/16"}}},
		{name: "partial_invalid_keeps_valid", security: SchedulerSecurityConfig{CIDRAllowlist: []string{"10.0.0.0/8", "bad"}}},
		{name: "valid_trustedproxies", security: SchedulerSecurityConfig{TrustedProxies: []string{"172.16.0.0/12"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Security: tt.security}
			require.NoError(t, checkScheduler(cfg))
		})
	}
}

// TestLoadFailsOnAllInvalidCIDRAllowlistEnv proves the env -> comma-split -> validate
// chain: a multi-element env list of invalid CIDRs splits into several invalid entries,
// which then fail startup instead of silently degrading to localhost-only.
func TestLoadFailsOnAllInvalidCIDRAllowlistEnv(t *testing.T) {
	clearEnvironmentVariables()
	t.Setenv("SCHEDULER_SECURITY_CIDRALLOWLIST", "bad1,bad2")
	cfg, err := Load()
	require.Error(t, err)
	assert.Nil(t, cfg)
	assert.Contains(t, err.Error(), "scheduler.security.cidrallowlist")
}
