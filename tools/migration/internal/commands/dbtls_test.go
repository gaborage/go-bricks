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
	testCertPath = "/etc/ssl/client.crt"
	testKeyPath  = "/etc/ssl/client.key"
	testCAPath   = "/etc/ssl/ca.pem"
)

// pgConfig returns a minimal PostgreSQL config with no TLS material.
func pgConfig() *config.DatabaseConfig {
	return &config.DatabaseConfig{Type: config.PostgreSQL, Host: "db.internal", Database: "app"}
}

// oracleConfig returns a minimal Oracle config with no TLS material.
func oracleConfig() *config.DatabaseConfig {
	return &config.DatabaseConfig{Type: config.Oracle, Host: "db.internal", Database: "PDB1"}
}

func TestInferDatabaseTypeFromConnectionString(t *testing.T) {
	tests := []struct {
		name string
		cs   string
		want string
	}{
		{name: "postgres_scheme", cs: "postgres://u:p@h:5432/d", want: config.PostgreSQL},
		{name: "postgresql_scheme", cs: "postgresql://u:p@h:5432/d", want: config.PostgreSQL},
		{name: "oracle_scheme", cs: "oracle://u:p@h:1521/PDB1", want: config.Oracle},
		{name: "uppercase_scheme_folds", cs: "POSTGRES://u:p@h/d", want: config.PostgreSQL},
		{name: "leading_whitespace_trimmed", cs: "  oracle://u:p@h/PDB1", want: config.Oracle},
		{name: "unrecognized_scheme", cs: "mysql://u:p@h/d", want: ""},
		{name: "empty", cs: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, inferDatabaseTypeFromConnectionString(tt.cs))
		})
	}
}

func TestValidateDatabaseTLSTrimsFieldsInPlace(t *testing.T) {
	cfg := pgConfig()
	cfg.TLS.Mode = "  require  "
	cfg.TLS.CertFile = " " + testCertPath + " "
	cfg.TLS.KeyFile = "\t" + testKeyPath + "\n"
	cfg.TLS.CAFile = "  " + testCAPath

	require.NoError(t, validateDatabaseTLS(cfg))

	// The DSN builders read these fields directly, so a padded value would reach
	// the wire as sslmode=%20require%20 without the in-place trim.
	assert.Equal(t, sslModeRequire, cfg.TLS.Mode)
	assert.Equal(t, testCertPath, cfg.TLS.CertFile)
	assert.Equal(t, testKeyPath, cfg.TLS.KeyFile)
	assert.Equal(t, testCAPath, cfg.TLS.CAFile)
}

func TestValidateDatabaseTLSRejectsWhitespaceOnlyModeAsUnset(t *testing.T) {
	// A whitespace-only mode trims to "", which is NOT a mandatory mode — material
	// alongside it must still be rejected rather than sneaking past the allowlist.
	cfg := pgConfig()
	cfg.TLS.Mode = "   "
	cfg.TLS.CAFile = testCAPath

	err := validateDatabaseTLS(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "require a mode that guarantees TLS")
}

func TestValidateDatabaseTLSNilConfig(t *testing.T) {
	assert.NoError(t, validateDatabaseTLS(nil))
}

func TestValidateDatabaseTLSDispatchesOnInferredType(t *testing.T) {
	// Type is empty; only the DSN scheme identifies the vendor. Without inference
	// this would fall to the default arm and validate nothing.
	cfg := &config.DatabaseConfig{ConnectionString: "oracle://u:p@h:1521/PDB1"}
	cfg.TLS.Mode = sslModeRequire

	err := validateDatabaseTLS(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported for Oracle")
}

func TestValidateDatabaseTLSUnknownVendorIsNotValidated(t *testing.T) {
	// Mirrors the framework's default arm: an unrecognized vendor is left alone
	// rather than being judged by PostgreSQL's rules.
	cfg := &config.DatabaseConfig{Type: "mysql"}
	cfg.TLS.Mode = "not-a-real-mode"

	assert.NoError(t, validateDatabaseTLS(cfg))
}

func TestValidatePostgreSQLTLSRejectsTLSAlongsideConnectionString(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*config.DatabaseConfig)
	}{
		{name: "mode", apply: func(c *config.DatabaseConfig) { c.TLS.Mode = sslModeRequire }},
		{name: "cert", apply: func(c *config.DatabaseConfig) { c.TLS.CertFile = testCertPath }},
		{name: "key", apply: func(c *config.DatabaseConfig) { c.TLS.KeyFile = testKeyPath }},
		{name: "ca", apply: func(c *config.DatabaseConfig) { c.TLS.CAFile = testCAPath }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.DatabaseConfig{Type: config.PostgreSQL, ConnectionString: "postgres://u:p@h/d"}
			tt.apply(cfg)

			err := validateDatabaseTLS(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "ignored when connectionstring is set")
		})
	}
}

