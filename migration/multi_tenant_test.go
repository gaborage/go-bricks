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
	testconsts "github.com/gaborage/go-bricks/testing"
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

// requireShellStubs skips tests whose Flyway stand-in is a shell script.
func requireShellStubs(t *testing.T) {
	t.Helper()
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}
}

// stubTenantConfigs returns a PostgreSQL config per tenant ID.
func stubTenantConfigs(ids ...string) map[string]*config.DatabaseConfig {
	cfgs := make(map[string]*config.DatabaseConfig, len(ids))
	for _, id := range ids {
		cfgs[id] = &config.DatabaseConfig{
			Type: "postgresql", Host: "h-" + id, Port: 5432, Database: "d-" + id,
			Username: "u-" + id, Password: "pw-tenant-" + id,
		}
	}
	return cfgs
}

type migrateAllOutcome struct {
	res *MigrateAllResult
	err error
}

// migrateAllAsync runs MigrateAll on a goroutine so a test can act while it is in flight.
func migrateAllAsync(
	ctx context.Context, fm *FlywayMigrator, ids []string, provider database.DBConfigProvider, opts MigrateAllOptions,
) <-chan migrateAllOutcome {
	done := make(chan migrateAllOutcome, 1)
	go func() {
		res, err := MigrateAll(ctx, fm, &fakeLister{ids: ids}, provider, ActionMigrate, opts)
		done <- migrateAllOutcome{res: res, err: err}
	}()
	return done
}

func awaitMigrateAll(t *testing.T, done <-chan migrateAllOutcome, limit time.Duration) migrateAllOutcome {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(limit):
		t.Fatal("MigrateAll did not return in time")
		return migrateAllOutcome{}
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
	assert.Equal(t, 3, res.Listed())
	assert.Empty(t, res.NeverDispatched)
	require.NoError(t, res.Verdict(), "every listed tenant dispatched and succeeded is a clean fleet")
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
	assert.Equal(t, []string{"t3"}, res.NeverDispatched)
	require.ErrorIs(t, res.Verdict(), ErrFleetSplit)
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
	assert.Empty(t, res.NeverDispatched)
	require.ErrorIs(t, res.Verdict(), ErrFleetSplit, "a failed tenant splits the fleet even when every tenant was dispatched")
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
	assert.Equal(t, 10, res.Listed())
	require.NoError(t, res.Verdict())
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
	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{err: boom},
		provider,
		ActionMigrate,
		MigrateAllOptions{},
	)
	require.ErrorIs(t, err, boom)
	require.Nil(t, res)
	assert.Zero(t, res.Listed())
	require.ErrorIs(t, res.Verdict(), ErrNothingAttempted, "a run that never listed its tenants attempted nothing")
}

func TestMigrateAllEmptyTenantListAttemptsNothing(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
	}{
		{name: "nil_listing", ids: nil},
		{name: "empty_listing", ids: []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm := newFlywayMigratorForTest(t)
			res, err := MigrateAll(
				context.Background(), fm, &fakeLister{ids: tc.ids}, newFakeConfigProvider(nil), ActionMigrate,
				MigrateAllOptions{},
			)
			require.NoError(t, err, "an empty listing keeps MigrateAll's own return contract")
			require.NotNil(t, res)
			assert.Empty(t, res.Results)
			assert.Zero(t, res.Listed())
			assert.Empty(t, res.NeverDispatched)
			require.ErrorIs(t, res.Verdict(), ErrNothingAttempted, "an empty fleet is not a clean fleet")
		})
	}
}

func TestMigrateAllDispatchesNothingWhenStoppedBeforeFirstDispatch(t *testing.T) {
	ids := []string{"t1", "t2", "t3", "t4"}
	// A parallel dispatch select racing the done channel would dispatch a random
	// prefix on some runs, so one clean run proves nothing there.
	cases := []struct {
		name         string
		parallelism  int
		runs         int
		cancelInGate bool
	}{
		{name: "done_context_sequential", parallelism: 1, runs: 1},
		{name: "done_context_parallel", parallelism: 4, runs: 32},
		{name: "cancel_during_quiesce_check_sequential", parallelism: 1, runs: 1, cancelInGate: true},
		{name: "cancel_during_quiesce_check_parallel", parallelism: 2, runs: 32, cancelInGate: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fm := newFlywayMigratorForTest(t)
			for run := range tc.runs {
				ctx, cancel := context.WithCancel(context.Background())
				opts := MigrateAllOptions{Parallelism: tc.parallelism}
				if tc.cancelInGate {
					opts.Quiesce = &cancelingQuiesceGate{cancel: cancel}
				} else {
					cancel()
				}
				provider := newFakeConfigProvider(nil)
				res, err := MigrateAll(ctx, fm, &fakeLister{ids: ids}, provider, ActionMigrate, opts)
				cancel()
				require.ErrorIs(t, err, context.Canceled)
				require.Empty(t, provider.hits, "run %d dispatched a tenant", run)
				require.Empty(t, res.Results)
				require.Equal(t, len(ids), res.Listed())
				require.Equal(t, ids, res.NeverDispatched)
				require.ErrorIs(t, res.Verdict(), ErrNothingAttempted)
			}
		})
	}
}

