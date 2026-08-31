package builder

import (
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
// a dispatch point is a method call rather than a vendor test. Every method
// takes an already-validated identifier: the renderer renders, it does not
// judge. EscapeIdentifier is deliberately NOT here — it is exported, so it is a
// trust boundary rather than a post-validation step.
// The three methods are the three renderings that differ in KIND between the
// vendors — reserved-word quoting, alias splitting, direction/NULLS parsing.
// Anything a vendor does the SAME way stays in the builder: a column LIST is
// rendered element-wise everywhere, and `*` is a wildcard everywhere, so those
// loops belong to the funnels rather than to two identical adapter methods.
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
}

// rendererFor picks the adapter for a vendor. There are exactly two: Oracle
// quotes reserved and case-sensitive names, and every other vendor renders a
// validated identifier verbatim — the behavior postgresRenderer carries and the
// behavior the deleted `default:` arms had, which is why it also serves an
// unrecognized vendor.
func rendererFor(vendor dbtypes.Vendor) vendorRenderer {
	if vendor == dbtypes.Oracle {
		return oracleRenderer{}
	}
	return postgresRenderer{}
}
