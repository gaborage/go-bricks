//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import "fmt"

// subqueryValidator allows builders to expose lightweight validation
// without forcing eager SQL generation.
type subqueryValidator interface {
	ValidateForSubquery() error
}

// Subquery Support
//
// Subquery support is implemented by reusing SelectQueryBuilder.
// Any SelectQueryBuilder instance can be used as a subquery in filter expressions.
//
// Example usage:
//
//	subquery := qb.Select("id").From("active_users").Where(f.Eq("status", "active"))
//	query := qb.Select("*").From("orders").Where(f.Exists(subquery))
//
// Supported patterns:
//   - EXISTS(subquery) - f.Exists(subquery)
//   - NOT EXISTS(subquery) - f.NotExists(subquery)
//   - column IN(subquery) - f.InSubquery(column, subquery)
//
// Correlated subqueries (referencing outer query columns) are supported by using
// JoinFilter.EqColumn() and other column-to-column comparison methods within the subquery.

// ValidateSubquery checks if a subquery is valid for use in filter expressions.
// Returns an error if subquery is nil or produces invalid SQL.
//
// Returns:
//   - ErrNilSubquery if subquery is nil
//   - ErrInvalidSubquery if subquery validation fails
//   - ErrEmptySubquerySQL if subquery produces empty SQL
//
// This function is called internally by filter implementations to ensure subqueries
// are constructed correctly before query execution.
func ValidateSubquery(subquery SelectQueryBuilder) error {
	if subquery == nil {
		return ErrNilSubquery
	}

	if validator, ok := subquery.(subqueryValidator); ok {
		if err := validator.ValidateForSubquery(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidSubquery, err)
		}
		return nil
	}

	// Test ToSQL() to catch construction errors early
	sql, _, err := subquery.ToSQL()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSubquery, err)
	}
	if sql == "" {
		return ErrEmptySubquerySQL
	}

	return nil
}

// MustValidateSubquery is like ValidateSubquery but panics on error.
// Use this only when errors indicate programming bugs that should crash immediately.
func MustValidateSubquery(subquery SelectQueryBuilder) {
	if err := ValidateSubquery(subquery); err != nil {
		panic(fmt.Sprintf("MustValidateSubquery: %v", err))
	}
}
