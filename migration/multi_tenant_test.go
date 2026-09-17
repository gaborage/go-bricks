package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/logger"
)

type fakeLister struct {
	ids []string
	err error
}

func (f *fakeLister) ListTenants(context.Context) ([]string, error) {
	return f.ids, f.err
}

type fakeConfigProvider struct {
	cfgs map[string]*config.DatabaseConfig
	errs map[string]error
	mu   sync.Mutex
	hits map[string]int
}

// nilConfigProvider violates the DBConfigProvider contract by returning (nil, nil).
type nilConfigProvider struct{}

func (nilConfigProvider) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	return nil, nil
}

func newFakeConfigProvider(cfgs map[string]*config.DatabaseConfig) *fakeConfigProvider {
	return &fakeConfigProvider{
		cfgs: cfgs,
		errs: map[string]error{},
		hits: map[string]int{},
	}
}

func (f *fakeConfigProvider) DBConfig(_ context.Context, key string) (*config.DatabaseConfig, error) {
	f.mu.Lock()
	f.hits[key]++
	f.mu.Unlock()

	if err, ok := f.errs[key]; ok && err != nil {
		return nil, err
	}
	cfg, ok := f.cfgs[key]
	if !ok {
		return nil, errors.New("tenant not configured")
	}
	cfgCopy := *cfg
	return &cfgCopy, nil
}

func newFlywayMigratorForTest(t *testing.T) *FlywayMigrator {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Type: "postgresql"},
		App:      config.AppConfig{Env: "test"},
	}
	return NewFlywayMigrator(cfg, logger.New("disabled", true))
}

func makeBaseConfig(t *testing.T, stub string) *Config {
	t.Helper()
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "flyway.conf")
	migrationPath := filepath.Join(tempDir, "migrations")
	require.NoError(t, os.WriteFile(configPath, []byte(""), 0o644))
	require.NoError(t, os.MkdirAll(migrationPath, 0o755))

	return &Config{
		FlywayPath:    stub,
		ConfigPath:    configPath,
		MigrationPath: migrationPath,
		Timeout:       10 * time.Second,
		Environment:   "test",
	}
}

func TestMigrateAllSequentialSuccess(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)

	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t2": {Type: "postgresql", Host: "h2", Port: 5432, Database: "d2", Username: "u2", Password: "pw-tenant-2"},
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "pw-tenant-3"},
	})

	var hookCalls int
	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{ids: []string{"t1", "t2", "t3"}},
		provider,
		ActionMigrate,
		MigrateAllOptions{
			BaseConfig: base,
			Hook:       func(TenantResult) { hookCalls++ },
		},
	)

	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Len(t, res.Results, 3)
	assert.Empty(t, res.Failed())
	assert.Equal(t, 3, hookCalls)
	for _, r := range res.Results {
		// One subtest per tenant: the tenants are independent, so a failure on one
		// must not stop the others from being checked.
		t.Run(r.TenantID, func(t *testing.T) {
			require.NoError(t, r.Err)
			assert.Equal(t, "postgresql", r.Vendor)
		})
	}
}

func TestMigrateAllSequentialFailFast(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)

	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		// t2 missing → DBConfig returns error → fail-fast
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "pw-tenant-3"},
	})

	var hookCalls int32
	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{ids: []string{"t1", "t2", "t3"}},
		provider,
		ActionMigrate,
		MigrateAllOptions{
			BaseConfig: base,
			Hook:       func(TenantResult) { atomic.AddInt32(&hookCalls, 1) },
		},
	)

	require.Error(t, err)
	require.NotNil(t, res)
	// Should have stopped after t2's failure — t3 not attempted.
	assert.Len(t, res.Results, 2)
	assert.Equal(t, int32(2), atomic.LoadInt32(&hookCalls))
	failed := res.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "t2", failed[0].TenantID)
}

func TestMigrateAllContinueOnError(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)

	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "pw-tenant-3"},
	})

	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{ids: []string{"t1", "t2", "t3"}},
		provider,
		ActionMigrate,
		MigrateAllOptions{
			BaseConfig:      base,
			ContinueOnError: true,
		},
	)

	require.NoError(t, err)
	assert.Len(t, res.Results, 3)
	failed := res.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "t2", failed[0].TenantID)
}

