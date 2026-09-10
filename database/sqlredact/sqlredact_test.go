package sqlredact

import (
	"strings"
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
		{name: "keyword_as_quoted_identifier", in: `SELECT "password" FROM users`},
		{name: "password_inside_a_connection_string", in: `CREATE SUBSCRIPTION s CONNECTION 'host=h password=x' PUBLICATION p`},
		{name: "password_assigned_by_expression", in: `UPDATE users SET password = crypt($1, gen_salt('bf'))`},
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
			name:   "block_comment_between_keyword_and_value",
			in:     `ALTER ROLE r1 PASSWORD /* rotate */ 'Sup3rS3cr3t'`,
			want:   `ALTER ROLE r1 PASSWORD '[REDACTED]'`,
			secret: "Sup3rS3cr3t",
		},
		{
			name:   "line_comment_between_keyword_and_value",
			in:     "ALTER ROLE r2 PASSWORD --rotate\n'Sup3rS3cr3t'",
			want:   `ALTER ROLE r2 PASSWORD '[REDACTED]'`,
			secret: "Sup3rS3cr3t",
		},
		{
			// An empty line comment puts the newline at offset zero of the search,
			// which a bound that rejects zero would read as never terminating.
			name:   "empty_line_comment_before_value",
			in:     "ALTER ROLE r PASSWORD --\n'Sup3rS3cr3t'",
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "Sup3rS3cr3t",
		},
		{
			name:   "comment_glued_to_keyword_and_value",
			in:     `ALTER ROLE r3 ENCRYPTED PASSWORD/*x*/'Sup3rS3cr3t'`,
			want:   `ALTER ROLE r3 ENCRYPTED PASSWORD '[REDACTED]'`,
			secret: "Sup3rS3cr3t",
		},
		{
			name:   "comment_between_identified_and_by",
			in:     `CREATE USER u1 IDENTIFIED /* rotate */ BY Sup3rS3cr3t`,
			want:   `CREATE USER u1 IDENTIFIED /* rotate */ BY [REDACTED]`,
			secret: "Sup3rS3cr3t",
		},
		{
			name:   "line_comment_between_identified_and_by",
			in:     "CREATE USER u2 IDENTIFIED --rotate\nBY Sup3rS3cr3t",
			want:   "CREATE USER u2 IDENTIFIED --rotate\nBY [REDACTED]",
			secret: "Sup3rS3cr3t",
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
			// The OPTIONS list's opening paren is left unbalanced by design.
			name:   "user_mapping_options_list",
			in:     `CREATE USER MAPPING FOR u SERVER s OPTIONS (user 'u', password 'S3cret')`,
			want:   `CREATE USER MAPPING FOR u SERVER s OPTIONS (user 'u', password '[REDACTED]'`,
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

// TestStatementHandlesTruncatedTails covers inputs that end mid-clause. Each one
// walks a bounds check to its last legal index, where an off-by-one reads past
// the end of the string.
func TestStatementHandlesTruncatedTails(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "identified_by_with_no_value",
			in:   `ALTER USER u IDENTIFIED BY`,
			want: `ALTER USER u IDENTIFIED BY [REDACTED]`,
		},
		{
			name: "escape_prefix_at_end_of_input",
			in:   `ALTER ROLE r PASSWORD E`,
			want: `ALTER ROLE r PASSWORD E`,
		},
		{
			name: "unicode_prefix_at_end_of_input",
			in:   `ALTER ROLE r PASSWORD U&`,
			want: `ALTER ROLE r PASSWORD U&`,
		},
		{
			name: "keyword_at_end_of_input",
			in:   `ALTER ROLE r PASSWORD`,
			want: `ALTER ROLE r PASSWORD`,
		},
		{
			name: "unterminated_line_comment_after_keyword",
			in:   `ALTER ROLE r PASSWORD --unclosed`,
			want: `ALTER ROLE r PASSWORD --unclosed`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Statement(tt.in))
		})
	}
}

// TestWordAt covers the whole-word match directly, because its boundary classes
// are what keep a `password` column from reading as the keyword and what let a
// keyword ending at the last byte still match.
func TestWordAt(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		i       int
		keyword string
		wantEnd int
		wantOK  bool
	}{
		{name: "exact_whole_string", sql: "password", i: 0, keyword: kwPassword, wantEnd: 8, wantOK: true},
		{name: "uppercase_folds", sql: "PASSWORD x", i: 0, keyword: kwPassword, wantEnd: 8, wantOK: true},
		{name: "ends_at_last_byte", sql: "a password", i: 2, keyword: kwPassword, wantEnd: 10, wantOK: true},
		{name: "runs_past_end", sql: "passwor", i: 0, keyword: kwPassword, wantOK: false},
		{name: "followed_by_underscore", sql: "password_hash", i: 0, keyword: kwPassword, wantOK: false},
		{name: "followed_by_digit", sql: "password9", i: 0, keyword: kwPassword, wantOK: false},
		{name: "followed_by_letter", sql: "passwords", i: 0, keyword: kwPassword, wantOK: false},
		{name: "preceded_by_letter", sql: "xpassword", i: 1, keyword: kwPassword, wantOK: false},
		{name: "preceded_by_digit", sql: "0password", i: 1, keyword: kwPassword, wantOK: false},
		{name: "preceded_by_underscore", sql: "_password", i: 1, keyword: kwPassword, wantOK: false},
		{name: "preceded_by_punctuation", sql: `"password`, i: 1, keyword: kwPassword, wantEnd: 9, wantOK: true},
		{name: "different_word", sql: "passwerd", i: 0, keyword: kwPassword, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			end, ok := wordAt(tt.sql, tt.i, tt.keyword)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantEnd, end)
			}
		})
	}
}

