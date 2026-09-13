package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gaborage/go-bricks/database/identifier"
	"github.com/gaborage/go-bricks/database/sqlredact"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// PGRoleSpec describes a PostgreSQL role-pair plus per-tenant schema for the
// migrator-vs-runtime role-separation model defined in issue #378.
//
// Migrator role: owns the per-tenant schema, holds DDL privileges, used
// exclusively by the migration runner. Created with NOSUPERUSER NOCREATEDB
// NOCREATEROLE NOREPLICATION NOBYPASSRLS so even a compromised migrator
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

	// IdentifierPolicy optionally tightens the identifier rule Validate
	// applies to Schema, MigratorRole and RuntimeRole; nil means the floor alone.
	// Leave the field unset for that — storing a typed nil
	// PGIdentifierCheckerFunc is a non-nil interface, and is refused. A spec
	// holding a PGIdentifierCheckerFunc is not comparable; see that type.
	IdentifierPolicy PGIdentifierChecker
}

// PGIdentifierChecker is a caller-supplied check layered on top of the
// identifier floor (database/identifier.Validate for PostgreSQL). Validate
// consults it once per identifier, after the floor and the reserved-name rule
// have accepted that identifier, so a policy can only refuse
// more — never admit a name either of those rejects. A returned error is
// wrapped with ErrInvalidPGIdentifier and the failing field name, so the policy
// itself does not need to identify the identifier it judged.
type PGIdentifierChecker interface {
	CheckPGIdentifier(value string) error
}

// PGIdentifierCheckerFunc adapts a plain function to PGIdentifierChecker.
//
// A typed nil of this type stored in PGRoleSpec.IdentifierPolicy is NOT the
// same as no policy: the interface value is non-nil, so Validate does consult
// it. Rather than panic on the nil call, the adapter refuses every identifier,
// so such a spec fails Validate instead of taking the process down. Leave the
// field unset for "no policy".
//
// A func value is not comparable, so a PGRoleSpec holding one is not safely
// comparable either. == compares fields in order and stops at the first
// difference, so it panics only when every earlier field is equal and the
// comparison reaches IdentifierPolicy; using such a spec as a map key always
// panics, because hashing reads every field. Compare such specs field by field
// or hold them by pointer; a comparable PGIdentifierChecker implementation keeps
// the spec comparable.
type PGIdentifierCheckerFunc func(value string) error

// errNilPGIdentifierCheckerFunc is what a nil PGIdentifierCheckerFunc refuses
// with; checkIdentifier wraps it with ErrInvalidPGIdentifier like any other
// policy refusal.
var errNilPGIdentifierCheckerFunc = errors.New("migration: IdentifierPolicy holds a nil PGIdentifierCheckerFunc")

// CheckPGIdentifier calls f, or refuses when f is nil.
func (f PGIdentifierCheckerFunc) CheckPGIdentifier(value string) error {
	if f == nil {
		return errNilPGIdentifierCheckerFunc
	}
	return f(value)
}

// checkIdentifier applies the identifier floor to the field's value, then the
// framework's own reserved-name rule, then — when one is configured — the
// caller's policy. A refusal from any of the three is wrapped with
// ErrInvalidPGIdentifier plus the field name and value.
func (s *PGRoleSpec) checkIdentifier(field, value string) error {
	err := identifier.Validate(dbtypes.PostgreSQL, value)
	if err == nil {
		err = checkReservedPGIdentifier(field, value)
	}
	if err == nil && s.IdentifierPolicy != nil {
		err = s.IdentifierPolicy.CheckPGIdentifier(value)
	}
	if err != nil {
		return fmt.Errorf("%w: %s=%q: %w", ErrInvalidPGIdentifier, field, value, err)
	}
	return nil
}

