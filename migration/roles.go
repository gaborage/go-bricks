package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// pgPasswordLiteralPattern matches the `PASSWORD 'literal'` clause emitted by
// buildRoleCreateAndLockdown / buildPGRoleStatements. Used by summarizeStmt
// to redact the literal before the SQL fragment is wrapped into any error
// message — keeps the resolved secret out of error logs and stack traces
// when ExecContext fails on a password-bearing statement.
//
// Matches a single-quoted PostgreSQL string literal: an opening single quote,
// any number of non-quote characters or escaped doubled-quote pairs, then a
// closing single quote. Case-insensitive on the PASSWORD keyword for safety.
// The keyword + whitespace prefix is captured in group 1 so the substitution
// preserves the original casing and spacing.
var pgPasswordLiteralPattern = regexp.MustCompile(`(?i)(PASSWORD\s+)'(?:[^']|'')*'`)

// PGRoleSpec describes a PostgreSQL role-pair plus per-tenant schema for the
// migrator-vs-runtime role-separation model defined in issue #378.
//
// Migrator role: owns the per-tenant schema, holds DDL privileges, used
// exclusively by the migration runner. Created with NOSUPERUSER NOCREATEDB
// NOCREATEROLE NOBYPASSRLS NOREPLICATION so even a compromised migrator
// credential cannot escalate itself.
//
// Runtime role: per-tenant LOGIN role granted only DML on the tenant schema.
// Does not own the schema, so PostgreSQL's default ownership model rejects
// ALTER/CREATE/DROP statements from this role without any explicit REVOKE.
// Granted SELECT/INSERT/UPDATE/DELETE on existing AND future tables via
// ALTER DEFAULT PRIVILEGES so subsequent migrations don't need per-script grants.
type PGRoleSpec struct {
	// Schema is the per-tenant schema name (e.g. "tenant_a"). Owned by
	// MigratorRole after provisioning.
	Schema string

	// MigratorRole owns Schema and is used exclusively by the migration runner.
	// Must differ from RuntimeRole.
	MigratorRole string

	// MigratorPassword is optionally assigned to MigratorRole via ALTER ROLE
	// PASSWORD on every call. Useful for the one-time bootstrap and for
	// secret rotation. Leave empty when credentials are managed externally
	// (e.g., the role is created out-of-band and password set via a
	// privileged migration pipeline).
	MigratorPassword string

	// RuntimeRole is the per-tenant DML-only role consumed by the running
	// service. Must differ from MigratorRole.
	RuntimeRole string

	// RuntimePassword is optionally assigned to RuntimeRole. Same semantics
	// as MigratorPassword — passing it on every call makes secret rotation a
	// no-op rerun.
	RuntimePassword string
}

// ErrInvalidPGIdentifier is returned by Validate when a role or schema name
// fails the safe-identifier check enforced by ProvisionPGRoles.
var ErrInvalidPGIdentifier = errors.New("migration: PostgreSQL identifier rejected")

// Field name constants used in Validate error messages. Exported via
// ErrInvalidPGIdentifier so callers (including tests) can assert which
// field failed without coupling to the literal string.
const (
	pgRoleFieldSchema       = "Schema"
	pgRoleFieldMigratorRole = "MigratorRole"
	pgRoleFieldRuntimeRole  = "RuntimeRole"
)