// TestByteClasses pins the character classes the word-boundary test is built
// from. Each case sits on a class edge, where widening or narrowing the range by
// one changes whether a keyword is recognized at all.
func TestByteClasses(t *testing.T) {
	wordBytes := []byte{'a', 'z', 'A', 'Z', '0', '9', '_', '5', 'm'}
	for _, c := range wordBytes {
		assert.Truef(t, isWordByte(c), "%q should be an identifier byte", string(c))
	}
	nonWordBytes := []byte{'/', ':', '@', '[', '`', '{', ' ', '\'', '"', '$', '-', '&', ';'}
	for _, c := range nonWordBytes {
		assert.Falsef(t, isWordByte(c), "%q should not be an identifier byte", string(c))
	}

	for _, c := range []byte{'a', 'z', 'A', 'Z', 'q'} {
		assert.Truef(t, isLetter(c), "%q should be a letter", string(c))
	}
	for _, c := range []byte{'`', '{', '@', '[', '0', '9', '_', ' '} {
		assert.Falsef(t, isLetter(c), "%q should not be a letter", string(c))
	}

	assert.Equal(t, byte('a'), lowerASCII('A'))
	assert.Equal(t, byte('z'), lowerASCII('Z'))
	assert.Equal(t, byte('q'), lowerASCII('q'))
	assert.Equal(t, byte('@'), lowerASCII('@'), "the byte below 'A' is left alone")
	assert.Equal(t, byte('['), lowerASCII('['), "the byte above 'Z' is left alone")
	assert.Equal(t, byte('5'), lowerASCII('5'))
}

// TestStatementFailsClosedOnUnterminatedComment pins the one gap the rule cannot
// read past. An unterminated block comment hides where the value starts, and
// this text reaches the log on the driver-error path, where malformed statements
// are exactly what arrives — so the keyword redacts rather than passing through.
func TestStatementFailsClosedOnUnterminatedComment(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "password_value_hidden_behind_it",
			in:     `ALTER ROLE r PASSWORD /* c 'sekret'`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "sekret",
		},
		{
			name:   "identified_by_hidden_behind_it",
			in:     `ALTER USER u IDENTIFIED /* x BY sekret`,
			want:   `ALTER USER u IDENTIFIED [REDACTED]`,
			secret: "sekret",
		},
		{
			// The trailing star forces the comment scanner to read the byte after
			// the last one, where an off-by-one bound reads past the string.
			name:   "ending_in_a_star",
			in:     `ALTER ROLE r PASSWORD /* c *'sekret'`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "sekret",
		},
		{
			name:   "ending_in_a_slash",
			in:     `ALTER ROLE r PASSWORD /* c /'sekret'`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "sekret",
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

// TestStatementFailsClosedOnAmbiguousComment covers the one place the two
// vendors read the same bytes differently: PostgreSQL nests block comments,
// Oracle closes at the first `*/`. A `*/` inside an Oracle password therefore
// ends the comment for the server but not for a nesting scanner, which would
// walk the gap past BY and find nothing to redact. A second `/*` is treated as
// unlocatable instead, so both readings stay safe.
func TestStatementFailsClosedOnAmbiguousComment(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   string
		secret string
	}{
		{
			name:   "oracle_non_nesting_close_inside_the_password",
			in:     `ALTER USER probeuser IDENTIFIED /*/**/ BY "a*/b"`,
			want:   `ALTER USER probeuser IDENTIFIED [REDACTED]`,
			secret: "a*/b",
		},
		{
			name:   "spaced_nested_opener_before_by",
			in:     `ALTER USER u IDENTIFIED /* /* */ BY "p*/x"`,
			want:   `ALTER USER u IDENTIFIED [REDACTED]`,
			secret: "p*/x",
		},
		{
			name:   "postgres_nested_comment_before_the_value",
			in:     `ALTER ROLE r PASSWORD /* a /* b */ c */ 'Sup3rS3cr3t'`,
			want:   `ALTER ROLE r PASSWORD '[REDACTED]'`,
			secret: "Sup3rS3cr3t",
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

// TestStatementFailsClosedOnOversizedGap pins the gap budget. Statement retries
// at every offset, so an unbounded separator run makes a statement of repeated
// comment openers quadratic on a path that runs for every tracked operation.
func TestStatementFailsClosedOnOversizedGap(t *testing.T) {
	in := "ALTER ROLE r PASSWORD --" + strings.Repeat("c", maxGapBytes+1) + "\n'Sup3rS3cr3t'"
	got := Statement(in)
	assert.Equal(t, `ALTER ROLE r PASSWORD '[REDACTED]'`, got)
	assert.NotContains(t, got, "Sup3rS3cr3t")
}