func TestMigrateAllSequentialStopMidRunSplitsFleet(t *testing.T) {
	cases := []struct {
		name    string
		opts    func(cancel context.CancelFunc) MigrateAllOptions
		wantErr error
	}{
		{
			name: "context_canceled_after_first_tenant",
			opts: func(cancel context.CancelFunc) MigrateAllOptions {
				return MigrateAllOptions{Hook: func(TenantResult) { cancel() }}
			},
			wantErr: context.Canceled,
		},
		{
			name: "quiesce_set_after_first_tenant",
			opts: func(context.CancelFunc) MigrateAllOptions {
				return MigrateAllOptions{Quiesce: &countingQuiesceGate{blockAfter: 1}}
			},
			wantErr: ErrQuiesceBlocked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireShellStubs(t)
			fm := newFlywayMigratorForTest(t)
			ids := []string{"t1", "t2", "t3"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := tc.opts(cancel)
			opts.BaseConfig = makeBaseConfig(t, createFlywayStub(t, "postgresql"))

			res, err := MigrateAll(ctx, fm, &fakeLister{ids: ids}, newFakeConfigProvider(stubTenantConfigs(ids...)), ActionMigrate, opts)
			require.ErrorIs(t, err, tc.wantErr)
			require.Len(t, res.Results, 1)
			require.NoError(t, res.Results[0].Err)
			assert.Equal(t, []string{"t2", "t3"}, res.NeverDispatched)
			require.ErrorIs(t, res.Verdict(), ErrFleetSplit, "never-dispatched tenants split the fleet with no failure")
		})
	}
}

func TestMigrateAllFlywayTimeoutCountsAsFailed(t *testing.T) {
	requireShellStubs(t)
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, createSlowFlywayStub(t, 5*time.Second))
	base.Timeout = 300 * time.Millisecond

	res, err := MigrateAll(
		context.Background(), fm, &fakeLister{ids: []string{"t1"}}, newFakeConfigProvider(stubTenantConfigs("t1")), ActionMigrate,
		MigrateAllOptions{BaseConfig: base},
	)
	require.ErrorIs(t, err, ErrFlywayTimeout)
	failed := res.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "t1", failed[0].TenantID)
	assert.Empty(t, res.NeverDispatched, "a timed-out tenant was dispatched; its schema state is unknown")
	require.ErrorIs(t, res.Verdict(), ErrFleetSplit)
}

func TestMigrateAllFlywayCancelCountsInFlightTenantAsFailed(t *testing.T) {
	requireShellStubs(t)
	readyMarker := filepath.Join(t.TempDir(), "flyway-started.marker")
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, createReadySignalingFlywayStub(t, readyMarker, 5*time.Second))
	ids := []string{"t1", "t2"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := migrateAllAsync(ctx, fm, ids, newFakeConfigProvider(stubTenantConfigs(ids...)),
		MigrateAllOptions{BaseConfig: base, ContinueOnError: true})
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(readyMarker)
		return statErr == nil
	}, 5*time.Second, 10*time.Millisecond, "Flyway stub never signaled readiness")
	cancel()

	got := awaitMigrateAll(t, done, flywayKillGraceDelay+5*time.Second)
	require.ErrorIs(t, got.err, context.Canceled)
	failed := got.res.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "t1", failed[0].TenantID)
	require.ErrorIs(t, failed[0].Err, ErrFlywayCanceled)
	assert.Equal(t, []string{"t2"}, got.res.NeverDispatched, "only the tenant the cancel kept from dispatch is never-dispatched")
	require.ErrorIs(t, got.res.Verdict(), ErrFleetSplit)
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
	assert.Equal(t, []string{"t1", "t2"}, res.NeverDispatched)
	require.ErrorIs(t, res.Verdict(), ErrNothingAttempted)
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
	assert.Equal(t, []string{"t1", "t2", "t3", "t4"}, res.NeverDispatched)
	require.ErrorIs(t, res.Verdict(), ErrNothingAttempted)
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

