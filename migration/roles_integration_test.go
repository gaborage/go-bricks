//go:build integration

package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database"
	testconsts "github.com/gaborage/go-bricks/testing"
)

// TestPGRolesRuntimeRoleRejectedOnDDL verifies the role-separation acceptance
// criterion: a runtime role attempting CREATE/ALTER/DROP TABLE against its
// own schema must be rejected by PostgreSQL. The runtime role does not own
// the schema (the migrator does), so default PG ownership semantics enforce
// this without explicit REVOKE statements.
func TestPGRolesRuntimeRoleRejectedOnDDL(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:           "tenant_ddl",
		MigratorRole:     "mig_ddl",
		MigratorPassword: testconsts.FakePassword("mig-ddl"),
		RuntimeRole:      "rt_ddl",
		RuntimePassword:  testconsts.FakePassword("rt-ddl"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec))

	// Pre-seed an existing table via the migrator so we can exercise ALTER
	// and DROP. CREATE TABLE is exercised separately below — it needs no
	// pre-existing object.
	migratorDB := env.openAsRole(t, spec.MigratorRole, spec.MigratorPassword)
	_, err := migratorDB.ExecContext(ctx,
		fmt.Sprintf(`CREATE TABLE %s.widgets (id INT PRIMARY KEY, label TEXT)`,
			quotePGIdent(spec.Schema)))
	require.NoError(t, err, "migrator must be able to create tables in its own schema")

	runtimeDB := env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword)

	// CREATE TABLE — rejected because the runtime role lacks CREATE on the schema.
	_, err = runtimeDB.ExecContext(ctx,
		fmt.Sprintf(`CREATE TABLE %s.unauthorized (id INT)`, quotePGIdent(spec.Schema)))
	require.Error(t, err, "runtime role must not be able to CREATE TABLE")
	assert.True(t, isPermissionDenied(err), "CREATE TABLE should fail with permission denied, got: %v", err)

	// ALTER TABLE — rejected because the runtime role is not the table owner.
	_, err = runtimeDB.ExecContext(ctx,
		fmt.Sprintf(`ALTER TABLE %s.widgets ADD COLUMN sneaky TEXT`, quotePGIdent(spec.Schema)))
	require.Error(t, err, "runtime role must not be able to ALTER TABLE")
	assert.True(t, isPermissionDenied(err), "ALTER TABLE should fail with permission denied, got: %v", err)

	// DROP TABLE — rejected for the same reason.
	_, err = runtimeDB.ExecContext(ctx,
		fmt.Sprintf(`DROP TABLE %s.widgets`, quotePGIdent(spec.Schema)))
	require.Error(t, err, "runtime role must not be able to DROP TABLE")
	assert.True(t, isPermissionDenied(err), "DROP TABLE should fail with permission denied, got: %v", err)
}

// TestPGRolesAlterDefaultPrivilegesAutoGrants verifies the second acceptance
// criterion: after the migrator creates a new table, the runtime role must
// automatically have SELECT/INSERT/UPDATE/DELETE on it via ALTER DEFAULT
// PRIVILEGES — no per-migration grant DDL required.
func TestPGRolesAlterDefaultPrivilegesAutoGrants(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:           "tenant_adp",
		MigratorRole:     "mig_adp",
		MigratorPassword: testconsts.FakePassword("mig-adp"),
		RuntimeRole:      "rt_adp",
		RuntimePassword:  testconsts.FakePassword("rt-adp"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec))

	// Migrator creates a new table AFTER provisioning ran — this is what
	// ALTER DEFAULT PRIVILEGES covers (existing-table grants ran during
	// provisioning, but at that point the schema was empty).
	migratorDB := env.openAsRole(t, spec.MigratorRole, spec.MigratorPassword)
	createTable := fmt.Sprintf(
		`CREATE TABLE %s.gadgets (id INT PRIMARY KEY, name TEXT NOT NULL, qty INT NOT NULL DEFAULT 0)`,
		quotePGIdent(spec.Schema),
	)
	_, err := migratorDB.ExecContext(ctx, createTable)
	require.NoError(t, err)

	runtimeDB := env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword)
	qualified := quotePGIdent(spec.Schema) + ".gadgets"

	// INSERT
	_, err = runtimeDB.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (id, name, qty) VALUES (1, 'widget-a', 5)`, qualified))
	require.NoError(t, err, "ALTER DEFAULT PRIVILEGES must auto-grant INSERT on future tables")

	// SELECT
	var name string
	var qty int
	err = runtimeDB.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT name, qty FROM %s WHERE id = 1`, qualified)).Scan(&name, &qty)
	require.NoError(t, err, "ALTER DEFAULT PRIVILEGES must auto-grant SELECT on future tables")
	assert.Equal(t, "widget-a", name)
	assert.Equal(t, 5, qty)

	// UPDATE
	_, err = runtimeDB.ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s SET qty = qty + 1 WHERE id = 1`, qualified))
	require.NoError(t, err, "ALTER DEFAULT PRIVILEGES must auto-grant UPDATE on future tables")

	// DELETE
	_, err = runtimeDB.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE id = 1`, qualified))
	require.NoError(t, err, "ALTER DEFAULT PRIVILEGES must auto-grant DELETE on future tables")
}

// TestPGRolesRuntimeRoleHasNoSuperPowers verifies the third acceptance
// criterion: the runtime role must not carry any privilege-escalation
// attributes (SUPERUSER, CREATEDB, CREATEROLE, BYPASSRLS, or REPLICATION).
// The migrator role is verified for the same flat 'no' to give auditors a
// single answer for the entire role pair.
func TestPGRolesRuntimeRoleHasNoSuperPowers(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:          "tenant_caps",
		MigratorRole:    "mig_caps",
		RuntimeRole:     "rt_caps",
		RuntimePassword: testconsts.FakePassword("rt-caps"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec))

	for _, role := range []string{spec.MigratorRole, spec.RuntimeRole} {
		t.Run(role, func(t *testing.T) {
			var attrs struct {
				IsSuperuser, CanCreateDB, CanCreateRole, CanBypassRLS, CanReplicate bool
			}
			err := admin.QueryRowContext(ctx,
				`SELECT rolsuper, rolcreatedb, rolcreaterole, rolbypassrls, rolreplication
				 FROM pg_catalog.pg_roles WHERE rolname = $1`,
				role,
			).Scan(&attrs.IsSuperuser, &attrs.CanCreateDB, &attrs.CanCreateRole, &attrs.CanBypassRLS, &attrs.CanReplicate)
			require.NoError(t, err)

			assert.False(t, attrs.IsSuperuser, "%s must not be SUPERUSER", role)
			assert.False(t, attrs.CanCreateDB, "%s must not have CREATEDB", role)
			assert.False(t, attrs.CanCreateRole, "%s must not have CREATEROLE", role)
			assert.False(t, attrs.CanBypassRLS, "%s must not have BYPASSRLS", role)
			assert.False(t, attrs.CanReplicate, "%s must not have REPLICATION", role)
		})
	}
}

// TestPGRolesProvisioningIsIdempotent verifies that running provisioning twice
// with the same spec is a no-op the second time — required on the
// ProvisionPGRoles path, where each statement lands on its own connection and a
// partial failure leaves earlier steps in place, so callers converge by
// rerunning. Re-runs also exercise the in-place password rotation path.
//
// Roles are INSTANCE-global, so the two runners get disjoint identifiers:
// sharing them would make the second subtest provision over the first's roles
// and stop testing anything. Each rerun is pinned as a rerun rather than a
// fresh creation by asserting the prior run's state from outside its own
// transaction — countRoles on a separate admin connection, which on the tx
// door only passes once the transaction has committed — and again via the
// rotated-password login at the end.
func TestPGRolesProvisioningIsIdempotent(t *testing.T) {
	tests := []struct {
		name   string
		suffix string
		run    func(t *testing.T, ctx context.Context, env *integrationEnv, spec *PGRoleSpec) error
	}{
		{
			name:   "sql_db_door",
			suffix: "idem",
			run: func(t *testing.T, ctx context.Context, env *integrationEnv, spec *PGRoleSpec) error {
				t.Helper()
				return ProvisionPGRoles(ctx, env.adminDB(t), spec)
			},
		},
		{
			name:   "tx_door",
			suffix: "idemtx",
			run: func(t *testing.T, ctx context.Context, env *integrationEnv, spec *PGRoleSpec) error {
				t.Helper()
				return database.WithTx(ctx, env.adminConn(t), func(ctx context.Context, tx database.Tx) error {
					return ProvisionPGRolesTx(ctx, tx, spec)
				})
			},
		},
	}

	env := newIntegrationEnv(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &PGRoleSpec{
				Schema:           "tenant_" + tt.suffix,
				MigratorRole:     "mig_" + tt.suffix,
				MigratorPassword: testconsts.FakePassword("mig-" + tt.suffix),
				RuntimeRole:      "rt_" + tt.suffix,
				RuntimePassword:  testconsts.FakePassword("rt-" + tt.suffix),
			}

			ctx, cancel := testCtx(t)
			defer cancel()

			admin := env.adminDB(t)

			require.NoError(t, tt.run(t, ctx, env, spec), "first run")
			require.Equal(t, 2, countRoles(ctx, t, admin, spec),
				"the first run must be visible outside its own transaction before the rerun is meaningful")
			require.NoError(t, tt.run(t, ctx, env, spec), "second run must be idempotent")

			// Rotate the runtime password and verify the new credential works.
			spec.RuntimePassword = testconsts.FakePassword("rt-" + tt.suffix + "-rotated")
			require.NoError(t, tt.run(t, ctx, env, spec), "rotate runtime password")

			rotated := env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword)
			require.NoError(t, rotated.PingContext(ctx))
		})
	}
}

