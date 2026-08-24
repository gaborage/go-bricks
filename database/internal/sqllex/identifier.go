package sqllex

import "regexp"

// The SQL identifier grammar every door validates an identifier argument against
// before it becomes SQL syntax (ADR-031, ADR-082). It lives here because two
// packages judge against it — the query builder and the columns package — and the
// builder imports the columns package, so neither can own it without a cycle.
const (
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
