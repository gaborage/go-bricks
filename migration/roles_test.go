package migration

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database"
	"github.com/gaborage/go-bricks/database/identifier"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestPGRoleSpecValidateAccepts(t *testing.T) {
	specs := []*PGRoleSpec{
		{Schema: "tenant_a", MigratorRole: "migrator", RuntimeRole: "tenant_a_app"},
		{Schema: "TenantA", MigratorRole: "Mig", RuntimeRole: "App"},
		{Schema: "_underscore_start", MigratorRole: "_m", RuntimeRole: "_r"},
		// PostgreSQL accepts $ after the first byte (#1311).
		{Schema: "tnz_$a", MigratorRole: "mig$1", RuntimeRole: "app$"},
		// Boundary: 63-char identifier (NAMEDATALEN-1).
		{
			Schema:       strings.Repeat("a", 63),
			MigratorRole: strings.Repeat("b", 63),
			RuntimeRole:  strings.Repeat("c", 63),
		},
	}
	for _, s := range specs {
		t.Run(s.Schema, func(t *testing.T) {
			assert.NoError(t, s.Validate())
		})
	}
}

func TestPGRoleSpecValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		spec *PGRoleSpec
		// fieldOrReason is a substring expected in the error message so the
		// caller knows which field failed.
		fieldOrReason string
	}{
		{
			name:          "empty_schema",
			spec:          &PGRoleSpec{MigratorRole: "m", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldSchema,
		},
		{
			name:          "schema_with_hyphen",
			spec:          &PGRoleSpec{Schema: "tenant-a", MigratorRole: "m", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldSchema,
		},
		{
			name:          "schema_starts_with_digit",
			spec:          &PGRoleSpec{Schema: "1tenant", MigratorRole: "m", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldSchema,
		},
		{
			name:          "schema_with_quote",
			spec:          &PGRoleSpec{Schema: `bad"quote`, MigratorRole: "m", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldSchema,
		},
		{
			name:          "schema_too_long",
			spec:          &PGRoleSpec{Schema: strings.Repeat("a", 64), MigratorRole: "m", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldSchema,
		},
		{
			name:          "migrator_role_empty",
			spec:          &PGRoleSpec{Schema: "s", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldMigratorRole,
		},
		{
			name:          "runtime_role_with_space",
			spec:          &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "runtime app"},
			fieldOrReason: pgRoleFieldRuntimeRole,
		},
		{
			name:          "migrator_equals_runtime",
			spec:          &PGRoleSpec{Schema: "s", MigratorRole: "same", RuntimeRole: "same"},
			fieldOrReason: "MigratorRole and RuntimeRole must differ",
		},
		{
			name:          "schema_with_null_byte",
			spec:          &PGRoleSpec{Schema: "tenant\x00a", MigratorRole: "m", RuntimeRole: "r"},
			fieldOrReason: pgRoleFieldSchema,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.fieldOrReason)
			require.ErrorIs(t, err, ErrInvalidPGIdentifier,
				"want wrapped ErrInvalidPGIdentifier, got %v", err)
		})
	}
}

func TestPGRoleSpecValidatePassesIdentifierSentinelThrough(t *testing.T) {
	err := (&PGRoleSpec{Schema: strings.Repeat("a", 64), MigratorRole: "m", RuntimeRole: "r"}).Validate()
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	require.ErrorIs(t, err, identifier.ErrIdentifierTooLong)
}

func TestPGRoleProvisioningSQLRejectsInvalidSpec(t *testing.T) {
	_, err := PGRoleProvisioningSQL(&PGRoleSpec{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidPGIdentifier)
}

func TestPGRoleProvisioningSQLRejectsNilSpec(t *testing.T) {
	_, err := PGRoleProvisioningSQL(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil *PGRoleSpec")
}

func TestPGRoleProvisioningSQLContainsExpectedStatements(t *testing.T) {
	spec := &PGRoleSpec{
		Schema:           "tenant_a",
		MigratorRole:     "migrator",
		MigratorPassword: "mpw",
		RuntimeRole:      "tenant_a_app",
		RuntimePassword:  "rpw",
	}
	stmts, err := PGRoleProvisioningSQL(spec)
	require.NoError(t, err)
	require.NotEmpty(t, stmts)

	all := strings.Join(stmts, "\n;\n")

	// Role creation in a DO block (idempotent, race-safe via EXCEPTION).
	assert.Contains(t, all, `CREATE ROLE "migrator"`)
	assert.Contains(t, all, `CREATE ROLE "tenant_a_app"`)
	assert.Contains(t, all, "EXCEPTION WHEN duplicate_object")

	// Attribute lockdown on both roles.
	assert.Contains(t, all, `ALTER ROLE "migrator" NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`)
	assert.Contains(t, all, `ALTER ROLE "tenant_a_app" NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`)

	// Passwords applied.
	assert.Contains(t, all, `ALTER ROLE "migrator" PASSWORD 'mpw'`)
	assert.Contains(t, all, `ALTER ROLE "tenant_a_app" PASSWORD 'rpw'`)

	// Schema ownership and runtime grants.
	assert.Contains(t, all, `CREATE SCHEMA IF NOT EXISTS "tenant_a" AUTHORIZATION "migrator"`)
	assert.Contains(t, all, `GRANT USAGE ON SCHEMA "tenant_a" TO "tenant_a_app"`)
	assert.Contains(t, all, `GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "tenant_a" TO "tenant_a_app"`)

	// The AC-critical ALTER DEFAULT PRIVILEGES line.
	assert.Contains(t, all, `ALTER DEFAULT PRIVILEGES FOR ROLE "migrator" IN SCHEMA "tenant_a" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "tenant_a_app"`)

	// Default search_path on both roles: migrator-side keeps Flyway's
	// no-explicit-schema fallback pointed at the tenant schema; runtime-side
	// keeps unqualified DML off public.
	assert.Contains(t, all, `ALTER ROLE "migrator" SET search_path = "tenant_a"`)
	assert.Contains(t, all, `ALTER ROLE "tenant_a_app" SET search_path = "tenant_a"`)
}

// TestPGRoleProvisioningSQLSearchPathUsesQuotedIdents pins the exact quoted
// ALTER ROLE ... SET search_path statements for a mixed-case schema — mixed
// case is where quoting is semantically load-bearing: unquoted, PostgreSQL
// would fold the identifier to lowercase and the search_path would miss the
// actual (mixed-case) schema.
func TestPGRoleProvisioningSQLSearchPathUsesQuotedIdents(t *testing.T) {
	spec := &PGRoleSpec{
		Schema:       "TenantX",
		MigratorRole: "MigX",
		RuntimeRole:  "AppX",
	}
	stmts, err := PGRoleProvisioningSQL(spec)
	require.NoError(t, err)

	all := strings.Join(stmts, "\n;\n")
	assert.Contains(t, all, `ALTER ROLE "MigX" SET search_path = "TenantX"`)
	assert.Contains(t, all, `ALTER ROLE "AppX" SET search_path = "TenantX"`)
}

func TestPGRoleProvisioningSQLOmitsEmptyPasswordALTERs(t *testing.T) {
	spec := &PGRoleSpec{
		Schema:       "tenant_b",
		MigratorRole: "migrator2",
		RuntimeRole:  "tenant_b_app",
		// Both passwords intentionally empty.
	}
	stmts, err := PGRoleProvisioningSQL(spec)
	require.NoError(t, err)

	all := strings.Join(stmts, "\n;\n")
	assert.NotContains(t, all, "PASSWORD '",
		"empty passwords must not emit ALTER ROLE ... PASSWORD statements")
}

// wantCreateRoleStmt spells out the race-safe CREATE ROLE block for one quoted
// role, attribute floor included, as the template emits it.
func wantCreateRoleStmt(quotedRole string) string {
	return `DO $$ BEGIN
  BEGIN
    CREATE ROLE ` + quotedRole + ` LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  EXCEPTION WHEN duplicate_object OR unique_violation THEN
    NULL; -- another provisioner created it concurrently; not an error
  END;
END $$`
}

// The skip options are additive: the zero value must keep today's template
// statement for statement, and each option must drop exactly its statements.
func TestPGRoleProvisioningSQLPinsTheListPerSkipOption(t *testing.T) {
	fixture := txDoorSpec()
	var (
		createMigrator   = wantCreateRoleStmt(`"mig_tx"`)
		lockMigrator     = `ALTER ROLE "mig_tx" NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`
		migratorPassword = `ALTER ROLE "mig_tx" PASSWORD '` + fixture.MigratorPassword + `'`
		createRuntime    = wantCreateRoleStmt(`"rt_tx"`)
		lockRuntime      = `ALTER ROLE "rt_tx" NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`
		runtimePassword  = `ALTER ROLE "rt_tx" PASSWORD '` + fixture.RuntimePassword + `'`
		migratorPath     = `ALTER ROLE "mig_tx" SET search_path = "tenant_tx"`
		runtimePath      = `ALTER ROLE "rt_tx" SET search_path = "tenant_tx"`
	)
	schemaAndGrants := []string{
		`CREATE SCHEMA IF NOT EXISTS "tenant_tx" AUTHORIZATION "mig_tx"`,
		`GRANT USAGE ON SCHEMA "tenant_tx" TO "rt_tx"`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "tenant_tx" TO "rt_tx"`,
		`GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA "tenant_tx" TO "rt_tx"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "mig_tx" IN SCHEMA "tenant_tx" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "rt_tx"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "mig_tx" IN SCHEMA "tenant_tx" GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO "rt_tx"`,
	}
	concat := func(groups ...[]string) []string {
		var out []string
		for _, g := range groups {
			out = append(out, g...)
		}
		return out
	}

	tests := []struct {
		name  string
		apply func(*PGRoleSpec)
		want  []string
	}{
		{
			name:  "zero_value_is_the_current_template",
			apply: func(*PGRoleSpec) {},
			want: concat(
				[]string{createMigrator, lockMigrator, migratorPassword, createRuntime, lockRuntime, runtimePassword},
				schemaAndGrants,
				[]string{migratorPath, runtimePath},
			),
		},
		{
			name: "skip_migrator_role",
			apply: func(s *PGRoleSpec) {
				s.SkipMigratorRole = true
				s.MigratorPassword = ""
			},
			want: concat(
				[]string{createRuntime, lockRuntime, runtimePassword},
				schemaAndGrants,
				[]string{runtimePath},
			),
		},
		{
			name:  "skip_floor_reassert",
			apply: func(s *PGRoleSpec) { s.SkipFloorReassert = true },
			want: concat(
				[]string{createMigrator, migratorPassword, createRuntime, runtimePassword},
				schemaAndGrants,
				[]string{migratorPath, runtimePath},
			),
		},
		{
			name: "skip_both",
			apply: func(s *PGRoleSpec) {
				s.SkipMigratorRole = true
				s.SkipFloorReassert = true
				s.MigratorPassword = ""
			},
			want: concat(
				[]string{createRuntime, runtimePassword},
				schemaAndGrants,
				[]string{runtimePath},
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := txDoorSpec()
			tt.apply(spec)
			stmts, err := PGRoleProvisioningSQL(spec)
			require.NoError(t, err)
			assert.Equal(t, tt.want, stmts)
		})
	}
}

// A leftover MigratorPassword must never reach an out-of-band migrator: the
// combination is refused at Validate, before any door composes a statement.
func TestPGRoleSpecValidateRefusesMigratorPasswordWhenSkippingMigratorRole(t *testing.T) {
	spec := txDoorSpec()
	spec.SkipMigratorRole = true

	require.ErrorIs(t, spec.Validate(), ErrPGRoleSkippedMigratorHasPassword)

	stmts, err := PGRoleProvisioningSQL(spec)
	require.ErrorIs(t, err, ErrPGRoleSkippedMigratorHasPassword)
	assert.Nil(t, stmts)

	exec := newRecordingRoleExecutor()
	require.ErrorIs(t, ProvisionPGRolesTx(context.Background(), exec, spec), ErrPGRoleSkippedMigratorHasPassword)
	assert.Empty(t, exec.stmts, "a refused spec must reach no statement at all")

	// No expectations are queued, so any Exec would fail with sqlmock's own error
	// instead of the sentinel.
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	require.ErrorIs(t, ProvisionPGRoles(context.Background(), db, spec), ErrPGRoleSkippedMigratorHasPassword)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestBuildRoleCreateAndLockdownSwallowsDuplicate pins the race-safe
// CREATE ROLE form: an EXCEPTION handler that swallows both duplicate_object
// (42710, role already committed) and unique_violation (23505, the loser of a
// concurrent race colliding on pg_authid's rolname index) instead of a
// check-then-create pg_roles lookup, which races when two provisioners create
// the same role concurrently.
func TestBuildRoleCreateAndLockdownSwallowsDuplicate(t *testing.T) {
	stmts := buildRoleCreateAndLockdown(`"tenant_a_app"`)
	all := strings.Join(stmts, "\n;\n")

	assert.Contains(t, all, "EXCEPTION WHEN duplicate_object OR unique_violation")
	assert.NotContains(t, all, "IF NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles")
}

func TestQuotePGIdent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "simple", `"simple"`},
		{"mixed_case", "Mixed_Case", `"Mixed_Case"`},
		// Defense-in-depth: even though Validate rejects embedded quotes,
		// quotePGIdent still doubles them so a direct misuse from inside
		// the package can't smuggle a quote-break.
		{"embedded_quote_is_doubled", `weird"name`, `"weird""name"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, quotePGIdent(tt.in))
		})
	}
}

func TestQuotePGStringLiteral(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "''"},
		{"simple", "simple", "'simple'"},
		{"single_quote_is_doubled", "O'Brien", "'O''Brien'"},
		// Backslashes are literal under standard_conforming_strings=on.
		{"backslash_is_literal", `back\slash`, `'back\slash'`},
		{"two_doubles_become_four", "two''doubles", "'two''''doubles'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, quotePGStringLiteral(tt.in))
		})
	}
}

