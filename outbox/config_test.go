package outbox

import (
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/stretchr/testify/assert"
)

func TestApplyDefaultsAllZero(t *testing.T) {
	cfg := &config.OutboxConfig{}
	applyDefaults(cfg)

	assert.Equal(t, DefaultTableName, cfg.TableName)
	assert.Equal(t, 5*time.Second, cfg.PollInterval)
	assert.Equal(t, 100, cfg.BatchSize)
	assert.Equal(t, 5, cfg.MaxRetries)
	assert.Equal(t, 72*time.Hour, cfg.RetentionPeriod)
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
}