func TestValidatePostgreSQLTLSAllowsConnectionStringWithoutTLSBlock(t *testing.T) {
	cfg := &config.DatabaseConfig{Type: config.PostgreSQL, ConnectionString: "postgres://u:p@h/d?sslmode=verify-full"}

	assert.NoError(t, validateDatabaseTLS(cfg))
}

func TestValidatePostgreSQLTLSModeAllowlist(t *testing.T) {
	for _, mode := range pgSSLModes {
		t.Run("accepts_"+mode, func(t *testing.T) {
			cfg := pgConfig()
			cfg.TLS.Mode = mode

			assert.NoError(t, validateDatabaseTLS(cfg))
		})
	}
}

func TestValidatePostgreSQLTLSRejectsUnknownMode(t *testing.T) {
	cfg := pgConfig()
	cfg.TLS.Mode = "verify-none"

	err := validateDatabaseTLS(cfg)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "database.tls.mode")
	assert.Contains(t, err.Error(), "verify-none")
}

func TestValidatePostgreSQLTLSMaterialRequiresMandatoryMode(t *testing.T) {
	material := []struct {
		name  string
		apply func(*config.DatabaseConfig)
	}{
		// CA alone is server authentication with no client certificate — valid under any
		// TLS-mandatory mode, rejected under the opportunistic ones like every other material.
		{name: "ca", apply: func(c *config.DatabaseConfig) { c.TLS.CAFile = testCAPath }},
		{name: "cert_and_key", apply: func(c *config.DatabaseConfig) {
			c.TLS.CertFile = testCertPath
			c.TLS.KeyFile = testKeyPath
		}},
	}
	// Every mode pgx accepts but under which it may silently skip or downgrade TLS,
	// plus the unset mode (which pgx treats as prefer).
	opportunistic := []string{"", sslModeDisable, sslModeAllow, sslModePrefer}

	for _, m := range material {
		for _, mode := range opportunistic {
			t.Run(m.name+"_under_"+modeLabel(mode), func(t *testing.T) {
				cfg := pgConfig()
				cfg.TLS.Mode = mode
				m.apply(cfg)

				err := validateDatabaseTLS(cfg)

				require.Error(t, err)
				assert.Contains(t, err.Error(), "require a mode that guarantees TLS")
			})
		}
		for _, mode := range pgTLSMandatorySSLModes {
			t.Run(m.name+"_under_"+mode, func(t *testing.T) {
				cfg := pgConfig()
				cfg.TLS.Mode = mode
				m.apply(cfg)

				assert.NoError(t, validateDatabaseTLS(cfg))
			})
		}
	}
}

func modeLabel(mode string) string {
	if mode == "" {
		return "unset"
	}
	return mode
}

func TestValidatePostgreSQLTLSRequiresCertAndKeyTogether(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*config.DatabaseConfig)
	}{
		{name: "cert_without_key", apply: func(c *config.DatabaseConfig) { c.TLS.CertFile = testCertPath }},
		{name: "key_without_cert", apply: func(c *config.DatabaseConfig) { c.TLS.KeyFile = testKeyPath }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := pgConfig()
			cfg.TLS.Mode = sslModeVerifyFull // mandatory mode, so only the pairing rule can fire
			tt.apply(cfg)

			err := validateDatabaseTLS(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "sslcert and sslkey must be configured together")
		})
	}
}

func TestValidatePostgreSQLTLSAcceptsNoTLSBlock(t *testing.T) {
	assert.NoError(t, validateDatabaseTLS(pgConfig()))
}

func TestValidateOracleTLSRejectsEveryTLSField(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*config.DatabaseConfig)
	}{
		{name: "mode", apply: func(c *config.DatabaseConfig) { c.TLS.Mode = sslModeRequire }},
		{name: "cert", apply: func(c *config.DatabaseConfig) { c.TLS.CertFile = testCertPath }},
		{name: "key", apply: func(c *config.DatabaseConfig) { c.TLS.KeyFile = testKeyPath }},
		{name: "ca", apply: func(c *config.DatabaseConfig) { c.TLS.CAFile = testCAPath }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := oracleConfig()
			tt.apply(cfg)

			err := validateDatabaseTLS(cfg)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "not supported for Oracle")
		})
	}
}

