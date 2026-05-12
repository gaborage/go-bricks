package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
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
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "p1"},
		"t2": {Type: "postgresql", Host: "h2", Port: 5432, Database: "d2", Username: "u2", Password: "p2"},
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "p3"},
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
		assert.NoError(t, r.Err)
		assert.Equal(t, "postgresql", r.Vendor)
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
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "p1"},
		// t2 missing → DBConfig returns error → fail-fast
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "p3"},
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
		"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "p1"},
		"t3": {Type: "postgresql", Host: "h3", Port: 5432, Database: "d3", Username: "u3", Password: "p3"},
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
		cfgs[id] = &config.DatabaseConfig{Type: "postgresql", Host: "h", Port: 5432, Database: "d", Username: "u", Password: "p"}
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
		"pg":  {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: "p1"},
		"ora": {Type: "oracle", Host: "h2", Port: 1521, Database: "PDB1", Username: "u2", Password: "p2"},
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

	_, err := MigrateAll(context.Background(), nil, lister, provider, ActionMigrate, MigrateAllOptions{})
	assert.Error(t, err)

	_, err = MigrateAll(context.Background(), fm, nil, provider, ActionMigrate, MigrateAllOptions{})
	assert.ErrorIs(t, err, ErrNoLister)

	_, err = MigrateAll(context.Background(), fm, lister, nil, ActionMigrate, MigrateAllOptions{})
	assert.ErrorIs(t, err, ErrNoConfigProvider)
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
}