// errRollbackProvisioning is returned from the WithTx callback below AFTER
// provisioning succeeded, so WithTx rolls the transaction back with every
// statement already applied inside it.
var errRollbackProvisioning = errors.New("roll the provisioning back")

// TestPGRolesProvisioningTxRollsBackWholesale pins that on the tx door a
// rollback leaves NO trace of the spec — no role, no schema. The
// post-conditions are read on a SEPARATE admin connection, so a leftover would
// be genuinely visible rather than hidden behind the aborted session's own
// snapshot: if any emitted statement really were non-transactional, its effect
// would survive the rollback and be found here.
func TestPGRolesProvisioningTxRollsBackWholesale(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:           "tenant_rb",
		MigratorRole:     "mig_rb",
		MigratorPassword: testconsts.FakePassword("mig-rb"),
		RuntimeRole:      "rt_rb",
		RuntimePassword:  testconsts.FakePassword("rt-rb"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.Zero(t, countRoles(ctx, t, admin, spec), "the roles must not exist before the run")
	require.Zero(t, countSchemas(ctx, t, admin, spec), "the schema must not exist before the run")

	err := database.WithTx(ctx, env.adminConn(t), func(ctx context.Context, tx database.Tx) error {
		if provErr := ProvisionPGRolesTx(ctx, tx, spec); provErr != nil {
			return provErr
		}
		// Proof the statements really ran inside this transaction: the
		// transaction's own view sees the role it just created.
		var visible int
		if scanErr := tx.QueryRow(ctx,
			`SELECT count(*) FROM pg_roles WHERE rolname IN ($1, $2)`,
			spec.MigratorRole, spec.RuntimeRole).Scan(&visible); scanErr != nil {
			return scanErr
		}
		// assert, not require: a require here would Goexit past the sentinel
		// return and skip the rollback assertions below.
		assert.Equal(t, 2, visible, "both roles must be visible inside the transaction")
		// Without this, the post-rollback countSchemas assertion below passes
		// vacuously for any run that never created the schema at all.
		var schemaVisible int
		if scanErr := tx.QueryRow(ctx,
			`SELECT count(*) FROM pg_namespace WHERE nspname = $1`,
			spec.Schema).Scan(&schemaVisible); scanErr != nil {
			return scanErr
		}
		assert.Equal(t, 1, schemaVisible, "the schema must be visible inside the transaction")
		return errRollbackProvisioning
	})
	require.ErrorIs(t, err, errRollbackProvisioning)

	assert.Zero(t, countRoles(ctx, t, admin, spec),
		"a rolled-back provisioning must leave no role behind")
	assert.Zero(t, countSchemas(ctx, t, admin, spec),
		"a rolled-back provisioning must leave no schema behind")
}

// countRoles reports how many of the spec's two roles exist on the instance.
func countRoles(ctx context.Context, t *testing.T, db *sql.DB, spec *PGRoleSpec) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_roles WHERE rolname IN ($1, $2)`,
		spec.MigratorRole, spec.RuntimeRole).Scan(&n))
	return n
}

// countSchemas reports how many schemas match the spec's name — 0 or 1.
func countSchemas(ctx context.Context, t *testing.T, db *sql.DB, spec *PGRoleSpec) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_namespace WHERE nspname = $1`, spec.Schema).Scan(&n))
	return n
}

// TestPGRolesSearchPathSetOnBothRoles verifies that provisioning sets a
// default search_path on both roles, pointing at the tenant schema, and that
// the setting converges (idempotent) on rerun.
//
// Stored-form trap: search_path is a GUC_LIST_QUOTE parameter — PostgreSQL
// re-normalizes the value through quote_identifier at SET time, stripping
// quotes from any name that is legal unquoted. The test uses a lowercase
// snake_case schema, so even though the emitted SQL says
// SET search_path = "tenant_sp", the stored rolconfig entry is unquoted:
// search_path=tenant_sp. Assert on that unquoted form.
func TestPGRolesSearchPathSetOnBothRoles(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:           "tenant_sp",
		MigratorRole:     "mig_sp",
		MigratorPassword: testconsts.FakePassword("mig-sp"),
		RuntimeRole:      "rt_sp",
		RuntimePassword:  testconsts.FakePassword("rt-sp"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec))

	rolconfigs := func() map[string][]string {
		// rolconfig is a text[]; pgx v5.11+ on Go 1.27 scans it straight into []string.
		rows, err := admin.QueryContext(ctx,
			`SELECT rolname, rolconfig FROM pg_roles WHERE rolname IN ($1, $2) ORDER BY rolname`,
			spec.MigratorRole, spec.RuntimeRole)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()

		out := make(map[string][]string)
		for rows.Next() {
			var rolname string
			var cfg []string
			require.NoError(t, rows.Scan(&rolname, &cfg))
			out[rolname] = cfg
		}
		require.NoError(t, rows.Err())
		return out
	}

	cfgs := rolconfigs()
	require.Len(t, cfgs, 2)
	assert.Contains(t, cfgs[spec.MigratorRole], "search_path="+spec.Schema,
		"migrator rolconfig: %q", cfgs[spec.MigratorRole])
	assert.Contains(t, cfgs[spec.RuntimeRole], "search_path="+spec.Schema,
		"runtime rolconfig: %q", cfgs[spec.RuntimeRole])

	// Convergence: rerunning with the same spec must not error, and the
	// stored rolconfig must be unchanged (issue AC #3).
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "second run must be idempotent")
	assert.Equal(t, cfgs, rolconfigs(), "rolconfig must be unchanged after a convergent rerun")
}

// TestPGRolesUnqualifiedResolutionUsesTenantSchema verifies the runtime-
// ergonomics half of the search_path change: unqualified DDL from the
// migrator role and unqualified DML from the runtime role both resolve
// against the tenant schema instead of falling through to public.
func TestPGRolesUnqualifiedResolutionUsesTenantSchema(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:           "tenant_unq",
		MigratorRole:     "mig_unq",
		MigratorPassword: testconsts.FakePassword("mig-unq"),
		RuntimeRole:      "rt_unq",
		RuntimePassword:  testconsts.FakePassword("rt-unq"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec))

	// Migrator role: the openAsRole DSN does not pin search_path, so the role
	// default under test actually takes effect for this connection.
	migratorDB := env.openAsRole(t, spec.MigratorRole, spec.MigratorPassword)
	_, err := migratorDB.ExecContext(ctx, `CREATE TABLE sp_probe (id INT)`)
	require.NoError(t, err, "migrator must be able to CREATE TABLE unqualified")

	var tenantRegclass, publicRegclass *string
	require.NoError(t, admin.QueryRowContext(ctx,
		`SELECT to_regclass($1)`, spec.Schema+".sp_probe").Scan(&tenantRegclass))
	assert.NotNil(t, tenantRegclass, "sp_probe must exist in the tenant schema")

	require.NoError(t, admin.QueryRowContext(ctx,
		`SELECT to_regclass('public.sp_probe')`).Scan(&publicRegclass))
	assert.Nil(t, publicRegclass, "sp_probe must NOT exist in public")

	runtimeDB := env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword)

	// 1. Schema-qualified SELECT succeeds — isolates the grants chain
	// (schema USAGE + the ALTER DEFAULT PRIVILEGES auto-grant, already
	// pinned by TestPGRolesAlterDefaultPrivilegesAutoGrants). This is a
	// dependency of assertion 2 below, not what this plan changes.
	var count int
	qualified := quotePGIdent(spec.Schema) + ".sp_probe"
	require.NoError(t, runtimeDB.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s`, qualified)).Scan(&count),
		"qualified SELECT must succeed via the existing grants chain")

	// 2. Unqualified SELECT succeeds — isolates the new search_path default.
	// If only this assertion fails (with #1 passing), search_path is the
	// culprit; if #1 fails, the grants model regressed instead.
	require.NoError(t, runtimeDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sp_probe`).Scan(&count),
		"unqualified SELECT must succeed via the new search_path default")
}

// isPermissionDenied reports whether err looks like a PostgreSQL privilege-
// rejection error (SQLSTATE 42501). We match on the message because the test
// uses database/sql + pgx stdlib, which surfaces the SQLSTATE inside the
// wrapped error string. Substring match is sufficient — the prefix is stable
// across pgx versions.
func isPermissionDenied(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 42501") ||
		strings.Contains(msg, "permission denied") ||
		strings.Contains(msg, "must be owner")
}