// safePGIdentifier matches the conservative ASCII subset of PostgreSQL
// identifiers permitted in role and schema names: letters, digits, and
// underscores, starting with a letter or underscore, up to 63 bytes (the
// NAMEDATALEN-1 default). Tenant IDs sourced from outside should be
// normalized to this subset upstream; rejecting at the migration boundary
// gives a single forcing function rather than scattering input filters.
var safePGIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// Validate reports whether the spec's identifiers pass the safe-identifier
// check and the two roles differ. Returns ErrInvalidPGIdentifier wrapped
// with the offending field name (and value) on failure.
func (s *PGRoleSpec) Validate() error {
	for _, f := range []struct{ name, value string }{
		{pgRoleFieldSchema, s.Schema},
		{pgRoleFieldMigratorRole, s.MigratorRole},
		{pgRoleFieldRuntimeRole, s.RuntimeRole},
	} {
		if !safePGIdentifier.MatchString(f.value) {
			return fmt.Errorf("%w: %s=%q", ErrInvalidPGIdentifier, f.name, f.value)
		}
	}
	if s.MigratorRole == s.RuntimeRole {
		return fmt.Errorf("%w: MigratorRole and RuntimeRole must differ", ErrInvalidPGIdentifier)
	}
	return nil
}

// ProvisionPGRoles applies the role-pair + schema described by spec to the
// PostgreSQL instance reachable via db. All statements are idempotent: a
// rerun against an already-provisioned tenant is a no-op, except that
// MigratorPassword / RuntimePassword (when non-empty) are reapplied on
// every call to support secret rotation.
//
// db MUST be authenticated as a role with CREATEROLE plus the right to
// CREATE SCHEMA AUTHORIZATION <other> — typically the instance bootstrap
// superuser or a dedicated provisioner role granted those capabilities.
// The migrator and runtime roles created here cannot self-provision: they
// are denied SUPERUSER, CREATEDB, CREATEROLE, BYPASSRLS, and REPLICATION
// per the deliverables of #378.
//
// PostgreSQL is not fully transactional across role + schema boundaries
// (CREATE ROLE in particular is not transactional), so a partial-progress
// failure can leak intermediate state. Callers should rerun the same spec
// to converge; the idempotent template makes that safe.
func ProvisionPGRoles(ctx context.Context, db *sql.DB, spec *PGRoleSpec) error {
	if spec == nil {
		return fmt.Errorf("migration: ProvisionPGRoles requires a non-nil *PGRoleSpec")
	}
	if db == nil {
		return fmt.Errorf("migration: ProvisionPGRoles requires a non-nil *sql.DB")
	}
	if err := spec.Validate(); err != nil {
		return err
	}

	stmts := buildPGRoleStatements(spec)
	for i, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migration: provisioning step %d (%s) failed: %w",
				i, summarizeStmt(stmt), err)
		}
	}
	return nil
}

