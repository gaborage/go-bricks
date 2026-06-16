package builder

import (
	"fmt"
	"regexp"
	"strings"
)

// SQL identifier grammar used to validate the direct-string builder APIs
// (From, OrderBy, GroupBy, Set, SetMap) BEFORE the value is interpolated into the
// SQL string. Validation runs on ALL vendors so these APIs cannot be used as a
// SQL injection vector (M9 / ADR-031). Complex expressions must go through
// qb.Expr() / Raw(), which carry an explicit security annotation.
const (
	// identifierSegment is a single unquoted identifier: a leading letter or
	// underscore followed by letters, digits, underscore, $ or # — the same
	// alphabet enforced for db-tag struct fields by the columns package.
	identifierSegment = `[A-Za-z_][A-Za-z0-9_$#]*`

	// quotedSegment is a single double-quoted identifier with NO embedded quotes
	// or escapes. This is exactly the shape the framework's own vendor quoting
	// emits for reserved words (e.g. Oracle "level"); allowing it lets the
	// type-safe Columns()/cols.Col() output flow through Set/Where unchanged while
	// still rejecting attacker-supplied quote payloads (which would contain spaces,
	// additional quotes, semicolons, or comment markers).
	quotedSegment = `"` + identifierSegment + `"`

	// segment matches either form.
	segment = `(?:` + identifierSegment + `|` + quotedSegment + `)`

	// qualified matches a simple or dot-qualified identifier: "col", "table.col",
	// "schema.table.col", and the quoted variants ("schema"."number", u."level").
	qualified = segment + `(\.` + segment + `)*`
)

// validIdentifierPattern matches a simple or qualified identifier.
var validIdentifierPattern = regexp.MustCompile(`^` + qualified + `$`)

// validClauseIdentifierPattern extends validIdentifierPattern with the bounded
// ORDER BY / GROUP BY direction grammar the public API documents and accepts:
//
//	col
//	col ASC | col DESC
//	col ASC NULLS FIRST | col DESC NULLS LAST   (and the other combinations)
//
// Anything outside this grammar (additional whitespace-separated tokens,
// semicolons, comment sequences, parentheses) is rejected so attacker-controlled
// clause arguments cannot smuggle a second statement or comment.
var validClauseIdentifierPattern = regexp.MustCompile(
	`^` + qualified + `( (?i:ASC|DESC))?( (?i:NULLS) (?i:FIRST|LAST))?$`,
)

// validTableNamePattern extends validIdentifierPattern with an optional trailing
// table alias ("users u", "schema.users u") — the inline-alias form the From/JOIN
// string APIs already accept alongside the explicit Table("users").As("u") helper.
// The alias is a bare identifier; anything beyond a single trailing identifier is
// rejected so the table argument cannot smuggle additional SQL.
var validTableNamePattern = regexp.MustCompile(
	`^` + qualified + `( ` + segment + `)?$`,
)

// validateIdentifier rejects identifier arguments (column names, UPDATE SET
// targets) that fall outside the safe simple/qualified-identifier grammar.
// Returns a descriptive error naming the rejected value.
func validateIdentifier(context, identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if !validIdentifierPattern.MatchString(trimmed) {
		return fmt.Errorf("invalid %s identifier %q: must be a simple or qualified identifier "+
			"matching %s — use qb.Expr()/Raw() for complex expressions", context, identifier, identifierSegment)
	}
	return nil
}

// validateTableName rejects table-name arguments that fall outside the safe
// simple/qualified-identifier grammar plus an optional inline alias ("users u").
func validateTableName(identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if !validTableNamePattern.MatchString(trimmed) {
		return fmt.Errorf("invalid table identifier %q: must be a simple or qualified identifier "+
			"with an optional alias (e.g. \"users\" or \"users u\") — use qb.Expr()/Raw() for complex expressions",
			identifier)
	}
	return nil
}

// validateClauseIdentifier rejects ORDER BY / GROUP BY arguments that fall
// outside the safe identifier-plus-optional-direction grammar. The bounded
// trailing direction (ASC/DESC [NULLS FIRST|LAST]) is permitted; everything
// else — extra tokens, semicolons, comment markers — is rejected.
func validateClauseIdentifier(context, identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if !validClauseIdentifierPattern.MatchString(trimmed) {
		return fmt.Errorf("invalid %s identifier %q: must be a simple or qualified identifier with an "+
			"optional ASC/DESC [NULLS FIRST|LAST] direction — use qb.Expr()/Raw() for complex expressions",
			context, identifier)
	}
	return nil
}
