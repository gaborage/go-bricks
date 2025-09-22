// Package database provides cross-database query building utilities
package database

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/squirrel"
)

// Database type constants
const (
	PostgreSQL = "postgresql"
	Oracle     = "oracle"
	MongoDB    = "mongodb"
)

// QueryBuilder provides vendor-specific SQL query building
type QueryBuilder struct {
	vendor           string
	statementBuilder squirrel.StatementBuilderType
}

// NewQueryBuilder creates a new query builder for the specified database vendor
func NewQueryBuilder(vendor string) *QueryBuilder {
	var sb squirrel.StatementBuilderType

	switch vendor {
	case PostgreSQL:
		// PostgreSQL uses $1, $2, ... placeholders
		sb = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar)
	case Oracle:
		// Oracle uses :1, :2, ... placeholders
		sb = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Colon)
	case MongoDB:
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

// Vendor returns the database vendor
func (qb *QueryBuilder) Vendor() string {
	return qb.vendor
}

// quoteOracleColumn handles Oracle-specific column name quoting
func (qb *QueryBuilder) quoteOracleColumn(column string) string {
	if qb.vendor == Oracle {
		// Quote columns that might be Oracle reserved words
		if column == "number" {
			return `"number"`
		}
	}
	return column
}

// quoteOracleColumns handles Oracle-specific column name quoting for multiple columns
func (qb *QueryBuilder) quoteOracleColumns(columns ...string) []string {
	if qb.vendor == Oracle {
		quotedColumns := make([]string, len(columns))
		for i, col := range columns {
			quotedColumns[i] = qb.quoteOracleColumn(col)
		}
		return quotedColumns
	}
	return columns
}

// quoteOracleColumnsForDML applies Oracle-specific quoting for column lists used in DML statements
// like INSERT or UPDATE where reserved words must be safely referenced. In these contexts, we
// prefer upper-cased quoted identifiers for reserved words to match Oracle's default identifier case.
func (qb *QueryBuilder) quoteOracleColumnsForDML(columns ...string) []string {
	if qb.vendor != Oracle {
		return columns
	}

	quoted := make([]string, len(columns))
	for i, col := range columns {
		if col == "number" { // Oracle reserved word
			quoted[i] = `"` + strings.ToUpper(col) + `"`
			continue
		}
		quoted[i] = col
	}
	return quoted
}

// Select creates a new SELECT query builder
func (qb *QueryBuilder) Select(columns ...string) squirrel.SelectBuilder {
	return qb.statementBuilder.Select(qb.quoteOracleColumns(columns...)...)
}

// Insert creates a new INSERT query builder
func (qb *QueryBuilder) Insert(table string) squirrel.InsertBuilder {
	return qb.statementBuilder.Insert(table)
}

// InsertWithColumns creates an INSERT builder and applies vendor-specific
// quoting to the provided column list (e.g., quotes reserved words on Oracle).
func (qb *QueryBuilder) InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder {
	return qb.statementBuilder.Insert(table).Columns(qb.quoteOracleColumnsForDML(columns...)...)
}

// Update creates a new UPDATE query builder
func (qb *QueryBuilder) Update(table string) squirrel.UpdateBuilder {
	return qb.statementBuilder.Update(table)
}

// Delete creates a new DELETE query builder
func (qb *QueryBuilder) Delete(table string) squirrel.DeleteBuilder {
	return qb.statementBuilder.Delete(table)
}

// BuildCaseInsensitiveLike creates a case-insensitive LIKE condition based on vendor
func (qb *QueryBuilder) BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer {
	likeValue := "%" + value + "%"

	switch qb.vendor {
	case PostgreSQL:
		// PostgreSQL supports ILIKE for case-insensitive matching
		return squirrel.ILike{column: likeValue}
	case Oracle:
		// Oracle requires UPPER() for case-insensitive matching and quoted column names
		quotedColumn := qb.quoteOracleColumn(column)
		return squirrel.Like{"UPPER(" + quotedColumn + ")": strings.ToUpper(likeValue)}
	default:
		// Default to standard LIKE (case-sensitive)
		return squirrel.Like{column: likeValue}
	}
}

