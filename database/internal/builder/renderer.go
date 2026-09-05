package builder

import (
	"fmt"

	"github.com/Masterminds/squirrel"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// vendorRenderer is the query builder's rendering seam. The builder decides
// WHICH identifier a clause names and validates it; the renderer decides HOW
// that identifier is spelled for the vendor. Splitting the two is what keeps a
// quoting rule in one place: the capture-quote-splice shape behind FROM and
// behind ORDER BY drifted apart precisely because each clause carried its own
// `switch qb.vendor` (#1156, e7816760).
//
// The adapter is chosen once, in NewQueryBuilder, and held on QueryBuilder, so
// a dispatch point is a method call rather than a vendor test. Every RENDERING
// method takes an already-validated identifier: the renderer renders, it does
// not judge. The one judgement it does supply is ValidateCharset, the vendor's
// unquoted-segment grammar — which characters this vendor accepts is a vendor
// fact and belongs beside the vendor's quoting, while WHICH tokens of an
// argument are identifier positions stays the door's question (#1202).
// EscapeIdentifier is deliberately NOT here — it is exported, so it is a
// trust boundary rather than a post-validation step.
// Every method here is a rendering that differs in KIND between the vendors:
// three quoting ones — reserved-word quoting, alias splitting, direction/NULLS
// parsing — and six expression ones, where the vendors disagree about the
// operator (ILIKE vs a folded LIKE), the shape (POSIX operators vs REGEXP_LIKE),
// the function name (NOW/SYSDATE, gen_random_uuid/SYS_GUID), the argument type
// (native bool vs NUMBER(1)), or whether the predicate exists at all.
// Anything a vendor does the SAME way stays in the builder: a column LIST is
// rendered element-wise everywhere, and `*` is a wildcard everywhere, so those
// loops belong to the funnels rather than to two identical adapter methods.
// A difference in statement SHAPE rather than spelling — Oracle's OFFSET/FETCH
// pagination, MERGE for an upsert, `FROM dual` for a table-less SELECT — stays
// inline in the builder behind a vendor test too: it is one clause the builder
// owns, not a rendering every method would repeat.
type vendorRenderer interface {
	// QuoteColumn renders a single column reference for a SELECT/WHERE/SET
	// position, preserving the caller's case verbatim.
	QuoteColumn(column string) string
	// QuoteTable renders a FROM/JOIN table argument, keeping an inline alias
	// ("users u") outside the quoted identifier.
	QuoteTable(table string) string
	// QuoteIdentifierForClause renders an ORDER BY / GROUP BY item, keeping the
	// direction and NULLS ordering keywords outside the quoted identifier.
	QuoteIdentifierForClause(identifier string) string

	// ValidateCharset reports whether segment is legal as ONE bare, unquoted
	// identifier segment on this vendor. The door splits an argument into
	// segments and skips the quoted ones and the wildcard; this answers only
	// the character question, and only for the vendor's own alphabet — the
	// byte cap is not judged here.
	ValidateCharset(segment string) error

	// CaseInsensitiveLike renders a case-insensitive containment match. The
	// column arrives quoted and the pattern arrives already wrapped in its
	// wildcards, because both are the builder's decision; whether the vendor
	// spells the match as an operator or as a folded comparison is the
	// renderer's.
	CaseInsensitiveLike(quotedColumn, likePattern string) squirrel.Sqlizer
	// Regex renders a regular-expression match. A vendor with no regex
	// predicate returns an errorSqlizer: an unsupported expression is a
	// rendering outcome, not a builder precondition.
	Regex(quotedColumn, pattern string, caseInsensitive, negated bool) squirrel.Sqlizer
	// JSONContains renders a JSON containment predicate. The column is passed
	// as a deferred quoter rather than a quoted string so a vendor never pays
	// for a quote it will not use: two of the three never name a column here,
	// and the third abandons the predicate on a malformed payload.
	JSONContains(value any, quoteColumn func() (string, error)) squirrel.Sqlizer
	// CurrentTimestamp renders the vendor's current-timestamp function.
	CurrentTimestamp() string
	// UUIDGeneration renders the vendor's UUID-generation function.
	UUIDGeneration() string
	// BooleanValue renders a Go bool as the vendor's boolean argument.
	BooleanValue(value bool) any
}

// rendererFor picks the adapter for a vendor. Identifier quoting has two
// behaviors, but expression rendering has three: an unrecognized vendor renders
// identifiers exactly as PostgreSQL does, yet the deleted `default:` arms spelled
// four expressions differently from the PostgreSQL arms beside them (LIKE rather
// than ILIKE, UUID() rather than gen_random_uuid(), and no regex or JSON
// containment at all). defaultRenderer is where that third behavior lives.
func rendererFor(vendor dbtypes.Vendor) vendorRenderer {
	switch vendor {
	case dbtypes.Oracle:
		return oracleRenderer{}
	case dbtypes.PostgreSQL:
		return postgresRenderer{}
	default:
		return defaultRenderer{vendor: vendor}
	}
}

// defaultRenderer is the unknown-vendor behavior class that already exists on
// main: generic SQL where a generic spelling exists, and an unsupported-feature
// error where none does. It is NOT a supported vendor and is not a hook for
// adding one — GoBricks supports PostgreSQL and Oracle, and a third vendor would
// arrive as its own adapter with its own tests, not by growing this one.
//
// It serves a vendor the builder does not recognize. It embeds
// postgresRenderer because every identifier-quoting method, the timestamp
// function and the boolean argument were literally the PostgreSQL arm's
// behavior; it overrides exactly the four expressions where the `default:` arm
// said something else. It carries the vendor name because two of those four
// report it.
type defaultRenderer struct {
	postgresRenderer
	vendor dbtypes.Vendor
}

var _ vendorRenderer = defaultRenderer{}

// CaseInsensitiveLike renders a plain LIKE: ILIKE is a PostgreSQL extension, so
// an unknown vendor gets the standard operator and the caller's case.
func (defaultRenderer) CaseInsensitiveLike(quotedColumn, likePattern string) squirrel.Sqlizer {
	return squirrel.Like{quotedColumn: likePattern}
}

// Regex reports that the vendor has no regex predicate this builder can spell.
func (r defaultRenderer) Regex(_, _ string, _, _ bool) squirrel.Sqlizer {
	return errorSqlizer{err: fmt.Errorf("regex matching is not supported for vendor %q", r.vendor)}
}

// JSONContains reports that the vendor has no containment predicate this
// builder can spell. The column is never quoted and the payload never encoded:
// neither can change the outcome.
func (r defaultRenderer) JSONContains(_ any, _ func() (string, error)) squirrel.Sqlizer {
	return errorSqlizer{err: fmt.Errorf("JSONContains: unsupported vendor %q", r.vendor)}
}

// UUIDGeneration renders the SQL-standard-ish UUID() rather than PostgreSQL's
// gen_random_uuid(), which is a PostgreSQL function name.
func (defaultRenderer) UUIDGeneration() string { return "UUID()" }
