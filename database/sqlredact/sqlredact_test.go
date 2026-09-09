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
			want: "ALTER ROLE \"svc\" PASSWORD\n  '[REDACTED]'",
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
			name: "bare_token_keeps_trailing_clause",
			in:   `CREATE USER app IDENTIFIED BY s3cret DEFAULT TABLESPACE users`,
			want: `CREATE USER app IDENTIFIED BY [REDACTED] DEFAULT TABLESPACE users`,
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "s3cret")
		})
	}
}

func TestStatementLeavesNonCredentialSQLUnchanged(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "password_null", in: `ALTER ROLE "svc" PASSWORD NULL`},
		{name: "identified_externally", in: `CREATE USER app IDENTIFIED EXTERNALLY`},
		{name: "identified_globally", in: `CREATE USER app IDENTIFIED GLOBALLY AS 'CN=app'`},
		{name: "quoted_value_in_where", in: `SELECT id FROM t WHERE name = 'hunter2'`},
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

// TestContainsFold covers the pre-filter directly. The end-of-string case pins
// the loop bound: no credential clause can END at the statement's last byte (a
// match needs its literal after the keyword), so a bound that stopped one byte
// early would go unnoticed through Statement alone.
func TestContainsFold(t *testing.T) {
	tests := []struct {
		name   string
		s      string
		needle string
		want   bool
	}{
		{name: "exact_lowercase", s: "password", needle: "password", want: true},
		{name: "at_end_of_string", s: "alter role x password", needle: "password", want: true},
		{name: "at_start_of_string", s: "password 'x'", needle: "password", want: true},
		{name: "uppercase_input", s: "ALTER ROLE X PASSWORD 'x'", needle: "password", want: true},
		{name: "mixed_case_input", s: "alter user a Identified By b", needle: "identified", want: true},
		{name: "absent", s: "SELECT 1", needle: "password", want: false},
		{name: "truncated_needle_at_end", s: "alter role x passwor", needle: "password", want: false},
		{name: "needle_longer_than_input", s: "pass", needle: "password", want: false},
		{name: "empty_input", s: "", needle: "password", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, containsFold(tt.s, tt.needle))
		})
	}
}

// TestStatementResistsBypass covers credential shapes a quote-blind pattern
// misses or mis-anchors. Each case was an observed leak before the scanner
// replaced the two regexps.
func TestStatementResistsBypass(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "decoy_literal_ending_in_keyword",
			in:     `SELECT 'PASSWORD '; ALTER ROLE r PASSWORD 'REALSECRET'`,
			want:   `SELECT 'PASSWORD '; ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "REALSECRET",
		},
		{
			name:   "decoy_literal_with_doubled_quote",
			in:     `COMMENT ON ROLE r IS 'PASSWORD '''; ALTER ROLE r PASSWORD 'REALSECRET'`,
			want:   `COMMENT ON ROLE r IS 'PASSWORD '''; ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "REALSECRET",
		},
		{
			name:   "dollar_quoted_untagged",
			in:     `ALTER ROLE app PASSWORD $$S3cret!$$`,
			want:   `ALTER ROLE app PASSWORD '[REDACTED]'`,
			secret: "S3cret!",
		},
		{
			name:   "dollar_quoted_tagged",
			in:     `CREATE ROLE app LOGIN PASSWORD $tag$p'w$tag$ VALID UNTIL 'infinity'`,
			want:   `CREATE ROLE app LOGIN PASSWORD '[REDACTED]' VALID UNTIL 'infinity'`,
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
			name:   "block_comment_before_literal",
			in:     `ALTER ROLE app PASSWORD /*c*/ 'S3cret!'`,
			want:   `ALTER ROLE app PASSWORD /*c*/ '[REDACTED]'`,
			secret: "S3cret!",
		},
		{
			name:   "line_comment_before_literal",
			in:     "ALTER ROLE app PASSWORD -- c\n'S3cret!'",
			want:   "ALTER ROLE app PASSWORD -- c\n'[REDACTED]'",
			secret: "S3cret!",
		},
		{
			name:   "oracle_replace_clause_hides_old_password",
			in:     `ALTER USER app IDENTIFIED BY newpw REPLACE oldpw`,
			want:   `ALTER USER app IDENTIFIED BY [REDACTED] REPLACE [REDACTED]`,
			secret: "oldpw",
		},
		{
			name:   "oracle_identified_by_values_verifier",
			in:     `CREATE USER app IDENTIFIED BY VALUES 'S:ABCDEF0123456789'`,
			want:   `CREATE USER app IDENTIFIED BY VALUES [REDACTED]`,
			secret: "S:ABCDEF0123456789",
		},
		{
			name:   "oracle_comment_between_identified_and_by",
			in:     `ALTER USER app IDENTIFIED /*c*/ BY S3cret`,
			want:   `ALTER USER app IDENTIFIED /*c*/ BY [REDACTED]`,
			secret: "S3cret",
		},
		{
			name:   "single_quoted_value_with_space",
			in:     `CREATE USER app IDENTIFIED BY 'S3 cret'`,
			want:   `CREATE USER app IDENTIFIED BY [REDACTED]`,
			secret: "S3 cret",
		},
		{
			name:   "multiple_clauses_all_redacted",
			in:     `CREATE USER a IDENTIFIED BY pw1; ALTER ROLE b PASSWORD 'pw2'`,
			want:   `CREATE USER a IDENTIFIED BY [REDACTED]; ALTER ROLE b PASSWORD '[REDACTED]'`,
			secret: "pw1",
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

// TestStatementDoesNotOverRedact covers statements whose text merely mentions a
// credential keyword. A scrub that mangles ordinary SQL costs diagnosability and
// can leave unbalanced quotes in a log line.
func TestStatementDoesNotOverRedact(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "keyword_inside_string_literal", in: `SELECT * FROM notes WHERE body = 'user IDENTIFIED BY bob'`},
		{name: "keyword_as_column_name", in: `SELECT password FROM users`},
		{name: "keyword_as_column_prefix", in: `SELECT id FROM users WHERE password_changed_at > '2026-01-01'`},
		{name: "keyword_in_dml_assignment", in: `UPDATE users SET password = 'already-hashed'`},
		{name: "keyword_as_quoted_identifier", in: `SELECT "password" FROM users ORDER BY identified_by`},
		{name: "password_bound_parameter", in: `ALTER ROLE app PASSWORD $1`},
		{name: "insert_of_literal_mentioning_keyword", in: `INSERT INTO audit VALUES ('PASSWORD ''x''')`},
		{name: "dml_password_column_bound", in: `UPDATE users SET password = $1 WHERE id = $2`},
		{name: "select_password_hash_column", in: `SELECT password_hash FROM t`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.in, Statement(tt.in))
		})
	}
}