func TestMigrateAllParallel(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)

	cfgs := map[string]*config.DatabaseConfig{}
	ids := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		id := "tenant-" + string(rune('a'+i))
		ids = append(ids, id)
		cfgs[id] = &config.DatabaseConfig{Type: "postgresql", Host: "h", Port: 5432, Database: "d", Username: "u", Password: "pw-tenant-x"}
	}
	provider := newFakeConfigProvider(cfgs)

	var hookMu sync.Mutex
	hooked := []string{}
	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{ids: ids},
		provider,
		ActionMigrate,
		MigrateAllOptions{
			BaseConfig:  base,
			Parallelism: 4,
			Hook: func(r TenantResult) {
				hookMu.Lock()
				defer hookMu.Unlock()
				hooked = append(hooked, r.TenantID)
			},
		},
	)

	require.NoError(t, err)
	assert.Len(t, res.Results, 10)
	assert.Empty(t, res.Failed())
	hookMu.Lock()
	defer hookMu.Unlock()
	assert.Len(t, hooked, 10)
	sort.Strings(hooked)
	sort.Strings(ids)
	assert.Equal(t, ids, hooked)
}

func TestMigrateAllMixedVendors(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	pgStub := createFlywayStub(t, "postgresql")
	oracleStub := createFlywayStub(t, "oracle")

	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"pg":  {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"ora": {Type: "oracle", Host: "h2", Port: 1521, Database: "PDB1", Username: "u2", Password: "pw-tenant-2"},
	})

	cases := []struct {
		id   string
		stub string
	}{
		{id: "pg", stub: pgStub},
		{id: "ora", stub: oracleStub},
	}

	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			fm := newFlywayMigratorForTest(t)
			base := makeBaseConfig(t, c.stub)

			res, err := MigrateAll(
				context.Background(),
				fm,
				&fakeLister{ids: []string{c.id}},
				provider,
				ActionMigrate,
				MigrateAllOptions{BaseConfig: base},
			)

			require.NoError(t, err)
			require.NotNil(t, res)
			require.Len(t, res.Results, 1)
			assert.NoError(t, res.Results[0].Err)
		})
	}
}

func TestMigrateAllListerError(t *testing.T) {
	fm := newFlywayMigratorForTest(t)
	provider := newFakeConfigProvider(nil)

	boom := errors.New("api down")
	_, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{err: boom},
		provider,
		ActionMigrate,
		MigrateAllOptions{},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
}

func TestMigrateAllNilArguments(t *testing.T) {
	fm := newFlywayMigratorForTest(t)
	provider := newFakeConfigProvider(nil)
	lister := &fakeLister{}

	// Each nil argument is an independent contract, so each gets its own subtest:
	// bundled, the first failure would hide whether the others still hold.
	t.Run("nil_migrator", func(t *testing.T) {
		_, err := MigrateAll(context.Background(), nil, lister, provider, ActionMigrate, MigrateAllOptions{})
		require.Error(t, err)
	})

	t.Run("nil_lister", func(t *testing.T) {
		_, err := MigrateAll(context.Background(), fm, nil, provider, ActionMigrate, MigrateAllOptions{})
		require.ErrorIs(t, err, ErrNoLister)
	})

	t.Run("nil_config_provider", func(t *testing.T) {
		_, err := MigrateAll(context.Background(), fm, lister, nil, ActionMigrate, MigrateAllOptions{})
		require.ErrorIs(t, err, ErrNoConfigProvider)
	})
}

func TestActionString(t *testing.T) {
	assert.Equal(t, "migrate", ActionMigrate.String())
	assert.Equal(t, "validate", ActionValidate.String())
	assert.Equal(t, "info", ActionInfo.String())
	assert.Contains(t, Action(99).String(), "unknown")
}

