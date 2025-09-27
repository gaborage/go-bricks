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
func (qb *QueryBuilder) Select(columns ...string) squirrel.SelectBuilder {
	return qb.statementBuilder.Select(qb.quoteColumnsForSelect(columns...)...)
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

// BuildLimitOffset applies LIMIT and OFFSET to a SELECT query using vendor-specific syntax.
// Different databases have different pagination mechanisms.
func (qb *QueryBuilder) BuildLimitOffset(query squirrel.SelectBuilder, limit, offset int) squirrel.SelectBuilder {
	switch qb.vendor {
	case dbtypes.Oracle:
		// Oracle uses OFFSET ... ROWS FETCH NEXT ... ROWS ONLY semantics (12c+)
		oracleSuffix := buildOraclePaginationClause(limit, offset)
		if oracleSuffix != "" {
			query = query.Suffix(oracleSuffix)
		}
		return query
	default:
		// Default to standard LIMIT/OFFSET
		if limit > 0 {
			query = query.Limit(uint64(limit))
		}
		if offset > 0 {
			query = query.Offset(uint64(offset))
		}
		return query
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
		return qb.quoteOracleColumns(columns...)
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
