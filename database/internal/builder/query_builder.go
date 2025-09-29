// Package builder provides cross-database query building utilities.
// This package implements vendor-specific SQL generation and identifier handling
// for PostgreSQL, Oracle, and other database backends.
package builder

import (
	"strings"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// QueryBuilder provides vendor-specific SQL query building.
// It wraps squirrel.StatementBuilderType with database-specific customizations
// for placeholder formats, identifier quoting, and function generation.
type QueryBuilder struct {
	vendor           dbtypes.Vendor
	statementBuilder squirrel.StatementBuilderType
}

// SelectQueryBuilder provides a type-safe interface for building SELECT queries
// with proper identifier quoting and vendor-specific optimizations.
type SelectQueryBuilder struct {
	qb            *QueryBuilder
	selectBuilder squirrel.SelectBuilder
	limit         uint64 // 0 means no limit
	offset        uint64 // 0 means no offset
}

// check if SelectQueryBuilder implements dbtypes.SelectQueryBuilder
var _ dbtypes.SelectQueryBuilder = (*SelectQueryBuilder)(nil)

// ========== QueryBuilder Methods ==========

// NewQueryBuilder creates a new query builder for the specified database vendor.
// It configures placeholder formats and prepares for vendor-specific SQL generation.
func NewQueryBuilder(vendor dbtypes.Vendor) *QueryBuilder {
	var sb squirrel.StatementBuilderType

	switch vendor {
	case dbtypes.PostgreSQL:
		// PostgreSQL uses $1, $2, ... placeholders
		sb = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar)
	case dbtypes.Oracle:
		// Oracle uses :1, :2, ... placeholders
		sb = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Colon)
	case dbtypes.MongoDB:
		panic("QueryBuilder is SQL-only; do not construct for MongoDB")
	default:
		// Default to question mark placeholders
		sb = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Question)
	}

	return &QueryBuilder{
		vendor:           vendor,
		statementBuilder: sb,
	}
}

// Vendor returns the database vendor string
func (qb *QueryBuilder) Vendor() string {
	return qb.vendor
}

// Select creates a SELECT query builder with vendor-specific column quoting.
// For Oracle, it applies identifier quoting to handle reserved words appropriately.
func (qb *QueryBuilder) Select(columns ...string) *SelectQueryBuilder {
	selectBuilder := qb.statementBuilder.Select(qb.quoteColumnsForSelect(columns...)...)
	return &SelectQueryBuilder{
		qb:            qb,
		selectBuilder: selectBuilder,
	}
}

// Insert creates an INSERT query builder for the specified table
func (qb *QueryBuilder) Insert(table string) squirrel.InsertBuilder {
	return qb.statementBuilder.Insert(table)
}

// InsertWithColumns creates an INSERT query builder with pre-specified columns.
// It applies vendor-specific column quoting to the provided column list.
func (qb *QueryBuilder) InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder {
	return qb.statementBuilder.Insert(table).Columns(qb.quoteColumnsForDML(columns...)...)
}

// Update creates an UPDATE query builder for the specified table
func (qb *QueryBuilder) Update(table string) squirrel.UpdateBuilder {
	return qb.statementBuilder.Update(table)
}

// Delete creates a DELETE query builder for the specified table
func (qb *QueryBuilder) Delete(table string) squirrel.DeleteBuilder {
	return qb.statementBuilder.Delete(table)
}

// BuildCaseInsensitiveLike creates a case-insensitive LIKE expression.
// The implementation varies by database vendor.
func (qb *QueryBuilder) BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer {
	likeValue := "%" + value + "%"

	switch qb.vendor {
	case dbtypes.PostgreSQL:
		// PostgreSQL uses ILIKE operator
		return squirrel.ILike{column: likeValue}
	case dbtypes.Oracle:
		// Oracle requires UPPER() for case-insensitive matching and quoted column names
		quotedColumn := qb.quoteColumnForQuery(column)
		return squirrel.Like{"UPPER(" + quotedColumn + ")": strings.ToUpper(likeValue)}
	default:
		// Default to standard LIKE
		return squirrel.Like{column: likeValue}
	}
}