func TestMergeConfigs(t *testing.T) {
	defaults := &Config{
		FlywayPath:    "flyway",
		ConfigPath:    "default.conf",
		MigrationPath: "default/",
		Timeout:       5 * time.Minute,
		Environment:   "test",
	}

	t.Run("nil_override_returns_defaults", func(t *testing.T) {
		out := mergeConfigs(defaults, nil)
		assert.Equal(t, defaults.ConfigPath, out.ConfigPath)
	})

	t.Run("override_replaces_set_fields", func(t *testing.T) {
		override := &Config{ConfigPath: "custom.conf", Timeout: 10 * time.Minute}
		out := mergeConfigs(defaults, override)
		assert.Equal(t, "custom.conf", out.ConfigPath)
		assert.Equal(t, "default/", out.MigrationPath)
		assert.Equal(t, 10*time.Minute, out.Timeout)
	})

	t.Run("nil_defaults_returns_override", func(t *testing.T) {
		override := &Config{ConfigPath: "x"}
		out := mergeConfigs(nil, override)
		assert.Equal(t, override, out)
	})

	t.Run("audit_fields_propagate_individually", func(t *testing.T) {
		base := &Config{
			Audit: AuditContext{
				Principal:     "ci-baseline",
				GitCommitSHA:  "deadbeef",
				PipelineRunID: "run-1",
				Target:        "tenant-default",
			},
		}
		override := &Config{
			Audit: AuditContext{
				Principal: "tenant-operator",
				Target:    "tenant-acme",
			},
		}
		out := mergeConfigs(base, override)
		assert.Equal(t, "tenant-operator", out.Audit.Principal, "override Principal should win")
		assert.Equal(t, "tenant-acme", out.Audit.Target, "override Target should win")
		assert.Equal(t, "deadbeef", out.Audit.GitCommitSHA, "unset override field should inherit from base")
		assert.Equal(t, "run-1", out.Audit.PipelineRunID, "unset override field should inherit from base")
	})

	t.Run("audit_only_override_is_not_empty", func(t *testing.T) {
		override := &Config{Audit: AuditContext{Principal: "compliance-bot"}}
		assert.False(t, isEmptyConfig(override), "config with only Audit set must not be treated as empty")
		out := mergeConfigs(defaults, override)
		assert.Equal(t, "compliance-bot", out.Audit.Principal)
		assert.Equal(t, defaults.ConfigPath, out.ConfigPath, "non-Audit fields should still inherit from base")
	})
}

func TestMigrateAllStopsWhenQuiesced(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}
	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)
	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t2": {Type: "postgresql", Host: "h2", Port: 5432, Database: "d2", Username: "u2", Password: "pw-tenant-2"},
	})
	gate := NewMemoryQuiesceController()
	_, err := gate.Set(context.Background(), QuiesceSetOptions{By: "deployer", TTL: time.Hour})
	require.NoError(t, err)

	res, err := MigrateAll(
		context.Background(), fm, &fakeLister{ids: []string{"t1", "t2"}}, provider, ActionMigrate,
		MigrateAllOptions{BaseConfig: base, Quiesce: gate},
	)
	require.ErrorIs(t, err, ErrQuiesceBlocked)
	assert.Empty(t, res.Results, "no tenant is dispatched while quiesced at start")
}

func TestMigrateAllProceedsWhenNotQuiesced(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}
	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)
	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t2": {Type: "postgresql", Host: "h2", Port: 5432, Database: "d2", Username: "u2", Password: "pw-tenant-2"},
	})
	gate := NewMemoryQuiesceController() // never set

	res, err := MigrateAll(
		context.Background(), fm, &fakeLister{ids: []string{"t1", "t2"}}, provider, ActionMigrate,
		MigrateAllOptions{BaseConfig: base, Quiesce: gate},
	)
	require.NoError(t, err)
	assert.Len(t, res.Results, 2)
}

func TestMigrateAllParallelStopsWhenQuiesced(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}
	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)
	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t2": {Type: "postgresql", Host: "h2", Port: 5432, Database: "d2", Username: "u2", Password: "pw-tenant-2"},
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "pw-tenant-3"},
		"t4": {Type: "postgresql", Host: "h4", Port: 5432, Database: "d4", Username: "u4", Password: "pw-tenant-4"},
	})
	gate := NewMemoryQuiesceController()
	_, err := gate.Set(context.Background(), QuiesceSetOptions{By: "deployer", TTL: time.Hour})
	require.NoError(t, err)

	res, err := MigrateAll(
		context.Background(), fm, &fakeLister{ids: []string{"t1", "t2", "t3", "t4"}}, provider, ActionMigrate,
		MigrateAllOptions{BaseConfig: base, Parallelism: 4, Quiesce: gate},
	)
	require.ErrorIs(t, err, ErrQuiesceBlocked, "the parallel dispatch path must surface ErrQuiesceBlocked")
	assert.Empty(t, res.Results, "no tenant is dispatched when quiesced before the first dispatch")
}