// BuildLimitOffset creates LIMIT/OFFSET clause based on vendor
func (qb *QueryBuilder) BuildLimitOffset(query squirrel.SelectBuilder, limit, offset int) squirrel.SelectBuilder {
	switch qb.vendor {
	case Oracle:
		// Oracle uses OFFSET ... ROWS FETCH NEXT ... ROWS ONLY semantics (12c+)
		oracleSuffix := buildOraclePaginationClause(limit, offset)
		if oracleSuffix != "" {
			query = query.Suffix(oracleSuffix)
		}
		return query
	default:
		// PostgreSQL and other databases support standard LIMIT and OFFSET
		if limit > 0 {
			query = query.Limit(uint64(limit))
		}
		if offset > 0 {
			query = query.Offset(uint64(offset))
		}
		return query
	}
}

// buildOraclePaginationClause builds an Oracle-compatible pagination suffix.
// It returns an empty string if both limit and offset are non-positive.
// When provided, offset yields "OFFSET {offset} ROWS", limit yields "FETCH NEXT {limit} ROWS ONLY",
// and both are joined with a space (e.g. "OFFSET 10 ROWS FETCH NEXT 20 ROWS ONLY").
func buildOraclePaginationClause(limit, offset int) string {
	if limit <= 0 && offset <= 0 {
		return ""
	}

	parts := make([]string, 0, 2)
	if offset > 0 {
		parts = append(parts, fmt.Sprintf("OFFSET %d ROWS", offset))
	}
	if limit > 0 {
		parts = append(parts, fmt.Sprintf("FETCH NEXT %d ROWS ONLY", limit))
	}

	return strings.Join(parts, " ")
}

// BuildUpsert creates an UPSERT/MERGE query based on vendor
func (qb *QueryBuilder) BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	switch qb.vendor {
	case PostgreSQL:
		// PostgreSQL uses ON CONFLICT ... DO UPDATE
		insertQuery := qb.Insert(table)
		// deterministic order
		cols := make([]string, 0, len(insertColumns))
		for c := range insertColumns {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		vals := make([]any, 0, len(cols))
		for _, c := range cols {
			vals = append(vals, insertColumns[c])
		}
		insertQuery = insertQuery.Columns(cols...).Values(vals...)

		// Build conflict resolution
		conflictClause := "ON CONFLICT (" + strings.Join(conflictColumns, ", ") + ") DO UPDATE SET "
		var setParts []string
		updateCols := make([]string, 0, len(updateColumns))
		for c := range updateColumns {
			updateCols = append(updateCols, c)
		}
		sort.Strings(updateCols)
		for _, col := range updateCols {
			setParts = append(setParts, col+" = EXCLUDED."+col)
		}
		conflictClause += strings.Join(setParts, ", ")

		sql, args, err := insertQuery.ToSql()
		if err != nil {
			return "", nil, err
		}

		return sql + " " + conflictClause, args, nil

	case Oracle:
		// Oracle uses MERGE statement
		// This is more complex and would need careful implementation
		// For now, we'll fall back to separate INSERT/UPDATE logic
		return "", nil, nil // To be implemented based on specific requirements

	default:
		return "", nil, nil
	}
}

// BuildCurrentTimestamp returns the current timestamp function for the vendor
func (qb *QueryBuilder) BuildCurrentTimestamp() string {
	switch qb.vendor {
	case PostgreSQL:
		return "NOW()"
	case Oracle:
		return "SYSDATE"
	default:
		return "NOW()"
	}
}

// BuildUUIDGeneration returns the UUID generation function for the vendor
func (qb *QueryBuilder) BuildUUIDGeneration() string {
	switch qb.vendor {
	case PostgreSQL:
		return "gen_random_uuid()"
	case Oracle:
		return "SYS_GUID()" // Oracle's UUID generation
	default:
		return "UUID()" // Generic UUID function
	}
}

// BuildBooleanValue converts Go boolean to vendor-specific boolean representation
func (qb *QueryBuilder) BuildBooleanValue(value bool) any {
	switch qb.vendor {
	case PostgreSQL:
		return value // PostgreSQL has native boolean support
	case Oracle:
		if value {
			return 1 // Oracle uses NUMBER(1) for boolean
		}
		return 0
	default:
		return value
	}
}

// EscapeIdentifier escapes database identifiers (table names, column names) for the vendor
func (qb *QueryBuilder) EscapeIdentifier(identifier string) string {
	switch qb.vendor {
	case PostgreSQL:
		return `"` + identifier + `"` // PostgreSQL uses double quotes
	case Oracle:
		return `"` + strings.ToUpper(identifier) + `"` // Oracle prefers uppercase
	default:
		return identifier // No escaping by default
	}
}
