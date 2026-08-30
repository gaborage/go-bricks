package outbox

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestApplyDefaultsAllZero(t *testing.T) {
	cfg := &config.OutboxConfig{}
	applyDefaults(cfg)

	assert.Equal(t, DefaultTableName, cfg.TableName)
	assert.Equal(t, 5*time.Second, cfg.PollInterval)
	assert.Equal(t, 100, cfg.BatchSize)
	assert.Equal(t, 5, cfg.MaxRetries)
	assert.Equal(t, 72*time.Hour, cfg.RetentionPeriod)
	assert.Equal(t, 60*time.Second, cfg.PublishTimeout)
}

func TestApplyDefaultsPreservesExplicitValues(t *testing.T) {
	cfg := &config.OutboxConfig{
		TableName:       "custom_outbox",
		PollInterval:    10 * time.Second,
		BatchSize:       50,
		MaxRetries:      3,
		RetentionPeriod: 24 * time.Hour,
	}
	applyDefaults(cfg)

	assert.Equal(t, "custom_outbox", cfg.TableName)
	assert.Equal(t, 10*time.Second, cfg.PollInterval)
	assert.Equal(t, 50, cfg.BatchSize)
	assert.Equal(t, 3, cfg.MaxRetries)
	assert.Equal(t, 24*time.Hour, cfg.RetentionPeriod)
}

func TestApplyDefaultsPartialOverride(t *testing.T) {
	cfg := &config.OutboxConfig{
		TableName: "my_outbox",
		// Leave others as zero
	}
	applyDefaults(cfg)

	assert.Equal(t, "my_outbox", cfg.TableName)
	assert.Equal(t, 5*time.Second, cfg.PollInterval)
	assert.Equal(t, 100, cfg.BatchSize)
	assert.Equal(t, 5, cfg.MaxRetries)
	assert.Equal(t, 72*time.Hour, cfg.RetentionPeriod)
	assert.Equal(t, 60*time.Second, cfg.PublishTimeout)
}

func TestValidateConfigNegativePublishTimeout(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval:   5 * time.Second,
		BatchSize:      100,
		PublishTimeout: -1 * time.Second,
	}
	err := validateConfig(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "publishtimeout")
}

func TestValidateConfigValid(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval:    5 * time.Second,
		BatchSize:       100,
		MaxRetries:      5,
		RetentionPeriod: 72 * time.Hour,
		Tenancy:         config.TenancyPerTenant,
	}
	assert.NoError(t, validateConfig(cfg))
}

func TestValidateConfigZeroRetentionAllowed(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval: 5 * time.Second,
		BatchSize:    100,
		MaxRetries:   5,
		Tenancy:      config.TenancyPerTenant,
		// RetentionPeriod = 0 means cleanup disabled
	}
	assert.NoError(t, validateConfig(cfg))
}

func TestValidateConfigNegativePollInterval(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval: -1 * time.Second,
		BatchSize:    100,
	}
	err := validateConfig(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "pollinterval")
}

func TestValidateConfigZeroBatchSize(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval: 5 * time.Second,
		BatchSize:    0,
	}
	err := validateConfig(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batchsize")
}

func TestValidateConfigNegativeMaxRetries(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval: 5 * time.Second,
		BatchSize:    100,
		MaxRetries:   -1,
	}
	err := validateConfig(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "maxretries")
}

func TestValidateConfigNegativeRetentionPeriod(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval:    5 * time.Second,
		BatchSize:       100,
		RetentionPeriod: -1 * time.Hour,
	}
	err := validateConfig(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "retentionperiod")
}

func TestApplyDefaultsNormalizesEmptyTenancyToPerTenant(t *testing.T) {
	cfg := &config.OutboxConfig{}
	applyDefaults(cfg)
	assert.Equal(t, config.TenancyPerTenant, cfg.Tenancy)
}

func TestApplyDefaultsPreservesExplicitTenancy(t *testing.T) {
	cfg := &config.OutboxConfig{Tenancy: config.TenancyShared}
	applyDefaults(cfg)
	assert.Equal(t, config.TenancyShared, cfg.Tenancy)
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
			cfg := &config.OutboxConfig{
				PollInterval: 5 * time.Second,
				BatchSize:    100,
				Tenancy:      tt.tenancy,
			}
			err := validateConfig(cfg)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "tenancy")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateConfigSuperStreams(t *testing.T) {
	tests := []struct {
		name         string
		superStreams []string
		errFrag      string
	}{
		{
			name:         "superstreams_empty_entry",
			superStreams: []string{"orders", ""},
			errFrag:      "superstreams[1] must not be empty",
		},
		{
			name:         "superstreams_duplicate",
			superStreams: []string{"orders", "orders"},
			errFrag:      `lists "orders" twice`,
		},
		{
			name:         "superstreams_ok",
			superStreams: []string{"orders", "payments"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.OutboxConfig{
				PollInterval: 5 * time.Second,
				BatchSize:    100,
				Tenancy:      config.TenancyPerTenant,
				SuperStreams: tt.superStreams,
			}
			err := validateConfig(cfg)
			if tt.errFrag == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errFrag)
		})
	}
}

// TestValidateConfigRejectsOverlongTableName pins the leader-table headroom at the config
// door, so the operator sees the key name. The bound itself is enforced (and boundary-tested)
// in validateTableName, which the store constructors call unconditionally.
func TestValidateConfigRejectsOverlongTableName(t *testing.T) {
	cfg := &config.OutboxConfig{
		PollInterval: 5 * time.Second,
		BatchSize:    100,
		Tenancy:      config.TenancyPerTenant,
		TableName:    strings.Repeat("a", 57),
	}
	err := validateConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox.tablename")
	assert.Contains(t, err.Error(), "56")
}
