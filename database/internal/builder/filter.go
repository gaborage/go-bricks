package builder

import (
	"reflect"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// Filter represents a composable WHERE clause filter that can be combined with AND/OR/NOT operators.
// Filters are created through FilterFactory methods and maintain vendor-specific quoting rules.
//
// Note: Filter does NOT apply placeholder formatting (?, $1, :1) - that's handled by squirrel's
// StatementBuilder when the full query is built. This ensures proper sequential numbering
// across multiple WHERE clauses.
type Filter struct {
	sqlizer squirrel.Sqlizer
}

// ToSql generates the SQL fragment and arguments for this filter.
// This method implements the squirrel.Sqlizer interface (inherited by dbtypes.Filter).
//
//nolint:revive // ToSql is required by squirrel.Sqlizer interface (lowercase 's')
func (f Filter) ToSql() (sql string, args []any, err error) {
	return f.sqlizer.ToSql()
}

// ToSQL is a convenience method with idiomatic Go naming (uppercase SQL).
// It delegates to ToSql() for actual implementation.
func (f Filter) ToSQL() (sql string, args []any, err error) {
	return f.ToSql()
}

// Verify Filter implements dbtypes.Filter interface (which embeds squirrel.Sqlizer)
var _ dbtypes.Filter = Filter{}

// FilterFactory provides methods for creating type-safe filters with automatic vendor-specific quoting.
// Obtain a FilterFactory through QueryBuilder.Filter().
type FilterFactory struct {
	qb *QueryBuilder
}

// Verify FilterFactory implements dbtypes.FilterFactory interface
var _ dbtypes.FilterFactory = (*FilterFactory)(nil)

// ========== Factory Method ==========

// newFilterFactory creates a new FilterFactory bound to the provided QueryBuilder.
// This is an internal method - users should call qb.Filter() instead.
func newFilterFactory(qb *QueryBuilder) *FilterFactory {
	return &FilterFactory{qb: qb}
}

// ========== Comparison Operators ==========

// Eq creates an equality filter (column = value).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Eq(column string, value any) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.Eq(column, value)}
}

// NotEq creates a not-equal filter (column <> value).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) NotEq(column string, value any) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.NotEq(column, value)}
}

// Lt creates a less-than filter (column < value).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Lt(column string, value any) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.Lt(column, value)}
}

// Lte creates a less-than-or-equal filter (column <= value).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Lte(column string, value any) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.LtOrEq(column, value)}
}

// Gt creates a greater-than filter (column > value).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Gt(column string, value any) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.Gt(column, value)}
}

// Gte creates a greater-than-or-equal filter (column >= value).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Gte(column string, value any) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.GtOrEq(column, value)}
}

// normalizeToSlice ensures the value is a slice for IN/NOT IN operations.
// This prevents squirrel.Eq from generating "column = ?" instead of "column IN (?)".
//
// Squirrel's Eq checks value type at runtime:
//   - Slice/Array → generates "column IN (?, ?, ...)"
//   - Scalar → generates "column = ?"
//
// By normalizing scalars to single-element slices, we ensure consistent IN semantics.
func normalizeToSlice(value any) any {
	if value == nil {
		return []any{}
	}

	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		// Already a slice/array - return as-is
		return value
	default:
		// Scalar value - wrap in single-element slice
		return []any{value}
	}
}

// In creates an IN filter (column IN (values...)).
// Accepts both slices and scalar values. Scalars are automatically wrapped in a slice.
// Column names are automatically quoted according to database vendor rules.
//
// Examples:
//
//	f.In("status", []string{"active", "pending"})  // IN with multiple values
//	f.In("status", "active")                       // IN with single value (wrapped automatically)
func (ff *FilterFactory) In(column string, values any) dbtypes.Filter {
	quotedColumn := ff.qb.quoteColumnForQuery(column)
	normalized := normalizeToSlice(values)
	return Filter{sqlizer: squirrel.Eq{quotedColumn: normalized}}
}

// NotIn creates a NOT IN filter (column NOT IN (values...)).
// Accepts both slices and scalar values. Scalars are automatically wrapped in a slice.
// Column names are automatically quoted according to database vendor rules.
//
// Examples:
//
//	f.NotIn("status", []string{"deleted", "banned"})  // NOT IN with multiple values
//	f.NotIn("status", "deleted")                      // NOT IN with single value (wrapped automatically)
func (ff *FilterFactory) NotIn(column string, values any) dbtypes.Filter {
	quotedColumn := ff.qb.quoteColumnForQuery(column)
	normalized := normalizeToSlice(values)
	return Filter{sqlizer: squirrel.NotEq{quotedColumn: normalized}}
}

// Like creates a case-insensitive LIKE filter.
// This uses vendor-specific case-insensitive logic:
// - PostgreSQL: Uses ILIKE operator
// - Oracle: Uses UPPER() function on both column and value
// - Other vendors: Uses standard LIKE
func (ff *FilterFactory) Like(column, pattern string) dbtypes.Filter {
	return Filter{sqlizer: ff.qb.BuildCaseInsensitiveLike(column, pattern)}
}

