// Package builder provides cross-database query building utilities.
// This package implements vendor-specific SQL generation and identifier handling
// for PostgreSQL, Oracle, and other database backends.
package builder

import (
	"fmt"
	"strings"

	"github.com/Masterminds/squirrel"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	joinOnPlaceholder = "%s ON %s"
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
	err           error  // Captured error from filter operations
}

// check if SelectQueryBuilder implements dbtypes.SelectQueryBuilder
var _ dbtypes.SelectQueryBuilder = (*SelectQueryBuilder)(nil)

// UpdateQueryBuilder provides a type-safe interface for building UPDATE queries
// with Filter API support and vendor-specific column quoting.
type UpdateQueryBuilder struct {
	qb            *QueryBuilder
	updateBuilder squirrel.UpdateBuilder
}

// check if UpdateQueryBuilder implements dbtypes.UpdateQueryBuilder
var _ dbtypes.UpdateQueryBuilder = (*UpdateQueryBuilder)(nil)

// DeleteQueryBuilder provides a type-safe interface for building DELETE queries
// with Filter API support.
type DeleteQueryBuilder struct {
	qb            *QueryBuilder
	deleteBuilder squirrel.DeleteBuilder
}

// check if DeleteQueryBuilder implements dbtypes.DeleteQueryBuilder
var _ dbtypes.DeleteQueryBuilder = (*DeleteQueryBuilder)(nil)

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

// Filter returns a FilterFactory for creating composable WHERE clause filters.
// The factory provides type-safe methods (Eq, Lt, Gt, etc.) that automatically handle
// vendor-specific column quoting, as well as composition methods (And, Or, Not).
//
// Example:
//
//	f := qb.Filter()
//	query := qb.Select("*").From("users").Where(f.And(
//	    f.Eq("status", "active"),
//	    f.Gt("age", 18),
//	))
func (qb *QueryBuilder) Filter() dbtypes.FilterFactory {
	return newFilterFactory(qb)
}

// JoinFilter returns a JoinFilterFactory for creating composable JOIN ON conditions.
// The factory provides type-safe methods (EqColumn, LtColumn, GtColumn, etc.) for comparing
// columns to other columns (not values) with automatic vendor-specific quoting.
//
// Example:
//
//	jf := qb.JoinFilter()
//	query := qb.Select("*").From("users").JoinOn("profiles", jf.And(
//	    jf.EqColumn("users.id", "profiles.user_id"),
//	    jf.GtColumn("profiles.created_at", "users.created_at"),
//	))
func (qb *QueryBuilder) JoinFilter() dbtypes.JoinFilterFactory {
	return newJoinFilterFactory(qb)
}

// Expr creates a raw SQL expression for use in SELECT, GROUP BY, and ORDER BY clauses.
// See dbtypes.Expr() for full documentation and security warnings.
func (qb *QueryBuilder) Expr(sql string, alias ...string) dbtypes.RawExpression {
	return dbtypes.Expr(sql, alias...)
}

func (qb *QueryBuilder) appendSelectColumn(processed *[]string, col any) {
	switch v := col.(type) {
	case nil:
		panic("nil column in Select")
	case string:
		*processed = append(*processed, qb.quoteColumnsForSelect(v)...)
	case dbtypes.RawExpression:
		if v.Alias != "" {
			*processed = append(*processed, fmt.Sprintf("%s AS %s", v.SQL, v.Alias))
		} else {
			*processed = append(*processed, v.SQL)
		}
	case []string:
		for _, item := range v {
			qb.appendSelectColumn(processed, item)
		}
	case []dbtypes.RawExpression:
		for _, item := range v {
			qb.appendSelectColumn(processed, item)
		}
	case []any:
		for _, item := range v {
			qb.appendSelectColumn(processed, item)
		}
	default:
		panic(fmt.Sprintf("unsupported column type in Select: %T (must be string or RawExpression)", col))
	}
}