func TestSummarizeStmt(t *testing.T) {
	assert.Equal(t, "short", summarizeStmt("short"))
	assert.Equal(t, "first line", summarizeStmt("first line\nsecond line"))
	assert.Equal(t, "trimmed", summarizeStmt("  trimmed  "))

	long := strings.Repeat("x", 100)
	got := summarizeStmt(long)
	assert.Len(t, got, 83, "long statements truncate to 80 chars plus the ellipsis sentinel")
	assert.True(t, strings.HasSuffix(got, "..."))
}

// TestSummarizeStmtRedactsPasswordLiteral verifies that the password literal
// in ALTER ROLE ... PASSWORD '<secret>' is replaced with [REDACTED] before
// the summary is returned. Without this redaction, a failing password-rotation
// statement would leak the resolved secret into the wrapped error string.
func TestSummarizeStmtRedactsPasswordLiteral(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "simple_password",
			in:   `ALTER ROLE "tenant_a_app" PASSWORD 'super-secret-123'`,
			want: `ALTER ROLE "tenant_a_app" PASSWORD '[REDACTED]'`,
		},
		{
			name: "password_with_doubled_quote",
			in:   `ALTER ROLE "x" PASSWORD 'hard''quote'`,
			want: `ALTER ROLE "x" PASSWORD '[REDACTED]'`,
		},
		{
			name: "lowercase_keyword",
			in:   `alter role "x" password 'lower-cased'`,
			want: `alter role "x" password '[REDACTED]'`,
		},
		{
			name: "no_password_unchanged",
			in:   `CREATE SCHEMA IF NOT EXISTS "tenant_a" AUTHORIZATION "migrator"`,
			want: `CREATE SCHEMA IF NOT EXISTS "tenant_a" AUTHORIZATION "migrator"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeStmt(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "super-secret-123")
			assert.NotContains(t, got, "lower-cased")
		})
	}
}

// TestSummarizeStmtRedactsMultilinePassword pins the redact-before-split
// ordering. A password carrying a newline produces a multi-line ALTER ROLE
// statement; splitting first would leave a fragment ending mid-literal that
// the closing-quote-anchored pattern cannot match, leaking the first line of
// the secret verbatim into the wrapped error.
func TestSummarizeStmtRedactsMultilinePassword(t *testing.T) {
	stmt := `ALTER ROLE "tenant_a_app" PASSWORD ` + quotePGStringLiteral("line1\nline2")
	got := summarizeStmt(stmt)
	assert.Contains(t, got, "[REDACTED]")
	assert.NotContains(t, got, "line1")
	assert.NotContains(t, got, "line2")
	assert.NotContains(t, got, "\n", "the summary must stay single-line")
}

// TestSummarizeStmtLeadingNewlineKeepsStatement covers a newline at index 0:
// the idx > 0 guard must decline to split, since slicing [:0] would discard
// the whole statement and return an empty summary.
func TestSummarizeStmtLeadingNewlineKeepsStatement(t *testing.T) {
	got := summarizeStmt("\n" + `ALTER ROLE "x" PASSWORD 'p'`)
	assert.Equal(t, `ALTER ROLE "x" PASSWORD '[REDACTED]'`, got)
}

// TestSummarizeStmtTruncatesMultilineRedactedStatement verifies truncation
// still applies once a multi-line statement collapses into a single redacted
// line longer than the 80-char budget.
func TestSummarizeStmtTruncatesMultilineRedactedStatement(t *testing.T) {
	ident := strings.Repeat("r", 63)
	stmt := `ALTER ROLE "` + ident + `" PASSWORD ` + quotePGStringLiteral("sec\nret")
	got := summarizeStmt(stmt)
	assert.Len(t, got, 83)
	assert.True(t, strings.HasSuffix(got, "..."))
	assert.NotContains(t, got, "sec")
	assert.NotContains(t, got, "ret")
}

// TestPGRoleSpecValidateRejectsControlCharPasswords pins the CR/LF/NUL
// rejection on both password fields. PostgreSQL accepts such passwords; the
// restriction is this API's, because the provisioning path cannot carry them
// log-safely.
func TestPGRoleSpecValidateRejectsControlCharPasswords(t *testing.T) {
	tests := []struct {
		name     string
		spec     *PGRoleSpec
		field    string
		badValue string
	}{
		{
			name:     "migrator_password_lf",
			spec:     &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r", MigratorPassword: "bad\npw"},
			field:    pgRoleFieldMigratorPassword,
			badValue: "bad\npw",
		},
		{
			name:     "migrator_password_cr",
			spec:     &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r", MigratorPassword: "bad\rpw"},
			field:    pgRoleFieldMigratorPassword,
			badValue: "bad\rpw",
		},
		{
			name:     "migrator_password_nul",
			spec:     &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r", MigratorPassword: "bad\x00pw"},
			field:    pgRoleFieldMigratorPassword,
			badValue: "bad\x00pw",
		},
		{
			name:     "runtime_password_lf",
			spec:     &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r", RuntimePassword: "bad\npw"},
			field:    pgRoleFieldRuntimePassword,
			badValue: "bad\npw",
		},
		{
			name:     "runtime_password_cr",
			spec:     &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r", RuntimePassword: "bad\rpw"},
			field:    pgRoleFieldRuntimePassword,
			badValue: "bad\rpw",
		},
		{
			name:     "runtime_password_nul",
			spec:     &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r", RuntimePassword: "bad\x00pw"},
			field:    pgRoleFieldRuntimePassword,
			badValue: "bad\x00pw",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			require.Error(t, err)
			// The non-disclosure check must run even when the error is the wrong
			// kind, so the identity assertion goes last.
			assert.Contains(t, err.Error(), tt.field)
			assert.NotContains(t, err.Error(), tt.badValue,
				"the error must name the field, never the password value")
			assert.ErrorIs(t, err, ErrPGRolePasswordHasControlChar,
				"want wrapped ErrPGRolePasswordHasControlChar, got %v", err)
		})
	}

	clean := &PGRoleSpec{
		Schema: "s", MigratorRole: "m", RuntimeRole: "r",
		MigratorPassword: "clean-migrator-pw", RuntimePassword: "clean-runtime-pw",
	}
	assert.NoError(t, clean.Validate(), "control-char-free passwords stay valid")

	empty := &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "r"}
	assert.NoError(t, empty.Validate(), "empty passwords stay valid — they emit no ALTER ROLE statement")
}

// errTestPolicyRejected is the sentinel returned by the test policies below, so
// a test can assert the caller still reaches its own error through the wrap.
var errTestPolicyRejected = errors.New("test policy rejected the identifier")

// rejectUppercase refuses any identifier carrying an uppercase byte — a rule
// the floor admits, so it exercises the tightening direction.
func rejectUppercase(value string) error {
	if strings.ToLower(value) != value {
		return errTestPolicyRejected
	}
	return nil
}

func TestPGRoleSpecValidatePolicyErrorReachesCaller(t *testing.T) {
	spec := &PGRoleSpec{
		Schema:           "tenant_a",
		MigratorRole:     "MigratorX",
		RuntimeRole:      "r",
		IdentifierPolicy: PGIdentifierCheckerFunc(rejectUppercase),
	}
	err := spec.Validate()
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	require.ErrorIs(t, err, errTestPolicyRejected)
	assert.Contains(t, err.Error(), pgRoleFieldMigratorRole)
	assert.Contains(t, err.Error(), "MigratorX")
}

// An admit-everything policy must not re-admit what the floor refused — one
// charset refusal and one length refusal, the floor's two independent rules.
func TestPGRoleSpecValidatePolicyCannotWidenFloor(t *testing.T) {
	admitEverything := PGIdentifierCheckerFunc(func(string) error { return nil })
	tests := []struct {
		name  string
		spec  *PGRoleSpec
		field string
	}{
		{
			name:  "hyphen_schema",
			spec:  &PGRoleSpec{Schema: "tenant-a", MigratorRole: "m", RuntimeRole: "r"},
			field: pgRoleFieldSchema,
		},
		{
			name:  "over_63_bytes_schema",
			spec:  &PGRoleSpec{Schema: strings.Repeat("a", 64), MigratorRole: "m", RuntimeRole: "r"},
			field: pgRoleFieldSchema,
		},
		{
			name:  "hyphen_migrator_role",
			spec:  &PGRoleSpec{Schema: "tenant_a", MigratorRole: "mig-rator", RuntimeRole: "r"},
			field: pgRoleFieldMigratorRole,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.spec.IdentifierPolicy = admitEverything
			err := tt.spec.Validate()
			require.ErrorIs(t, err, ErrInvalidPGIdentifier)
			assert.Contains(t, err.Error(), tt.field)
		})
	}
}

func TestPGRoleSpecValidatePolicySeesEveryIdentifier(t *testing.T) {
	var seen []string
	spec := &PGRoleSpec{
		Schema:       "tenant_a",
		MigratorRole: "migrator",
		RuntimeRole:  "tenant_a_app",
		IdentifierPolicy: PGIdentifierCheckerFunc(func(value string) error {
			seen = append(seen, value)
			return nil
		}),
	}
	require.NoError(t, spec.Validate())
	assert.Equal(t, []string{"tenant_a", "migrator", "tenant_a_app"}, seen)
}

// Every target below is an identifier the floor admits, so the policy is the
// only thing that can refuse it — the tightening direction.
func TestPGRoleSpecValidatePolicyRejectsEachIdentifierField(t *testing.T) {
	tests := []struct {
		name   string
		spec   *PGRoleSpec
		target string
		field  string
	}{
		{
			name:   "schema",
			spec:   &PGRoleSpec{Schema: "TenantX", MigratorRole: "m", RuntimeRole: "r"},
			target: "TenantX",
			field:  pgRoleFieldSchema,
		},
		{
			name:   "migrator_role",
			spec:   &PGRoleSpec{Schema: "s", MigratorRole: "MigratorX", RuntimeRole: "r"},
			target: "MigratorX",
			field:  pgRoleFieldMigratorRole,
		},
		{
			name:   "runtime_role",
			spec:   &PGRoleSpec{Schema: "s", MigratorRole: "m", RuntimeRole: "RuntimeX"},
			target: "RuntimeX",
			field:  pgRoleFieldRuntimeRole,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			floorOnly := *tt.spec
			require.NoError(t, floorOnly.Validate(), "floor admits the identifier the policy refuses")

			tt.spec.IdentifierPolicy = PGIdentifierCheckerFunc(func(value string) error {
				if value == tt.target {
					return errTestPolicyRejected
				}
				return nil
			})
			err := tt.spec.Validate()
			require.ErrorIs(t, err, errTestPolicyRejected)
			require.ErrorIs(t, err, ErrInvalidPGIdentifier)
			assert.Contains(t, err.Error(), tt.field)
			assert.Contains(t, err.Error(), tt.target)
		})
	}
}

func TestPGRoleSpecValidateFloorRunsBeforePolicy(t *testing.T) {
	consulted := 0
	spec := &PGRoleSpec{
		Schema:       "tenant-a",
		MigratorRole: "m",
		RuntimeRole:  "r",
		IdentifierPolicy: PGIdentifierCheckerFunc(func(string) error {
			consulted++
			return nil
		}),
	}
	require.ErrorIs(t, spec.Validate(), ErrInvalidPGIdentifier)
	assert.Zero(t, consulted, "policy must not see an identifier the floor already refused")
}

// Every name below is one the floor admits, so the reserved-name rule is the
// only thing that can refuse it. Case is folded deliberately: this code QUOTES
// identifiers, so "Public" is a distinct schema from "public", but an operator
// or script that writes the name unquoted resolves to the shared one.
func TestPGRoleSpecValidateRejectsReservedSchemas(t *testing.T) {
	for _, schema := range []string{
		"public", "Public", "PUBLIC",
		"pg_temp", "pg_toast", "PG_CATALOG", "Pg_Anything",
		// The mixed-case spelling is "Information_SCHEMA", not "Information_Schema":
		// the assertion below looks for the field name "Schema" in the message, and
		// a value containing that exact casing would satisfy it on its own.
		"information_schema", "Information_SCHEMA", "INFORMATION_SCHEMA",
	} {
		t.Run(schema, func(t *testing.T) {
			spec := &PGRoleSpec{Schema: schema, MigratorRole: "m", RuntimeRole: "r"}
			require.NoError(t, identifier.Validate(dbtypes.PostgreSQL, schema),
				"the floor admits this name, so only the reserved rule can refuse it")

			err := spec.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), pgRoleFieldSchema)
			assert.Contains(t, err.Error(), schema)
			require.ErrorIs(t, err, ErrReservedPGIdentifier)
			require.ErrorIs(t, err, ErrInvalidPGIdentifier,
				"a reserved-schema refusal stays an identifier refusal for existing matchers")
		})
	}
}

// The near misses: every one differs from a reserved name by at least one byte
// and must still provision.
func TestPGRoleSpecValidateAcceptsReservedNameNearMisses(t *testing.T) {
	for _, schema := range []string{"publicity", "pg", "pgx", "information_schemas", "mypublic", "public_tenant"} {
		t.Run(schema, func(t *testing.T) {
			spec := &PGRoleSpec{Schema: schema, MigratorRole: "m", RuntimeRole: "r"}
			assert.NoError(t, spec.Validate())
		})
	}
}

// PostgreSQL's RoleSpec grammar maps the name "public" — quoted included — onto
// the PUBLIC pseudo-role, so a runtime role spelled that way would grant the
// tenant's DML to every role on the instance. "pg_" is PostgreSQL's own reserved
// role namespace.
func TestPGRoleSpecValidateRejectsReservedRoles(t *testing.T) {
	for _, field := range []string{pgRoleFieldMigratorRole, pgRoleFieldRuntimeRole} {
		for _, name := range []string{"public", "PUBLIC", "Public", "pg_x", "PG_x"} {
			t.Run(field+"_"+name, func(t *testing.T) {
				spec := &PGRoleSpec{Schema: "tenant_a", MigratorRole: "m", RuntimeRole: "r"}
				if field == pgRoleFieldMigratorRole {
					spec.MigratorRole = name
				} else {
					spec.RuntimeRole = name
				}
				require.NoError(t, identifier.Validate(dbtypes.PostgreSQL, name),
					"the floor admits this name, so only the reserved rule can refuse it")

				err := spec.Validate()
				require.Error(t, err)
				assert.Contains(t, err.Error(), field)
				assert.Contains(t, err.Error(), name)
				require.ErrorIs(t, err, ErrReservedPGIdentifier)
				require.ErrorIs(t, err, ErrInvalidPGIdentifier,
					"a reserved-name refusal stays an identifier refusal for existing matchers")
			})
		}
	}
}

// The role half is narrower than the schema half: "information_schema" is a
// schema concept with no role meaning, and every near miss below differs from a
// reserved name by at least one byte.
func TestPGRoleSpecValidateAcceptsNonReservedRoles(t *testing.T) {
	for _, name := range []string{"information_schema", "publicx", "mypublic", "pgx", "pg"} {
		t.Run(name, func(t *testing.T) {
			assert.NoError(t, (&PGRoleSpec{Schema: "tenant_a", MigratorRole: name, RuntimeRole: "r"}).Validate())
			assert.NoError(t, (&PGRoleSpec{Schema: "tenant_a", MigratorRole: "m", RuntimeRole: name}).Validate())
		})
	}
}

// The operator-script path has no server backstop: ProvisionPGRoles would at
// least meet PostgreSQL's own reserved_name error, but a script handed to psql
// carries the GRANT to PUBLIC as written, so the refusal must happen here.
func TestPGRoleProvisioningSQLRejectsReservedRole(t *testing.T) {
	stmts, err := PGRoleProvisioningSQL(&PGRoleSpec{Schema: "tenant_a", MigratorRole: "m", RuntimeRole: "public"})
	require.ErrorIs(t, err, ErrReservedPGIdentifier)
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	assert.Contains(t, err.Error(), pgRoleFieldRuntimeRole)
	assert.Empty(t, stmts)
}

// Ordering, stated as observation rather than as a second copy of the rule: the
// policy never sees a reserved schema, so it can neither admit it nor mask the
// sentinel with a refusal of its own.
func TestPGRoleSpecValidateReservedRuleRunsBeforePolicy(t *testing.T) {
	var seen []string
	spec := &PGRoleSpec{
		Schema:       "public",
		MigratorRole: "m",
		RuntimeRole:  "r",
		IdentifierPolicy: PGIdentifierCheckerFunc(func(value string) error {
			seen = append(seen, value)
			return errTestPolicyRejected
		}),
	}
	err := spec.Validate()
	require.ErrorIs(t, err, ErrReservedPGIdentifier)
	require.NotErrorIs(t, err, errTestPolicyRejected)
	assert.Empty(t, seen, "the policy must not be consulted for a reserved schema")
}

// The floor still runs first: a name that is reserved-shaped AND outside the
// grammar comes back as a floor refusal, not as a reserved-name one.
func TestPGRoleSpecValidateFloorRunsBeforeReservedRule(t *testing.T) {
	spec := &PGRoleSpec{Schema: "pg_temp-1", MigratorRole: "m", RuntimeRole: "r"}
	err := spec.Validate()
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	assert.NotErrorIs(t, err, ErrReservedPGIdentifier)
}

// A typed nil in the interface field is non-nil as an interface, so the adapter
// is called. It must refuse rather than panic on the nil call.
func TestPGRoleSpecValidateRefusesNilPolicyFunc(t *testing.T) {
	spec := &PGRoleSpec{
		Schema:           "tenant_a",
		MigratorRole:     "m",
		RuntimeRole:      "r",
		IdentifierPolicy: PGIdentifierCheckerFunc(nil),
	}
	require.NotPanics(t, func() {
		require.ErrorIs(t, spec.Validate(), ErrInvalidPGIdentifier)
	})
}

// allowAllChecker is a comparable PGIdentifierChecker: a struct with no fields.
type allowAllChecker struct{}

func (allowAllChecker) CheckPGIdentifier(string) error { return nil }

// TestPGRoleSpecComparabilityFollowsItsPolicy pins the comparability claim in the
// PGIdentifierCheckerFunc godoc: with a func-backed policy, == on otherwise-equal
// copies panics, == on specs that differ in an earlier field returns false without
// reaching the policy, and a map key always panics; a comparable policy leaves the
// spec comparable.
func TestPGRoleSpecComparabilityFollowsItsPolicy(t *testing.T) {
	funcPolicySpec := PGRoleSpec{
		Schema:           "tenant_a",
		MigratorRole:     "migrator",
		RuntimeRole:      "tenant_a_app",
		IdentifierPolicy: PGIdentifierCheckerFunc(func(string) error { return nil }),
	}

	t.Run("func_policy_panics_on_equality_of_otherwise_equal_copies", func(t *testing.T) {
		other := funcPolicySpec
		require.Panics(t, func() { _ = funcPolicySpec == other })
	})

	t.Run("earlier_field_difference_stops_before_the_policy", func(t *testing.T) {
		other := funcPolicySpec
		other.Schema = "tenant_b"
		var equal bool
		require.NotPanics(t, func() { equal = funcPolicySpec == other })
		if equal {
			t.Fatal("specs that differ in Schema must compare unequal")
		}
	})

	t.Run("func_policy_panics_as_map_key", func(t *testing.T) {
		specs := map[PGRoleSpec]struct{}{}
		require.Panics(t, func() { specs[funcPolicySpec] = struct{}{} })
	})

	t.Run("comparable_policy_keeps_spec_comparable", func(t *testing.T) {
		spec := PGRoleSpec{
			Schema:           "tenant_a",
			MigratorRole:     "migrator",
			RuntimeRole:      "tenant_a_app",
			IdentifierPolicy: allowAllChecker{},
		}
		other := spec
		if spec != other {
			t.Fatal("a spec holding a comparable policy must compare equal to its copy")
		}
	})
}

// The exported entrypoint runs Validate, so a refusing policy must stop it
// before any statement is composed.
func TestPGRoleProvisioningSQLHonoursIdentifierPolicy(t *testing.T) {
	spec := &PGRoleSpec{
		Schema:       "tenant_a",
		MigratorRole: "m",
		RuntimeRole:  "r",
		IdentifierPolicy: PGIdentifierCheckerFunc(func(string) error {
			return errTestPolicyRejected
		}),
	}
	stmts, err := PGRoleProvisioningSQL(spec)
	require.ErrorIs(t, err, errTestPolicyRejected)
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	assert.Empty(t, stmts)
}

// TestPGRoleDetectSQLMatchesMigrationsAtom pins each C65.4 detect query the
// integration oracles run to the SQL fence wiki/migrations.md publishes. The
// oracles need a container, so without this a fence could drift from its const —
// and publish a query nothing tested — in any run that skips them. The consts are
// read by parsing roles_integration_test.go, so the pin needs no build tag.
func TestPGRoleDetectSQLMatchesMigrationsAtom(t *testing.T) {
	consts := detectSQLConsts(t)
	fences := c654AtomSQLFences(t)

	for _, tt := range []struct {
		name      string
		constName string
	}{
		{name: "public_grant", constName: "pgPublicGrantDetectSQL"},
		{name: "public_schema_residue", constName: "pgPublicSchemaResidueDetectSQL"},
		{name: "public_named_role_grant", constName: "pgPublicNamedRoleGrantDetectSQL"},
		{name: "reserved_role", constName: "pgReservedRoleDetectSQL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			query, ok := consts[tt.constName]
			require.Truef(t, ok, "%s must be declared in roles_integration_test.go", tt.constName)
			matches := 0
			for _, fence := range fences {
				if fence == query {
					matches++
				}
			}
			assert.Equalf(t, 1, matches, "%s must appear verbatim exactly once as an sql fence in the C65.4 atom", tt.constName)
		})
	}

	// Re-provisioning overwrites search_path, so a named-role query keyed on it
	// reads clean at verify over a grant the revokes missed.
	t.Run("named_role_query_ignores_search_path", func(t *testing.T) {
		assert.NotContains(t, consts["pgPublicNamedRoleGrantDetectSQL"], "search_path")
		assert.NotContains(t, consts["pgPublicNamedRoleGrantDetectSQL"], "pg_db_role_setting")
	})
}

// detectSQLConsts parses roles_integration_test.go and returns every string
// declaration whose name ends in DetectSQL, unquoted.
func detectSQLConsts(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "roles_integration_test.go", nil, 0)
	require.NoError(t, err)

	consts := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		valueSpec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range valueSpec.Names {
			if strings.HasSuffix(name.Name, "DetectSQL") && i < len(valueSpec.Values) {
				consts[name.Name] = unquoteStringLit(t, name.Name, valueSpec.Values[i])
			}
		}
		return false
	})
	return consts
}

// unquoteStringLit returns the value of a string literal expression.
func unquoteStringLit(t *testing.T, name string, expr ast.Expr) string {
	t.Helper()
	lit, ok := expr.(*ast.BasicLit)
	require.Truef(t, ok, "%s must be a string literal", name)
	value, err := strconv.Unquote(lit.Value)
	require.NoError(t, err, name)
	return value
}

// c654AtomSQLFences returns the body of every sql fence in the C65.4 atom of
// wiki/migrations.md, with the atom's two-space list indent removed.
func c654AtomSQLFences(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "wiki", "migrations.md"))
	require.NoError(t, err)
	// A Windows checkout may carry CRLF line endings; the fences are matched on LF.
	doc := strings.ReplaceAll(string(raw), "\r\n", "\n")

	start := strings.Index(doc, "### [C65.4]")
	require.NotEqual(t, -1, start, "the C65.4 atom must exist")
	end := strings.Index(doc[start:], "\n- ref: ")
	require.NotEqual(t, -1, end, "the C65.4 atom must end in its ref line")
	atom := doc[start : start+end]

	blocks := strings.Split(atom, "\n  ```sql\n")[1:]
	fences := make([]string, 0, len(blocks))
	for _, block := range blocks {
		body, _, found := strings.Cut(block, "\n  ```\n")
		require.True(t, found, "every sql fence in the C65.4 atom must close")
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			lines[i] = strings.TrimPrefix(line, "  ")
		}
		fences = append(fences, strings.Join(lines, "\n"))
	}
	return fences
}

