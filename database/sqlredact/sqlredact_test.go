package sqlredact

import (
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
)

func TestStatementRedactsPostgresPassword(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "simple_literal",
			in:   `ALTER ROLE "svc" PASSWORD 'hunter2'`,
			want: `ALTER ROLE "svc" PASSWORD '[REDACTED]'`,
		},
		{
			name: "doubled_quote_escape",
			in:   `ALTER ROLE "svc" PASSWORD 'it''s'`,
			want: `ALTER ROLE "svc" PASSWORD '[REDACTED]'`,
		},
		{
			name: "lowercase_keyword_preserved",
			in:   `alter role "svc" password 'hunter2'`,
			want: `alter role "svc" password '[REDACTED]'`,
		},
		{
			name: "encrypted_prefix",
			in:   `CREATE ROLE "svc" LOGIN ENCRYPTED PASSWORD 'hunter2'`,
			want: `CREATE ROLE "svc" LOGIN ENCRYPTED PASSWORD '[REDACTED]'`,
		},
		{
			name: "newline_between_keyword_and_literal",
			in:   "ALTER ROLE \"svc\" PASSWORD\n  'hunter2'",
			want: `ALTER ROLE "svc" PASSWORD '[REDACTED]'`,
		},
		{
			name: "trailing_clause_is_dropped",
			in:   `CREATE ROLE app LOGIN PASSWORD 'hunter2' VALID UNTIL 'infinity'`,
			want: `CREATE ROLE app LOGIN PASSWORD '[REDACTED]'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "hunter2")
		})
	}
}

func TestStatementRedactsOracleIdentifiedBy(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "bare_token_drops_trailing_clause",
			in:   `CREATE USER app IDENTIFIED BY s3cret DEFAULT TABLESPACE users`,
			want: `CREATE USER app IDENTIFIED BY [REDACTED]`,
		},
		{
			name: "double_quoted_with_doubling",
			in:   `ALTER USER app IDENTIFIED BY "p""w"`,
			want: `ALTER USER app IDENTIFIED BY [REDACTED]`,
		},
		{
			name: "lowercase_keyword_preserved",
			in:   `alter user app identified by s3cret`,
			want: `alter user app identified by [REDACTED]`,
		},
		{
			name: "newline_before_by",
			in:   "ALTER USER app IDENTIFIED\n  BY s3cret",
			want: "ALTER USER app IDENTIFIED\n  BY [REDACTED]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "s3cret")
		})
	}
}

// TestStatementLeavesNonCredentialSQLUnchanged pins the queries a blunt rule
// must not touch: a keyword that names a column, or one that introduces no
// value at all.
func TestStatementLeavesNonCredentialSQLUnchanged(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "password_null", in: `ALTER ROLE "svc" PASSWORD NULL`},
		{name: "identified_externally", in: `CREATE USER app IDENTIFIED EXTERNALLY`},
		{name: "identified_globally", in: `CREATE USER app IDENTIFIED GLOBALLY AS 'CN=app'`},
		{name: "quoted_value_in_where", in: `SELECT id FROM t WHERE name = 'hunter2'`},
		{name: "dml_password_column_bound", in: `UPDATE users SET password = $1 WHERE id = $2`},
		{name: "select_password_hash_column", in: `SELECT id, password_hash FROM t`},
		{name: "select_password_column", in: `SELECT id, password FROM users WHERE email = $1`},
		{name: "keyword_in_comment", in: `/* password */ SELECT 1`},
		{name: "keyword_at_end_of_statement", in: `SELECT id FROM audit WHERE field = 'x' ORDER BY password`},
		{name: "empty", in: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.in, Statement(tt.in))
		})
	}
}

// TestStatementReturnsInputWhenNothingMatches pins the no-allocation contract:
// an unmatched statement comes back as the identical string header, not a copy.
func TestStatementReturnsInputWhenNothingMatches(t *testing.T) {
	in := `SELECT id FROM t WHERE name = 'hunter2'`
	out := Statement(in)
	assert.Same(t, unsafe.StringData(in), unsafe.StringData(out))
}

// TestStatementDropsTheTailThatCarriesTheSecret is the core of the rule. Every
// case here defeated an implementation that tried to parse the value: the
// credential shape is never examined, it is discarded with everything after the
// keyword. Each name is a bypass a literal-aware version leaked.
func TestStatementDropsTheTailThatCarriesTheSecret(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "decoy_literal_ending_in_keyword",
			in:     `SELECT 'PASSWORD '; ALTER ROLE r PASSWORD 'REALSECRET'`,
			want:   `SELECT 'PASSWORD '[REDACTED]'`,
			secret: "REALSECRET",
		},
		{
			name:   "dollar_quoted_untagged",
			in:     `ALTER ROLE app PASSWORD $$S3cret!$$`,
			want:   `ALTER ROLE app PASSWORD '[REDACTED]'`,
			secret: "S3cret",
		},
		{
			name:   "dollar_quoted_tagged",
			in:     `CREATE ROLE app LOGIN PASSWORD $tag$p'w$tag$ VALID UNTIL 'infinity'`,
			want:   `CREATE ROLE app LOGIN PASSWORD '[REDACTED]'`,
			secret: "p'w",
		},
		{
			name:   "escape_string_constant",
			in:     `CREATE ROLE app LOGIN PASSWORD E'S3cret\'!'`,
			want:   `CREATE ROLE app LOGIN PASSWORD '[REDACTED]'`,
			secret: "S3cret",
		},
		{
			name:   "unicode_escape_string_constant",
			in:     `CREATE ROLE app LOGIN PASSWORD U&'S3cret'`,
			want:   `CREATE ROLE app LOGIN PASSWORD '[REDACTED]'`,
			secret: "S3cret",
		},
		{
			name:   "oracle_replace_clause_hides_old_password",
			in:     `ALTER USER app IDENTIFIED BY newpw REPLACE oldpw`,
			want:   `ALTER USER app IDENTIFIED BY [REDACTED]`,
			secret: "oldpw",
		},
		{
			name:   "oracle_identified_by_values_verifier",
			in:     `CREATE USER app IDENTIFIED BY VALUES 'S:ABCDEF0123456789'`,
			want:   `CREATE USER app IDENTIFIED BY [REDACTED]`,
			secret: "S:ABCDEF0123456789",
		},
		{
			name:   "single_quoted_value_with_space",
			in:     `CREATE USER app IDENTIFIED BY 'S3 cret'`,
			want:   `CREATE USER app IDENTIFIED BY [REDACTED]`,
			secret: "S3 cret",
		},
		{
			name:   "unterminated_literal",
			in:     `ALTER ROLE r PASSWORD 'unterminated`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "unterminated",
		},
		{
			name:   "unterminated_dollar_block",
			in:     `ALTER ROLE r PASSWORD $tag$unterminated`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "unterminated",
		},
		{
			name:   "second_statement_carries_the_credential",
			in:     `SELECT $1, x ; ALTER ROLE "r" PASSWORD 'se $cret'`,
			want:   `SELECT $1, x ; ALTER ROLE "r" PASSWORD '[REDACTED]'`,
			secret: "cret",
		},
		{
			name:   "credential_inside_do_block",
			in:     `DO $$ BEGIN ALTER ROLE r PASSWORD 'S3cret'; END $$`,
			want:   `DO $$ BEGIN ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "S3cret",
		},
		{
			name:   "mysql_identified_by_password_hash",
			in:     `CREATE USER u IDENTIFIED BY PASSWORD 'S3cret'`,
			want:   `CREATE USER u IDENTIFIED BY [REDACTED]`,
			secret: "S3cret",
		},
		{
			name:   "multiple_clauses_first_wins_and_drops_the_rest",
			in:     `CREATE USER a IDENTIFIED BY pw1; ALTER ROLE b PASSWORD 'pw2'`,
			want:   `CREATE USER a IDENTIFIED BY [REDACTED]`,
			secret: "pw2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, tt.secret)
		})
	}
}

// TestStatementOverRedactsQuoteShapedNeighbours pins the rule's accepted cost.
// These statements carry no credential, but the keyword sits in front of
// something value-shaped, so the tail goes. Losing a log line's tail is the
// deliberate trade against examining the value.
func TestStatementOverRedactsQuoteShapedNeighbours(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keyword_inside_string_literal",
			in:   `SELECT * FROM notes WHERE body = 'user IDENTIFIED BY bob'`,
			want: `SELECT * FROM notes WHERE body = 'user IDENTIFIED BY [REDACTED]`,
		},
		{
			name: "keyword_as_quoted_identifier",
			in:   `SELECT "password" FROM users`,
			want: `SELECT "password '[REDACTED]'`,
		},
		{
			name: "insert_of_literal_mentioning_keyword",
			in:   `INSERT INTO audit VALUES ('PASSWORD ''x''')`,
			want: `INSERT INTO audit VALUES ('PASSWORD '[REDACTED]'`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Statement(tt.in))
		})
	}
}