// BuildCurrentTimestamp returns the current timestamp function for the database vendor
func (qb *QueryBuilder) BuildCurrentTimestamp() string {
	switch qb.vendor {
	case dbtypes.PostgreSQL:
		return "NOW()"
	case dbtypes.Oracle:
		return "SYSDATE"
	default:
		return "NOW()"
	}
}

// BuildUUIDGeneration returns the UUID generation function for the database vendor
func (qb *QueryBuilder) BuildUUIDGeneration() string {
	switch qb.vendor {
	case dbtypes.PostgreSQL:
		return "gen_random_uuid()"
	case dbtypes.Oracle:
		return "SYS_GUID()" // Oracle's UUID generation
	default:
		return "UUID()"
	}
}

// BuildBooleanValue converts a Go boolean to the appropriate database representation
func (qb *QueryBuilder) BuildBooleanValue(value bool) any {
	switch qb.vendor {
	case dbtypes.PostgreSQL:
		return value // PostgreSQL has native boolean support
	case dbtypes.Oracle:
		if value {
			return 1 // Oracle uses NUMBER(1) for boolean
		}
		return 0
	default:
		return value
	}
}

// EscapeIdentifier escapes a database identifier (table/column name) according to vendor rules
func (qb *QueryBuilder) EscapeIdentifier(identifier string) string {
	parts := strings.Split(identifier, ".")
	for i, part := range parts {
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			// Already quoted, skip
			continue
		}
		// All vendors now preserve case for quoted identifiers
		parts[i] = `"` + part + `"`
	}

	return strings.Join(parts, ".")
}

// quoteColumnsForSelect handles vendor-specific column name quoting for SELECT statements
func (qb *QueryBuilder) quoteColumnsForSelect(columns ...string) []string {
	switch qb.vendor {
	case dbtypes.Oracle:
		quoted := make([]string, 0, len(columns))
		for _, col := range columns {
			if col == "*" || strings.HasSuffix(col, ".*") {
				// Do not quote wildcard selectors
				quoted = append(quoted, col)
				continue
			}
			quoted = append(quoted, qb.quoteOracleColumn(col))
		}
		return quoted
	default:
		return columns
	}
}

// quoteColumnsForDML handles vendor-specific column name quoting for DML statements
func (qb *QueryBuilder) quoteColumnsForDML(columns ...string) []string {
	switch qb.vendor {
	case dbtypes.Oracle:
		return qb.quoteOracleColumnsForDML(columns...)
	default:
		return columns
	}
}

// quoteColumnForQuery handles vendor-specific column name quoting for query conditions
func (qb *QueryBuilder) quoteColumnForQuery(column string) string {
	switch qb.vendor {
	case dbtypes.Oracle:
		return qb.quoteOracleColumn(column)
	default:
		return column
	}
}

// Eq creates an equality condition with proper column quoting for the database vendor
func (qb *QueryBuilder) Eq(column string, value any) squirrel.Eq {
	quotedColumn := qb.quoteColumnForQuery(column)
	return squirrel.Eq{quotedColumn: value}
}

// NotEq creates a not-equal condition with proper column quoting for the database vendor
func (qb *QueryBuilder) NotEq(column string, value any) squirrel.NotEq {
	quotedColumn := qb.quoteColumnForQuery(column)
	return squirrel.NotEq{quotedColumn: value}
}

// Lt creates a less-than condition with proper column quoting for the database vendor
func (qb *QueryBuilder) Lt(column string, value any) squirrel.Lt {
	quotedColumn := qb.quoteColumnForQuery(column)
	return squirrel.Lt{quotedColumn: value}
}

// LtOrEq creates a less-than-or-equal condition with proper column quoting for the database vendor
func (qb *QueryBuilder) LtOrEq(column string, value any) squirrel.LtOrEq {
	quotedColumn := qb.quoteColumnForQuery(column)
	return squirrel.LtOrEq{quotedColumn: value}
}

// Gt creates a greater-than condition with proper column quoting for the database vendor
func (qb *QueryBuilder) Gt(column string, value any) squirrel.Gt {
	quotedColumn := qb.quoteColumnForQuery(column)
	return squirrel.Gt{quotedColumn: value}
}