// countingQuiesceGate reports "not set" for the first blockAfter IsSet calls,
// then "set" — letting a test deterministically flip quiesce mid-dispatch.
type countingQuiesceGate struct {
	mu         sync.Mutex
	calls      int
	blockAfter int
}

func (g *countingQuiesceGate) IsSet(context.Context) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.calls > g.blockAfter, nil
}

func (g *countingQuiesceGate) Query(context.Context) (*QuiesceStatus, error) {
	return &QuiesceStatus{}, nil
}

func TestMigrateAllParallelStopsDispatchWhenQuiesceFlipsMidRun(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}
	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, stub)
	provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
		"t2": {Type: "postgresql", Host: "h2", Port: 5432, Database: "d2", Username: "u2", Password: "pw-tenant-2"},
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "pw-tenant-3"},
		"t4": {Type: "postgresql", Host: "h4", Port: 5432, Database: "d4", Username: "u4", Password: "pw-tenant-4"},
	})
	// First dispatch iteration sees not-quiesced; the flag flips before the
	// second, so dispatch stops after exactly one tenant.
	gate := &countingQuiesceGate{blockAfter: 1}

	res, err := MigrateAll(
		context.Background(), fm, &fakeLister{ids: []string{"t1", "t2", "t3", "t4"}}, provider, ActionMigrate,
		MigrateAllOptions{BaseConfig: base, Parallelism: 2, Quiesce: gate},
	)
	require.ErrorIs(t, err, ErrQuiesceBlocked)
	require.Len(t, res.Results, 1, "dispatch must stop after the flag flips; in-flight tenant is in the partial result")
	assert.Equal(t, "t1", res.Results[0].TenantID)
	assert.NoError(t, res.Results[0].Err, "the already-dispatched tenant completes normally")
}

// TestMigrateAllRejectsNilTenantConfig proves a provider returning (nil, nil) yields a
// TenantResult error wrapping database.ErrNoDatabaseConfig instead of dereferencing it.
func TestMigrateAllRejectsNilTenantConfig(t *testing.T) {
	fm := newFlywayMigratorForTest(t)

	var res *MigrateAllResult
	var err error
	require.NotPanics(t, func() {
		res, err = MigrateAll(
			context.Background(),
			fm,
			&fakeLister{ids: []string{"pg"}},
			nilConfigProvider{},
			ActionMigrate,
			MigrateAllOptions{},
		)
	})

	require.Error(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Results, 1)
	one := res.Results[0]
	assert.Empty(t, one.Vendor)
	require.ErrorIs(t, one.Err, database.ErrNoDatabaseConfig)
}

// createEnvCapturingFlywayStub builds a flyway-stub that appends the connection
// environment and argv it was started with to capturePath, one KEY=value per line,
// then emits a parseable migrate success envelope.
func createEnvCapturingFlywayStub(t *testing.T) (stubPath, capturePath string) {
	t.Helper()
	dir := t.TempDir()
	stubPath = filepath.Join(dir, "flyway-stub.sh")
	capturePath = filepath.Join(dir, "captured_env")

	var script strings.Builder
	script.WriteString("#!/bin/sh\n{\n")
	for _, name := range []string{
		"DB_HOST", "DB_PORT", "DB_USER", "DB_PASSWORD", "DB_NAME",
		"ORACLE_HOST", "ORACLE_PORT", "ORACLE_USER", "ORACLE_PASSWORD", "ORACLE_PDB",
	} {
		fmt.Fprintf(&script, "printf '%s=%%s\\n' \"${%s}\"\n", name, name)
	}
	fmt.Fprintf(&script, "printf 'ARGS=%%s\\n' \"$*\"\n} >> %q\n", capturePath)
	script.WriteString("echo '" + minimalMigrateSuccessJSON + "'\nexit 0\n")
	require.NoError(t, os.WriteFile(stubPath, []byte(script.String()), 0o755))
	return stubPath, capturePath
}

func readCapturedEnv(t *testing.T, capturePath string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(capturePath)
	require.NoError(t, err)
	env := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		require.True(t, ok, "capture line %q has no '='", line)
		env[key] = value
	}
	return env
}