// TestStatementFailsClosedOnMalformedInput pins over-redaction as the safe
// failure. A literal, comment or dollar block that never closes after a
// credential keyword takes the rest of the statement with it, rather than
// letting the scanner lose state and emit an unscrubbed tail.
func TestStatementFailsClosedOnMalformedInput(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "unterminated_password_literal",
			in:     `ALTER ROLE r PASSWORD 'unterminated`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "unterminated",
		},
		{
			name:   "unterminated_dollar_quoted_password",
			in:     `ALTER ROLE r PASSWORD $tag$unterminated`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "unterminated",
		},
		{
			name:   "unterminated_oracle_quoted_value",
			in:     `ALTER USER app IDENTIFIED BY "unterminated value`,
			want:   `ALTER USER app IDENTIFIED BY [REDACTED]`,
			secret: "unterminated",
		},
		{
			name:   "unterminated_block_comment_before_literal",
			in:     `ALTER ROLE r PASSWORD /* never closed 'S3cret'`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "S3cret",
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

// TestStatementFailsClosedOnLostState covers the scanner losing track of where a
// literal, identifier or comment ends. Reading such a construct as running to
// the end of input is fail-OPEN — the scan simply stops and nothing downstream
// is redacted — so an unterminated region that still names a credential keyword
// must take the rest of the statement with it instead.
func TestStatementFailsClosedOnLostState(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{name: "cr_terminated_line_comment", in: "SELECT 1 --note\rALTER ROLE r PASSWORD 'S3cret'", secret: "S3cret"},
		{name: "non_ascii_dollar_tag", in: `ALTER ROLE r PASSWORD $tág$S3cret$tág$`, secret: "S3cret"},
		{name: "non_ascii_dollar_tag_earlier_in_statement", in: `SELECT $é$x$é$; ALTER ROLE r PASSWORD 'S3cret'`, secret: "S3cret"},
		{name: "dollar_inside_identifier", in: `SELECT foo$bar$baz FROM t; ALTER ROLE r PASSWORD 'S3cret'`, secret: "S3cret"},
		{name: "dollar_inside_identifier_after_a_redacted_clause", in: `ALTER ROLE r PASSWORD 'ok'; SELECT x$y$z; ALTER ROLE q PASSWORD 'S3cret'`, secret: "S3cret"},
		{name: "unterminated_quoted_identifier", in: `SELECT "a""b FROM t; ALTER ROLE r PASSWORD 'S3cret'`, secret: "S3cret"},
		{name: "unterminated_dollar_block", in: `SELECT $q$x ; ALTER ROLE r PASSWORD 'S3cret'`, secret: "S3cret"},
		{name: "unterminated_comment_after_by_values", in: `ALTER USER u IDENTIFIED BY VALUES /*S3cret`, secret: "S3cret"},
		{name: "unterminated_comment_after_replace", in: `ALTER USER u IDENTIFIED BY new REPLACE /*S3cret`, secret: "S3cret"},
		{name: "credential_inside_do_block", in: `DO $$ BEGIN ALTER ROLE r PASSWORD 'S3cret'; END $$`, secret: "S3cret"},
		{name: "credential_inside_function_body", in: `CREATE FUNCTION f() RETURNS void AS $$ ALTER ROLE r PASSWORD 'S3cret' $$ LANGUAGE sql`, secret: "S3cret"},
		{name: "mysql_identified_by_password_hash", in: `CREATE USER u IDENTIFIED BY PASSWORD 'S3cret'`, secret: "S3cret"},
		{name: "bare_value_containing_a_quote", in: `ALTER USER u IDENTIFIED BY ab'cd ef' PASSWORD 'S3cret'`, secret: "S3cret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.NotContains(t, got, tt.secret)
			assert.Contains(t, got, redactedMarker)
		})
	}
}