// GtOrEq creates a greater-than-or-equal condition with proper column quoting for the database vendor
func (qb *QueryBuilder) GtOrEq(column string, value any) squirrel.GtOrEq {
	quotedColumn := qb.quoteColumnForQuery(column)
	return squirrel.GtOrEq{quotedColumn: value}
}

// ========== SelectQueryBuilder Methods ==========

// From specifies the table(s) to select from
func (sqb *SelectQueryBuilder) From(from ...string) dbtypes.SelectQueryBuilder {
	for _, table := range from {
		sqb.selectBuilder = sqb.selectBuilder.From(table)
	}
	return sqb
}

// Where adds a WHERE condition using the raw squirrel interface
func (sqb *SelectQueryBuilder) Where(pred any, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(pred, rest...)
	return sqb
}

// Limit sets the LIMIT for the query
func (sqb *SelectQueryBuilder) Limit(limit uint64) dbtypes.SelectQueryBuilder {
	sqb.limit = limit
	return sqb
}

// Offset sets the OFFSET for the query
func (sqb *SelectQueryBuilder) Offset(offset uint64) dbtypes.SelectQueryBuilder {
	sqb.offset = offset
	return sqb
}

// WhereEq adds an equality condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereEq(column string, value any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.Eq(column, value))
	return sqb
}

// WhereNotEq adds a not-equal condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereNotEq(column string, value any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.NotEq(column, value))
	return sqb
}

// WhereLt adds a less-than condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereLt(column string, value any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.Lt(column, value))
	return sqb
}

// WhereLte adds a less-than-or-equal condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereLte(column string, value any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.LtOrEq(column, value))
	return sqb
}

// WhereGt adds a greater-than condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereGt(column string, value any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.Gt(column, value))
	return sqb
}

// WhereGte adds a greater-than-or-equal condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereGte(column string, value any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.GtOrEq(column, value))
	return sqb
}

// WhereIn adds an IN condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereIn(column string, values any) dbtypes.SelectQueryBuilder {
	quotedColumn := sqb.qb.quoteColumnForQuery(column)
	sqb.selectBuilder = sqb.selectBuilder.Where(squirrel.Eq{quotedColumn: values})
	return sqb
}

// WhereNotIn adds a NOT IN condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereNotIn(column string, values any) dbtypes.SelectQueryBuilder {
	quotedColumn := sqb.qb.quoteColumnForQuery(column)
	sqb.selectBuilder = sqb.selectBuilder.Where(squirrel.NotEq{quotedColumn: values})
	return sqb
}

// WhereLike adds a case-insensitive LIKE condition to the WHERE clause.
// This uses vendor-specific case-insensitive logic:
// - PostgreSQL: Uses ILIKE operator
// - Oracle: Uses UPPER() function on both column and value
// - Other vendors: Uses standard LIKE
func (sqb *SelectQueryBuilder) WhereLike(column, pattern string) dbtypes.SelectQueryBuilder {
	condition := sqb.qb.BuildCaseInsensitiveLike(column, pattern)
	sqb.selectBuilder = sqb.selectBuilder.Where(condition)
	return sqb
}

// WhereNull adds an IS NULL condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereNull(column string) dbtypes.SelectQueryBuilder {
	quotedColumn := sqb.qb.quoteColumnForQuery(column)
	sqb.selectBuilder = sqb.selectBuilder.Where(squirrel.Eq{quotedColumn: nil})
	return sqb
}

// WhereNotNull adds an IS NOT NULL condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereNotNull(column string) dbtypes.SelectQueryBuilder {
	quotedColumn := sqb.qb.quoteColumnForQuery(column)
	sqb.selectBuilder = sqb.selectBuilder.Where(squirrel.NotEq{quotedColumn: nil})
	return sqb
}

// WhereBetween adds a BETWEEN condition to the WHERE clause.
// The column name is automatically quoted according to database vendor rules.
func (sqb *SelectQueryBuilder) WhereBetween(column string, lowerBound, upperBound any) dbtypes.SelectQueryBuilder {
	quotedColumn := sqb.qb.quoteColumnForQuery(column)
	condition := squirrel.And{
		squirrel.GtOrEq{quotedColumn: lowerBound},
		squirrel.LtOrEq{quotedColumn: upperBound},
	}
	sqb.selectBuilder = sqb.selectBuilder.Where(condition)
	return sqb
}