func TestMigrateAllMigratorIdentityOverlaysTenantCredentials(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	pgTenant := &config.DatabaseConfig{
		Type: "postgresql", Host: "tenant-pg-host", Port: 5432, Database: "tenant_db",
		Username: "tenant_runtime", Password: "pw-tenant-runtime",
		PostgreSQL: config.PostgreSQLConfig{Schema: "tenant_schema"},
	}
	oracleTenant := &config.DatabaseConfig{
		Type: "oracle", Host: "tenant-ora-host", Port: 1521, Database: "TENANTPDB",
		Username: "tenant_runtime", Password: "pw-tenant-runtime",
	}
	migrator := &MigratorIdentity{Username: "fleet_migrator", Password: "pw-fleet-migrator"}

	cases := []struct {
		name     string
		tenant   *config.DatabaseConfig
		action   Action
		identity *MigratorIdentity
		wantEnv  map[string]string
		wantArgs []string
	}{
		{
			name: "postgresql_migrate_connects_as_migrator", tenant: pgTenant, action: ActionMigrate, identity: migrator,
			wantEnv: map[string]string{
				"DB_USER": "fleet_migrator", "DB_PASSWORD": "pw-fleet-migrator",
				"DB_HOST": "tenant-pg-host", "DB_PORT": "5432", "DB_NAME": "tenant_db",
			},
			wantArgs: []string{"-schemas=tenant_schema", "-defaultSchema=tenant_schema"},
		},
		{
			name: "postgresql_validate_connects_as_migrator", tenant: pgTenant, action: ActionValidate, identity: migrator,
			wantEnv: map[string]string{
				"DB_USER": "fleet_migrator", "DB_PASSWORD": "pw-fleet-migrator",
				"DB_HOST": "tenant-pg-host", "DB_NAME": "tenant_db",
			},
			wantArgs: []string{"-schemas=tenant_schema"},
		},
		{
			name: "postgresql_info_connects_as_migrator", tenant: pgTenant, action: ActionInfo, identity: migrator,
			wantEnv: map[string]string{
				"DB_USER": "fleet_migrator", "DB_PASSWORD": "pw-fleet-migrator",
				"DB_HOST": "tenant-pg-host", "DB_NAME": "tenant_db",
			},
			wantArgs: []string{"-schemas=tenant_schema"},
		},
		{
			name: "oracle_migrate_connects_as_migrator", tenant: oracleTenant, action: ActionMigrate, identity: migrator,
			wantEnv: map[string]string{
				"ORACLE_USER": "fleet_migrator", "ORACLE_PASSWORD": "pw-fleet-migrator",
				"ORACLE_HOST": "tenant-ora-host", "ORACLE_PORT": "1521", "ORACLE_PDB": "TENANTPDB",
			},
		},
		{
			name: "nil_identity_connects_as_tenant_secret", tenant: pgTenant, action: ActionMigrate,
			wantEnv: map[string]string{
				"DB_USER": "tenant_runtime", "DB_PASSWORD": "pw-tenant-runtime",
				"DB_HOST": "tenant-pg-host", "DB_PORT": "5432", "DB_NAME": "tenant_db",
			},
			wantArgs: []string{"-schemas=tenant_schema", "-defaultSchema=tenant_schema"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub, capture := createEnvCapturingFlywayStub(t)
			res, err := MigrateAll(
				context.Background(),
				newFlywayMigratorForTest(t),
				&fakeLister{ids: []string{"t1"}},
				newFakeConfigProvider(map[string]*config.DatabaseConfig{"t1": tc.tenant}),
				tc.action,
				MigrateAllOptions{BaseConfig: makeBaseConfig(t, stub), MigratorIdentity: tc.identity},
			)

			require.NoError(t, err)
			require.Len(t, res.Results, 1)
			env := readCapturedEnv(t, capture)
			for key, want := range tc.wantEnv {
				assert.Equal(t, want, env[key], key)
			}
			for _, arg := range tc.wantArgs {
				assert.Contains(t, env["ARGS"], arg)
			}
		})
	}
}

// cachingConfigProvider hands out the same *config.DatabaseConfig on every call, the
// way a caching provider does, so a mutation of the returned value is observable.
type cachingConfigProvider struct {
	cfg *config.DatabaseConfig
}

