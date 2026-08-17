package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

const (
	testCertPath  = "/etc/ssl/client.crt"
	testKeyPath   = "/etc/ssl/client.key"
	testCAPath    = "/etc/ssl/ca.pem"
	testFieldTLS  = "database.tls"
	testModeFull  = "verify-full"
	testModeOn    = "require"
	testTenantKey = "tenant-a"
)

// pgConfig returns a minimal PostgreSQL config with no TLS material.
func pgConfig() *config.DatabaseConfig {
	return &config.DatabaseConfig{Type: config.PostgreSQL, Host: "db.internal", Database: "app"}
}

// oracleConfig returns a minimal Oracle config with no TLS material.
func oracleConfig() *config.DatabaseConfig {
	return &config.DatabaseConfig{Type: config.Oracle, Host: "db.internal", Database: "PDB1"}
}

// stubProvider is a DBConfigProvider whose response is fixed per test.
type stubProvider struct {
	cfg     *config.DatabaseConfig
	err     error
	lastKey string
}

func (s *stubProvider) DBConfig(_ context.Context, key string) (*config.DatabaseConfig, error) {
	s.lastKey = key
	return s.cfg, s.err
}

func resolve(t *testing.T, cfg *config.DatabaseConfig, key string) (*config.DatabaseConfig, error) {
	t.Helper()
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: cfg}}
	return p.DBConfig(context.Background(), key)
}