// Null creates an IS NULL filter.
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Null(column string) dbtypes.Filter {
	quotedColumn := ff.qb.quoteColumnForQuery(column)
	return Filter{sqlizer: squirrel.Eq{quotedColumn: nil}}
}

// NotNull creates an IS NOT NULL filter.
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) NotNull(column string) dbtypes.Filter {
	quotedColumn := ff.qb.quoteColumnForQuery(column)
	return Filter{sqlizer: squirrel.NotEq{quotedColumn: nil}}
}

// Between creates a BETWEEN filter (column BETWEEN lowerBound AND upperBound).
// Column names are automatically quoted according to database vendor rules.
func (ff *FilterFactory) Between(column string, lowerBound, upperBound any) dbtypes.Filter {
	quotedColumn := ff.qb.quoteColumnForQuery(column)
	condition := squirrel.And{
		squirrel.GtOrEq{quotedColumn: lowerBound},
		squirrel.LtOrEq{quotedColumn: upperBound},
	}
	return Filter{sqlizer: condition}
}

// ========== Logical Operators ==========

// And combines multiple filters with AND logic.
// Returns a filter that matches when ALL provided filters match.
// Nil filters are treated as no-ops and skipped.
//
// Example:
//
//	f := qb.Filter()
//	filter := f.And(
//	    f.Eq("status", "active"),
//	    f.Gt("age", 18),
//	)
func (ff *FilterFactory) And(filters ...dbtypes.Filter) dbtypes.Filter {
	sqlizers := make(squirrel.And, 0, len(filters))
	for _, filter := range filters {
		if filter == nil {
			continue // Skip nil filters - treat as no-op
		}
		// Extract the underlying squirrel.Sqlizer
		// We know all filters are actually our Filter type
		if concreteFilter, ok := filter.(Filter); ok {
			sqlizers = append(sqlizers, concreteFilter.sqlizer)
		} else {
			// Fallback: use the filter as-is (it implements Sqlizer via ToSQL)
			sqlizers = append(sqlizers, filter)
		}
	}
	return Filter{sqlizer: sqlizers}
}

// Or combines multiple filters with OR logic.
// Returns a filter that matches when ANY provided filter matches.
// Nil filters are treated as no-ops and skipped.
//
// Example:
//
//	f := qb.Filter()
//	filter := f.Or(
//	    f.Eq("status", "active"),
//	    f.Eq("role", "admin"),
//	)
func (ff *FilterFactory) Or(filters ...dbtypes.Filter) dbtypes.Filter {
	sqlizers := make(squirrel.Or, 0, len(filters))
	for _, filter := range filters {
		if filter == nil {
			continue // Skip nil filters - treat as no-op
		}
		// Extract the underlying squirrel.Sqlizer
		if concreteFilter, ok := filter.(Filter); ok {
			sqlizers = append(sqlizers, concreteFilter.sqlizer)
		} else {
			// Fallback: use the filter as-is
			sqlizers = append(sqlizers, filter)
		}
	}
	return Filter{sqlizer: sqlizers}
}

// Not negates a filter.
// Returns a filter that matches when the provided filter does NOT match.
//
// Example:
//
//	f := qb.Filter()
//	filter := f.Not(f.Eq("status", "deleted"))
func (ff *FilterFactory) Not(filter dbtypes.Filter) dbtypes.Filter {
	// Extract the underlying sqlizer
	var baseSqlizer squirrel.Sqlizer
	if concreteFilter, ok := filter.(Filter); ok {
		baseSqlizer = concreteFilter.sqlizer
	} else {
		baseSqlizer = filter
	}

	// Use squirrel.Expr to wrap the filter in NOT()
	sql, args, err := baseSqlizer.ToSql()
	if err != nil {
		// If ToSql fails, return a filter that will propagate the error
		return Filter{sqlizer: baseSqlizer}
	}
	return Filter{sqlizer: squirrel.Expr("NOT ("+sql+")", args...)}
}

// ========== Raw Escape Hatch ==========

// Raw creates a filter from raw SQL with manual placeholder handling.
//
// WARNING: This method bypasses all identifier quoting and SQL injection protection.
// It is the caller's responsibility to:
//   - Properly quote any identifiers (especially Oracle reserved words like "number", "level", "size")
//   - Ensure the SQL fragment is valid for the target database
//   - Never concatenate user input directly into the condition string
//
// Use this method ONLY when the type-safe methods cannot express your condition.
// For Oracle, remember to quote reserved words: Raw(`"number" = ?`, value)
//
// Examples:
//
//	f.Raw(`"number" = ?`, accountNumber)  // Oracle reserved word
//	f.Raw(`ROWNUM <= ?`, 10)              // Oracle-specific syntax
//	f.Raw(`ST_Distance(location, ?) < ?`, point, radius) // Spatial queries
func (ff *FilterFactory) Raw(condition string, args ...any) dbtypes.Filter {
	return Filter{sqlizer: squirrel.Expr(condition, args...)}
}
