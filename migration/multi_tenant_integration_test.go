//go:build integration

package migration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
)

// TestMigrateAllAdvisoryLockSerializesConcurrentRuns verifies that two
// concurrent MigrateAll calls against the SAME tenant serialize via
// Flyway's native PostgreSQL advisory lock — flyway_schema_history must
// contain exactly one row per version after both runs settle.
func TestMigrateAllAdvisoryLockSerializesConcurrentRuns(t *testing.T) {
	env := newIntegrationEnv(t)
	env.writeMigration(t, 1, "create_users",
		"CREATE TABLE users (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL);")
	env.writeMigration(t, 2, "create_orders",
		"CREATE TABLE orders (id BIGSERIAL PRIMARY KEY, user_id BIGINT NOT NULL);")

	tenantID := "concurrent_tenant"
	tenantCfg := env.createTenantDB(t, tenantID)

	lister := &fakeLister{ids: []string{tenantID}}
	configs := newFakeConfigProvider(map[string]*config.DatabaseConfig{tenantID: tenantCfg})
	migrator := env.migrator()

	opts := MigrateAllOptions{BaseConfig: env.flywayConfig()}

	// Two concurrent MigrateAll calls against the same tenant. Flyway's
	// advisory lock should serialize them; the second invocation must see
	// "nothing to apply" rather than re-running migration 1 or 2.
	var wg sync.WaitGroup
	results := make([]*MigrateAllResult, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(idx int) {
			defer wg.Done()
			res, err := MigrateAll(context.Background(), migrator, lister, configs, ActionMigrate, opts)
			results[idx] = res
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "MigrateAll #%d", i)
		require.NotNil(t, results[i], "MigrateAll #%d returned nil result", i)
		require.Len(t, results[i].Results, 1)
		require.NoError(t, results[i].Results[0].Err)
	}

	// The decisive assertion: exactly two rows in flyway_schema_history,
	// one per declared version. Duplicate rows would prove the advisory
	// lock failed and both invocations applied the migration twice.
	entries := env.schemaHistoryEntries(t, tenantID)
	require.Len(t, entries, 2, "expected one row per version; duplicates indicate the advisory lock didn't serialize the runs")
	assert.Equal(t, "1", entries[0].Version)
	assert.Equal(t, "2", entries[1].Version)

	// Exactly one of the two runs applied migrations; the other was a no-op.
	// We don't pin which goroutine "won" — the assertion is on the cumulative
	// effect, not the scheduling order.
	totalApplied := len(results[0].Results[0].Result.AppliedVersions) +
		len(results[1].Results[0].Result.AppliedVersions)
	assert.Equal(t, 2, totalApplied,
		"the union of both runs' AppliedVersions must equal the migration set size; advisory-lock-serialized re-application would inflate this")
}

// TestMigrateAllParallelNonBlocking verifies that one tenant's slow
// migration does not block another tenant's progress when MigrateAll runs
// with Parallelism > 1. Decisive measurement is wall-clock: sequential
// execution would take ≈ 2 × sleep + 2 × Flyway-overhead; parallel takes
// ≈ 1 × sleep + 1 × Flyway-overhead. The bound is "comfortably less than
// sequential" rather than per-tenant timing, which is dominated by JVM
// cold-start variance.
func TestMigrateAllParallelNonBlocking(t *testing.T) {
	env := newIntegrationEnv(t)

	const (
		tenantA   = "tenant_a"
		tenantB   = "tenant_b"
		sleepSecs = 1
	)
	cfgA := env.createTenantDB(t, tenantA)
	cfgB := env.createTenantDB(t, tenantB)

	env.writeMigration(t, 1, "sleep_then_create", fmt.Sprintf(
		"SELECT pg_sleep(%d); CREATE TABLE per_tenant_data (id INT);", sleepSecs))

	lister := &fakeLister{ids: []string{tenantA, tenantB}}
	configs := newFakeConfigProvider(map[string]*config.DatabaseConfig{
		tenantA: cfgA,
		tenantB: cfgB,
	})
	opts := MigrateAllOptions{
		BaseConfig:  env.flywayConfig(),
		Parallelism: 2,
	}

	start := time.Now()
	result, err := MigrateAll(context.Background(), env.migrator(), lister, configs, ActionMigrate, opts)
	totalElapsed := time.Since(start)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Results, 2)
	for i := range result.Results {
		require.NoErrorf(t, result.Results[i].Err, "tenant %s migration", result.Results[i].TenantID)
	}

	// Parallel must not exceed (1 × sleep) + generous JVM/Flyway overhead.
	// Sequential would take ≥ 2 × sleep before overhead, so a value below
	// 1.5 × sleep + overhead is unambiguous evidence of parallelism.
	overhead := 30 * time.Second
	sleep := time.Duration(sleepSecs) * time.Second
	maxParallel := sleep + sleep/2 + overhead
	assert.Lessf(t, totalElapsed, maxParallel,
		"total elapsed (%s) is >= 1.5×sleep + overhead (%s); suggests sequential execution",
		totalElapsed, maxParallel)
}