// pgPublicGrantDetectSQL is the RuntimeRole half of the [C65.4] detect step in
// wiki/migrations.md — and, since the query answers both, its verify step too.
// It is kept here verbatim so the atom's query has a live oracle and the two
// cannot drift apart silently.
//
// A RuntimeRole spelled "public" leaves FOUR residues, because PGRoleProvisioningSQL
// emits four kinds of grant against the PUBLIC pseudo-role (migration/roles.go):
// GRANT USAGE ON SCHEMA lands in pg_namespace.nspacl; GRANT … ON ALL TABLES and
// GRANT … ON ALL SEQUENCES both land in pg_class.relacl (a sequence IS a pg_class
// row, which is why no relkind filter belongs here); and the two ALTER DEFAULT
// PRIVILEGES statements land in pg_default_acl.defaclacl. Reading pg_class alone
// therefore reports "clean" for a tenant provisioned into a still-empty schema —
// there are no relations yet, but the schema USAGE grant and both default-privilege
// rows are already there. Hence the UNION ALL over all three catalogs, with a
// `source` column so the operator sees WHICH residue they hit.
//
// aclexplode breaks an ACL into one row per privilege and represents the PUBLIC
// pseudo-role as grantee 0. It is strict, so a NULL acl column contributes no rows.
// The catalog schemas are excluded on every arm because PostgreSQL grants PUBLIC
// SELECT on its own catalogs by design. The schema arm skips public as well: every
// instance ships public with a USAGE-to-PUBLIC entry, that same aclitem is the
// only schema-level grant the template can add to public's own ACL, so a schema
// row for public could never tell residue from baseline, and acting on one
// (REVOKE ALL ON SCHEMA public FROM PUBLIC) strips the baseline from every other
// role on the database. The relation and default_acl arms still cover public,
// where PostgreSQL grants PUBLIC nothing by default. Database-wide default ACLs
// (pg_default_acl.defaclnamespace = 0) are deliberately out of scope: the inner
// join to pg_namespace drops them, they can never be produced by this template —
// which always emits `ALTER DEFAULT PRIVILEGES … IN SCHEMA <schema>` — and they
// have no tenant schema to attribute or to revoke against.
//
// The query is NOT information_schema.role_table_grants, whose documented
// difference from table_privileges is that it OMITS what a grant to PUBLIC made
// reachable — exactly the rows we are hunting.
const pgPublicGrantDetectSQL = `SELECT 'schema'::text AS source, n.nspname AS schema_name,
       ''::text AS object_name, a.privilege_type
FROM pg_namespace n, aclexplode(n.nspacl) a
WHERE a.grantee = 0
  AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'public')
UNION ALL
SELECT 'relation', n.nspname, c.relname, a.privilege_type
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace,
     aclexplode(c.relacl) a
WHERE a.grantee = 0
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
UNION ALL
SELECT 'default_acl', n.nspname,
       CASE d.defaclobjtype WHEN 'r' THEN 'TABLES'
                            WHEN 'S' THEN 'SEQUENCES'
                            WHEN 'f' THEN 'FUNCTIONS'
                            WHEN 'T' THEN 'TYPES'
                            WHEN 'n' THEN 'SCHEMAS'
                            ELSE d.defaclobjtype::text END,
       a.privilege_type
FROM pg_default_acl d
JOIN pg_namespace n ON n.oid = d.defaclnamespace,
     aclexplode(d.defaclacl) a
WHERE a.grantee = 0
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
ORDER BY 1, 2, 3, 4`

// publicGrantProbeTable lives in the shared public schema, so the test drops it on
// the way out whatever happens.
const publicGrantProbeTable = "c654_public_grant_probe_tbl"

// TestPGPublicGrantDetectSQLFindsEveryPublicResidue is the falsifiability check
// on the atom's detect/verify query. Each of the four residues a RuntimeRole
// spelled "public" leaves behind must be found, with the right source value, and
// each must disappear once the atom's apply step revokes it.
func TestPGPublicGrantDetectSQLFindsEveryPublicResidue(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)

	// The built-in `public` schema carries a PUBLIC USAGE grant on every
	// PostgreSQL instance by design (PG15+ dropped CREATE, never USAGE). The only
	// schema-level grant the template can add to public's own ACL is that same
	// USAGE-to-PUBLIC entry, so a schema row for public could never tell residue
	// from baseline, and an operator acting on it would strip the baseline from
	// every unrelated role. The schema arm therefore never reports public.
	require.Empty(t, publicGrantRows(ctx, t, admin, "public"),
		"an untouched instance must report nothing on public, baseline USAGE included")

	t.Run("public_relation_grant_still_reports", func(t *testing.T) {
		_, err := admin.ExecContext(ctx, `CREATE TABLE public.`+publicGrantProbeTable+` (id INT PRIMARY KEY)`)
		require.NoError(t, err)
		defer func() {
			_, cleanupErr := admin.ExecContext(ctx, `DROP TABLE IF EXISTS public.`+publicGrantProbeTable)
			assert.NoError(t, cleanupErr)
		}()
		_, err = admin.ExecContext(ctx, `GRANT SELECT ON public.`+publicGrantProbeTable+` TO PUBLIC`)
		require.NoError(t, err)

		require.Equal(t, []string{"relation|public|" + publicGrantProbeTable + "|SELECT"},
			publicGrantRows(ctx, t, admin, "public"),
			"only the schema arm skips public; a PUBLIC grant on one of its tables is still a finding")
	})

	t.Run("all_four_residues", func(t *testing.T) {
		_, err := admin.ExecContext(ctx, `CREATE SCHEMA acl_probe`)
		require.NoError(t, err)
		_, err = admin.ExecContext(ctx, `CREATE TABLE acl_probe.widgets (id INT PRIMARY KEY)`)
		require.NoError(t, err)
		_, err = admin.ExecContext(ctx, `CREATE SEQUENCE acl_probe.widget_ids`)
		require.NoError(t, err)

		require.Empty(t, publicGrantRows(ctx, t, admin, "acl_probe"),
			"PostgreSQL grants nothing to PUBLIC on a schema, table or sequence you create")

		// The four statements PGRoleProvisioningSQL emits against PUBLIC when
		// RuntimeRole is spelled "public" (migration/roles.go), one per catalog.
		for _, stmt := range []string{
			`GRANT USAGE ON SCHEMA acl_probe TO PUBLIC`,
			`GRANT SELECT ON acl_probe.widgets TO PUBLIC`,
			`GRANT USAGE ON SEQUENCE acl_probe.widget_ids TO PUBLIC`,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA acl_probe GRANT SELECT ON TABLES TO PUBLIC`,
		} {
			_, err = admin.ExecContext(ctx, stmt)
			require.NoError(t, err, stmt)
		}

		got := publicGrantRows(ctx, t, admin, "acl_probe")
		require.ElementsMatch(t, []string{
			"schema|acl_probe||USAGE",
			"relation|acl_probe|widgets|SELECT",
			"relation|acl_probe|widget_ids|USAGE",
			"default_acl|acl_probe|TABLES|SELECT",
		}, got, "every residue must be found, tagged with the catalog it came from")
		require.Equal(t, []string{"default_acl", "relation", "relation", "schema"},
			sourceColumn(got), "the query must order its arms deterministically")

		for _, stmt := range []string{
			`REVOKE ALL ON SCHEMA acl_probe FROM PUBLIC`,
			`REVOKE ALL ON ALL TABLES IN SCHEMA acl_probe FROM PUBLIC`,
			`REVOKE ALL ON ALL SEQUENCES IN SCHEMA acl_probe FROM PUBLIC`,
			`ALTER DEFAULT PRIVILEGES IN SCHEMA acl_probe REVOKE SELECT ON TABLES FROM PUBLIC`,
		} {
			_, err = admin.ExecContext(ctx, stmt)
			require.NoError(t, err, stmt)
		}

		require.Empty(t, publicGrantRows(ctx, t, admin, "acl_probe"),
			"the apply step's revokes must make the query go quiet again")
	})

	// Regression test for the defect this query fixes: a tenant provisioned into
	// a schema that has no relations yet produces ZERO pg_class rows, so a
	// pg_class-only query reads clean on a genuinely affected instance.
	t.Run("empty_schema_still_reports", func(t *testing.T) {
		_, err := admin.ExecContext(ctx, `CREATE SCHEMA acl_empty`)
		require.NoError(t, err)
		_, err = admin.ExecContext(ctx, `GRANT USAGE ON SCHEMA acl_empty TO PUBLIC`)
		require.NoError(t, err)

		var relations int
		require.NoError(t, admin.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = 'acl_empty'`).Scan(&relations))
		require.Zero(t, relations, "the regression case needs a genuinely empty schema")

		require.Equal(t, []string{"schema|acl_empty||USAGE"},
			publicGrantRows(ctx, t, admin, "acl_empty"),
			"a schema-only PUBLIC grant must be found even with no relations at all")

		_, err = admin.ExecContext(ctx, `REVOKE ALL ON SCHEMA acl_empty FROM PUBLIC`)
		require.NoError(t, err)
		require.Empty(t, publicGrantRows(ctx, t, admin, "acl_empty"))
	})
}

// publicGrantRows runs pgPublicGrantDetectSQL verbatim and flattens the rows for
// one schema to "source|schema|object|privilege" so a test can assert the exact
// row set. The filter is applied in Go, never in the SQL, so the const under test
// stays byte-identical to the query the atom publishes.
func publicGrantRows(ctx context.Context, t *testing.T, db *sql.DB, schema string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, pgPublicGrantDetectSQL)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var source, nspname, object, privilege string
		require.NoError(t, rows.Scan(&source, &nspname, &object, &privilege))
		if nspname != schema {
			continue
		}
		out = append(out, strings.Join([]string{source, nspname, object, privilege}, "|"))
	}
	require.NoError(t, rows.Err())
	return out
}

