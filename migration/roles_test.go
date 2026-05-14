package migration

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPGRoleSpecValidateAccepts(t *testing.T) {
	specs := []*PGRoleSpec{
		{Schema: "tenant_a", MigratorRole: "migrator", RuntimeRole: "tenant_a_app"},
		{Schema: "TenantA", MigratorRole: "Mig", RuntimeRole: "App"},
		{Schema: "_underscore_start", MigratorRole: "_m", RuntimeRole: "_r"},
		// Boundary: 63-char identifier (NAMEDATALEN-1).
		{
			Schema:       strings.Repeat("a", 63),
			MigratorRole: strings.Repeat("b", 63),
			RuntimeRole:  strings.Repeat("c", 63),
		},
	}
	for _, s := range specs {
		s := s
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
			assert.True(t, errors.Is(err, ErrInvalidPGIdentifier),
				"want wrapped ErrInvalidPGIdentifier, got %v", err)
			assert.Contains(t, err.Error(), tt.fieldOrReason)
		})
	}
}

func TestPGRoleProvisioningSQLRejectsInvalidSpec(t *testing.T) {
	_, err := PGRoleProvisioningSQL(&PGRoleSpec{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidPGIdentifier))
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

	// Role creation in a DO block (idempotent).
	assert.Contains(t, all, `CREATE ROLE "migrator"`)
	assert.Contains(t, all, `CREATE ROLE "tenant_a_app"`)
	assert.Contains(t, all, `pg_catalog.pg_roles WHERE rolname = 'migrator'`)
	assert.Contains(t, all, `pg_catalog.pg_roles WHERE rolname = 'tenant_a_app'`)

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
