package migration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

func TestSafeVendorSegmentKnownPasses(t *testing.T) {
	assert.Equal(t, "postgresql", safeVendorSegment("postgresql", ""))
	assert.Equal(t, "oracle", safeVendorSegment("oracle", ""))
}

func TestSafeVendorSegmentUnknownFallsBack(t *testing.T) {
	// Path-traversal attempt falls back to the migrator's own configured vendor.
	assert.Equal(t, "postgresql", safeVendorSegment("../../tmp", "postgresql"))
	assert.Equal(t, "oracle", safeVendorSegment("rm -rf /", "oracle"))
}

func TestSafeVendorSegmentBothUnknownYieldsSentinel(t *testing.T) {
	assert.Equal(t, "unknown", safeVendorSegment("../../tmp", ""))
	assert.Equal(t, "unknown", safeVendorSegment("../../tmp", "garbage"))
}

func TestRedactPasswordReplacesAll(t *testing.T) {
	db := &config.DatabaseConfig{Password: "s3cr3t"}
	out := redactPassword("connecting with s3cr3t to host (s3cr3t)", db)
	assert.Equal(t, "connecting with [REDACTED] to host ([REDACTED])", out)
}

func TestRedactPasswordEmptyOrNilLeavesOutputUntouched(t *testing.T) {
	assert.Equal(t, "no secrets here", redactPassword("no secrets here", nil))
	assert.Equal(t, "no secrets here", redactPassword("no secrets here", &config.DatabaseConfig{}))
}

func TestSecretsProviderEmptyTenantID(t *testing.T) {
	p := &SecretsProvider{
		Fetch: func(context.Context, string) ([]byte, error) {
			t.Fatal("Fetch should not be called for empty tenantID")
			return nil, nil
		},
	}
	_, err := p.DBConfig(context.Background(), "   ")
	require.ErrorIs(t, err, ErrEmptyTenantID)
}