// pgPublicSchemaResidueDetectSQL is the SCHEMA half of the [C65.4] detect step
// in wiki/migrations.md, kept here verbatim for the same reason as the other two
// queries in this file: so the atom's copy has a live oracle and the two cannot
// drift apart silently.
//
// A Schema spelled "public" leaves no ownership trace: buildPGRoleStatements
// emits CREATE SCHEMA IF NOT EXISTS "public" AUTHORIZATION <migrator>
// (migration/roles.go), and IF NOT EXISTS makes the WHOLE statement a no-op
// against the built-in schema — the AUTHORIZATION clause included — so nspowner
// never moves and \dn+ public reads as it does on an untouched instance. Nor do
// the grant and role-name queries see it: pgPublicGrantDetectSQL skips public's
// own ACL, whose baseline PUBLIC USAGE entry is indistinguishable from the
// residue, and pg_roles is about role names. What such a run DOES leave are rows a
// fresh instance has for no role of yours, written by statements further down the
// same list:
//
//   - the two ALTER DEFAULT PRIVILEGES FOR ROLE <migrator> IN SCHEMA "public"
//     statements, as pg_default_acl rows whose defaclnamespace is the public
//     schema and whose defaclrole is the migrator;
//   - the two ALTER ROLE <role> SET search_path = "public" statements, as one
//     pg_db_role_setting row per role (setrole joins pg_roles.oid; a setrole of 0
//     is an ALTER DATABASE … SET row, which this template never emits and the
//     join drops).
//
// One spelling of the setting is enough: PostgreSQL normalises the stored value
// through flatten_set_variable_args -> quote_identifier, which drops quotes an
// identifier does not need, so the helper's ALTER ROLE … SET search_path =
// "public" and a hand-written bare one BOTH land in pg_db_role_setting as
// search_path=public.
//
// The search_path arm is SELECT DISTINCT because pg_db_role_setting carries one
// row per (setdatabase, setrole) pair: a role holding BOTH a cluster-wide
// ALTER ROLE … SET and an ALTER ROLE … IN DATABASE … SET contributes the
// identical (source, rolname, setting) triple twice, and the projection has no
// column that tells the two apart, so the duplicate is pure noise. DISTINCT
// scopes to this UNION ALL branch alone; the default_acl arm cannot duplicate,
// since pg_default_acl is keyed on (defaclrole, defaclnamespace, defaclobjtype).
//
// pg_db_role_setting is a SHARED catalog, so the arm sees the IN DATABASE rows of
// every database in the cluster while pg_default_acl, pg_namespace and pg_class
// are per-database. A role pointed at public in some OTHER database therefore
// reports here — an over-report, never an under-report.
const pgPublicSchemaResidueDetectSQL = `SELECT 'default_acl'::text AS source, r.rolname AS role_name,
       CASE d.defaclobjtype WHEN 'r' THEN 'TABLES'
                            WHEN 'S' THEN 'SEQUENCES'
                            WHEN 'f' THEN 'FUNCTIONS'
                            WHEN 'T' THEN 'TYPES'
                            ELSE d.defaclobjtype::text END AS detail
FROM pg_default_acl d
JOIN pg_namespace n ON n.oid = d.defaclnamespace
JOIN pg_roles r ON r.oid = d.defaclrole
WHERE n.nspname = 'public'
UNION ALL
SELECT DISTINCT 'search_path', r.rolname, c.setting
FROM pg_db_role_setting s
JOIN pg_roles r ON r.oid = s.setrole,
     unnest(s.setconfig) AS c(setting)
WHERE c.setting = 'search_path=public'
ORDER BY 1, 2, 3`

// Probe role names for the schema-residue oracle. Every assertion is scoped to
// them, so a container shared with another test (or another run) cannot make
// this test flaky.
const (
	schemaResidueProbeRole       = "c654_schema_probe"
	schemaResidueQuotedProbeRole = "c654_quoted_probe"
)

// TestPGPublicSchemaResidueDetectSQLFindsSchemaHalfResidue is the falsifiability
// check on the schema half's detect query. The pre-fix state cannot be
// provisioned any more — Validate refuses Schema: "public" outright — so the
// oracle reproduces the two residues by hand with the same statements
// buildPGRoleStatements would have emitted, asserts the query finds each one
// tagged with its source, and asserts that undoing both residues makes the query
// go quiet again. The undo here RESETs the search_path because the probe roles are
// then dropped; the atom's own apply step never resets — re-provisioning OVERWRITES
// the setting with the new schema — and the query cannot tell the two apart, since
// both stop the row matching 'search_path=public'.
func TestPGPublicSchemaResidueDetectSQLFindsSchemaHalfResidue(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)

	_, err := admin.ExecContext(ctx, `CREATE ROLE `+schemaResidueProbeRole)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, `CREATE ROLE `+schemaResidueQuotedProbeRole)
	require.NoError(t, err)

	// Cleaned up whatever happens. The revoke and the reset must precede the
	// drops: a role still owning a pg_default_acl row cannot be dropped.
	defer func() {
		for _, stmt := range []string{
			`ALTER DEFAULT PRIVILEGES FOR ROLE ` + schemaResidueProbeRole +
				` IN SCHEMA public REVOKE SELECT ON TABLES FROM ` + schemaResidueProbeRole,
			`ALTER ROLE ` + schemaResidueProbeRole + ` RESET search_path`,
			`ALTER ROLE ` + schemaResidueQuotedProbeRole + ` RESET search_path`,
			`DROP ROLE IF EXISTS ` + schemaResidueProbeRole,
			`DROP ROLE IF EXISTS ` + schemaResidueQuotedProbeRole,
		} {
			_, cleanupErr := admin.ExecContext(ctx, stmt)
			assert.NoError(t, cleanupErr, stmt)
		}
	}()

	require.Empty(t, publicSchemaResidueRows(ctx, t, admin, schemaResidueProbeRole),
		"a freshly created role carries neither residue")
	require.Empty(t, publicSchemaResidueRows(ctx, t, admin, schemaResidueQuotedProbeRole),
		"a freshly created role carries neither residue")

	t.Run("both_residues_are_found", func(t *testing.T) {
		// The two statement shapes a Schema spelled "public" would have left
		// behind (migration/roles.go), against the probe role instead.
		for _, stmt := range []string{
			`ALTER DEFAULT PRIVILEGES FOR ROLE ` + schemaResidueProbeRole +
				` IN SCHEMA public GRANT SELECT ON TABLES TO ` + schemaResidueProbeRole,
			`ALTER ROLE ` + schemaResidueProbeRole + ` SET search_path = public`,
			`ALTER ROLE ` + schemaResidueQuotedProbeRole + ` SET search_path = "public"`,
		} {
			_, execErr := admin.ExecContext(ctx, stmt)
			require.NoError(t, execErr, stmt)
		}

		require.Equal(t, []string{
			"default_acl|" + schemaResidueProbeRole + "|TABLES",
			"search_path|" + schemaResidueProbeRole + "|search_path=public",
		}, publicSchemaResidueRows(ctx, t, admin, schemaResidueProbeRole),
			"both residues must be found, each tagged with the catalog it came from")

		// The helper QUOTES the schema, so `search_path = "public"` is the
		// spelling a real provisioning run emits — and this is the arm that
		// pins what the SERVER does with it. flatten_set_variable_args renders
		// the value through quote_identifier, which drops quotes an identifier
		// does not need, so the quoted statement is stored BARE, exactly as the
		// hand-written one above is, and the single-spelling predicate in the
		// query detects it. Pinned exactly, not by prefix: a prefix assertion
		// passes under either spelling and so cannot tell the two apart, which
		// is how a dead second IN-list entry and a doc sentence claiming the
		// quoted spelling is stored both survived earlier review rounds.
		require.Equal(t, []string{
			"search_path|" + schemaResidueQuotedProbeRole + "|search_path=public",
		}, publicSchemaResidueRows(ctx, t, admin, schemaResidueQuotedProbeRole),
			`SET search_path = "public" must be stored — and detected — as the bare search_path=public`)
	})

	// Undo both residues. The default-privilege REVOKE is the atom's apply step
	// verbatim; the RESET stands in for what apply really does to the search_path,
	// which is to OVERWRITE it by re-provisioning against the renamed schema.
	for _, stmt := range []string{
		`ALTER DEFAULT PRIVILEGES FOR ROLE ` + schemaResidueProbeRole +
			` IN SCHEMA public REVOKE SELECT ON TABLES FROM ` + schemaResidueProbeRole,
		`ALTER ROLE ` + schemaResidueProbeRole + ` RESET search_path`,
		`ALTER ROLE ` + schemaResidueQuotedProbeRole + ` RESET search_path`,
	} {
		_, err = admin.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}

	require.Empty(t, publicSchemaResidueRows(ctx, t, admin, schemaResidueProbeRole),
		"the revoke and the search_path reset must make the query go quiet again")
	require.Empty(t, publicSchemaResidueRows(ctx, t, admin, schemaResidueQuotedProbeRole),
		"the revoke and the search_path reset must make the query go quiet again")
}

// schemaResidueDualScopeProbeRole carries BOTH a cluster-wide and an IN DATABASE
// search_path setting — the pair that makes pg_db_role_setting hold two rows for
// one role, and the case the search_path arm's DISTINCT exists for.
const schemaResidueDualScopeProbeRole = "c654_dual_scope_probe"