// The decorator's job is to put the framework's rules in front of every resolved config.
// These assert the rules actually bite through the seam — not that the seam implements
// them, which is go-bricks' own test surface.
func TestTLSValidatingProviderRejectsFrameworkInvalidTLS(t *testing.T) {
	tests := []struct {
		name    string
		cfg     func() *config.DatabaseConfig
		wantErr string
	}{
		{
			name: "pg_mode_outside_allowlist",
			cfg: func() *config.DatabaseConfig {
				c := pgConfig()
				c.TLS.Mode = "verify-none"
				return c
			},
			wantErr: "database.tls.mode",
		},
		{
			name: "pg_material_under_unset_mode",
			cfg: func() *config.DatabaseConfig {
				c := pgConfig()
				c.TLS.CAFile = testCAPath
				return c
			},
			wantErr: "require a mode that guarantees TLS",
		},
		{
			name: "pg_lone_cert",
			cfg: func() *config.DatabaseConfig {
				c := pgConfig()
				c.TLS.Mode = testModeFull
				c.TLS.CertFile = testCertPath
				return c
			},
			wantErr: "sslcert and sslkey must be configured together",
		},
		{
			name: "pg_tls_alongside_connectionstring",
			cfg: func() *config.DatabaseConfig {
				c := &config.DatabaseConfig{Type: config.PostgreSQL, ConnectionString: "postgres://u:p@h/d"}
				c.TLS.Mode = testModeOn
				return c
			},
			wantErr: "ignored when connectionstring is set",
		},
		{
			name: "oracle_any_tls",
			cfg: func() *config.DatabaseConfig {
				c := oracleConfig()
				c.TLS.Mode = testModeOn
				return c
			},
			wantErr: "not supported for Oracle",
		},
		{
			name: "oracle_inferred_from_dsn_scheme",
			cfg: func() *config.DatabaseConfig {
				// No Type: only the DSN scheme identifies the vendor. The seam infers it,
				// so the block is judged rather than silently skipped.
				c := &config.DatabaseConfig{ConnectionString: "oracle://u:p@h:1521/PDB1"}
				c.TLS.Mode = testModeOn
				return c
			},
			wantErr: "not supported for Oracle",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolve(t, tt.cfg(), "")

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestTLSValidatingProviderAcceptsCoherentTLS(t *testing.T) {
	caOnly := pgConfig()
	caOnly.TLS.Mode = testModeFull
	caOnly.TLS.CAFile = testCAPath

	paired := pgConfig()
	paired.TLS.Mode = testModeFull
	paired.TLS.CertFile = testCertPath
	paired.TLS.KeyFile = testKeyPath

	for name, cfg := range map[string]*config.DatabaseConfig{
		"no_tls_block":  pgConfig(),
		"ca_only":       caOnly,
		"paired_client": paired,
		"oracle_no_tls": oracleConfig(),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := resolve(t, cfg, "")

			require.NoError(t, err)
			require.NotNil(t, got)
		})
	}
}

func TestTLSValidatingProviderPassesValidConfigThrough(t *testing.T) {
	inner := &stubProvider{cfg: pgConfig()}
	p := &tlsValidatingProvider{inner: inner}

	got, err := p.DBConfig(context.Background(), testTenantKey)

	require.NoError(t, err)
	assert.Equal(t, config.PostgreSQL, got.Type)
	assert.Equal(t, testTenantKey, inner.lastKey, "the key must reach the inner provider unchanged")
}

func TestTLSValidatingProviderPropagatesInnerError(t *testing.T) {
	sentinel := errors.New("secret fetch failed")
	p := &tlsValidatingProvider{inner: &stubProvider{err: sentinel}}

	_, err := p.DBConfig(context.Background(), testTenantKey)

	assert.ErrorIs(t, err, sentinel)
}

func TestTLSValidatingProviderNamesTenantOnRejection(t *testing.T) {
	cfg := oracleConfig()
	cfg.TLS.Mode = testModeOn

	_, err := resolve(t, cfg, "tenant-b")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "tenant-b"`)
	assert.Contains(t, err.Error(), "not supported for Oracle")

	// The wrapped ConfigError must stay reachable so callers can still categorize it.
	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, testFieldTLS, cfgErr.Field)
}

func TestTLSValidatingProviderOmitsTenantPrefixForSingleTenant(t *testing.T) {
	cfg := oracleConfig()
	cfg.TLS.CAFile = testCAPath

	_, err := resolve(t, cfg, "")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "tenant", "single-tenant runs have no tenant to name")
}

// config.TenantStore returns a cached, shared pointer for the "" and "named:" keys, and
// the seam normalizes in place, so validating the original would mutate state other
// callers hold — and race under --parallel. The decorator must validate a copy.
func TestTLSValidatingProviderDoesNotMutateTheProvidersConfig(t *testing.T) {
	shared := pgConfig()
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: shared}}

	got, err := p.DBConfig(context.Background(), "")

	require.NoError(t, err)
	assert.NotSame(t, shared, got, "the decorator must not hand back the provider's own pointer")
	// The seam fills pool and timezone defaults; none of that may reach the provider's copy.
	assert.Empty(t, shared.Timezone, "the provider's config must be left un-normalized")
	assert.Equal(t, "UTC", got.Timezone, "the returned copy carries the framework's defaults")
}

// A second resolution of the same shared pointer must behave identically to the first —
// the guard against normalization that silently persisted into the provider's config.
func TestTLSValidatingProviderIsIdempotentAcrossCalls(t *testing.T) {
	shared := oracleConfig()
	shared.TLS.Mode = testModeOn
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: shared}}
	original := shared.TLS.Mode

	_, firstErr := p.DBConfig(context.Background(), testTenantKey)
	_, secondErr := p.DBConfig(context.Background(), testTenantKey)

	require.Error(t, firstErr)
	require.Error(t, secondErr)
	assert.Equal(t, firstErr.Error(), secondErr.Error())
	// Matching errors alone would not prove the invariant: the rejected path could still
	// have normalized the source. Assert the source value directly.
	assert.Equal(t, original, shared.TLS.Mode, "the rejected path must not mutate the provider's config either")
}

func TestTLSValidatingProviderHandlesNilConfig(t *testing.T) {
	got, err := resolve(t, nil, "")

	require.NoError(t, err)
	assert.Nil(t, got)
}

// tlsTenantStoreYAML carries a tenant whose TLS block the framework rejects: a CA file
// under the default (unset) mode, which pgx would silently ignore.
const tlsTenantStoreYAML = `
multitenant:
  enabled: true
  source:
    type: config
  tenants:
    tenant-a:
      database:
        type: postgresql
        host: a.example.com
        port: 5432
        database: tenant_a
        username: u_a
        password: p_a
        tls:
          ca: /etc/ssl/ca.pem
`

// The decorator is only useful if buildConfigProvider actually installs it. Resolving a
// tenant through the real file-credentials path is what proves the wiring — the unit
// tests above would all still pass with the wrapper left off.
func TestBuildConfigProviderRejectsInvalidTenantTLS(t *testing.T) {
	path := writeTenantStoreYAMLContent(t, tlsTenantStoreYAML)

	provider, err := buildConfigProvider(context.Background(), &CommonFlags{
		SourceConfig:    path,
		CredentialsFrom: credsSourceFile,
	}, nil)
	require.NoError(t, err)

	_, err = provider.DBConfig(context.Background(), testTenantKey)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "tenant-a"`)
	assert.Contains(t, err.Error(), "require a mode that guarantees TLS")
}
