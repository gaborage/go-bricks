package inbox

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestApplyDefaults(t *testing.T) {
	c := &config.InboxConfig{}
	applyDefaults(c)
	assert.Equal(t, DefaultTableName, c.TableName)
	assert.Equal(t, DefaultRetentionPeriod, c.RetentionPeriod)
	assert.False(t, c.AutoCreateTable, "AutoCreateTable stays opt-in (false)")
}

func TestApplyDefaultsPreservesExplicitValues(t *testing.T) {
	c := &config.InboxConfig{TableName: "my_inbox", RetentionPeriod: 48 * time.Hour}
	applyDefaults(c)
	assert.Equal(t, "my_inbox", c.TableName)
	assert.Equal(t, 48*time.Hour, c.RetentionPeriod)
}

func TestValidateConfig(t *testing.T) {
	require.NoError(t, validateConfig(&config.InboxConfig{
		TableName: "gobricks_inbox", RetentionPeriod: time.Hour, Tenancy: config.TenancyPerTenant,
	}))

	err := validateConfig(&config.InboxConfig{
		TableName: "gobricks_inbox", RetentionPeriod: -time.Hour, Tenancy: config.TenancyPerTenant,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retentionperiod must not be negative")

	err = validateConfig(&config.InboxConfig{
		TableName: "schema.inbox", RetentionPeriod: time.Hour, Tenancy: config.TenancyPerTenant,
	})
	require.Error(t, err, "qualified table name rejected")
	assert.Contains(t, err.Error(), "unqualified")
}

func TestApplyDefaultsNormalizesEmptyTenancyToPerTenant(t *testing.T) {
	c := &config.InboxConfig{}
	applyDefaults(c)
	assert.Equal(t, config.TenancyPerTenant, c.Tenancy)
}

func TestApplyDefaultsPreservesExplicitTenancy(t *testing.T) {
	c := &config.InboxConfig{Tenancy: config.TenancyShared}
	applyDefaults(c)
	assert.Equal(t, config.TenancyShared, c.Tenancy)
}

func TestValidateConfigTenancy(t *testing.T) {
	tests := []struct {
		name    string
		tenancy string
		wantErr bool
	}{
		{name: "per-tenant_accepted", tenancy: config.TenancyPerTenant, wantErr: false},
		{name: "shared_accepted", tenancy: config.TenancyShared, wantErr: false},
		{name: "bogus_rejected", tenancy: "bogus", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &config.InboxConfig{TableName: "gobricks_inbox", RetentionPeriod: time.Hour, Tenancy: tt.tenancy}
			err := validateConfig(c)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "tenancy")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestHoldDefaultsApplyOnlyWhenTheHoldIsOn pins that the hold's defaults are
// filled for a hold that is on and left alone otherwise: a deployment reading
// inbox.hold.* back must not find a table name for a hold it never enabled.
func TestHoldDefaultsApplyOnlyWhenTheHoldIsOn(t *testing.T) {
	t.Run("hold_defaults_applied", func(t *testing.T) {
		c := &config.InboxConfig{Enabled: true, Tenancy: config.TenancyShared, Hold: config.InboxHoldConfig{Enabled: true}}

		applyDefaults(c)

		assert.Equal(t, DefaultHoldTableName, c.Hold.TableName)
		assert.Equal(t, DefaultHoldDrainInterval, c.Hold.DrainInterval)
		assert.Equal(t, DefaultHoldMaxBackoff, c.Hold.MaxBackoff)
		assert.Equal(t, DefaultHoldMaxAge, c.Hold.MaxAge)
		assert.Equal(t, DefaultHoldLeaseDuration, c.Hold.LeaseDuration)
	})

	t.Run("hold_disabled_leaves_zero_values", func(t *testing.T) {
		c := &config.InboxConfig{Enabled: true}

		applyDefaults(c)

		assert.Empty(t, c.Hold.TableName)
		assert.Zero(t, c.Hold.DrainInterval)
	})

	t.Run("hold_defaults_preserve_explicit_values", func(t *testing.T) {
		c := &config.InboxConfig{Enabled: true, Hold: config.InboxHoldConfig{
			Enabled: true, TableName: "my_hold", DrainInterval: time.Minute,
		}}

		applyDefaults(c)

		assert.Equal(t, "my_hold", c.Hold.TableName)
		assert.Equal(t, time.Minute, c.Hold.DrainInterval)
	})
}

// TestValidateConfigRejectsAnUnusableHold pins the rules a hold owes before the
// drain can run at all.
func TestValidateConfigRejectsAnUnusableHold(t *testing.T) {
	tests := []struct {
		name    string
		hold    config.InboxHoldConfig
		wantErr string
	}{
		{
			name: "accepts_a_defaulted_hold",
			hold: config.InboxHoldConfig{Enabled: true},
		},
		{
			name:    "hold_rejects_negative_backoff",
			hold:    config.InboxHoldConfig{Enabled: true, MaxBackoff: -time.Second},
			wantErr: "maxbackoff",
		},
		{
			name:    "hold_rejects_negative_drain_interval",
			hold:    config.InboxHoldConfig{Enabled: true, DrainInterval: -time.Second},
			wantErr: "draininterval",
		},
		{
			name:    "hold_rejects_negative_max_age",
			hold:    config.InboxHoldConfig{Enabled: true, MaxAge: -time.Second},
			wantErr: "maxage",
		},
		{
			name:    "hold_rejects_negative_lease_duration",
			hold:    config.InboxHoldConfig{Enabled: true, LeaseDuration: -time.Second},
			wantErr: "leaseduration",
		},
		{
			// The rule is POSITIVE, so zero is refused: a zero lease would expire the
			// instant it was taken, and a zero interval would drain in a tight loop.
			// applyDefaults only fills a zero it OWNS, so an explicit zero arrives here.
			name:    "hold_rejects_a_zero_lease_duration",
			hold:    config.InboxHoldConfig{Enabled: true, LeaseDuration: -1},
			wantErr: "leaseduration",
		},
		{
			// And the smallest positive value is accepted, which is what makes the
			// rule a boundary rather than a range.
			name: "hold_accepts_the_smallest_positive_durations",
			hold: config.InboxHoldConfig{
				Enabled: true, DrainInterval: 1, MaxBackoff: 1, MaxAge: 1, LeaseDuration: 1,
			},
		},
		{
			name:    "hold_rejects_bad_table_name",
			hold:    config.InboxHoldConfig{Enabled: true, TableName: "schema.hold"},
			wantErr: "hold",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &config.InboxConfig{Enabled: true, Tenancy: config.TenancyShared, Hold: tc.hold}
			applyDefaults(c)

			err := validateConfig(c)

			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestValidateHoldConfigDoesNotAssumeItsDefaultsRan pins the boundary the
// duration rule turns on. Through applyDefaults a zero can never arrive — every
// zero is filled — so the rule reads as "positive" only when the validator is
// asked directly, which is also the contract: it must not assume its caller ran
// the defaults first.
func TestValidateHoldConfigDoesNotAssumeItsDefaultsRan(t *testing.T) {
	filled := config.InboxHoldConfig{
		Enabled: true, TableName: DefaultHoldTableName, DrainInterval: 1,
		MaxBackoff: 1, MaxAge: 1, LeaseDuration: 1,
	}

	t.Run("every_duration_filled_is_accepted", func(t *testing.T) {
		hold := filled
		assert.NoError(t, validateHoldConfig(&hold))
	})

	for _, tc := range []struct {
		name  string
		blank func(*config.InboxHoldConfig)
		key   string
	}{
		{"drain_interval", func(h *config.InboxHoldConfig) { h.DrainInterval = 0 }, "draininterval"},
		{"max_backoff", func(h *config.InboxHoldConfig) { h.MaxBackoff = 0 }, "maxbackoff"},
		{"max_age", func(h *config.InboxHoldConfig) { h.MaxAge = 0 }, "maxage"},
		{"lease_duration", func(h *config.InboxHoldConfig) { h.LeaseDuration = 0 }, "leaseduration"},
	} {
		t.Run("a_zero_"+tc.name+"_is_refused", func(t *testing.T) {
			hold := filled
			tc.blank(&hold)

			err := validateHoldConfig(&hold)

			require.Error(t, err, "zero is not positive: a zero lease expires the instant it is taken")
			assert.Contains(t, err.Error(), tc.key)
		})
	}
}