// TestPGPublicSchemaResidueDetectSQLDeduplicatesDualScopeSettings pins the
// DISTINCT on the schema-residue query's search_path arm. pg_db_role_setting keys
// one row per (setdatabase, setrole) pair, so a role pointed at public both
// cluster-wide (setdatabase = 0) and IN DATABASE <this one> contributes the same
// (source, rolname, setting) triple twice; nothing in the projection tells the two
// apart, so without DISTINCT the operator reads a duplicated finding.
//
// This is the A/B the single-setting test above cannot make: that one sets ONE
// scope, so it passes with or without the DISTINCT.
func TestPGPublicSchemaResidueDetectSQLDeduplicatesDualScopeSettings(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)

	var dbName string
	require.NoError(t, admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&dbName))
	quotedDB := quotePGIdent(dbName)

	_, err := admin.ExecContext(ctx, `CREATE ROLE `+schemaResidueDualScopeProbeRole)
	require.NoError(t, err)

	defer func() {
		for _, stmt := range []string{
			`ALTER ROLE ` + schemaResidueDualScopeProbeRole + ` IN DATABASE ` + quotedDB + ` RESET search_path`,
			`ALTER ROLE ` + schemaResidueDualScopeProbeRole + ` RESET search_path`,
			`DROP ROLE IF EXISTS ` + schemaResidueDualScopeProbeRole,
		} {
			_, cleanupErr := admin.ExecContext(ctx, stmt)
			assert.NoError(t, cleanupErr, stmt)
		}
	}()

	_, err = admin.ExecContext(ctx,
		`ALTER ROLE `+schemaResidueDualScopeProbeRole+` SET search_path = public`)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx,
		`ALTER ROLE `+schemaResidueDualScopeProbeRole+` IN DATABASE `+quotedDB+` SET search_path = public`)
	require.NoError(t, err)

	// Both scopes really are stored as separate rows — the premise of the dedup.
	var settingRows int
	require.NoError(t, admin.QueryRowContext(ctx,
		`SELECT count(*) FROM pg_db_role_setting s JOIN pg_roles r ON r.oid = s.setrole
		 WHERE r.rolname = $1`, schemaResidueDualScopeProbeRole).Scan(&settingRows))
	require.Equal(t, 2, settingRows,
		"the cluster-wide and IN DATABASE settings must be two pg_db_role_setting rows")

	require.Equal(t, []string{
		"search_path|" + schemaResidueDualScopeProbeRole + "|search_path=public",
	}, publicSchemaResidueRows(ctx, t, admin, schemaResidueDualScopeProbeRole),
		"two settings rows for one role must report as ONE finding, not two")
}

// publicSchemaResidueRows runs pgPublicSchemaResidueDetectSQL verbatim and
// flattens the rows for one role to "source|role|detail". The filter is applied
// in Go, never in the SQL, so the const under test stays byte-identical to the
// query the atom publishes.
func publicSchemaResidueRows(ctx context.Context, t *testing.T, db *sql.DB, role string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, pgPublicSchemaResidueDetectSQL)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var source, roleName, detail string
		require.NoError(t, rows.Scan(&source, &roleName, &detail))
		if roleName != role {
			continue
		}
		out = append(out, strings.Join([]string{source, roleName, detail}, "|"))
	}
	require.NoError(t, rows.Err())
	return out
}

// pgPublicNamedRoleGrantDetectSQL is the FOURTH detect step of the [C65.4]
// atom in wiki/migrations.md, kept here verbatim for the same reason as the
// other three: so the atom's copy has a live oracle and the two cannot drift
// apart silently.
//
// It closes a LIVE EXPOSURE the other three miss entirely. For a Schema spelled
// "public", buildPGRoleStatements (migration/roles.go) emits
// GRANT USAGE ON SCHEMA "public" TO <runtime>,
// GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "public" TO <runtime>
// and GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA "public" TO <runtime>.
// Those go to a REAL role, so grantee 0 never matches them: pgPublicGrantDetectSQL
// is blind to them, pgPublicSchemaResidueDetectSQL reads pg_default_acl and
// pg_db_role_setting only, pgReservedRoleDetectSQL is about role NAMES, and the
// atom's apply step revokes only FROM PUBLIC. An operator could therefore finish
// the remediation, verify, read clean — and the tenant's runtime role would still
// hold DML on every pre-existing table in the shared schema.
//
// The query reads every real role's ACL entries straight out of
// pg_namespace.nspacl for the public schema itself and pg_class.relacl for its
// objects — no relkind filter, because a sequence IS a pg_class row, exactly as in
// pgPublicGrantDetectSQL — and the join to pg_roles drops grantee 0 (PUBLIC),
// which that query already covers. It deliberately does NOT pick its roles by
// their search_path=public residue: the atom's step 4 re-provisions against the
// tenant's own schema, which overwrites that setting, and a query keyed on it
// would then read clean over a grant the revokes missed — at verify, exactly when
// it has to answer.
//
// The owner's own implicit ACL entry (grantee = nspowner / relowner) is excluded:
// that is ownership, not a grant, and it appears on an object once any grant is
// made. Every other row names a role holding a real grant on public, so rows for
// another application's roles are expected; corroborate each row against the spec.
const pgPublicNamedRoleGrantDetectSQL = `SELECT 'schema'::text AS source, r.rolname AS role_name,
       n.nspname AS object_name, a.privilege_type
FROM pg_namespace n, aclexplode(n.nspacl) a, pg_roles r
WHERE n.nspname = 'public'
  AND r.oid = a.grantee
  AND a.grantee <> n.nspowner
UNION ALL
SELECT 'relation', r.rolname, c.relname, a.privilege_type
FROM pg_class c, pg_namespace n, aclexplode(c.relacl) a, pg_roles r
WHERE n.oid = c.relnamespace
  AND n.nspname = 'public'
  AND r.oid = a.grantee
  AND a.grantee <> c.relowner
ORDER BY 1, 2, 3, 4`

// Probe identifiers for the named-role grant oracle. Every assertion is scoped to
// the role, so a container shared with another test (or another run) cannot make
// this test flaky; the table lives in the shared public schema and is therefore
// dropped on the way out whatever happens.
const (
	namedGrantProbeRole         = "c654_named_grant_probe"
	namedGrantProbeTable        = "c654_named_grant_probe_tbl"
	namedGrantProbeTenantSchema = "c654_named_grant_tenant"
)

// TestPGPublicNamedRoleGrantDetectSQLFindsNamedRoleGrants is the falsifiability
// check on the atom's fourth detect query. The pre-fix state cannot be
// provisioned any more — Validate refuses Schema: "public" outright — so the
// oracle reproduces the exposure by hand with the same statement shapes
// buildPGRoleStatements would have emitted against a real role.
//
// The repoint assertion is the one that matters for verify: the atom's step 4
// re-provisions against the tenant's own schema, which OVERWRITES search_path, so
// a query that picked its roles by search_path would read clean over a grant the
// revokes missed. The grants must be reported whatever search_path says.
func TestPGPublicNamedRoleGrantDetectSQLFindsNamedRoleGrants(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)

	_, err := admin.ExecContext(ctx, `CREATE ROLE `+namedGrantProbeRole)
	require.NoError(t, err)
	_, err = admin.ExecContext(ctx, `CREATE TABLE public.`+namedGrantProbeTable+` (id INT PRIMARY KEY)`)
	require.NoError(t, err)

	// Cleaned up whatever happens, and in this order: a role still holding a
	// privilege on an object cannot be dropped, and the table lives in the
	// shared public schema where a leftover would poison every later run.
	defer func() {
		for _, stmt := range []string{
			`REVOKE ALL ON public.` + namedGrantProbeTable + ` FROM ` + namedGrantProbeRole,
			`REVOKE ALL ON SCHEMA public FROM ` + namedGrantProbeRole,
			`ALTER ROLE ` + namedGrantProbeRole + ` RESET search_path`,
			`DROP TABLE IF EXISTS public.` + namedGrantProbeTable,
			`DROP ROLE IF EXISTS ` + namedGrantProbeRole,
		} {
			_, cleanupErr := admin.ExecContext(ctx, stmt)
			assert.NoError(t, cleanupErr, stmt)
		}
	}()

	require.Empty(t, publicNamedRoleGrantRows(ctx, t, admin, ""),
		"an untouched instance reports nothing: the public schema owner's own ACL entry is ownership, not a grant")

	t.Run("named_role_grants_are_found", func(t *testing.T) {
		// The statement shapes a Schema spelled "public" leaves on a real role
		// (migration/roles.go), against the probe role instead.
		for _, stmt := range []string{
			`GRANT USAGE ON SCHEMA public TO ` + namedGrantProbeRole,
			`GRANT SELECT ON public.` + namedGrantProbeTable + ` TO ` + namedGrantProbeRole,
		} {
			_, execErr := admin.ExecContext(ctx, stmt)
			require.NoError(t, execErr, stmt)
		}

		want := []string{
			"relation|" + namedGrantProbeRole + "|" + namedGrantProbeTable + "|SELECT",
			"schema|" + namedGrantProbeRole + "|public|USAGE",
		}
		require.Equal(t, want, publicNamedRoleGrantRows(ctx, t, admin, namedGrantProbeRole),
			"both grants must be found whatever the role's search_path, each tagged with its catalog, in ORDER BY order")

		for _, stmt := range []string{
			`ALTER ROLE ` + namedGrantProbeRole + ` SET search_path = public`,
			`ALTER ROLE ` + namedGrantProbeRole + ` SET search_path = ` + namedGrantProbeTenantSchema,
		} {
			_, execErr := admin.ExecContext(ctx, stmt)
			require.NoError(t, execErr, stmt)
		}
		require.Equal(t, want, publicNamedRoleGrantRows(ctx, t, admin, namedGrantProbeRole),
			"re-provisioning overwrites search_path; a grant the revokes missed must still be reported afterwards")

		require.Empty(t, publicNamedRoleGrantRows(ctx, t, admin, env.adminUser),
			"the table owner's own ACL entry is ownership, not a grant, and is not reported")

		for _, stmt := range []string{
			`REVOKE ALL ON public.` + namedGrantProbeTable + ` FROM ` + namedGrantProbeRole,
			`REVOKE ALL ON SCHEMA public FROM ` + namedGrantProbeRole,
		} {
			_, revokeErr := admin.ExecContext(ctx, stmt)
			require.NoError(t, revokeErr, stmt)
		}

		require.Empty(t, publicNamedRoleGrantRows(ctx, t, admin, namedGrantProbeRole),
			"the apply step's named-role revokes must make the query go quiet again")
	})
}

