package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/migration"
)

func writeTenantStoreYAML(t *testing.T) string {
	t.Helper()
	return writeTenantStoreYAMLContent(t, tenantStoreYAML)
}

// writeTenantStoreYAMLContent writes an arbitrary tenants.yaml body, for tests that need
// a store shaped differently from the shared fixture.
func writeTenantStoreYAMLContent(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tenants.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
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

// tenantStoreWithKeystoreYAML is a service config file used as the tenant source: besides
// multitenant.tenants it carries the keystore and the seal selector, written as ADR-144
// dotted names (nested), exactly as the service's own Load reads them.
const tenantStoreWithKeystoreYAML = tenantStoreYAML + `
keystore:
  keys:
    payments:
      sign:
        v1:
          public: {value: sign-pub}
        v2:
          public: {value: sign-pub-2}
messaging:
  seal:
    active:
      payments:
        sign: v2
`

// TestLoadTenantStoreFromFileReadsDottedKeystoreNames: --source-config decodes the whole
// file, so a file that boots the service must load here too. The CLI's own copy of the
// framework decoder lacked the keystore tree reader, so the nested selector the ADR
// recommends aborted the load ('[payments]' expected type 'string').
func TestLoadTenantStoreFromFileReadsDottedKeystoreNames(t *testing.T) {
	store, err := loadTenantStoreFromFile(writeTenantStoreYAMLContent(t, tenantStoreWithKeystoreYAML))
	require.NoError(t, err)
	tenants := store.Tenants()
	assert.Len(t, tenants, 2)
	assert.Contains(t, tenants, "tenant-a")
}

// TestLoadTenantStoreFromFileRefusesWhatTheFrameworkRefuses: the file is decoded by the
// framework's own decoder, so a keystore entry nested under another fails here as it fails
// the service's startup, instead of decoding the namespace "tokens" as a phantom entry with
// "our" dropped.
func TestLoadTenantStoreFromFileRefusesWhatTheFrameworkRefuses(t *testing.T) {
	_, err := loadTenantStoreFromFile(writeTenantStoreYAMLContent(t, tenantStoreYAML+`
keystore:
  keys:
    tokens:
      public: {value: tokens-pub}
      our:
        public: {value: our-pub}
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"tokens" is an entry (it sets public) and the parent of entry "tokens.our"`)
}

// TestLoadTenantStoreFromFileRefusesTopLevelDottedKeys: config.LoadFromMap splits a
// top-level key on '.' (koanf's confmap unflatten), but a service's Load reads its YAML files
// with no unflatten, so it keeps such a key literal and ignores it. Decoded as is, the file
// would hand this tool tenants the service never serves: a quoted "multitenant.tenants"
// replaced the nested tenants, and flat keys alone enabled tenancy the service reads as off.
func TestLoadTenantStoreFromFileRefusesTopLevelDottedKeys(t *testing.T) {
	cases := []struct {
		name string
		body string
		key  string
	}{
		{
			name: "quoted_key_beside_nested_section",
			body: tenantStoreYAML + `
"multitenant.tenants":
  tenant-x:
    database: {type: postgresql, host: x.example.com, port: 5432, database: tenant_x, username: u_x, password: p_x}
`,
			key: "multitenant.tenants",
		},
		{
			name: "flat_keys_only",
			body: `
multitenant.enabled: true
multitenant.tenants.tenant-f.database.type: postgresql
multitenant.tenants.tenant-f.database.host: f.example.com
`,
			key: "multitenant.enabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := loadTenantStoreFromFile(writeTenantStoreYAMLContent(t, tc.body))
			require.Error(t, err)
			assert.Nil(t, store)
			assert.Contains(t, err.Error(), `top-level key "`+tc.key+`" contains '.'`)
		})
	}
}

// TestLoadTenantStoreFromFileRejectsDeliveredEmptyScalar proves the delivered-empty guard
// reaches a tenants.yaml: a tenant's port set to "" fails the load instead of decoding as a
// legal 0.
func TestLoadTenantStoreFromFileRejectsDeliveredEmptyScalar(t *testing.T) {
	_, err := loadTenantStoreFromFile(writeTenantStoreYAMLContent(t, `
multitenant:
  enabled: true
  tenants:
    tenant-a:
      database:
        type: postgresql
        host: a.example.com
        port: ""
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delivered empty")
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

// Sanity: confirm migration.SecretsProvider is still the type the AWS path resolves to.
// buildConfigProvider now returns it wrapped in the TLS-validating decorator, so the
// assertion unwraps one layer rather than matching the outer type.
func TestBuildConfigProviderAWSReturnsSecretsProvider(t *testing.T) {
	provider, err := buildConfigProvider(context.Background(), &CommonFlags{
		CredentialsFrom: credsSourceAWS,
		SecretsPrefix:   migration.DefaultSecretsPrefix,
		AWSRegion:       "us-east-1",
	}, nil)
	// LoadDefaultConfig may succeed without real creds; we only care about the type
	// when there's no error. If it errors out (e.g., no AWS_REGION), that's fine.
	if err == nil {
		wrapper, ok := provider.(*tlsValidatingProvider)
		require.True(t, ok, "expected *tlsValidatingProvider, got %T", provider)
		_, ok = wrapper.inner.(*migration.SecretsProvider)
		assert.True(t, ok, "expected *migration.SecretsProvider, got %T", wrapper.inner)
	}
}

// Compile-time guard that helps reviewers see the type even without running tests.
var (
	_ = []*migration.SecretsProvider{nil}
	_ = (*config.TenantStore)(nil)
)
