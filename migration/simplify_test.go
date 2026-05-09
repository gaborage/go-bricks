package migration

import (
	"context"
	"net/url"
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
	db := &config.DatabaseConfig{Password: "s3cr3tValue"}
	out := redactPassword("connecting with s3cr3tValue to host (s3cr3tValue)", db)
	assert.Equal(t, "connecting with [REDACTED] to host ([REDACTED])", out)
}

func TestRedactPasswordEmptyOrNilLeavesOutputUntouched(t *testing.T) {
	assert.Equal(t, "no secrets here", redactPassword("no secrets here", nil))
	assert.Equal(t, "no secrets here", redactPassword("no secrets here", &config.DatabaseConfig{}))
}

func TestRedactPasswordHandlesPercentEncodedJDBCURL(t *testing.T) {
	// JDBC URLs percent-encode reserved characters in the userinfo segment.
	// A password like "p@ssw0rd!" appears as "p%40ssw0rd%21" in Flyway error
	// output; the raw substring will not match, but PathEscape will.
	db := &config.DatabaseConfig{Password: "p@ssw0rd!"}
	output := "Unable to connect to jdbc:postgresql://user:p%40ssw0rd%21@host:5432/db"
	out := redactPassword(output, db)
	assert.NotContains(t, out, "p%40ssw0rd%21")
	assert.NotContains(t, out, "p@ssw0rd!")
	assert.Contains(t, out, "[REDACTED]")
}

func TestRedactPasswordHandlesQueryEncodedForm(t *testing.T) {
	// Form-encoded variant uses '+' for spaces; redaction must catch both.
	db := &config.DatabaseConfig{Password: "pass word!"}
	queryForm := "?password=" + url.QueryEscape(db.Password)
	out := redactPassword("dump: "+queryForm, db)
	assert.NotContains(t, out, url.QueryEscape(db.Password))
	assert.Contains(t, out, "[REDACTED]")
}

func TestRedactPasswordSuppressesOutputForShortPasswords(t *testing.T) {
	// Short passwords substring-collide with unrelated bytes; we drop the
	// whole output instead of risking partial redaction.
	db := &config.DatabaseConfig{Password: "abc"}
	out := redactPassword("connection refused: jdbc:postgresql://user:abc@host", db)
	assert.Equal(t, outputSuppressedSentinel, out)
	assert.NotContains(t, out, "abc")
}

func TestRedactPasswordKeepsAlphanumericLongPasswordsRedactedRaw(t *testing.T) {
	// Pure-alphanumeric passwords need no encoding; raw substring redaction
	// suffices.
	db := &config.DatabaseConfig{Password: "longalphanumeric1"}
	out := redactPassword("error: jdbc:postgresql://user:longalphanumeric1@host/db", db)
	assert.Contains(t, out, "[REDACTED]")
	assert.NotContains(t, out, "longalphanumeric1")
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