// recordingRoleExecutor is a database.Executor that captures every statement it
// is handed and can fail at a chosen index, so a test can pin the statement
// list, its order, and the error wrap without a database.
type recordingRoleExecutor struct {
	stmts   []string
	failAt  int
	failErr error
}

func newRecordingRoleExecutor() *recordingRoleExecutor {
	return &recordingRoleExecutor{failAt: -1}
}

// Query completes the database.Executor surface; provisioning never queries.
func (r *recordingRoleExecutor) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New("Query is unused by the provisioning path")
}

func (r *recordingRoleExecutor) Exec(_ context.Context, query string, _ ...any) (sql.Result, error) {
	r.stmts = append(r.stmts, query)
	if len(r.stmts)-1 == r.failAt {
		return nil, r.failErr
	}
	return driver.RowsAffected(0), nil
}

// valueRoleExecutor is a non-pointer database.Executor, so isNilExecutor's
// non-nil-able default arm decides it.
type valueRoleExecutor struct{ stmts *[]string }

// Query completes the database.Executor surface; provisioning never queries.
func (valueRoleExecutor) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, errors.New("Query is unused by the provisioning path")
}

func (v valueRoleExecutor) Exec(_ context.Context, query string, _ ...any) (sql.Result, error) {
	*v.stmts = append(*v.stmts, query)
	return driver.RowsAffected(0), nil
}

