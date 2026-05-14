//go:build integration

package provisioning

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/migration"
	"github.com/gaborage/go-bricks/testing/containers"
)

// testCtx returns a context bound to the test's deadline (if any) so DB
// operations issued from helpers cannot outlive the test's allowed runtime.
func testCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if dl, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), dl)
	}
	return context.WithCancel(context.Background())
}

// pgEnv bundles a per-test PG container + admin sql.DB. Mirrors the helper
// pattern in migration/integration_helpers_test.go but lives in the
// provisioning package so it can also exercise the role-provisioning step.
type pgEnv struct {
	host, defaultDB     string
	port                int
	adminUser, adminPwd string
	adminDB             *sql.DB
	logger              logger.Logger
}

func newPGEnv(t *testing.T) *pgEnv {
	t.Helper()

	parent, cancelParent := testCtx(t)
	t.Cleanup(cancelParent)
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	t.Cleanup(cancel)

	cfg := containers.DefaultPostgreSQLConfig()
	pg := containers.MustStartPostgreSQLContainer(ctx, t, cfg).WithCleanup(t)
	host, err := pg.Host(ctx)
	require.NoError(t, err)
	port, err := pg.MappedPort(ctx)
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		cfg.Username, cfg.Password, host, port, cfg.Database)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(ctx))

	return &pgEnv{
		host: host, port: port, defaultDB: cfg.Database,
		adminUser: cfg.Username, adminPwd: cfg.Password,
		adminDB: db,
		logger:  logger.New("disabled", true),
	}
}

// newStore returns a freshly-provisioned PostgresStore against the env's DB.
// Each test gets a unique table name so parallel runs (if introduced later)
// don't collide.
func (e *pgEnv) newStore(t *testing.T, tableSuffix string) *PostgresStore {
	t.Helper()
	tableName := "provisioning_jobs_" + tableSuffix
	store, err := NewPostgresStore(e.adminDB, tableName)
	require.NoError(t, err)

	ctx, cancel := testCtx(t)
	defer cancel()
	require.NoError(t, store.CreateTable(ctx))
	return store
}

// schemaExists reports whether a schema named s exists in the env's database.
func (e *pgEnv) schemaExists(t *testing.T, s string) bool {
	t.Helper()
	ctx, cancel := testCtx(t)
	defer cancel()
	var exists bool
	err := e.adminDB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`,
		s,
	).Scan(&exists)
	require.NoError(t, err)
	return exists
}

// roleExists reports whether a PG role named r exists.
func (e *pgEnv) roleExists(t *testing.T, r string) bool {
	t.Helper()
	ctx, cancel := testCtx(t)
	defer cancel()
	var exists bool
	err := e.adminDB.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = $1)`,
		r,
	).Scan(&exists)
	require.NoError(t, err)
	return exists
}

// dropTenantArtifacts cleans up the schema and roles a Cleanup step would
// drop. Used by both the cleanup test (as the consumer's Cleanup impl) and
// by per-test teardown to keep one PG container from accumulating state.
func (e *pgEnv) dropTenantArtifacts(t *testing.T, schema, migratorRole, runtimeRole string) {
	t.Helper()
	ctx, cancel := testCtx(t)
	defer cancel()
	// CASCADE drops dependent objects (tables, sequences). Roles must be
	// dropped after the schema since they own it.
	for _, stmt := range []string{
		fmt.Sprintf(`DROP SCHEMA IF EXISTS "%s" CASCADE`, schema),
		fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, runtimeRole),
		fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, migratorRole),
	} {
		if _, err := e.adminDB.ExecContext(ctx, stmt); err != nil {
			t.Logf("cleanup of %q failed (test-side): %v", stmt, err)
		}
	}
}