func TestValidateOracleTLSAcceptsNoTLSBlock(t *testing.T) {
	assert.NoError(t, validateDatabaseTLS(oracleConfig()))
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

func TestTLSValidatingProviderPassesValidConfigThrough(t *testing.T) {
	inner := &stubProvider{cfg: pgConfig()}
	p := &tlsValidatingProvider{inner: inner}

	got, err := p.DBConfig(context.Background(), "tenant-a")

	require.NoError(t, err)
	assert.Equal(t, *inner.cfg, *got, "contents must survive the copy unchanged")
	assert.Equal(t, "tenant-a", inner.lastKey, "the key must reach the inner provider unchanged")
}

// config.TenantStore returns a cached, shared pointer for the "" and "named:" keys, so
// trimming the resolved config in place would mutate state other callers hold — and race
// under --parallel. The decorator must validate a copy and leave the original untouched.
func TestTLSValidatingProviderDoesNotMutateTheProvidersConfig(t *testing.T) {
	shared := pgConfig()
	shared.TLS.Mode = "  require  "
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: shared}}

	got, err := p.DBConfig(context.Background(), "")

	require.NoError(t, err)
	assert.NotSame(t, shared, got, "the decorator must not hand back the provider's own pointer")
	assert.Equal(t, "  require  ", shared.TLS.Mode, "the provider's config must be left untrimmed")
	assert.Equal(t, sslModeRequire, got.TLS.Mode, "the returned copy carries the canonical value")
}

// A second resolution of the same shared pointer must behave identically to the first —
// the guard against a trim that silently persisted into the provider's cached config.
func TestTLSValidatingProviderIsIdempotentAcrossCalls(t *testing.T) {
	shared := oracleConfig()
	shared.TLS.Mode = " require "
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: shared}}

	_, firstErr := p.DBConfig(context.Background(), "tenant-a")
	_, secondErr := p.DBConfig(context.Background(), "tenant-a")

	require.Error(t, firstErr)
	require.Error(t, secondErr)
	assert.Equal(t, firstErr.Error(), secondErr.Error())
}

func TestTLSValidatingProviderHandlesNilConfig(t *testing.T) {
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: nil}}

	got, err := p.DBConfig(context.Background(), "")

	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestTLSValidatingProviderPropagatesInnerError(t *testing.T) {
	sentinel := errors.New("secret fetch failed")
	p := &tlsValidatingProvider{inner: &stubProvider{err: sentinel}}

	_, err := p.DBConfig(context.Background(), "tenant-a")

	assert.ErrorIs(t, err, sentinel)
}

func TestTLSValidatingProviderNamesTenantOnRejection(t *testing.T) {
	cfg := oracleConfig()
	cfg.TLS.Mode = sslModeRequire
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: cfg}}

	_, err := p.DBConfig(context.Background(), "tenant-b")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "tenant-b"`)
	assert.Contains(t, err.Error(), "not supported for Oracle")

	// The wrapped ConfigError must stay reachable so callers can still categorize it.
	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, fieldDatabaseTLS, cfgErr.Field)
}

func TestTLSValidatingProviderOmitsTenantPrefixForSingleTenant(t *testing.T) {
	cfg := oracleConfig()
	cfg.TLS.CAFile = testCAPath
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: cfg}}

	_, err := p.DBConfig(context.Background(), "")

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "tenant", "single-tenant runs have no tenant to name")
}

// tlsTenantStoreYAML carries a tenant whose TLS block the framework rejects at startup:
// a CA file under the default (unset) mode, which pgx would silently ignore.
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

	_, err = provider.DBConfig(context.Background(), "tenant-a")

	require.Error(t, err)
	assert.Contains(t, err.Error(), `tenant "tenant-a"`)
	assert.Contains(t, err.Error(), "require a mode that guarantees TLS")
}

func TestTLSValidatingProviderTrimsBeforeHandingConfigBack(t *testing.T) {
	cfg := pgConfig()
	cfg.TLS.Mode = "  verify-full  "
	p := &tlsValidatingProvider{inner: &stubProvider{cfg: cfg}}

	got, err := p.DBConfig(context.Background(), "")

	require.NoError(t, err)
	assert.Equal(t, sslModeVerifyFull, got.TLS.Mode)
}