// publicNamedRoleGrantRows runs pgPublicNamedRoleGrantDetectSQL verbatim and
// flattens the rows for one role — every role when role is empty — to
// "source|role|object|privilege". The filter is applied in Go, never in the SQL,
// so the const under test stays byte-identical to the query the atom publishes.
func publicNamedRoleGrantRows(ctx context.Context, t *testing.T, db *sql.DB, role string) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, pgPublicNamedRoleGrantDetectSQL)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var source, roleName, object, privilege string
		require.NoError(t, rows.Scan(&source, &roleName, &object, &privilege))
		if role != "" && roleName != role {
			continue
		}
		out = append(out, strings.Join([]string{source, roleName, object, privilege}, "|"))
	}
	require.NoError(t, rows.Err())
	return out
}

// pgReservedRoleDetectSQL is the SECOND detect step of the [C65.4] atom in
// wiki/migrations.md, kept here verbatim for the same reason as the query
// above: so the atom's copy has a live oracle and the two cannot drift apart
// silently.
//
// PostgreSQL's own reserved-name check is exact-case, so it refuses CREATE ROLE
// "public" and CREATE ROLE "pg_x" but accepts "Public" and "PG_x" as ordinary
// roles with ordinary grants. Those roles are real, grantee 0 never matches
// them, and pgPublicGrantDetectSQL therefore reports nothing at all for them —
// yet C65.4 matches case-insensitively, so those deployments do hit the gate.
// This query is how an operator finds them.
//
// LIKE 'pg^_%' ESCAPE '^' names its own escape character, so the underscore is
// a LITERAL underscore regardless of session settings. The backslash form the
// query used to carry (LIKE 'pg\_%', escape implied) is correct only while
// standard_conforming_strings = on, because with it off the string parser eats
// the backslash and _ degrades to a single-character wildcard — matching pgx…
// too. The explicit ESCAPE removes that dependency; the near-miss role in the
// test below is what falsifies it either way. The lower() governs BOTH arms on
// purpose: the server accepts "PG_x" as an ordinary role, so the LIKE arm has
// to fold case as well to see it.
//
// Unlike the PUBLIC-grant query, a row here is not by itself a finding: the
// pg_-prefixed predefined roles ship with every instance and are baseline
// noise. A row is a match only when the name is one your own spec provisions.
const pgReservedRoleDetectSQL = `SELECT rolname FROM pg_roles
WHERE lower(rolname) = 'public' OR lower(rolname) LIKE 'pg^_%' ESCAPE '^'`

// pgPredefinedRoleBaseline lists PostgreSQL's own pg_-prefixed predefined roles
// that pgReservedRoleDetectSQL necessarily returns on every instance. It is
// asserted as a SUBSET rather than an exact set — the same shape as the
// public-schema USAGE row the grant oracle pins — so a PostgreSQL release that
// REMOVES or renames one of these is noticed here instead of being silently
// absorbed, while one that adds a new predefined role does not red the suite.
// Every name below has existed since PostgreSQL 14; the container runs the
// renovate-pinned tag in testing/containers/postgresql.go.
var pgPredefinedRoleBaseline = []string{
	"pg_database_owner",
	"pg_execute_server_program",
	"pg_monitor",
	"pg_read_all_data",
	"pg_read_all_settings",
	"pg_read_all_stats",
	"pg_read_server_files",
	"pg_signal_backend",
	"pg_stat_scan_tables",
	"pg_write_all_data",
	"pg_write_server_files",
}

// TestPGReservedRoleDetectSQLFindsCaseVariantRoles is the falsifiability check
// on the atom's second detect query. A case variant of a reserved name is a
// role PostgreSQL genuinely created, so the assertions are scoped: the probe
// roles must APPEAR once they exist and disappear once they are dropped, the
// predefined baseline must be present throughout, and the near-miss role
// pgx_probe must never appear — if it does, the LIKE escape is broken and the
// query over-reports exactly as the atom's caveat sentence warns.
//
// There are two probe roles because the query has two arms and each needs its
// own witness: "Public" pins lower(rolname) = 'public', and "PG_Probe" pins the
// lower() on the LIKE arm — drop that one call and the arm becomes
// rolname LIKE 'pg^_%' ESCAPE '^', which still finds every lowercase
// predefined role and still finds "Public", so nothing but "PG_Probe" would
// notice.
func TestPGReservedRoleDetectSQLFindsCaseVariantRoles(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)

	before := reservedRoleNames(ctx, t, admin)
	require.Subset(t, before, pgPredefinedRoleBaseline,
		"the pg_-prefixed predefined roles are expected baseline noise, not a finding")
	require.NotContains(t, before, "Public",
		"the probe role must not already exist on the container")
	require.NotContains(t, before, "PG_Probe",
		"the pg_-prefixed probe role must not already exist on the container")
	require.NotContains(t, before, "pgx_probe",
		"the near-miss role must not already exist on the container")

	t.Run("case_variant_role_is_found", func(t *testing.T) {
		// PostgreSQL accepts all three: its reserved check is exact-case, so
		// "Public" and "PG_Probe" are real roles, and "pgx_probe" is not
		// reserved-shaped at all — it only LOOKS like one if the LIKE escape
		// is broken.
		_, err := admin.ExecContext(ctx, `CREATE ROLE "Public"`)
		require.NoError(t, err)
		_, err = admin.ExecContext(ctx, `CREATE ROLE "PG_Probe"`)
		require.NoError(t, err)
		_, err = admin.ExecContext(ctx, `CREATE ROLE pgx_probe`)
		require.NoError(t, err)

		// Dropped on the way out whatever happens, so a failure here cannot
		// poison a later run sharing the same container. assert, not require: a
		// require on the first drop would Goexit past the other two.
		t.Cleanup(func() {
			for _, stmt := range []string{
				`DROP ROLE IF EXISTS "Public"`,
				`DROP ROLE IF EXISTS "PG_Probe"`,
				`DROP ROLE IF EXISTS pgx_probe`,
			} {
				_, dropErr := admin.ExecContext(ctx, stmt)
				assert.NoError(t, dropErr, stmt)
			}
		})

		got := reservedRoleNames(ctx, t, admin)
		require.Contains(t, got, "Public",
			"a case variant the server accepted must be found by the detect query")
		require.Contains(t, got, "PG_Probe",
			`the LIKE arm must fold case too; without lower(), 'pg^_%' misses PG_Probe entirely`)
		require.Subset(t, got, pgPredefinedRoleBaseline,
			"the predefined roles stay in the result set alongside the finding")
		require.NotContains(t, got, "pgx_probe",
			`LIKE 'pg^_%' ESCAPE '^' must match a LITERAL underscore; a pgx_probe hit means the escape is broken`)
	})

	after := reservedRoleNames(ctx, t, admin)
	require.NotContains(t, after, "Public",
		"the atom's apply step (rename or drop) must make the query go quiet again")
	require.NotContains(t, after, "PG_Probe",
		"the atom's apply step (rename or drop) must make the query go quiet again")
	require.Subset(t, after, pgPredefinedRoleBaseline,
		"the baseline is unaffected by the probe role's lifecycle")
}

// reservedRoleNames runs pgReservedRoleDetectSQL verbatim and returns the
// rolname column. No filtering happens in the SQL, so the const under test
// stays byte-identical to the query the atom publishes.
func reservedRoleNames(ctx context.Context, t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, pgReservedRoleDetectSQL)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var rolname string
		require.NoError(t, rows.Scan(&rolname))
		out = append(out, rolname)
	}
	require.NoError(t, rows.Err())
	return out
}

// Probe identifiers for the PUBLIC-pseudo-role premise oracle below. The schema
// is probe-specific and every assertion filters on it, so a container shared
// with another test (or another run) cannot make the grantee-0 read flaky.
const (
	publicPremiseProbeSchema = "c654_public_premise_probe"
	publicPremiseProbeRole   = "Public"
)