// TestStatementRecognisesOnlyRealDollarQuotes pins that a $tag$ delimiter is read
// only where one actually starts. Treating any `<punctuation><word>$` run as a
// delimiter carves a phantom body out of the statement and splits a real clause
// across two scan regions, which leaves the credential in clear.
func TestStatementRecognisesOnlyRealDollarQuotes(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{name: "dollar_quoted_value_after_an_earlier_one", in: `SELECT 'a', $q$b$q$; ALTER ROLE "r" PASSWORD $q$topsecret$q$`, secret: "topsecret"},
		{name: "password_containing_a_dollar", in: `SELECT 'a', $q$b$q$; ALTER ROLE "r" PASSWORD 'top $ecret'`, secret: "ecret"},
		{name: "bind_placeholder_earlier_in_statement", in: `SELECT $1, x ; ALTER ROLE "r" PASSWORD 'se $cret'`, secret: "cret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.NotContains(t, got, tt.secret)
			assert.Contains(t, got, redactedMarker)
		})
	}
}

// TestStatementFailsClosedAtMaxBodyDepth pins the recursion cap as a fail-closed
// boundary. Dropping a body the scanner declined to descend into would make
// nesting a way to hide a credential from the scrub.
func TestStatementFailsClosedAtMaxBodyDepth(t *testing.T) {
	in := `DO $L0$ $L1$ $L2$ $L3$ $L4$ ALTER ROLE r PASSWORD 'deepsecret' $L4$ $L3$ $L2$ $L1$ $L0$`
	got := Statement(in)
	assert.NotContains(t, got, "deepsecret")
	assert.Contains(t, got, redactedMarker)
}

// TestStatementRedactsKeywordShapedOraclePasswords covers a value that is
// spelled like the keyword that would introduce one. `password` and `with` are
// legal Oracle passwords, so declining them outright abandoned the clause and
// shipped both the new and the retired credential.
func TestStatementRedactsKeywordShapedOraclePasswords(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		secrets []string
	}{
		{
			name:    "password_as_the_password",
			in:      `ALTER USER u IDENTIFIED BY password REPLACE oldsecret`,
			want:    `ALTER USER u IDENTIFIED BY [REDACTED] REPLACE [REDACTED]`,
			secrets: []string{"oldsecret"},
		},
		{
			name:    "with_as_the_password",
			in:      `ALTER USER u IDENTIFIED BY with REPLACE oldsecret`,
			want:    `ALTER USER u IDENTIFIED BY [REDACTED] REPLACE [REDACTED]`,
			secrets: []string{"oldsecret"},
		},
		{
			name:    "mysql_hash_still_redacted",
			in:      `CREATE USER u IDENTIFIED BY PASSWORD 'mysqlhash'`,
			want:    `CREATE USER u IDENTIFIED BY PASSWORD '[REDACTED]'`,
			secrets: []string{"mysqlhash"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Statement(tt.in)
			assert.Equal(t, tt.want, got)
			for _, secret := range tt.secrets {
				assert.NotContains(t, got, secret)
			}
		})
	}
}