func (p cachingConfigProvider) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	return p.cfg, nil
}

func TestMigrateAllMigratorIdentityLeavesProviderDocumentUntouched(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	stub, capture := createEnvCapturingFlywayStub(t)
	cached := &config.DatabaseConfig{
		Type: "postgresql", Host: "tenant-pg-host", Port: 5432, Database: "tenant_db",
		Username: "tenant_runtime", Password: "pw-tenant-runtime",
	}

	_, err := MigrateAll(
		context.Background(),
		newFlywayMigratorForTest(t),
		&fakeLister{ids: []string{"t1", "t2"}},
		cachingConfigProvider{cfg: cached},
		ActionMigrate,
		MigrateAllOptions{
			BaseConfig:       makeBaseConfig(t, stub),
			MigratorIdentity: &MigratorIdentity{Username: "fleet_migrator", Password: "pw-fleet-migrator"},
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "tenant_runtime", cached.Username)
	assert.Equal(t, "pw-tenant-runtime", cached.Password)
	assert.Equal(t, "fleet_migrator", readCapturedEnv(t, capture)["DB_USER"])
}

type countingLister struct {
	ids   []string
	calls atomic.Int32
}

func (l *countingLister) ListTenants(context.Context) ([]string, error) {
	l.calls.Add(1)
	return l.ids, nil
}

func TestMigrateAllRejectsIncompleteMigratorIdentity(t *testing.T) {
	cases := []struct {
		name     string
		identity *MigratorIdentity
		wantMsg  string
	}{
		{name: "empty_username", identity: &MigratorIdentity{Password: "pw-fleet-migrator"}, wantMsg: "username"},
		{name: "empty_password", identity: &MigratorIdentity{Username: "fleet_migrator"}, wantMsg: "password"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lister := &countingLister{ids: []string{"t1"}}
			provider := newFakeConfigProvider(map[string]*config.DatabaseConfig{
				"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
			})

			res, err := MigrateAll(
				context.Background(),
				newFlywayMigratorForTest(t),
				lister,
				provider,
				ActionMigrate,
				MigrateAllOptions{MigratorIdentity: tc.identity},
			)

			require.ErrorIs(t, err, ErrInvalidMigratorIdentity)
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.NotContains(t, err.Error(), "pw-fleet-migrator")
			assert.Nil(t, res)
			assert.Zero(t, lister.calls.Load())
			assert.Empty(t, provider.hits)
		})
	}
}

func TestMigrateAllMigratorIdentityPasswordStaysOutOfLogsAndErrors(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	const migratorPassword = "pw-fleet-migrator"
	dir := t.TempDir()
	stub := filepath.Join(dir, "flyway-leaky.sh")
	script := "#!/bin/sh\n" +
		"echo \"FATAL: password authentication failed for user ${DB_USER} with ${DB_PASSWORD}\"\n" +
		"echo \"FATAL: password ${DB_PASSWORD} rejected\" >&2\n" +
		"exit 1\n"
	require.NoError(t, os.WriteFile(stub, []byte(script), 0o755))

	cfg := &config.Config{Database: config.DatabaseConfig{Type: "postgresql"}, App: config.AppConfig{Env: "test"}}
	var res *MigrateAllResult
	var err error
	output := captureMigrationStdout(t, func() {
		log := logger.New("debug", false)
		res, err = MigrateAll(
			context.Background(),
			NewFlywayMigrator(cfg, log),
			&fakeLister{ids: []string{"t1"}},
			newFakeConfigProvider(map[string]*config.DatabaseConfig{
				"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "pw-tenant-1"},
			}),
			ActionMigrate,
			MigrateAllOptions{
				BaseConfig:       makeBaseConfig(t, stub),
				Logger:           log,
				MigratorIdentity: &MigratorIdentity{Username: "fleet_migrator", Password: migratorPassword},
			},
		)
	})

	require.Error(t, err)
	require.Len(t, res.Results, 1)
	assert.Contains(t, output, "fleet_migrator", "the Flyway output must reach the log for the check to mean anything")
	assert.Contains(t, output, redactionPlaceholder)
	assert.NotContains(t, output, migratorPassword)
	assert.NotContains(t, err.Error(), migratorPassword)
	assert.NotContains(t, fmt.Sprintf("%+v", res), migratorPassword)
}