// txDoorSpec is the spec the ProvisionPGRolesTx tests provision. Both passwords
// are set so the optional ALTER ROLE ... PASSWORD statements are in the list.
func txDoorSpec() *PGRoleSpec {
	return &PGRoleSpec{
		Schema:           "tenant_tx",
		MigratorRole:     "mig_tx",
		MigratorPassword: "mig-tx-pw",
		RuntimeRole:      "rt_tx",
		RuntimePassword:  "rt-tx-pw",
	}
}

// Both doors must reach the identical statement list, in the identical order,
// with and without the skip options that live in the shared builder. The
// *sql.DB door is driven through sqlmock so what it actually executed is
// observed, not assumed.
func TestProvisionPGRolesTxRunsTheSameStatementsAsTheSQLDBDoor(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*PGRoleSpec)
	}{
		{name: "default_spec", apply: func(*PGRoleSpec) {}},
		{
			name: "skip_options",
			apply: func(s *PGRoleSpec) {
				s.SkipMigratorRole = true
				s.SkipFloorReassert = true
				s.MigratorPassword = ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := txDoorSpec()
			tt.apply(spec)
			want, err := PGRoleProvisioningSQL(spec)
			require.NoError(t, err)
			require.NotEmpty(t, want)

			exec := newRecordingRoleExecutor()
			require.NoError(t, ProvisionPGRolesTx(context.Background(), exec, spec))
			require.Equal(t, want, exec.stmts, "the tx door must execute the published list verbatim, in order")

			var viaSQLDB []string
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(
				sqlmock.QueryMatcherFunc(func(_, actualSQL string) error {
					viaSQLDB = append(viaSQLDB, actualSQL)
					return nil
				})))
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			mock.MatchExpectationsInOrder(true)
			for range want {
				mock.ExpectExec("").WillReturnResult(sqlmock.NewResult(0, 0))
			}

			require.NoError(t, ProvisionPGRoles(context.Background(), db, spec))
			require.NoError(t, mock.ExpectationsWereMet())
			require.Equal(t, want, viaSQLDB, "the *sql.DB door must execute the same list, in the same order")
		})
	}
}

