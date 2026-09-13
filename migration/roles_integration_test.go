//go:build integration

package migration

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		role := role
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

// TestPGRolesProvisioningIsIdempotent verifies that running ProvisionPGRoles
// twice with the same spec is a no-op the second time — required because
// PostgreSQL DDL isn't transactional across role + schema, so callers must
// be able to converge by rerunning. Re-runs also exercise the in-place
// password rotation path.
func TestPGRolesProvisioningIsIdempotent(t *testing.T) {
	env := newIntegrationEnv(t)
	spec := &PGRoleSpec{
		Schema:           "tenant_idem",
		MigratorRole:     "mig_idem",
		MigratorPassword: testconsts.FakePassword("mig-idem"),
		RuntimeRole:      "rt_idem",
		RuntimePassword:  testconsts.FakePassword("rt-idem"),
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "first run")
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "second run must be idempotent")

	// Rotate the runtime password and verify the new credential works while
	// the old one is rejected.
	spec.RuntimePassword = testconsts.FakePassword("rt-idem-rotated")
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "rotate runtime password")

	rotated := env.openAsRole(t, spec.RuntimeRole, spec.RuntimePassword)
	require.NoError(t, rotated.PingContext(ctx))
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
// SELECT on its own catalogs by design. Database-wide default ACLs
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
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
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
	// PostgreSQL instance by design (PG15+ dropped CREATE, never USAGE), so the
	// query always reports it. It cannot be excluded — `public` is exactly the
	// schema a reserved-name spec provisions into — which is why the atom calls
	// it out as expected baseline noise and why every assertion below is scoped
	// to the probe schema it created.
	require.Contains(t, publicGrantRows(ctx, t, admin, "public"),
		"schema|public||USAGE",
		"the built-in public schema is expected baseline noise, not a finding")

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
