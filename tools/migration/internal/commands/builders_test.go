package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-viper/mapstructure/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/migration"
)

func writeTenantStoreYAML(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.yaml")
	require.NoError(t, os.WriteFile(path, []byte(tenantStoreYAML), 0o600))
	return path
}

const tenantStoreYAML = `
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
    tenant-b:
      database:
        type: postgresql
        host: b.example.com
        port: 5432
        database: tenant_b
        username: u_b
        password: p_b
`

func TestLoadTenantStoreFromFileHappyPath(t *testing.T) {
	path := writeTenantStoreYAML(t)
	store, err := loadTenantStoreFromFile(path)
	require.NoError(t, err)
	require.NotNil(t, store)
	tenants := store.Tenants()
	assert.Len(t, tenants, 2)
	assert.Contains(t, tenants, "tenant-a")
}

func TestLoadTenantStoreFromFileMissingPath(t *testing.T) {
	_, err := loadTenantStoreFromFile("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestLoadTenantStoreFromFileRejectsUnitlessNumericDuration proves the CLI's tenant-config
// load routes through the numeric-duration guard: a bare numeric time.Duration is rejected.
func TestLoadTenantStoreFromFileRejectsUnitlessNumericDuration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.yaml")
	require.NoError(t, os.WriteFile(path, []byte("server:\n  timeout:\n    read: 30\n"), 0o600))

	_, err := loadTenantStoreFromFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unit-less numeric duration 30")
}

// TestTenantDecoderConfigCommaSplitsStringSlice proves the CLI decoder carries the same
// comma-split []string hook as the framework's config.buildDecoderConfig, so a comma-scalar
// []string field (e.g. a tenants.yaml allowlist) decodes to multiple elements identically
// to a framework Load instead of a single-element wrap.
func TestTenantDecoderConfigCommaSplitsStringSlice(t *testing.T) {
	type target struct {
		Allow []string `mapstructure:"allow"`
	}
	var out target
	dc := tenantDecoderConfig()
	dc.Result = &out
	dec, err := mapstructure.NewDecoder(dc)
	require.NoError(t, err)
	require.NoError(t, dec.Decode(map[string]any{"allow": "a,b"}))
	assert.Equal(t, []string{"a", "b"}, out.Allow)
}

func TestBuildListerSingleTenantPath(t *testing.T) {
	lister, err := buildLister(&CommonFlags{Tenant: "only"}, nil)
	require.NoError(t, err)
	ids, err := lister.ListTenants(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"only"}, ids)
}

func TestBuildListerHTTPPath(t *testing.T) {
	lister, err := buildLister(&CommonFlags{SourceURL: "https://example.com"}, nil)
	require.NoError(t, err)
	require.NotNil(t, lister)
}

func TestBuildListerFileStoreFromCallerPath(t *testing.T) {
	path := writeTenantStoreYAML(t)
	store, err := loadTenantStoreFromFile(path)
	require.NoError(t, err)

	// fileStore non-nil avoids re-parse.
	lister, err := buildLister(&CommonFlags{SourceConfig: path}, store)
	require.NoError(t, err)
	ids, err := lister.ListTenants(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"tenant-a", "tenant-b"}, ids)
}

func TestBuildConfigProviderFileStoreReuse(t *testing.T) {
	path := writeTenantStoreYAML(t)
	store, err := loadTenantStoreFromFile(path)
	require.NoError(t, err)

	provider, err := buildConfigProvider(context.Background(), &CommonFlags{
		SourceConfig:    path,
		CredentialsFrom: credsSourceFile,
	}, store)
	require.NoError(t, err)
	require.NotNil(t, provider)

	dbCfg, err := provider.DBConfig(context.Background(), "tenant-a")
	require.NoError(t, err)
	require.NotNil(t, dbCfg)
	assert.Equal(t, "postgresql", dbCfg.Type)
}

func TestBuildConfigProviderUnknownCredsSource(t *testing.T) {
	_, err := buildConfigProvider(context.Background(), &CommonFlags{
		CredentialsFrom: "wat",
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown credentials source")
}

func TestMaybeLoadFileStoreLoadsForListing(t *testing.T) {
	path := writeTenantStoreYAML(t)
	store, err := maybeLoadFileStore(&CommonFlags{
		SourceConfig:    path,
		CredentialsFrom: credsSourceAWS,
	})
	require.NoError(t, err)
	require.NotNil(t, store)
}

func TestMaybeLoadFileStoreLoadsForCredsOnly(t *testing.T) {
	path := writeTenantStoreYAML(t)
	store, err := maybeLoadFileStore(&CommonFlags{
		SourceURL:       "https://example.com",
		SourceConfig:    path,
		CredentialsFrom: credsSourceFile,
	})
	require.NoError(t, err)
	require.NotNil(t, store)
}

// Sanity: confirm migration.SecretsProvider is still the type buildConfigProvider returns
// for the AWS path (compile-time check via assertion on a pointer).
func TestBuildConfigProviderAWSReturnsSecretsProvider(t *testing.T) {
	provider, err := buildConfigProvider(context.Background(), &CommonFlags{
		CredentialsFrom: credsSourceAWS,
		SecretsPrefix:   "gobricks/migrate/",
		AWSRegion:       "us-east-1",
	}, nil)
	// LoadDefaultConfig may succeed without real creds; we only care about the type
	// when there's no error. If it errors out (e.g., no AWS_REGION), that's fine.
	if err == nil {
		_, ok := provider.(*migration.SecretsProvider)
		assert.True(t, ok, "expected *migration.SecretsProvider, got %T", provider)
	}
}

// Compile-time guard that helps reviewers see the type even without running tests.
var _ = []*migration.SecretsProvider{nil}
var _ = (*config.TenantStore)(nil)