// checkReservedPGIdentifier refuses the names PostgreSQL owns, per the ADR-061
// amendment: "public" and the "pg_" prefix in both namespaces,
// "information_schema" for schemas alone. Folding is safe because the floor has
// already restricted value to the ASCII grammar.
func checkReservedPGIdentifier(field, value string) error {
	folded := strings.ToLower(value)
	switch {
	case folded == "public",
		folded == "information_schema" && field == pgRoleFieldSchema,
		strings.HasPrefix(folded, "pg_"):
		return ErrReservedPGIdentifier
	}
	return nil
}

// ErrInvalidPGIdentifier is returned by Validate when a role or schema name
// fails the safe-identifier check enforced by ProvisionPGRoles.
var ErrInvalidPGIdentifier = errors.New("migration: PostgreSQL identifier rejected")

// ErrReservedPGIdentifier is returned by Validate when a spec field names
// something PostgreSQL reserves, matched case-insensitively: "public" or a
// "pg_"-prefixed name in any of the three fields, plus "information_schema" for
// Schema alone. It is always wrapped with ErrInvalidPGIdentifier, so a caller
// matching the identifier sentinel keeps matching, and no IdentifierPolicy can
// waive it. Such a name passes every charset check while landing the tenant's
// tables in the schema every role on the instance can read (Schema "public") or
// granting that tenant's DML to every role on the instance (a role named
// "public", which PostgreSQL's RoleSpec maps onto the PUBLIC pseudo-role).
var ErrReservedPGIdentifier = errors.New("migration: identifier is reserved by PostgreSQL")

// ErrPGRolePasswordHasControlChar is returned by Validate when a role password
// contains CR, LF, or NUL. Such a password cannot be carried log-safely through
// the provisioning path: summarizeStmt collapses a failing statement to its
// first line, so an embedded newline would split a redacted summary apart.
// PostgreSQL itself accepts these passwords — the rejection is ours, at this
// API's boundary. Mirrors ErrEnvFieldHasControlChar, which guards the Flyway
// subprocess environment.
var ErrPGRolePasswordHasControlChar = errors.New("migration: role password contains forbidden control character (CR/LF/NUL)")

// Field name constants used in Validate error messages — the identifier
// fields via ErrInvalidPGIdentifier, the password fields via
// ErrPGRolePasswordHasControlChar — so callers (including tests) can assert
// which field failed without coupling to the literal string.
const (
	pgRoleFieldSchema           = "Schema"
	pgRoleFieldMigratorRole     = "MigratorRole"
	pgRoleFieldRuntimeRole      = "RuntimeRole"
	pgRoleFieldMigratorPassword = "MigratorPassword"
	pgRoleFieldRuntimePassword  = "RuntimePassword"
)