// realRoleProvisioningSteps returns Steps wired against migration.ProvisionPGRoles
// for the schema/role artifacts described by spec. CreateSchema invokes
// ProvisionPGRoles (which creates BOTH schema and role atomically per #378);
// CreateRole is symbolically a no-op so the FSM still walks the two distinct
// states. Migrate and Seed are no-ops in v1 — exercising real Flyway here
// would duplicate `migration/flyway_integration_test.go` without testing
// anything new for the state-machine contract. Cleanup drops both artifacts.
func (e *pgEnv) realRoleProvisioningSteps(t *testing.T, spec *migration.PGRoleSpec) Steps {
	t.Helper()
	return Steps{
		CreateSchema: func(ctx context.Context, _ *Job) error {
			return migration.ProvisionPGRoles(ctx, e.adminDB, spec)
		},
		CreateRole: func(_ context.Context, _ *Job) error { return nil },
		Migrate:    func(_ context.Context, _ *Job) error { return nil },
		Seed:       func(_ context.Context, _ *Job) error { return nil },
		Cleanup: func(_ context.Context, _ *Job) error {
			e.dropTenantArtifacts(t, spec.Schema, spec.MigratorRole, spec.RuntimeRole)
			return nil
		},
	}
}

// TestProvisioningCrashRecoveryResumesFromPersistedState verifies AC #1 from
// issue #379: simulate a worker crash mid-flow (after a step's side effect
// succeeded but before the next forward step could run), restart with a
// fresh Executor against the persisted state, and observe convergence to
// StateReady.
func TestProvisioningCrashRecoveryResumesFromPersistedState(t *testing.T) {
	env := newPGEnv(t)
	store := env.newStore(t, "crash_recovery")

	schema := "tenant_crash"
	migratorRole := "mig_crash"
	runtimeRole := "rt_crash"
	t.Cleanup(func() { env.dropTenantArtifacts(t, schema, migratorRole, runtimeRole) })

	spec := &migration.PGRoleSpec{
		Schema:           schema,
		MigratorRole:     migratorRole,
		MigratorPassword: "mig-crash-pw-1234",
		RuntimeRole:      runtimeRole,
		RuntimePassword:  "rt-crash-pw-1234",
	}

	steps := env.realRoleProvisioningSteps(t, spec)

	ctx, cancel := testCtx(t)
	defer cancel()

	job := &Job{ID: "job-crash-1", TenantID: "tenant-crash"}
	_, err := store.Upsert(ctx, job)
	require.NoError(t, err)

	// Simulate a previous executor that crashed after Migrate succeeded but
	// before the seeded transition was persisted: replicate the schema/role
	// side effects out-of-band (the real CreateSchema step calls
	// ProvisionPGRoles internally), then advance the persisted state to
	// StateMigrated as a real run would have left it.
	require.NoError(t, migration.ProvisionPGRoles(ctx, env.adminDB, spec),
		"out-of-band: simulate the side effects of CreateSchema before the crash")
	require.NoError(t, store.Transition(ctx, job.ID, StatePending, StateSchemaCreated, nil, ""))
	require.NoError(t, store.Transition(ctx, job.ID, StateSchemaCreated, StateRoleCreated, nil, ""))
	require.NoError(t, store.Transition(ctx, job.ID, StateRoleCreated, StateMigrated, nil, ""))

	// Instantiate a fresh Executor (as a worker restart would) and resume.
	exec, err := NewExecutor(store, steps, env.logger)
	require.NoError(t, err)
	require.NoError(t, exec.Run(ctx, job.ID))

	final, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateReady, final.State,
		"crash-recovered run must converge to StateReady from the persisted StateMigrated")
}

