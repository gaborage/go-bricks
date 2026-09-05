package sqllex

import "regexp"

// The SQL identifier grammar every door validates an identifier argument against
// before it becomes SQL syntax (ADR-031, ADR-082). It lives here because two
// packages judge against it — the query builder and the columns package — and the
// builder imports the columns package, so neither can own it without a cycle.
const (
	// IdentifierSegment is the SHAPE alphabet — the union of both vendors'
	// unquoted identifier characters, used to decide where one segment ends and
	// the next begins. It is deliberately NOT the vendor's own grammar: `#` is
	// an identifier character on Oracle and an operator on PostgreSQL, so which
	// characters a segment may actually carry is the vendor's answer, given by
	// database/identifier and reached through the builder's renderer seam
	// (ADR-100). Judging an argument here alone would accept `a#b` on
	// PostgreSQL, which is what #1202 fixed.
	//
	// IdentifierSegment is a single unquoted identifier: a leading letter or
	// underscore followed by letters, digits, underscore, $ or # — the same
	// alphabet enforced for db-tag struct fields by the columns package.
	IdentifierSegment = `[A-Za-z_][A-Za-z0-9_$#]*`

	// quotedSegment is a single double-quoted identifier with NO embedded quotes
	// or escapes. This is exactly the shape the framework's own vendor quoting
	// emits for reserved words (e.g. Oracle "level"); allowing it lets the
	// type-safe Columns()/cols.Col() output flow through unchanged while still
	// rejecting attacker-supplied quote payloads (which would carry spaces,
	// additional quotes, semicolons, or comment markers).
	quotedSegment = `"` + IdentifierSegment + `"`

	// Segment matches either form.
	Segment = `(?:` + IdentifierSegment + `|` + quotedSegment + `)`
)

// bareIdentifierPattern matches exactly one segment and nothing else.
var bareIdentifierPattern = regexp.MustCompile(`^` + Segment + `$`)

// unquotedIdentifierPattern matches exactly one UNQUOTED segment. Narrower than
// bareIdentifierPattern, which also admits the framework's quoted reserved-word
// form.
var unquotedIdentifierPattern = regexp.MustCompile(`^` + IdentifierSegment + `$`)

// IsBareIdentifier reports whether s is a single identifier segment — unquoted,
// or in the framework's own quoted reserved-word form (`"level"`). This is the
// grammar the table argument already applies to the alias half of "users u".
//
// The check is deliberately not preceded by a trim: a caller's value is judged
// exactly as it will be interpolated, so validating a trimmed value while
// rendering the untrimmed one cannot let the two disagree (ADR-082).
func IsBareIdentifier(s string) bool {
	return bareIdentifierPattern.MatchString(s)
}

// IsUnquotedIdentifier reports whether s is a single UNQUOTED identifier segment.
// It differs from IsBareIdentifier in rejecting the quoted reserved-word form
// (`"level"`), for the doors where a quoted spelling has no meaning — an
// expression alias is one: the framework never emits a quoted alias, so accepting
// one would widen the grammar for caller-supplied text alone.
//
// Like IsBareIdentifier it does not trim: the value is judged exactly as it will
// be interpolated (ADR-082).
func IsUnquotedIdentifier(s string) bool {
	return unquotedIdentifierPattern.MatchString(s)
}