// The tx door runs Validate, so a refusing IdentifierPolicy must stop it before
// the executor is touched at all.
func TestProvisionPGRolesTxStopsBeforeExecWhenPolicyRefuses(t *testing.T) {
	spec := txDoorSpec()
	spec.IdentifierPolicy = PGIdentifierCheckerFunc(func(string) error { return errTestPolicyRejected })

	exec := newRecordingRoleExecutor()
	err := ProvisionPGRolesTx(context.Background(), exec, spec)
	require.ErrorIs(t, err, errTestPolicyRejected)
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	assert.Empty(t, exec.stmts, "a refused spec must reach no statement at all")
}

func TestProvisionPGRolesTxRejectsNilSpec(t *testing.T) {
	exec := newRecordingRoleExecutor()
	err := ProvisionPGRolesTx(context.Background(), exec, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil *PGRoleSpec")
	assert.Empty(t, exec.stmts)
}

func TestProvisionPGRolesTxRejectsNilExecutor(t *testing.T) {
	err := ProvisionPGRolesTx(context.Background(), nil, txDoorSpec())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil database.Executor")
}

// A non-nil-able executor lands on isNilExecutor's default arm, which must not
// over-refuse it: provisioning has to run the full statement list through it.
func TestProvisionPGRolesTxAcceptsAValueExecutor(t *testing.T) {
	spec := txDoorSpec()
	want, err := PGRoleProvisioningSQL(spec)
	require.NoError(t, err)
	require.NotEmpty(t, want)

	var stmts []string
	exec := valueRoleExecutor{stmts: &stmts}
	require.NotEqual(t, reflect.Pointer, reflect.ValueOf(database.Executor(exec)).Kind(),
		"premise: the boxed value must not be nil-able")

	require.NoError(t, ProvisionPGRolesTx(context.Background(), exec, spec))
	require.Equal(t, want, stmts, "a value executor must run the published list verbatim, in order")
}

// typedNilExecutor returns a nil *recordingRoleExecutor already boxed in the
// interface. The boxing has to happen behind an interface-typed return or
// staticcheck reads the concrete assignment and calls the premise check below
// dead (SA4023) — the very property the check exists to assert.
func typedNilExecutor() database.Executor {
	return (*recordingRoleExecutor)(nil)
}

// A typed-nil executor is a non-nil interface, so a plain `exec == nil` guard
// misses it and the call panics on the nil receiver inside the loop.
func TestProvisionPGRolesTxRejectsTypedNilExecutor(t *testing.T) {
	exec := typedNilExecutor()
	// Pin the premise: the interface is non-nil while the value inside it is nil.
	// Not a testify assertion: require.NotNil unwraps the pointer and would fail
	// on the very value this test needs, and testifylint rewrites any comparison
	// form into it.
	if exec == nil {
		t.Fatal("premise: a typed nil must box into a non-nil interface")
	}
	v := reflect.ValueOf(exec)
	require.Equal(t, reflect.Pointer, v.Kind(), "premise: the boxed value must be a pointer")
	require.True(t, v.IsNil(), "premise: the interface holds a nil pointer")

	spec := txDoorSpec()
	require.NoError(t, spec.Validate(), "the spec must be valid, so only the executor guard can refuse")

	err := ProvisionPGRolesTx(context.Background(), exec, spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil database.Executor")
}

// The tx door's wrap must name the step index and the redacted statement,
// exactly as the *sql.DB door's does. Step 2 is the migrator's
// ALTER ROLE ... PASSWORD, so the same failure pins index and redaction at once.
func TestProvisionPGRolesTxWrapNamesStepAndRedactedStatement(t *testing.T) {
	spec := txDoorSpec()
	want, err := PGRoleProvisioningSQL(spec)
	require.NoError(t, err)
	require.Contains(t, want[2], "PASSWORD", "step 2 is the migrator password statement")

	boom := errors.New("exec blew up")
	exec := newRecordingRoleExecutor()
	exec.failAt = 2
	exec.failErr = boom

	err = ProvisionPGRolesTx(context.Background(), exec, spec)
	require.ErrorIs(t, err, boom)
	assert.Contains(t, err.Error(), "provisioning step 2 (")
	assert.Contains(t, err.Error(), "[REDACTED]")
	assert.NotContains(t, err.Error(), spec.MigratorPassword)
	assert.Len(t, exec.stmts, 3, "the loop must stop at the failing statement")
}

// expectFloorRow queues the pg_roles read CheckPGRoleFloor issues for role,
// answering with the five attributes in the floor's order.
func expectFloorRow(mock sqlmock.Sqlmock, role string, attrs [5]bool) {
	mock.ExpectQuery(`FROM pg_catalog\.pg_roles WHERE rolname = \$1`).WithArgs(role).WillReturnRows(
		sqlmock.NewRows([]string{"rolsuper", "rolcreatedb", "rolcreaterole", "rolreplication", "rolbypassrls"}).
			AddRow(attrs[0], attrs[1], attrs[2], attrs[3], attrs[4]))
}

func TestCheckPGRoleFloorPassesARoleAtTheFloor(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	expectFloorRow(mock, "rt", [5]bool{})

	require.NoError(t, CheckPGRoleFloor(context.Background(), db, "rt"))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckPGRoleFloorNamesEveryAttributeAboveTheFloor(t *testing.T) {
	const prefix = `migration: role holds attributes above the provisioning floor: role "rt" holds `
	tests := []struct {
		name  string
		attrs [5]bool
		want  string
	}{
		{name: "superuser", attrs: [5]bool{true, false, false, false, false}, want: "SUPERUSER"},
		{name: "createdb", attrs: [5]bool{false, true, false, false, false}, want: "CREATEDB"},
		{name: "createrole", attrs: [5]bool{false, false, true, false, false}, want: "CREATEROLE"},
		{name: "replication", attrs: [5]bool{false, false, false, true, false}, want: "REPLICATION"},
		{name: "bypassrls", attrs: [5]bool{false, false, false, false, true}, want: "BYPASSRLS"},
		{name: "all_five", attrs: [5]bool{true, true, true, true, true}, want: "SUPERUSER, CREATEDB, CREATEROLE, REPLICATION, BYPASSRLS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer func() { _ = db.Close() }()
			expectFloorRow(mock, "rt", tt.attrs)

			err = CheckPGRoleFloor(context.Background(), db, "rt")
			require.ErrorIs(t, err, ErrPGRoleFloorViolated)
			assert.EqualError(t, err, prefix+tt.want)
		})
	}
}

func TestCheckPGRoleFloorReportsAMissingRole(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	mock.ExpectQuery(`FROM pg_catalog\.pg_roles`).WithArgs("gone").WillReturnRows(
		sqlmock.NewRows([]string{"rolsuper", "rolcreatedb", "rolcreaterole", "rolreplication", "rolbypassrls"}))

	err = CheckPGRoleFloor(context.Background(), db, "gone")
	require.ErrorIs(t, err, ErrPGRoleNotFound)
	assert.NotErrorIs(t, err, ErrPGRoleFloorViolated)
}

func TestCheckPGRoleFloorWrapsAQueryFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	boom := errors.New("connection reset")
	mock.ExpectQuery(`FROM pg_catalog\.pg_roles`).WithArgs("rt").WillReturnError(boom)

	err = CheckPGRoleFloor(context.Background(), db, "rt")
	require.ErrorIs(t, err, boom)
	require.NotErrorIs(t, err, ErrPGRoleNotFound)
	assert.NotErrorIs(t, err, ErrPGRoleFloorViolated)
}

func TestCheckPGRoleFloorRejectsAnInvalidIdentifierBeforeQuerying(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	err = CheckPGRoleFloor(context.Background(), db, `bad"role`)
	require.ErrorIs(t, err, ErrInvalidPGIdentifier)
	require.NoError(t, mock.ExpectationsWereMet(), "an invalid role must not reach the database")
}

func TestCheckPGRoleFloorRejectsNilDB(t *testing.T) {
	err := CheckPGRoleFloor(context.Background(), nil, "rt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil *sql.DB")
}