// TestPGQuotedPublicIsPseudoRoleAndMixedCaseIsCreatable pins the three server
// facts the C65.4 role half rests on. Every query in this file, the reserved
// role detect and the whole "a reserved role name never became a role, so look
// for the grants it produced instead" branch of the atom are downstream of
// them, and a code review has already asserted the opposite of the first two —
// so they are executed here rather than reasoned about:
//
//  1. CREATE ROLE "public" is REFUSED. The quoting does not help: PostgreSQL's
//     reserved-name check runs on the already-parsed identifier, so the server
//     answers SQLSTATE 42939 (reserved_name), "role name \"public\" is reserved".
//     That is why no real role by that name can exist to hold grants, and why
//     the PUBLIC-grant detect hunts grantee 0 instead of a rolname.
//  2. CREATE ROLE "Public" SUCCEEDS. The same check is exact-case, so a mixed-
//     case variant is an ordinary role with an ordinary oid — invisible to the
//     grantee-0 query, which is the entire reason pgReservedRoleDetectSQL
//     exists as a separate detect step.
//  3. GRANT … TO "public" lands on grantee oid 0 — the PUBLIC pseudo-role —
//     even while a real role named "Public" exists on the same instance. The
//     quoted spelling is resolved by RoleSpec, not by identifier lookup, so
//     PGRoleSpec.Validate refusing the name is the only thing between a
//     RuntimeRole: "public" and a grant to every role on the instance.
//
// The SQLSTATE is read through an anonymous interface{ SQLState() string }
// assertion rather than by importing pgconn: pgx's *pgconn.PgError satisfies
// it, and the test keeps the driver out of its import graph. The message text
// is asserted alongside it so a driver that ever stops exposing SQLState
// fails loudly instead of silently weakening the pin.
func TestPGQuotedPublicIsPseudoRoleAndMixedCaseIsCreatable(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)

	t.Run("exact_public_is_reserved_and_cannot_become_a_role", func(t *testing.T) {
		_, err := admin.ExecContext(ctx, `CREATE ROLE "public"`)
		require.Error(t, err,
			`CREATE ROLE "public" must be refused; if it ever succeeds, a real role can hold the grants the atom looks for at grantee 0`)

		var coded interface{ SQLState() string }
		require.ErrorAs(t, err, &coded,
			"the pgx driver must expose the server's SQLSTATE on this error")
		require.Equal(t, "42939", coded.SQLState(),
			"reserved_name is the class the atom's provisioning abort path keys on")
		require.Contains(t, err.Error(), "is reserved",
			`the server's own wording, pinned alongside the SQLSTATE`)
	})

	t.Run("mixed_case_public_is_an_ordinary_role", func(t *testing.T) {
		_, err := admin.ExecContext(ctx, `CREATE ROLE "`+publicPremiseProbeRole+`"`)
		require.NoError(t, err,
			`the reserved check is exact-case, so "Public" must be created as an ordinary role`)
		defer func() {
			_, dropErr := admin.ExecContext(ctx, `DROP ROLE IF EXISTS "`+publicPremiseProbeRole+`"`)
			assert.NoError(t, dropErr)
		}()

		var oid int64
		require.NoError(t, admin.QueryRowContext(ctx,
			`SELECT oid::bigint FROM pg_roles WHERE rolname = $1`, publicPremiseProbeRole).Scan(&oid),
			`"Public" must exist in pg_roles under that exact spelling`)
		require.NotZero(t, oid,
			"a real role carries a real oid; 0 is the PUBLIC pseudo-role and never a catalog row")

		t.Run("quoted_public_grant_lands_on_grantee_zero", func(t *testing.T) {
			// Deliberately run while the real "Public" role exists: the point
			// is that the quoted spelling still resolves to the pseudo-role.
			_, schemaErr := admin.ExecContext(ctx, `CREATE SCHEMA `+publicPremiseProbeSchema)
			require.NoError(t, schemaErr)
			defer func() {
				_, dropErr := admin.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+publicPremiseProbeSchema+` CASCADE`)
				assert.NoError(t, dropErr)
			}()

			_, grantErr := admin.ExecContext(ctx,
				`GRANT USAGE ON SCHEMA `+publicPremiseProbeSchema+` TO "public"`)
			require.NoError(t, grantErr)

			require.Contains(t, probeSchemaGrantees(ctx, t, admin), "0|USAGE",
				`GRANT … TO "public" must land on grantee 0, the PUBLIC pseudo-role`)
			require.NotContains(t, probeSchemaGrantees(ctx, t, admin), fmt.Sprintf("%d|USAGE", oid),
				`the grant must NOT have gone to the real "Public" role`)
		})
	})
}

// probeSchemaGrantees reads publicPremiseProbeSchema's ACL as "grantee|privilege"
// strings. Scoped to the probe schema by name, so nothing another test does to
// the shared container can move these rows.
func probeSchemaGrantees(ctx context.Context, t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx,
		`SELECT a.grantee::bigint, a.privilege_type
FROM pg_namespace n, aclexplode(n.nspacl) a
WHERE n.nspname = $1`, publicPremiseProbeSchema)
	require.NoError(t, err)
	defer rows.Close()

	var out []string
	for rows.Next() {
		var grantee int64
		var privilege string
		require.NoError(t, rows.Scan(&grantee, &privilege))
		out = append(out, fmt.Sprintf("%d|%s", grantee, privilege))
	}
	require.NoError(t, rows.Err())
	return out
}

// sourceColumn projects the source field out of publicGrantRows' flattened rows,
// preserving order, so a test can assert the query's ORDER BY without depending
// on the server's collation for the schema and object columns.
func sourceColumn(rows []string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, strings.SplitN(r, "|", 2)[0])
	}
	return out
}

// TestPGRolesCreateroleProvisionerMintsTheMigrator provisions as a CREATEROLE-only
// provisioner that mints the migrator, then reports floor drift with CheckPGRoleFloor.
func TestPGRolesCreateroleProvisionerMintsTheMigrator(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	provDB := createroleProvisioner(ctx, t, env, "prov_inband", "set, inherit")
	spec := &PGRoleSpec{
		Schema:            "tenant_inband",
		MigratorRole:      "mig_inband",
		MigratorPassword:  testconsts.FakePassword("mig-inband"),
		RuntimeRole:       "rt_inband",
		RuntimePassword:   testconsts.FakePassword("rt-inband"),
		SkipFloorReassert: true,
	}

	require.NoError(t, ProvisionPGRoles(ctx, provDB, spec), "first run")
	require.NoError(t, ProvisionPGRoles(ctx, provDB, spec), "a rerun must converge")

	requireRuntimeRoleSeparation(ctx, t,
		env.openAsRole(t, spec.MigratorRole, spec.MigratorPassword),
		env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword),
		spec.Schema)

	for _, role := range []string{spec.MigratorRole, spec.RuntimeRole} {
		require.NoError(t, CheckPGRoleFloor(ctx, provDB, role), "CREATE ROLE must set the floor on %s", role)
	}
	_, err := admin.ExecContext(ctx, `ALTER ROLE `+quotePGIdent(spec.RuntimeRole)+` CREATEDB`)
	require.NoError(t, err)
	err = CheckPGRoleFloor(ctx, provDB, spec.RuntimeRole)
	require.ErrorIs(t, err, ErrPGRoleFloorViolated)
	assert.Contains(t, err.Error(), "holds CREATEDB")
}

// TestPGRolesCreateroleProvisionerLeavesASharedMigratorUntouched provisions three
// tenants against one out-of-band migrator and reads it back unchanged.
func TestPGRolesCreateroleProvisionerLeavesASharedMigratorUntouched(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	const provisioner, migrator = "prov_shared", "mig_shared"
	// No createrole_self_grant: the DBA grant below is all this path needs.
	provDB := createroleProvisioner(ctx, t, env, provisioner, "")
	migratorPassword := testconsts.FakePassword("mig-shared")
	for i, stmt := range []string{
		// CREATEDB and a role setting stand in for what a DBA grants a migrator
		// on purpose; the default template would strip the first and repoint
		// the migrator's search_path.
		fmt.Sprintf(`CREATE ROLE %s LOGIN CREATEDB PASSWORD %s`,
			quotePGIdent(migrator), quotePGStringLiteral(migratorPassword)),
		fmt.Sprintf(`ALTER ROLE %s SET statement_timeout = '5min'`, quotePGIdent(migrator)),
		fmt.Sprintf(`GRANT %s TO %s WITH INHERIT TRUE, SET TRUE`, quotePGIdent(migrator), quotePGIdent(provisioner)),
	} {
		_, err := admin.ExecContext(ctx, stmt)
		require.NoError(t, err, "out-of-band setup statement %d", i)
	}
	before := pgRoleSnapshot(ctx, t, admin, migrator)
	require.Contains(t, before, "statement_timeout=5min", "premise: the snapshot must read the DBA's role setting")

	migratorDB := env.openAsRole(t, migrator, migratorPassword)
	for _, tt := range []struct {
		tenant string
		db     *sql.DB
	}{
		{tenant: "a", db: provDB},
		{tenant: "b", db: provDB},
		// The superuser may rewrite the migrator, so for this tenant the snapshot
		// comparison, not a server refusal, is what catches a regression.
		{tenant: "c", db: admin},
	} {
		spec := &PGRoleSpec{
			Schema:            "tenant_shared_" + tt.tenant,
			MigratorRole:      migrator,
			RuntimeRole:       "rt_shared_" + tt.tenant,
			RuntimePassword:   testconsts.FakePassword("rt-shared-" + tt.tenant),
			SkipMigratorRole:  true,
			SkipFloorReassert: true,
		}
		require.NoError(t, ProvisionPGRoles(ctx, tt.db, spec), "tenant %s", tt.tenant)
		requireRuntimeRoleSeparation(ctx, t, migratorDB,
			env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword), spec.Schema)
	}

	assert.Equal(t, before, pgRoleSnapshot(ctx, t, admin, migrator))
}