// TestProvisioningCleanupOnFailureDropsArtifacts verifies AC #2 from issue
// #379: a forward step that returns an error must transition the job
// through StateCleanup to StateFailed AND the cleanup step must actually
// drop the partially-provisioned schema and role.
func TestProvisioningCleanupOnFailureDropsArtifacts(t *testing.T) {
	env := newPGEnv(t)
	store := env.newStore(t, "cleanup_failure")

	schema := "tenant_fail"
	migratorRole := "mig_fail"
	runtimeRole := "rt_fail"
	t.Cleanup(func() { env.dropTenantArtifacts(t, schema, migratorRole, runtimeRole) })

	spec := &migration.PGRoleSpec{
		Schema:           schema,
		MigratorRole:     migratorRole,
		MigratorPassword: "mig-fail-pw-1234",
		RuntimeRole:      runtimeRole,
		RuntimePassword:  "rt-fail-pw-1234",
	}

	// CreateSchema succeeds (using the real role provisioner), but Migrate
	// fails — this is the realistic failure mode in production. Cleanup
	// must roll back the schema and role created in CreateSchema.
	steps := env.realRoleProvisioningSteps(t, spec)
	steps.Migrate = func(_ context.Context, job *Job) error {
		return errors.New("migration failed: simulated Flyway error")
	}

	exec, err := NewExecutor(store, steps, env.logger)
	require.NoError(t, err)

	ctx, cancel := testCtx(t)
	defer cancel()

	job := &Job{ID: "job-fail-1", TenantID: "tenant-fail"}
	_, err = store.Upsert(ctx, job)
	require.NoError(t, err)

	err = exec.Run(ctx, job.ID)
	require.Error(t, err, "Run must surface the underlying failure")
	assert.Contains(t, err.Error(), "ended in failed state")

	final, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, final.State, "cleanup must converge to StateFailed")
	assert.Contains(t, final.LastError, "simulated Flyway error")

	// The cleanup step must have actually dropped the schema and role.
	assert.False(t, env.schemaExists(t, schema), "schema must be dropped by cleanup")
	assert.False(t, env.roleExists(t, runtimeRole), "runtime role must be dropped by cleanup")
	assert.False(t, env.roleExists(t, migratorRole), "migrator role must be dropped by cleanup")
}

// TestProvisioningRerunSameJobIDIsNoOp verifies AC #3 from issue #379:
// re-running a job with the same ID after it reached StateReady is a no-op.
// In particular, the Seed step must not run a second time (which the
// consumer-supplied step records via a counter).
func TestProvisioningRerunSameJobIDIsNoOp(t *testing.T) {
	env := newPGEnv(t)
	store := env.newStore(t, "rerun_noop")

	schema := "tenant_rerun"
	migratorRole := "mig_rerun"
	runtimeRole := "rt_rerun"
	t.Cleanup(func() { env.dropTenantArtifacts(t, schema, migratorRole, runtimeRole) })

	spec := &migration.PGRoleSpec{
		Schema:           schema,
		MigratorRole:     migratorRole,
		MigratorPassword: "mig-rerun-pw-1234",
		RuntimeRole:      runtimeRole,
		RuntimePassword:  "rt-rerun-pw-1234",
	}

	var seedCalls int
	steps := env.realRoleProvisioningSteps(t, spec)
	steps.Seed = func(_ context.Context, job *Job) error {
		seedCalls++
		return nil
	}

	exec, err := NewExecutor(store, steps, env.logger)
	require.NoError(t, err)

	ctx, cancel := testCtx(t)
	defer cancel()

	job := &Job{ID: "job-rerun-1", TenantID: "tenant-rerun"}
	_, err = store.Upsert(ctx, job)
	require.NoError(t, err)

	require.NoError(t, exec.Run(ctx, job.ID))
	assert.Equal(t, 1, seedCalls, "first run must call Seed exactly once")

	final, err := store.Get(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, StateReady, final.State)

	// Second Run with the same jobID — must be a no-op.
	require.NoError(t, exec.Run(ctx, job.ID))
	assert.Equal(t, 1, seedCalls, "second run on a Ready job must NOT call Seed again")

	// Upsert with the same ID must also be a no-op — returns the
	// existing record, not a fresh StatePending.
	got, err := store.Upsert(ctx, &Job{ID: job.ID, TenantID: "tenant-rerun"})
	require.NoError(t, err)
	assert.Equal(t, StateReady, got.State,
		"Upsert on an existing job ID must preserve the persisted state")
}