// PGRoleProvisioningSQL returns the SQL statements that ProvisionPGRoles
// would execute for spec, in order. Use this when operators want to inspect
// or apply the provisioning manually via psql, or feed it into their own
// migration runner (Flyway, Liquibase) rather than the Go helper.
//
// Returns ErrInvalidPGIdentifier when spec fails Validate. The returned
// slice does not include trailing semicolons; callers concatenating them
// into a single script should add separators themselves.
//
// SECURITY: when spec.MigratorPassword or spec.RuntimePassword is non-empty,
// the returned statements include the password as an in-clear SQL literal
// (`ALTER ROLE "..." PASSWORD '<secret>'`). Treat the returned slice as a
// sensitive value: do not echo it to logs, CI build artifacts, or anywhere
// the original credential wouldn't be acceptable. Callers preparing scripts
// for review should redact the literal before persisting to disk.
func PGRoleProvisioningSQL(spec *PGRoleSpec) ([]string, error) {
	if spec == nil {
		return nil, fmt.Errorf("migration: PGRoleProvisioningSQL requires a non-nil *PGRoleSpec")
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	return buildPGRoleStatements(spec), nil
}

// buildPGRoleStatements composes the ordered statement list. Spec is assumed
// to be non-nil and Validate()-clean.
func buildPGRoleStatements(spec *PGRoleSpec) []string {
	schema := quotePGIdent(spec.Schema)
	migrator := quotePGIdent(spec.MigratorRole)
	runtime := quotePGIdent(spec.RuntimeRole)

	roles := []struct {
		quotedIdent string
		rolname     string
		password    string
	}{
		{migrator, spec.MigratorRole, spec.MigratorPassword},
		{runtime, spec.RuntimeRole, spec.RuntimePassword},
	}

	// Pre-size for the worst case: 2 roles × (create + lockdown + password) + 6 schema/grant statements.
	stmts := make([]string, 0, 2*3+6)
	for _, r := range roles {
		stmts = append(stmts, buildRoleCreateAndLockdown(r.rolname, r.quotedIdent)...)
		if r.password != "" {
			stmts = append(stmts, fmt.Sprintf(
				`ALTER ROLE %s PASSWORD %s`,
				r.quotedIdent, quotePGStringLiteral(r.password),
			))
		}
	}

	stmts = append(stmts,
		fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s AUTHORIZATION %s`, schema, migrator),
		fmt.Sprintf(`GRANT USAGE ON SCHEMA %s TO %s`, schema, runtime),
		// Existing-object grants — required when reprovisioning a schema
		// that already contains tables created out-of-band.
		fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %s TO %s`, schema, runtime),
		fmt.Sprintf(`GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA %s TO %s`, schema, runtime),
		// Future-object grants — the AC-critical bit: ALTER DEFAULT PRIVILEGES
		// scoped to the migrator role + the tenant schema so tables created
		// by future Flyway migrations auto-grant to the runtime role.
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s`, migrator, schema, runtime),
		fmt.Sprintf(`ALTER DEFAULT PRIVILEGES FOR ROLE %s IN SCHEMA %s GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO %s`, migrator, schema, runtime),
	)
	return stmts
}

// buildRoleCreateAndLockdown returns the two-statement idempotent template
// that creates a role (if missing) and snaps its attribute floor back to the
// locked-down baseline. Used for both the migrator and runtime roles since
// both share the same NO* attribute requirements.
//
// The CREATE wrapped in DO-block is the canonical idempotent pattern because
// PostgreSQL has no CREATE ROLE IF NOT EXISTS syntax; the unconditional
// ALTER on the next statement re-applies the attribute floor on every run
// so manual drift (e.g. someone ran ALTER ROLE ... SUPERUSER) snaps back.
func buildRoleCreateAndLockdown(rolname, quotedIdent string) []string {
	const lockdownAttrs = "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
	return []string{
		fmt.Sprintf(`DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = %s) THEN
    CREATE ROLE %s LOGIN %s;
  END IF;
END $$`, quotePGStringLiteral(rolname), quotedIdent, lockdownAttrs),
		fmt.Sprintf(`ALTER ROLE %s %s`, quotedIdent, lockdownAttrs),
	}
}

// quotePGIdent returns the PostgreSQL-safe quoted form of ident. Callers must
// have verified ident via safePGIdentifier or PGRoleSpec.Validate first;
// the double-quote escaping here is belt-and-suspenders for the (regex-
// rejected) embedded-quote case.
func quotePGIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// quotePGStringLiteral returns the PostgreSQL-safe quoted form of a string
// literal under standard_conforming_strings=on (the default since PG 9.1).
// Doubles embedded single quotes; backslashes are literal under this setting
// and need no escaping.
func quotePGStringLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, `'`, `''`) + "'"
}

// summarizeStmt returns the first line of stmt, trimmed and truncated to 80
// chars, for use in provisioning error messages. Keeps the wrapping error
// short while still naming the failing statement.
//
// Redacts any `PASSWORD '<literal>'` clause before truncation so a failure
// on ALTER ROLE ... PASSWORD doesn't leak the resolved secret into the
// returned error string (which downstream callers may log).
func summarizeStmt(stmt string) string {
	first := stmt
	if idx := strings.IndexByte(stmt, '\n'); idx > 0 {
		first = stmt[:idx]
	}
	first = strings.TrimSpace(first)
	first = pgPasswordLiteralPattern.ReplaceAllString(first, "${1}'[REDACTED]'")
	if len(first) > 80 {
		first = first[:80] + "..."
	}
	return first
}