// Validate reports whether the spec's identifiers pass
// database/identifier.Validate for PostgreSQL (the shared bare-identifier
// grammar and 63-byte cap), the two roles differ, and neither password carries
// CR, LF, or NUL. Tenant IDs sourced from outside should be normalized to that
// grammar upstream; rejecting at the migration boundary gives a single forcing
// function rather than scattering input filters.
// Every identifier additionally passes the reserved-name rule: "public" and any
// "pg_"-prefixed name are refused case-insensitively with
// ErrReservedPGIdentifier, and "information_schema" is refused for Schema alone.
// A non-nil IdentifierPolicy is consulted once per identifier after the floor
// and the reserved-name rule have accepted it, in Schema → MigratorRole →
// RuntimeRole order, stopping at the first refusal — so a policy can never
// re-admit a reserved name.
// Returns ErrInvalidPGIdentifier wrapped with the offending field name, value
// and the identifier sentinel for an identifier failure, or
// ErrPGRolePasswordHasControlChar wrapped with the offending field name —
// never the value — for a password failure.
func (s *PGRoleSpec) Validate() error {
	for _, f := range []struct{ name, value string }{
		{pgRoleFieldSchema, s.Schema},
		{pgRoleFieldMigratorRole, s.MigratorRole},
		{pgRoleFieldRuntimeRole, s.RuntimeRole},
	} {
		if err := s.checkIdentifier(f.name, f.value); err != nil {
			return err
		}
	}
	if s.MigratorRole == s.RuntimeRole {
		return fmt.Errorf("%w: MigratorRole and RuntimeRole must differ", ErrInvalidPGIdentifier)
	}
	for _, f := range []struct{ name, value string }{
		{pgRoleFieldMigratorPassword, s.MigratorPassword},
		{pgRoleFieldRuntimePassword, s.RuntimePassword},
	} {
		if strings.ContainsAny(f.value, "\r\n\x00") {
			return fmt.Errorf("%w: %s", ErrPGRolePasswordHasControlChar, f.name)
		}
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
		return errors.New("migration: ProvisionPGRoles requires a non-nil *PGRoleSpec")
	}
	if db == nil {
		return errors.New("migration: ProvisionPGRoles requires a non-nil *sql.DB")
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
		return nil, errors.New("migration: PGRoleProvisioningSQL requires a non-nil *PGRoleSpec")
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
		password    string
	}{
		{migrator, spec.MigratorPassword},
		{runtime, spec.RuntimePassword},
	}

	// Pre-size for the worst case: 2 roles × (create + lockdown + password) + 8 schema/grant/search_path statements.
	stmts := make([]string, 0, 2*3+8)
	for _, r := range roles {
		stmts = append(stmts, buildRoleCreateAndLockdown(r.quotedIdent)...)
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
		// Default search_path for both roles: without it, every unqualified
		// statement from either role resolves to public — migrations from the
		// migrator role land in the wrong schema (pre-#716 fallback), and
		// unqualified runtime queries silently miss tenant tables.
		fmt.Sprintf(`ALTER ROLE %s SET search_path = %s`, migrator, schema),
		fmt.Sprintf(`ALTER ROLE %s SET search_path = %s`, runtime, schema),
	)
	return stmts
}

// buildRoleCreateAndLockdown returns the two-statement idempotent template
// that creates a role (if missing) and snaps its attribute floor back to the
// locked-down baseline. Used for both the migrator and runtime roles since
// both share the same NO* attribute requirements.
//
// The CREATE is wrapped in a nested BEGIN/EXCEPTION block that swallows both
// duplicate_object (42710) and unique_violation (23505) rather than checking
// pg_roles first: the check-then-create form races when two provisioners run
// concurrently for the same role name. If the role is already committed the
// CREATE raises duplicate_object; if two sessions both pass CREATE ROLE's
// internal existence check and then collide on the pg_authid rolname unique
// index, the loser raises unique_violation instead — so both must be swallowed
// for the concurrent path to be safe. The unconditional ALTER on the next
// statement re-applies the attribute floor on every run so manual drift
// (e.g. someone ran ALTER ROLE ... SUPERUSER) snaps back.
func buildRoleCreateAndLockdown(quotedIdent string) []string {
	const lockdownAttrs = "NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
	return []string{
		fmt.Sprintf(`DO $$ BEGIN
  BEGIN
    CREATE ROLE %s LOGIN %s;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN
    NULL; -- another provisioner created it concurrently; not an error
  END;
END $$`, quotedIdent, lockdownAttrs),
		fmt.Sprintf(`ALTER ROLE %s %s`, quotedIdent, lockdownAttrs),
	}
}

// quotePGIdent returns the PostgreSQL-safe quoted form of ident. Callers must
// have verified ident via identifier.Validate or PGRoleSpec.Validate first;
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
// Delegates to sqlredact.Statement, the same helper the database tracking
// wrapper uses, so a failure on ALTER ROLE ... PASSWORD doesn't leak the
// resolved secret into the returned error string (which downstream callers may
// log) and the two redaction sites cannot drift. The scrub runs before the
// first-line split as well as before the 80-char truncation: either cut could
// remove the keyword sqlredact.Statement anchors on, and a password containing
// a newline would then put the leading line of the secret in the summary.
func summarizeStmt(stmt string) string {
	first := sqlredact.Statement(stmt)
	if idx := strings.IndexByte(first, '\n'); idx > 0 {
		first = first[:idx]
	}
	first = strings.TrimSpace(first)
	if len(first) > 80 {
		first = first[:80] + "..."
	}
	return first
}
