//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import "errors"

// Sentinel errors for validation failures in type constructors.
// These can be used with errors.Is() for programmatic error checking.
var (
	// ErrEmptyTableName is returned when Table() is called with an empty name.
	ErrEmptyTableName = errors.New("table name cannot be empty")

	// ErrEmptyTableAlias is returned when TableRef.As() is called with an empty alias.
	ErrEmptyTableAlias = errors.New("table alias cannot be empty")

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