// TestPGRolesCreateroleProvisionerLimits pins, by SQLSTATE and failing step,
// each privilege a CREATEROLE-only provisioner is refused without.
func TestPGRolesCreateroleProvisionerLimits(t *testing.T) {
	env := newIntegrationEnv(t)
	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	adminExec := func(t *testing.T, stmt string) {
		t.Helper()
		_, err := admin.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	outOfBandMigrator := func(grant string) func(t *testing.T, prov, mig, rt string) {
		return func(t *testing.T, prov, mig, _ string) {
			adminExec(t, `CREATE ROLE `+quotePGIdent(mig)+` LOGIN`)
			if grant != "" {
				adminExec(t, `GRANT `+quotePGIdent(mig)+` TO `+quotePGIdent(prov)+` `+grant)
			}
		}
	}
	skipBoth := func(s *PGRoleSpec) {
		s.SkipMigratorRole = true
		s.SkipFloorReassert = true
	}
	skipReassert := func(s *PGRoleSpec) { s.SkipFloorReassert = true }

	tests := []struct {
		name      string
		selfGrant string
		setup     func(t *testing.T, prov, mig, rt string)
		options   func(*PGRoleSpec)
		// wantStep is a format taking the case's role/schema suffix.
		wantStep string
	}{
		{
			name:      "default_spec_is_refused_at_the_floor_reassert",
			selfGrant: "set, inherit",
			options:   func(*PGRoleSpec) {},
			wantStep:  `provisioning step 1 (ALTER ROLE "mig_%s" NOSUPERUSER`,
		},
		{
			name:     "minted_migrator_without_self_grant_is_refused_at_create_schema",
			options:  skipReassert,
			wantStep: `provisioning step 2 (CREATE SCHEMA IF NOT EXISTS "tenant_%s"`,
		},
		{
			name:      "self_grant_without_inherit_is_refused_at_the_schema_grant",
			selfGrant: "set",
			options:   skipReassert,
			wantStep:  `provisioning step 3 (GRANT USAGE ON SCHEMA "tenant_%s"`,
		},
		{
			name:      "self_grant_without_set_is_refused_at_create_schema",
			selfGrant: "inherit",
			options:   skipReassert,
			wantStep:  `provisioning step 2 (CREATE SCHEMA IF NOT EXISTS "tenant_%s"`,
		},
		{
			name:     "out_of_band_migrator_without_grant_is_refused_at_create_schema",
			setup:    outOfBandMigrator(""),
			options:  skipBoth,
			wantStep: `provisioning step 1 (CREATE SCHEMA IF NOT EXISTS "tenant_%s"`,
		},
		{
			name:     "out_of_band_grant_without_inherit_is_refused_at_the_schema_grant",
			setup:    outOfBandMigrator("WITH SET TRUE, INHERIT FALSE"),
			options:  skipBoth,
			wantStep: `provisioning step 2 (GRANT USAGE ON SCHEMA "tenant_%s"`,
		},
		{
			name:     "out_of_band_grant_without_set_is_refused_at_create_schema",
			setup:    outOfBandMigrator("WITH INHERIT TRUE, SET FALSE"),
			options:  skipBoth,
			wantStep: `provisioning step 1 (CREATE SCHEMA IF NOT EXISTS "tenant_%s"`,
		},
		{
			name:      "runtime_role_created_by_another_role_is_refused_at_its_search_path",
			selfGrant: "set, inherit",
			setup: func(t *testing.T, _, _, rt string) {
				adminExec(t, `CREATE ROLE `+quotePGIdent(rt)+` LOGIN`)
			},
			options:  skipReassert,
			wantStep: `provisioning step 9 (ALTER ROLE "rt_%s" SET search_path`,
		},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			suffix := fmt.Sprintf("lim%d", i)
			prov, mig, rt := "prov_"+suffix, "mig_"+suffix, "rt_"+suffix
			provDB := createroleProvisioner(ctx, t, env, prov, tt.selfGrant)
			if tt.setup != nil {
				tt.setup(t, prov, mig, rt)
			}
			spec := &PGRoleSpec{Schema: "tenant_" + suffix, MigratorRole: mig, RuntimeRole: rt}
			tt.options(spec)

			err := ProvisionPGRoles(ctx, provDB, spec)
			requireSQLState(t, err, "42501")
			assert.Contains(t, err.Error(), fmt.Sprintf(tt.wantStep, suffix))
		})
	}
}

// pgRoleSnapshot renders every pg_roles column a provisioning call could change
// on role, plus a digest of its stored password, as one comparable string.
func pgRoleSnapshot(ctx context.Context, t *testing.T, db *sql.DB, role string) string {
	t.Helper()
	var snapshot string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT row(r.rolsuper, r.rolinherit, r.rolcreaterole, r.rolcreatedb, r.rolcanlogin,
		            r.rolreplication, r.rolbypassrls, r.rolconnlimit, r.rolvaliduntil, r.rolconfig,
		            md5(coalesce(a.rolpassword, '')))::text
		 FROM pg_catalog.pg_roles r JOIN pg_catalog.pg_authid a ON a.oid = r.oid
		 WHERE r.rolname = $1`, role).Scan(&snapshot))
	return snapshot
}

// createroleProvisioner creates a LOGIN CREATEROLE NOSUPERUSER role with CREATE
// on the database, stores selfGrant as its createrole_self_grant when non-empty,
// and connects as it.
func createroleProvisioner(ctx context.Context, t *testing.T, env *integrationEnv, name, selfGrant string) *sql.DB {
	t.Helper()
	admin := env.adminDB(t)
	password := testconsts.FakePassword(name)
	stmts := []string{
		fmt.Sprintf(`CREATE ROLE %s LOGIN CREATEROLE NOSUPERUSER PASSWORD %s`,
			quotePGIdent(name), quotePGStringLiteral(password)),
		fmt.Sprintf(`GRANT CREATE ON DATABASE %s TO %s`, quotePGIdent(env.defaultDB), quotePGIdent(name)),
	}
	if selfGrant != "" {
		stmts = append(stmts, fmt.Sprintf(`ALTER ROLE %s SET createrole_self_grant = %s`,
			quotePGIdent(name), quotePGStringLiteral(selfGrant)))
	}
	for i, stmt := range stmts {
		_, err := admin.ExecContext(ctx, stmt)
		require.NoError(t, err, "provisioner setup statement %d", i)
	}
	return env.openAsRole(t, name, password)
}

// requireSQLState asserts err carries the server's SQLSTATE code.
func requireSQLState(t *testing.T, err error, code string) {
	t.Helper()
	var coded interface{ SQLState() string }
	require.ErrorAs(t, err, &coded, "the pgx driver must expose the server's SQLSTATE on this error")
	require.Equal(t, code, coded.SQLState())
}

// requireRuntimeRoleSeparation runs the role-separation acceptance checks
// against a provisioned tenant: the migrator creates a table after
// provisioning, the runtime role reaches it through the default privileges,
// and the runtime role is refused DDL in the schema.
func requireRuntimeRoleSeparation(ctx context.Context, t *testing.T, migratorDB, runtimeDB *sql.DB, schema string) {
	t.Helper()
	qualified := quotePGIdent(schema) + ".gadgets"
	_, err := migratorDB.ExecContext(ctx, `CREATE TABLE `+qualified+` (id INT PRIMARY KEY, qty INT NOT NULL)`)
	require.NoError(t, err, "the migrator must create tables in the schema it owns")

	_, err = runtimeDB.ExecContext(ctx, `INSERT INTO `+qualified+` (id, qty) VALUES (1, 5)`)
	require.NoError(t, err, "default privileges must grant INSERT")
	_, err = runtimeDB.ExecContext(ctx, `UPDATE `+qualified+` SET qty = qty + 1 WHERE id = 1`)
	require.NoError(t, err, "default privileges must grant UPDATE")
	var qty int
	require.NoError(t, runtimeDB.QueryRowContext(ctx, `SELECT qty FROM `+qualified+` WHERE id = 1`).Scan(&qty),
		"default privileges must grant SELECT")
	require.Equal(t, 6, qty)
	_, err = runtimeDB.ExecContext(ctx, `DELETE FROM `+qualified+` WHERE id = 1`)
	require.NoError(t, err, "default privileges must grant DELETE")

	for _, stmt := range []string{
		`CREATE TABLE ` + quotePGIdent(schema) + `.unauthorized (id INT)`,
		`ALTER TABLE ` + qualified + ` ADD COLUMN sneaky TEXT`,
		`DROP TABLE ` + qualified,
	} {
		_, err = runtimeDB.ExecContext(ctx, stmt)
		require.Error(t, err, "the runtime role must be refused DDL: %s", stmt)
		assert.True(t, isPermissionDenied(err), "want permission denied for %s, got: %v", stmt, err)
	}
}