// cancelingQuiesceGate cancels the run while its check is in flight and fails the
// check, as a database-backed gate does when a cancel lands mid-query.
type cancelingQuiesceGate struct {
	cancel context.CancelFunc
}

func (g *cancelingQuiesceGate) IsSet(ctx context.Context) (bool, error) {
	g.cancel()
	return false, ctx.Err()
}

func (g *cancelingQuiesceGate) Query(context.Context) (*QuiesceStatus, error) {
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
	assert.Equal(t, []string{"t2", "t3", "t4"}, res.NeverDispatched)
	assert.Empty(t, res.Failed())
	require.ErrorIs(t, res.Verdict(), ErrFleetSplit, "a quiesce flip with no failed tenant still splits the fleet")
	assert.NoError(t, res.Results[0].Err, "the already-dispatched tenant completes normally")
}

func TestMigrateAllParallelFailFastCountsInFlightSiblingAsFailed(t *testing.T) {
	requireShellStubs(t)
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, createSlowFlywayStub(t, time.Minute))
	// t2 has no config, so it fails at once while t1's Flyway is still running;
	// fail-fast then cancels t1 and stops dispatch before t3.
	res, err := MigrateAll(
		context.Background(), fm, &fakeLister{ids: []string{"t1", "t2", "t3"}}, newFakeConfigProvider(stubTenantConfigs("t1", "t3")),
		ActionMigrate, MigrateAllOptions{BaseConfig: base, Parallelism: 2},
	)
	require.Error(t, err)
	failed := res.Failed()
	require.Len(t, failed, 2, "the canceled in-flight sibling is a failure, not a never-dispatched tenant")
	assert.ElementsMatch(t, []string{"t1", "t2"}, []string{failed[0].TenantID, failed[1].TenantID})
	assert.Equal(t, []string{"t3"}, res.NeverDispatched)
	require.ErrorIs(t, res.Verdict(), ErrFleetSplit)
}

