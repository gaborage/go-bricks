package migration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database/identifier"
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
