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
	// The repetition is NON-capturing: patterns below wrap this in a group of
	// their own so a renderer can read the identifier back, and a capture here
	// would shift every later group index.
	qualified = sqllex.Segment + `(?:\.` + sqllex.Segment + `)*`
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
// The groups are NAMED, and the Oracle clause renderer reads them through
// SubexpIndex rather than by position: it quotes the identifier and re-appends
// the rest instead of splitting on whitespace and understanding only two tokens
// (#1156). Positional indices made that coupling silent — inserting a group
// renumbers the others, and the renderer emits wrong SQL with nothing failing to
// compile. A name that stops existing fails loudly instead.
var validClauseIdentifierPattern = regexp.MustCompile(
	`^(?P<ident>` + qualified + `)(?P<dir> (?i:ASC|DESC))?(?P<nulls> (?i:NULLS) (?i:FIRST|LAST))?$`,
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
// Named for the same reason as validClauseIdentifierPattern: the Oracle table
// renderer quotes the identifier and keeps the alias, reading the same grammar
// the validator enforces instead of hand-rolling a second split (#1156).
var validTableNamePattern = regexp.MustCompile(
	`^(?P<ident>` + qualified + `)(?P<alias> ` + sqllex.Segment + `)?$`,
)

// normalizeAgainst is the one shape every identifier validator has: trim, match
// the trimmed value, and return THAT value so the caller renders what was judged.
// Returning the normalized identifier is the contract itself — validating a
// trimmed value while rendering the untrimmed one is what let `Select("t.* ")`
// render as `t."*"` on Oracle (ADR-082) and left padded identifiers in the SQL at
// every other door (#1158). mkErr builds the rejection, so each door keeps its own
// wording.
func (qb *QueryBuilder) normalizeAgainst(pattern *regexp.Regexp, identifier string, mkErr func() error) (normalized string, err error) {
	trimmed := strings.TrimSpace(identifier)
	match := pattern.FindStringSubmatch(trimmed)
	if match == nil {
		return "", mkErr()
	}
	if err := qb.validateVendorSegments(identifier, pattern, match); err != nil {
		return "", err
	}
	return trimmed, nil
}

// validateVendorSegments applies the VENDOR's segment alphabet to every
// identifier position the shape grammar just accepted. The two grammars answer
// different questions and both have to hold: the pattern says which tokens of
// the argument are identifiers (and which are a direction keyword, an alias or
// the wildcard), and the renderer says which characters this vendor takes in
// one bare segment — `#` is an Oracle identifier character and a PostgreSQL
// operator, so only the vendor can judge it (#1202, ADR-100).
//
// The identifier-bearing tokens are read through the patterns' NAMED groups, the
// same contract the Oracle clause and table renderers read, so inserting a group
// cannot renumber what gets judged the way a positional read would. It does NOT
// by itself guarantee a future identifier-bearing group would be seen — only
// `ident` and `alias` are read — so the closed vocabulary of group names is
// asserted by TestIdentifierPatternGroupNamesAreKnown. A pattern with no `ident`
// group is entirely one identifier, wildcard included.
// Quoted segments are skipped: a quoted identifier is legal on both vendors
// whatever it contains, and it is the framework's own reserved-word form.
func (qb *QueryBuilder) validateVendorSegments(argument string, pattern *regexp.Regexp, match []string) error {
	// match[0] is the whole match, which every pattern here anchors, so it is the
	// trimmed value the caller judged.
	tokens := []string{match[0]}
	if i := pattern.SubexpIndex("ident"); i > 0 {
		tokens = []string{match[i]}
		if a := pattern.SubexpIndex("alias"); a > 0 && match[a] != "" {
			tokens = append(tokens, strings.TrimSpace(match[a]))
		}
	}
	for _, token := range tokens {
		for _, segment := range sqllex.SplitIdentifierSegments(token) {
			if segment == "*" || sqllex.IsQuotedIdentifier(segment) {
				continue
			}
			if err := qb.renderer.ValidateCharset(segment); err != nil {
				return fmt.Errorf("invalid identifier %q for %s: %w", argument, qb.vendor, err)
			}
		}
	}
	return nil
}

// validateIdentifier rejects identifier arguments (column names, table names/
// aliases, UPDATE SET targets) that fall outside the safe simple/qualified-
// identifier grammar.
// Returns a descriptive error naming the rejected value.
func (qb *QueryBuilder) validateIdentifier(context, identifier string) (normalized string, err error) {
	return qb.normalizeAgainst(validIdentifierPattern, identifier, func() error {
		return fmt.Errorf("invalid %s identifier %q: must be a simple or qualified identifier "+
			"matching %s — use qb.Expr()/Raw() for complex expressions", context, identifier, sqllex.Segment)
	})
}

// validateTableName rejects table-name arguments that fall outside the safe
// simple/qualified-identifier grammar plus an optional inline alias ("users u").
func (qb *QueryBuilder) validateTableName(identifier string) (normalized string, err error) {
	return qb.normalizeAgainst(validTableNamePattern, identifier, func() error {
		return fmt.Errorf("invalid table identifier %q: must be a simple or qualified identifier "+
			"with an optional alias (e.g. \"users\" or \"users u\") — use qb.Expr()/Raw() for complex expressions",
			identifier)
	})
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
func (qb *QueryBuilder) validateSelectIdentifier(identifier string) (normalized string, err error) {
	return qb.normalizeAgainst(validSelectIdentifierPattern, identifier, func() error {
		return fmt.Errorf("invalid select identifier %q: must be a simple or qualified identifier, "+
			"or a wildcard (\"*\", \"t.*\") — use qb.Expr()/Raw() for expressions and aliases",
			identifier)
	})
}

// validateClauseIdentifier rejects ORDER BY / GROUP BY arguments that fall
// outside the safe identifier-plus-optional-direction grammar. The bounded
// trailing direction (ASC/DESC [NULLS FIRST|LAST]) is permitted; everything
// else — extra tokens, semicolons, comment markers — is rejected.
func (qb *QueryBuilder) validateClauseIdentifier(context, identifier string) (normalized string, err error) {
	return qb.normalizeAgainst(validClauseIdentifierPattern, identifier, func() error {
		return fmt.Errorf("invalid %s identifier %q: must be a simple or qualified identifier with an "+
			"optional ASC/DESC [NULLS FIRST|LAST] direction — use qb.Expr()/Raw() for complex expressions",
			context, identifier)
	})
}