func TestMigrateAllParallelRechecksQuiesceAfterWaitingForSlot(t *testing.T) {
	requireShellStubs(t)
	fm := newFlywayMigratorForTest(t)
	base := makeBaseConfig(t, createFlywayStub(t, "postgresql"))
	ids := []string{"t1", "t2", "t3"}
	gate := NewMemoryQuiesceController()
	// The first hook to run holds its worker slot, and the hook mutex keeps the
	// other worker's slot held too, so t3 waits for a slot until release closes.
	hookStarted := make(chan struct{}, 2)
	release := make(chan struct{})
	hook := func(TenantResult) {
		hookStarted <- struct{}{}
		<-release
	}

	done := migrateAllAsync(context.Background(), fm, ids, newFakeConfigProvider(stubTenantConfigs(ids...)),
		MigrateAllOptions{BaseConfig: base, Parallelism: 2, Quiesce: gate, Hook: hook})
	select {
	case <-hookStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("no tenant finished while both worker slots were free")
	}
	_, err := gate.Set(context.Background(), QuiesceSetOptions{By: "deployer", TTL: time.Hour})
	require.NoError(t, err)
	close(release)

	got := awaitMigrateAll(t, done, 10*time.Second)
	require.ErrorIs(t, got.err, ErrQuiesceBlocked)
	assert.Len(t, got.res.Results, 2)
	assert.Equal(t, []string{"t3"}, got.res.NeverDispatched, "quiesce set while t3 waited for a slot must stop it")
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

// createEnvCapturingFlywayStub builds a flyway-stub that appends the DB_*/ORACLE_*
// environment and argv it was started with to capturePath, one KEY=value per line,
// then emits a parseable migrate success envelope.
func createEnvCapturingFlywayStub(t *testing.T) (stubPath, capturePath string) {
	t.Helper()
	dir := t.TempDir()
	stubPath = filepath.Join(dir, "flyway-stub.sh")
	capturePath = filepath.Join(dir, "captured_env")
	script := fmt.Sprintf("#!/bin/sh\n{ env | grep -E '^(DB|ORACLE)_'; echo \"ARGS=$*\"; } >> %q\necho '%s'\nexit 0\n",
		capturePath, minimalMigrateSuccessJSON)
	require.NoError(t, os.WriteFile(stubPath, []byte(script), 0o755))
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

// cachingConfigProvider hands out the same *config.DatabaseConfig on every call, the
// way a caching provider does, so a mutation of the returned value is observable.
type cachingConfigProvider struct {
	cfg *config.DatabaseConfig
}

func (p cachingConfigProvider) DBConfig(context.Context, string) (*config.DatabaseConfig, error) {
	return p.cfg, nil
}

func TestMigrateAllMigratorIdentityOverlaysTenantCredentials(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	const tenantUser = "tenant_runtime"
	tenantPassword := testconsts.FakePassword("tenant-runtime")
	migratorPassword := testconsts.FakePassword("fleet-migrator")
	migrator := &MigratorIdentity{Username: "fleet_migrator", Password: migratorPassword}
	newPGTenant := func() *config.DatabaseConfig {
		return &config.DatabaseConfig{
			Type: "postgresql", Host: "tenant-pg-host", Port: 5432, Database: "tenant_db",
			Username: tenantUser, Password: tenantPassword,
			PostgreSQL: config.PostgreSQLConfig{Schema: "tenant_schema"},
		}
	}
	pgMigratorEnv := map[string]string{
		"DB_USER": "fleet_migrator", "DB_PASSWORD": migratorPassword,
		"DB_HOST": "tenant-pg-host", "DB_PORT": "5432", "DB_NAME": "tenant_db",
	}
	pgSchemaArgs := []string{"-schemas=tenant_schema", "-defaultSchema=tenant_schema"}

	cases := []struct {
		name        string
		tenant      *config.DatabaseConfig
		action      Action
		identity    *MigratorIdentity
		parallelism int
		wantEnv     map[string]string
		wantArgs    []string
	}{
		{name: "postgresql_migrate_connects_as_migrator", tenant: newPGTenant(), action: ActionMigrate, identity: migrator, wantEnv: pgMigratorEnv, wantArgs: pgSchemaArgs},
		{name: "parallel_postgresql_migrate_connects_as_migrator", tenant: newPGTenant(), action: ActionMigrate, identity: migrator, parallelism: 2, wantEnv: pgMigratorEnv, wantArgs: pgSchemaArgs},
		{name: "postgresql_validate_connects_as_migrator", tenant: newPGTenant(), action: ActionValidate, identity: migrator, wantEnv: pgMigratorEnv, wantArgs: pgSchemaArgs},
		{name: "postgresql_info_connects_as_migrator", tenant: newPGTenant(), action: ActionInfo, identity: migrator, wantEnv: pgMigratorEnv, wantArgs: pgSchemaArgs},
		{
			name: "oracle_migrate_connects_as_migrator", action: ActionMigrate, identity: migrator,
			tenant: &config.DatabaseConfig{
				Type: "oracle", Host: "tenant-ora-host", Port: 1521, Database: "TENANTPDB",
				Username: tenantUser, Password: tenantPassword,
			},
			wantEnv: map[string]string{
				"ORACLE_USER": "fleet_migrator", "ORACLE_PASSWORD": migratorPassword,
				"ORACLE_HOST": "tenant-ora-host", "ORACLE_PORT": "1521", "ORACLE_PDB": "TENANTPDB",
			},
		},
		{
			name: "nil_identity_connects_as_tenant_secret", tenant: newPGTenant(), action: ActionMigrate,
			wantEnv: map[string]string{
				"DB_USER": tenantUser, "DB_PASSWORD": tenantPassword,
				"DB_HOST": "tenant-pg-host", "DB_PORT": "5432", "DB_NAME": "tenant_db",
			},
			wantArgs: pgSchemaArgs,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub, capture := createEnvCapturingFlywayStub(t)
			res, err := MigrateAll(
				context.Background(),
				newFlywayMigratorForTest(t),
				&fakeLister{ids: []string{"t1"}},
				cachingConfigProvider{cfg: tc.tenant},
				tc.action,
				MigrateAllOptions{BaseConfig: makeBaseConfig(t, stub), MigratorIdentity: tc.identity, Parallelism: tc.parallelism},
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
			assert.Equal(t, tenantUser, tc.tenant.Username, "provider's document must not be mutated")
			assert.Equal(t, tenantPassword, tc.tenant.Password, "provider's document must not be mutated")
		})
	}
}

func TestMigrateAllRejectsInvalidMigratorIdentity(t *testing.T) {
	migratorPassword := testconsts.FakePassword("fleet-migrator")
	cases := []struct {
		name      string
		identity  *MigratorIdentity
		wantMsg   string
		wantCause error
	}{
		{name: "empty_username", identity: &MigratorIdentity{Password: migratorPassword}, wantMsg: "username"},
		{name: "empty_password", identity: &MigratorIdentity{Username: "fleet_migrator"}, wantMsg: "password"},
		{
			name:      "password_too_short_to_redact",
			identity:  &MigratorIdentity{Username: "fleet_migrator", Password: strings.Repeat("x", config.MinDatabasePasswordLength-1)},
			wantCause: ErrDatabasePasswordTooShort,
		},
		{
			name:      "password_with_line_feed",
			identity:  &MigratorIdentity{Username: "fleet_migrator", Password: migratorPassword + "\n"},
			wantMsg:   "Password",
			wantCause: ErrEnvFieldHasControlChar,
		},
		{
			name:      "username_with_nul",
			identity:  &MigratorIdentity{Username: "fleet_migrator\x00", Password: migratorPassword},
			wantMsg:   "Username",
			wantCause: ErrEnvFieldHasControlChar,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := newFakeConfigProvider(nil)
			res, err := MigrateAll(
				context.Background(),
				newFlywayMigratorForTest(t),
				&fakeLister{err: errors.New("lister must not run")},
				provider,
				ActionMigrate,
				MigrateAllOptions{MigratorIdentity: tc.identity},
			)

			require.ErrorIs(t, err, ErrInvalidMigratorIdentity)
			if tc.wantCause != nil {
				require.ErrorIs(t, err, tc.wantCause)
			}
			assert.Contains(t, err.Error(), tc.wantMsg)
			assert.NotContains(t, err.Error(), migratorPassword)
			assert.Nil(t, res)
			assert.Empty(t, provider.hits)
		})
	}
}

func TestMigrateAllSharedMigratorRefusesTenantWithoutSchema(t *testing.T) {
	requireShellStubs(t)
	// A shared migrator's refusal is per tenant, so it lands as that tenant's
	// TenantResult.Err — errors are never joined, and TenantResult.TenantID is
	// what names the tenant. The run therefore splits the fleet: ADR-115
	// classifies strictly by dispatch and has no "skipped" state, so a refused
	// tenant is a failure like any other.
	stub := createFlywayStub(t, "postgresql")
	fm := newFlywayMigratorForTest(t).WithSharedMigrator()
	base := makeBaseConfig(t, stub)

	cfgs := stubTenantConfigs("t1", "t2")
	cfgs["t1"].PostgreSQL = config.PostgreSQLConfig{Schema: "tenant_t1"}
	// t2 keeps stubTenantConfigs' empty postgresql.schema — the failure case.
	provider := newFakeConfigProvider(cfgs)

	res, err := MigrateAll(
		context.Background(),
		fm,
		&fakeLister{ids: []string{"t1", "t2"}},
		provider,
		ActionMigrate,
		MigrateAllOptions{BaseConfig: base},
	)

	require.ErrorIs(t, err, ErrSharedMigratorSchemaRequired)
	require.NotNil(t, res)
	require.Len(t, res.Results, 2, "both tenants were dispatched")
	assert.Empty(t, res.NeverDispatched)

	assert.Equal(t, "t1", res.Results[0].TenantID)
	require.NoError(t, res.Results[0].Err, "an explicitly targeted tenant still migrates")

	failed := res.Failed()
	require.Len(t, failed, 1)
	assert.Equal(t, "t2", failed[0].TenantID, "the refusal names its tenant via TenantResult.TenantID")
	require.ErrorIs(t, failed[0].Err, ErrSharedMigratorSchemaRequired)
	assert.Contains(t, failed[0].Err.Error(), "database.postgresql.schema")
	// Propagation, not construction: the schemaArgs unit test pins that the
	// fmt.Errorf itself interpolates no credential; this pins that nothing
	// between schemaArgs and TenantResult.Err re-renders one. runOne assigns
	// the error unwrapped today, so only a future wrapper would trip this.
	assert.NotContains(t, failed[0].Err.Error(), cfgs["t2"].Password,
		"the refused tenant's password must not ride the refusal out to TenantResult.Err")

	require.ErrorIs(t, res.Verdict(), ErrFleetSplit,
		"a refused tenant is a dispatched failure, so the fleet is split")
}

func TestMigrateAllMigratorIdentityPasswordStaysOutOfLogsAndErrors(t *testing.T) {
	if runtime.GOOS == windowsOS {
		t.Skip("shell stubs not supported on windows CI")
	}

	migratorPassword := testconsts.FakePassword("fleet-migrator")
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
				"t1": {Type: "postgresql", Host: "h1", Port: 5432, Database: "d1", Username: "u1", Password: testconsts.FakePassword("tenant-1")},
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