// Select creates a SELECT query builder with vendor-specific column quoting.
// For Oracle, it applies identifier quoting to handle reserved words appropriately.
// Accepts both string column names and RawExpression instances (v2.1+).
//
// Examples:
//
//	qb.Select("id", "name")                           // String columns
//	qb.Select("id", qb.Expr("COUNT(*)", "total"))     // Mixed: column + expression
//	qb.Select(qb.Expr("SUM(amount)", "revenue"))      // Expression only
func (qb *QueryBuilder) Select(columns ...any) *SelectQueryBuilder {
	processedColumns := make([]string, 0, len(columns))

	for _, col := range columns {
		qb.appendSelectColumn(&processedColumns, col)
	}

	selectBuilder := qb.statementBuilder.Select(processedColumns...)
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

// Update creates an UPDATE query builder for the specified table with Filter API support.
// The returned UpdateQueryBuilder provides type-safe filtering and vendor-specific column quoting.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
//
// Example:
//
//	f := qb.Filter()
//	query := qb.Update("users").
//	    Set("status", "active").
//	    Set("updated_at", time.Now()).
//	    Where(f.Eq("id", 123))
func (qb *QueryBuilder) Update(table string) dbtypes.UpdateQueryBuilder {
	quotedTable := qb.quoteTableForQuery(table)
	return &UpdateQueryBuilder{
		qb:            qb,
		updateBuilder: qb.statementBuilder.Update(quotedTable),
	}
}

// Delete creates a DELETE query builder for the specified table with Filter API support.
// The returned DeleteQueryBuilder provides type-safe filtering.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
//
// Example:
//
//	f := qb.Filter()
//	query := qb.Delete("users").Where(f.And(
//	    f.Eq("status", "deleted"),
//	    f.Lt("deleted_at", threshold),
//	))
func (qb *QueryBuilder) Delete(table string) dbtypes.DeleteQueryBuilder {
	quotedTable := qb.quoteTableForQuery(table)
	return &DeleteQueryBuilder{
		qb:            qb,
		deleteBuilder: qb.statementBuilder.Delete(quotedTable),
	}
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

// quoteTableForQuery handles vendor-specific table name quoting for FROM clauses
func (qb *QueryBuilder) quoteTableForQuery(table string) string {
	switch qb.vendor {
	case dbtypes.Oracle:
		return qb.quoteOracleColumn(table)
	default:
		return table
	}
}

// quoteTableReference handles vendor-specific table quoting for both string names and TableRef instances.
// Returns quoted table name with optional alias (e.g., "customers" c for PostgreSQL, "LEVEL" lvl for Oracle).
// Accepts either string or *TableRef. Returns empty string for invalid types (let Squirrel validation catch it).
func (qb *QueryBuilder) quoteTableReference(table any) string {
	switch t := table.(type) {
	case string:
		// Backward compatibility: plain string table name
		return qb.quoteTableForQuery(t)
	case *dbtypes.TableRef:
		quotedName := qb.quoteTableForQuery(t.Name())
		if t.HasAlias() {
			// Quote table name, preserve alias case (no quotes on alias for standard SQL)
			return quotedName + " " + t.Alias()
		}
		return quotedName
	default:
		// Invalid type - return empty string to let Squirrel's validation catch it
		return ""
	}
}

// quoteIdentifierForClause handles vendor-specific identifier quoting for ORDER BY and GROUP BY clauses
// It parses expressions to identify column references vs SQL functions and direction keywords
func (qb *QueryBuilder) quoteIdentifierForClause(identifier string) string {
	switch qb.vendor {
	case dbtypes.Oracle:
		return qb.quoteOracleIdentifierForClause(identifier)
	default:
		return identifier
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

// From specifies the table(s) to select from.
// Accepts either string table names or *TableRef instances with optional aliases.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
//
// Examples:
//
//	From("users")                                // Simple table
//	From("users", "profiles")                    // Multiple tables (cross join)
//	From(Table("customers").As("c"))            // Table with alias
//	From("users", Table("profiles").As("p"))     // Mixed
func (sqb *SelectQueryBuilder) From(from ...any) dbtypes.SelectQueryBuilder {
	if len(from) == 0 {
		return sqb
	}

	// Quote all tables and join with commas for multi-table FROM clause
	quotedTables := make([]string, len(from))
	for i, table := range from {
		quotedTables[i] = sqb.qb.quoteTableReference(table)
	}

	// Join with commas and pass as single FROM clause
	fromClause := strings.Join(quotedTables, ", ")
	sqb.selectBuilder = sqb.selectBuilder.From(fromClause)
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

// Where adds a filter to the WHERE clause.
// Multiple calls to Where() will be combined with AND logic.
//
// Create filters using the FilterFactory obtained from QueryBuilder.Filter():
//
// Simple condition:
//
//	f := qb.Filter()
//	query.Where(f.Eq("status", "active"))
//
// Multiple conditions with AND:
//
//	f := qb.Filter()
//	query.Where(f.And(
//	    f.Eq("status", "active"),
//	    f.Gt("age", 18),
//	))
//
// OR conditions:
//
//	f := qb.Filter()
//	query.Where(f.Or(
//	    f.Eq("status", "active"),
//	    f.Eq("role", "admin"),
//	))
//
// Complex nested logic:
//
//	f := qb.Filter()
//	query.Where(f.And(
//	    f.Or(
//	        f.Eq("status", "active"),
//	        f.Eq("status", "pending"),
//	    ),
//	    f.Gt("balance", 1000),
//	))
func (sqb *SelectQueryBuilder) Where(filter dbtypes.Filter) dbtypes.SelectQueryBuilder {
	// Pass the filter directly to squirrel - it implements squirrel.Sqlizer
	// Squirrel will call ToSql() and handle placeholder numbering across multiple Where() calls
	sqb.selectBuilder = sqb.selectBuilder.Where(filter)
	return sqb
}

// JoinOn adds a type-safe JOIN clause to the query using JoinFilter for column comparisons.
// Accepts either a string table name or *TableRef instance with optional alias.
// The table name is automatically quoted according to vendor rules.
//
// Example:
//
//	jf := qb.JoinFilter()
//	query.JoinOn(Table("profiles").As("p"), jf.EqColumn("users.id", "p.user_id"))
func (sqb *SelectQueryBuilder) JoinOn(table any, filter dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
	quotedTable := sqb.qb.quoteTableReference(table)
	condition, args, err := filter.ToSQL()
	if err != nil {
		// Capture error to be returned from ToSQL()
		sqb.err = fmt.Errorf("JoinOn filter error: %w", err)
		return sqb
	}

	joinClause := fmt.Sprintf(joinOnPlaceholder, quotedTable, condition)
	sqb.selectBuilder = sqb.selectBuilder.Join(joinClause, args...)
	return sqb
}

// LeftJoinOn adds a type-safe LEFT JOIN clause to the query using JoinFilter.
// Accepts either a string table name or *TableRef instance with optional alias.
// The table name is automatically quoted according to vendor rules.
func (sqb *SelectQueryBuilder) LeftJoinOn(table any, filter dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
	quotedTable := sqb.qb.quoteTableReference(table)
	condition, args, err := filter.ToSQL()
	if err != nil {
		// Capture error to be returned from ToSQL()
		sqb.err = fmt.Errorf("LeftJoinOn filter error: %w", err)
		return sqb
	}

	joinClause := fmt.Sprintf(joinOnPlaceholder, quotedTable, condition)
	sqb.selectBuilder = sqb.selectBuilder.LeftJoin(joinClause, args...)
	return sqb
}

// RightJoinOn adds a type-safe RIGHT JOIN clause to the query using JoinFilter.
// Accepts either a string table name or *TableRef instance with optional alias.
// The table name is automatically quoted according to vendor rules.
func (sqb *SelectQueryBuilder) RightJoinOn(table any, filter dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
	quotedTable := sqb.qb.quoteTableReference(table)
	condition, args, err := filter.ToSQL()
	if err != nil {
		// Capture error to be returned from ToSQL()
		sqb.err = fmt.Errorf("RightJoinOn filter error: %w", err)
		return sqb
	}

	joinClause := fmt.Sprintf(joinOnPlaceholder, quotedTable, condition)
	sqb.selectBuilder = sqb.selectBuilder.RightJoin(joinClause, args...)
	return sqb
}

// InnerJoinOn adds a type-safe INNER JOIN clause to the query using JoinFilter.
// Accepts either a string table name or *TableRef instance with optional alias.
// The table name is automatically quoted according to vendor rules.
func (sqb *SelectQueryBuilder) InnerJoinOn(table any, filter dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
	quotedTable := sqb.qb.quoteTableReference(table)
	condition, args, err := filter.ToSQL()
	if err != nil {
		// Capture error to be returned from ToSQL()
		sqb.err = fmt.Errorf("InnerJoinOn filter error: %w", err)
		return sqb
	}

	joinClause := fmt.Sprintf(joinOnPlaceholder, quotedTable, condition)
	sqb.selectBuilder = sqb.selectBuilder.InnerJoin(joinClause, args...)
	return sqb
}

// CrossJoinOn adds a CROSS JOIN clause to the query.
// Accepts either a string table name or *TableRef instance with optional alias.
// Cross joins do not have ON conditions, so no JoinFilter is needed.
// The table name is automatically quoted according to vendor rules.
func (sqb *SelectQueryBuilder) CrossJoinOn(table any) dbtypes.SelectQueryBuilder {
	quotedTable := sqb.qb.quoteTableReference(table)
	sqb.selectBuilder = sqb.selectBuilder.CrossJoin(quotedTable)
	return sqb
}

// OrderBy adds an ORDER BY clause to the query.
// Column names are automatically quoted according to database vendor rules.
// Accepts both string column names (with optional ASC/DESC) and RawExpression instances (v2.1+).
//
// Examples:
//
//	.OrderBy("created_at DESC")                          // String with direction
//	.OrderBy("name", "id DESC")                          // Multiple strings
//	.OrderBy(qb.Expr("COUNT(*) DESC"))                   // Expression with direction
//	.OrderBy("id", qb.Expr("UPPER(name) ASC"))           // Mixed
func (sqb *SelectQueryBuilder) OrderBy(orderBys ...any) dbtypes.SelectQueryBuilder {
	processedOrderBys := make([]string, 0, len(orderBys))

	for _, orderBy := range orderBys {
		sqb.appendClauseValue(&processedOrderBys, orderBy, "orderBy", sqb.qb.quoteIdentifierForClause)
	}

	sqb.selectBuilder = sqb.selectBuilder.OrderBy(processedOrderBys...)
	return sqb
}

// GroupBy adds a GROUP BY clause to the query.
// Column names are automatically quoted according to database vendor rules.
// Accepts both string column names and RawExpression instances (v2.1+).
//
// Examples:
//
//	.GroupBy("category_id", "status")                    // String columns
//	.GroupBy("id", qb.Expr("DATE(created_at)"))          // Mixed: column + expression
//	.GroupBy(qb.Expr("YEAR(order_date)"))                // Expression only
func (sqb *SelectQueryBuilder) GroupBy(groupBys ...any) dbtypes.SelectQueryBuilder {
	processedGroupBys := make([]string, 0, len(groupBys))

	for _, groupBy := range groupBys {
		sqb.appendClauseValue(&processedGroupBys, groupBy, "groupBy", sqb.qb.quoteIdentifierForClause)
	}

	sqb.selectBuilder = sqb.selectBuilder.GroupBy(processedGroupBys...)
	return sqb
}

func (sqb *SelectQueryBuilder) appendClauseValue(processed *[]string, value any, clauseName string, stringFormatter func(string) string) {
	switch v := value.(type) {
	case nil:
		panic(fmt.Sprintf("nil %s in %s", clauseName, clauseName))
	case string:
		*processed = append(*processed, stringFormatter(v))
	case dbtypes.RawExpression:
		*processed = append(*processed, v.SQL)
	case []string:
		for _, item := range v {
			sqb.appendClauseValue(processed, item, clauseName, stringFormatter)
		}
	case []dbtypes.RawExpression:
		for _, item := range v {
			sqb.appendClauseValue(processed, item, clauseName, stringFormatter)
		}
	case []any:
		for _, item := range v {
			sqb.appendClauseValue(processed, item, clauseName, stringFormatter)
		}
	default:
		panic(fmt.Sprintf("unsupported %s type: %T (must be string or RawExpression)", clauseName, value))
	}
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

// ValidateForSubquery provides lightweight validation without forcing SQL rendering.
func (sqb *SelectQueryBuilder) ValidateForSubquery() error {
	if sqb == nil {
		return fmt.Errorf("subquery cannot be nil")
	}

	return sqb.err
}

// buildSelectBuilder returns the underlying squirrel.SelectBuilder with pagination applied.
func (sqb *SelectQueryBuilder) buildSelectBuilder() squirrel.SelectBuilder {
	builder := sqb.selectBuilder

	// Apply pagination based on vendor
	if sqb.limit > 0 || sqb.offset > 0 {
		if sqb.qb.vendor == dbtypes.Oracle {
			// Oracle 12c+ uses OFFSET...FETCH syntax
			if clause := buildOraclePaginationClause(int(sqb.limit), int(sqb.offset)); clause != "" {
				builder = builder.Suffix(clause)
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

	return builder
}

// ToSQL generates the final SQL query string and arguments.
// For Oracle, pagination uses OFFSET...FETCH syntax; for others, uses LIMIT/OFFSET.
func (sqb *SelectQueryBuilder) ToSQL() (sql string, args []any, err error) {
	// Return any captured filter errors first
	if sqb.err != nil {
		return "", nil, sqb.err
	}

	builder := sqb.buildSelectBuilder()
	return builder.ToSql()
}

// ========== UpdateQueryBuilder Methods ==========

// Set sets a column to a value in the UPDATE statement.
// Column names are automatically quoted according to database vendor rules.
func (uqb *UpdateQueryBuilder) Set(column string, value any) dbtypes.UpdateQueryBuilder {
	quotedColumn := uqb.qb.quoteColumnForQuery(column)
	uqb.updateBuilder = uqb.updateBuilder.Set(quotedColumn, value)
	return uqb
}

// SetMap sets multiple columns to values in the UPDATE statement.
// Column names are automatically quoted according to database vendor rules.
func (uqb *UpdateQueryBuilder) SetMap(clauses map[string]any) dbtypes.UpdateQueryBuilder {
	quotedClauses := make(map[string]any, len(clauses))
	for k, v := range clauses {
		quotedClauses[uqb.qb.quoteColumnForQuery(k)] = v
	}
	uqb.updateBuilder = uqb.updateBuilder.SetMap(quotedClauses)
	return uqb
}

// Where adds a filter to the WHERE clause.
// Multiple calls to Where() will be combined with AND logic.
func (uqb *UpdateQueryBuilder) Where(filter dbtypes.Filter) dbtypes.UpdateQueryBuilder {
	uqb.updateBuilder = uqb.updateBuilder.Where(filter)
	return uqb
}

// ToSQL generates the final SQL query and arguments.
func (uqb *UpdateQueryBuilder) ToSQL() (sql string, args []any, err error) {
	return uqb.updateBuilder.ToSql()
}

// ========== DeleteQueryBuilder Methods ==========

// Where adds a filter to the WHERE clause.
// Multiple calls to Where() will be combined with AND logic.
func (dqb *DeleteQueryBuilder) Where(filter dbtypes.Filter) dbtypes.DeleteQueryBuilder {
	dqb.deleteBuilder = dqb.deleteBuilder.Where(filter)
	return dqb
}

// Limit sets the maximum number of rows to delete.
// Note: LIMIT in DELETE is not standard SQL and may not be supported by all databases.
func (dqb *DeleteQueryBuilder) Limit(limit uint64) dbtypes.DeleteQueryBuilder {
	dqb.deleteBuilder = dqb.deleteBuilder.Limit(limit)
	return dqb
}

// OrderBy adds ORDER BY clauses to the DELETE statement.
// Note: ORDER BY in DELETE is not standard SQL and may not be supported by all databases.
func (dqb *DeleteQueryBuilder) OrderBy(orderBys ...string) dbtypes.DeleteQueryBuilder {
	quotedOrderBys := make([]string, len(orderBys))
	for i, orderBy := range orderBys {
		quotedOrderBys[i] = dqb.qb.quoteIdentifierForClause(orderBy)
	}
	dqb.deleteBuilder = dqb.deleteBuilder.OrderBy(quotedOrderBys...)
	return dqb
}

// ToSQL generates the final SQL query and arguments.
func (dqb *DeleteQueryBuilder) ToSQL() (sql string, args []any, err error) {
	return dqb.deleteBuilder.ToSql()
}
