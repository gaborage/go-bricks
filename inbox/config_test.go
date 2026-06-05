package inbox

import (
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, validateConfig(&config.InboxConfig{TableName: "gobricks_inbox", RetentionPeriod: time.Hour}))

	err := validateConfig(&config.InboxConfig{TableName: "gobricks_inbox", RetentionPeriod: -time.Hour})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retentionperiod must not be negative")

	err = validateConfig(&config.InboxConfig{TableName: "schema.inbox", RetentionPeriod: time.Hour})
	require.Error(t, err, "qualified table name rejected")
	assert.Contains(t, err.Error(), "unqualified")
}
