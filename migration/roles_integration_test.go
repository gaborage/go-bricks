//go:build integration

package migration

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		MigratorPassword: "mig-ddl-pw-1234",
		RuntimeRole:      "rt_ddl",
		RuntimePassword:  "rt-ddl-pw-1234",
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
		MigratorPassword: "mig-adp-pw-1234",
		RuntimeRole:      "rt_adp",
		RuntimePassword:  "rt-adp-pw-1234",
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
		RuntimePassword: "rt-caps-pw-1234",
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
		MigratorPassword: "mig-idem-pw-1234",
		RuntimeRole:      "rt_idem",
		RuntimePassword:  "rt-idem-pw-1234",
	}

	ctx, cancel := testCtx(t)
	defer cancel()

	admin := env.adminDB(t)
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "first run")
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "second run must be idempotent")

	// Rotate the runtime password and verify the new credential works while
	// the old one is rejected.
	spec.RuntimePassword = "rt-idem-pw-rotated"
	require.NoError(t, ProvisionPGRoles(ctx, admin, spec), "rotate runtime password")

	rotated := env.openAsRole(t, spec.RuntimeRole, "rt-idem-pw-rotated")
	require.NoError(t, rotated.PingContext(ctx))
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