// WhereRaw adds a raw SQL WHERE condition to the query.
//
// WARNING: This method bypasses all identifier quoting and SQL injection protection.
// It is the caller's responsibility to:
//   - Properly quote any identifiers (especially Oracle reserved words like "number", "level", "size")
//   - Ensure the SQL fragment is valid for the target database
//   - Never concatenate user input directly into the condition string
//
// Use this method ONLY when the type-safe methods cannot express your condition.
// For Oracle, remember to quote reserved words: WhereRaw(`"number" = ?`, value)
//
// Examples:
//
//	sqb.WhereRaw(`"number" = ?`, accountNumber)  // Oracle reserved word
//	sqb.WhereRaw(`ROWNUM <= ?`, 10)              // Oracle-specific syntax
//	sqb.WhereRaw(`ST_Distance(location, ?) < ?`, point, radius) // Spatial queries
func (sqb *SelectQueryBuilder) WhereRaw(condition string, args ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Where(condition, args...)
	return sqb
}

// Join adds a JOIN clause to the query
func (sqb *SelectQueryBuilder) Join(join string, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Join(join, rest...)
	return sqb
}

// LeftJoin adds a LEFT JOIN clause to the query
func (sqb *SelectQueryBuilder) LeftJoin(join string, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.LeftJoin(join, rest...)
	return sqb
}

// RightJoin adds a RIGHT JOIN clause to the query
func (sqb *SelectQueryBuilder) RightJoin(join string, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.RightJoin(join, rest...)
	return sqb
}

// InnerJoin adds an INNER JOIN clause to the query
func (sqb *SelectQueryBuilder) InnerJoin(join string, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.InnerJoin(join, rest...)
	return sqb
}

// CrossJoin adds a CROSS JOIN clause to the query
func (sqb *SelectQueryBuilder) CrossJoin(join string, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.CrossJoin(join, rest...)
	return sqb
}

// OrderBy adds an ORDER BY clause to the query
func (sqb *SelectQueryBuilder) OrderBy(orderBys ...string) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.OrderBy(orderBys...)
	return sqb
}

// GroupBy adds a GROUP BY clause to the query
func (sqb *SelectQueryBuilder) GroupBy(groupBys ...string) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.GroupBy(groupBys...)
	return sqb
}

// Having adds a HAVING clause to the query
func (sqb *SelectQueryBuilder) Having(pred any, rest ...any) dbtypes.SelectQueryBuilder {
	sqb.selectBuilder = sqb.selectBuilder.Having(pred, rest...)
	return sqb
}

// Paginate applies pagination to the query with vendor-specific syntax.
// Use limit=0 for no limit (with offset only), offset=0 for no offset (limit only).
// Oracle 12c+ will use OFFSET...FETCH syntax, others use LIMIT/OFFSET.
func (sqb *SelectQueryBuilder) Paginate(limit, offset uint64) dbtypes.SelectQueryBuilder {
	sqb.limit = limit
	sqb.offset = offset
	return sqb
}

// ToSQL generates the final SQL query string and arguments.
// For Oracle, pagination uses OFFSET...FETCH syntax; for others, uses LIMIT/OFFSET.
func (sqb *SelectQueryBuilder) ToSQL() (sql string, args []any, err error) {
	builder := sqb.selectBuilder

	// Apply pagination based on vendor
	if sqb.limit > 0 || sqb.offset > 0 {
		if sqb.qb.vendor == dbtypes.Oracle {
			// Oracle 12c+ uses OFFSET...FETCH syntax
			paginationClause := buildOraclePaginationClause(int(sqb.limit), int(sqb.offset))
			if paginationClause != "" {
				builder = builder.Suffix(paginationClause)
			}
		} else {
			// Standard SQL LIMIT/OFFSET for PostgreSQL and others
			if sqb.limit > 0 {
				builder = builder.Limit(sqb.limit)
			}
			if sqb.offset > 0 {
				builder = builder.Offset(sqb.offset)
			}
		}
	}

	return builder.ToSql()
}
