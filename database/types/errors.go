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

	// ErrEmptyExpressionSQL is returned when Expr() is called with empty SQL.
	ErrEmptyExpressionSQL = errors.New("expression SQL cannot be empty")

	// ErrTooManyAliases is returned when Expr() is called with more than 1 alias.
	ErrTooManyAliases = errors.New("expression accepts maximum 1 alias")

	// ErrDangerousAlias is returned when an alias contains SQL injection patterns.
	ErrDangerousAlias = errors.New("alias contains dangerous characters")

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
