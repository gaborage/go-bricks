package builder

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
)

// SQL identifier grammar used to validate the direct-string builder APIs
// (From, OrderBy, GroupBy, Set, SetMap) BEFORE the value is interpolated into the
// SQL string. Validation runs on ALL vendors so these APIs cannot be used as a
// SQL injection vector (M9 / ADR-031). Complex expressions must go through
// qb.Expr() / Raw(), which carry an explicit security annotation.
// The segment productions are owned by sqllex: the columns package validates
// aliases and db tags against the same grammar and cannot import this package
// (builder imports columns).
const (
	// qualified matches a simple or dot-qualified identifier: "col", "table.col",
	// "schema.table.col", and the quoted variants ("schema"."number", u."level").
	qualified = sqllex.Segment + `(\.` + sqllex.Segment + `)*`
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

// validSelectIdentifierPattern extends validIdentifierPattern with the SELECT
// wildcard: `*` selects every column and `table.*` (or `schema.table.*`) every
// column of one table. Both are the documented idiom, neither is an identifier
// under the base grammar, and neither can carry a payload — the wildcard is the
// whole token or the trailing segment of an otherwise-qualified name. A function
// or computed expression is still not an identifier and goes through qb.Expr().
var validSelectIdentifierPattern = regexp.MustCompile(
	fmt.Sprintf(`^(?:\*|%s(?:\.\*)?)$`, qualified),
)

// validTableNamePattern extends validIdentifierPattern with an optional trailing
// table alias ("users u", "schema.users u") — the inline-alias form the From/JOIN
// string APIs already accept alongside the explicit Table("users").As("u") helper.
// The alias is a bare identifier; anything beyond a single trailing identifier is
// rejected so the table argument cannot smuggle additional SQL.
var validTableNamePattern = regexp.MustCompile(
	`^` + qualified + `( ` + sqllex.Segment + `)?$`,
)

// validateIdentifier rejects identifier arguments (column names, table names/
// aliases, UPDATE SET targets) that fall outside the safe simple/qualified-
// identifier grammar.
// Returns a descriptive error naming the rejected value.
func validateIdentifier(context, identifier string) error {
	trimmed := strings.TrimSpace(identifier)
	if !validIdentifierPattern.MatchString(trimmed) {
		return fmt.Errorf("invalid %s identifier %q: must be a simple or qualified identifier "+
			"matching %s — use qb.Expr()/Raw() for complex expressions", context, identifier, sqllex.IdentifierSegment)
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

// validateSelectIdentifier rejects SELECT column arguments that fall outside the
// safe identifier grammar plus the wildcard. A computed or function expression is
// not an identifier and must go through qb.Expr(), the same as for ORDER BY.
//
// It returns the NORMALIZED identifier, and callers must interpolate that rather
// than their own input. Validating a trimmed value while rendering the untrimmed
// one lets the two disagree: the renderer's wildcard bypass is a suffix test, so
// `t.* ` passed validation and then rendered as `t."*"` on Oracle — a blessed
// input the renderer mangles.
func validateSelectIdentifier(identifier string) (normalized string, err error) {
	trimmed := strings.TrimSpace(identifier)
	if !validSelectIdentifierPattern.MatchString(trimmed) {
		return "", fmt.Errorf("invalid select identifier %q: must be a simple or qualified identifier, "+
			"or a wildcard (\"*\", \"t.*\") — use qb.Expr()/Raw() for expressions and aliases",
			identifier)
	}
	return trimmed, nil
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
