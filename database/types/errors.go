//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"errors"
	"fmt"
)

// Sentinel errors for validation failures in type constructors.
// These can be used with errors.Is() for programmatic error checking.
var (
	// ErrEmptyTableName is returned when Table() is called with an empty name.
	ErrEmptyTableName = errors.New("table name cannot be empty")

	// ErrEmptyTableAlias is returned when TableRef.As() is called with an empty alias.
	ErrEmptyTableAlias = errors.New("table alias cannot be empty")

	ErrNilTableRef = errors.New("table reference cannot be nil")

	// ErrOrderingOperandNotComparable is returned when an ordering comparison
	// (<, <=, >, >=) is given an operand that is not a scalar. There is no
	// rendering for one — `col < NULL` is never true whatever the data, and a set
	// has no ordinal meaning — so the door refuses instead of emitting SQL that
	// silently matches nothing.
	//
	// The refused forms are judged AFTER the operand is resolved, so they are
	// wider than their surface spelling: nil; a slice; an array (`[3]int{…}`,
	// which the builder classifies as a list exactly as it does a slice); a typed
	// nil pointer (`(*int)(nil)`, the ordinary spelling of an optional field),
	// which is dereferenced to nil; and a `driver.Valuer` whose Value() resolves
	// to NULL (`sql.NullString{}`). A `[]byte` and a Valuer HOLDING a value stay
	// scalars and are compared normally. A Valuer whose Value() FAILS is a
	// different error: that cause is reported wrapped, not as this sentinel.
	//
	// It lives here rather than in the builder because the builder is an internal
	// package: a sentinel a consumer cannot import is a sentinel a consumer
	// cannot match.
	ErrOrderingOperandNotComparable = errors.New("ordering comparison requires a scalar operand")

	// ErrEmptyExpressionSQL is returned when Expr() is called with empty SQL.
	ErrEmptyExpressionSQL = errors.New("expression SQL cannot be empty")

	// ErrTooManyAliases is returned when Expr() is called with more than 1 alias.
	ErrTooManyAliases = errors.New("expression accepts maximum 1 alias")

	// ErrInvalidAlias is returned when a RawExpression alias is not a bare
	// unquoted identifier. It replaces the former ErrDangerousAlias substring
	// denylist, which accepted everything it did not list.
	ErrInvalidAlias = errors.New("alias must be an unquoted identifier: a letter or underscore followed by letters, digits, underscore, $ or #")

	// ErrAliasInHaving is returned when a RawExpression carrying an alias is passed
	// to Having. HAVING takes a predicate, not a projected column, so an alias has
	// nowhere to render; accepting one silently would drop it.
	ErrAliasInHaving = errors.New("alias is not allowed in a HAVING predicate")

	// ErrAliasInValue is returned when a RawExpression carrying an alias is passed
	// to Set, SetMap or Values. A SET or VALUES cell projects nothing, so an alias
	// has nowhere to render; accepting one silently would drop it.
	ErrAliasInValue = errors.New("alias is not allowed in a SET or VALUES value")

	// ErrNilSubquery is returned when ValidateSubquery() is called with nil subquery.
	ErrNilSubquery = errors.New("subquery cannot be nil")

	// ErrInvalidSubquery is returned when subquery validation fails.
	ErrInvalidSubquery = errors.New("invalid subquery")

	// ErrEmptySubquerySQL is returned when subquery produces empty SQL.
	ErrEmptySubquerySQL = errors.New("subquery produced empty SQL")
)

// InvalidAliasError reports an alias argument that the identifier grammar
// refuses. Columns.As panics with this value rather than returning an error:
// its signature has no error channel, and an alias is a developer constant, so a
// violation is a programming error surfaced at construction rather than a
// deferred query error.
//
// It is a distinct type so a recovery site can report the panic by TYPE without
// rendering the refused alias (ADR-081), and so a caller can match it:
//
//	var invalid *dbtypes.InvalidAliasError
//	if errors.As(recovered.(error), &invalid) { ... }
type InvalidAliasError struct {
	// Alias is the refused alias, exactly as it was passed.
	Alias string
}

func (e *InvalidAliasError) Error() string {
	return fmt.Sprintf("invalid table alias %q: must be a bare identifier "+
		"(e.g. \"u\") — an alias becomes SQL syntax and is validated before interpolation", e.Alias)
}
