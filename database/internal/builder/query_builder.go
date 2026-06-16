// Package builder provides cross-database query building utilities.
// This package implements vendor-specific SQL generation and identifier handling
// for PostgreSQL, Oracle, and other database backends.
package builder

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Masterminds/squirrel"
	colreg "github.com/gaborage/go-bricks/database/internal/columns"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	joinOnPlaceholder = "%s ON %s"
	sqlFuncNow        = "NOW()"
	// jsonLiteralNull is the JSON literal sent for nil/typed-nil JSONContains
	// payloads, matching encoding/json's representation of nil values.
	jsonLiteralNull = "null"
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

// InsertQueryBuilder wraps squirrel.InsertBuilder so the public API exposes
// idiomatic ToSQL() (uppercase, per S8179) instead of squirrel's ToSql().
//
// All chaining methods return the dbtypes.InsertQueryBuilder interface so users
// see a consistent ToSQL()-based surface across SELECT/INSERT/UPDATE/DELETE.
type InsertQueryBuilder struct {
	insertBuilder squirrel.InsertBuilder
	err           error // deferred error surfaced by ToSQL()
}

// check if InsertQueryBuilder implements dbtypes.InsertQueryBuilder
var _ dbtypes.InsertQueryBuilder = (*InsertQueryBuilder)(nil)

// UpdateQueryBuilder provides a type-safe interface for building UPDATE queries
// with Filter API support and vendor-specific column quoting.
type UpdateQueryBuilder struct {
	qb            *QueryBuilder
	updateBuilder squirrel.UpdateBuilder
	err           error // deferred error surfaced by ToSQL()
}

// check if UpdateQueryBuilder implements dbtypes.UpdateQueryBuilder
var _ dbtypes.UpdateQueryBuilder = (*UpdateQueryBuilder)(nil)

// DeleteQueryBuilder provides a type-safe interface for building DELETE queries
// with Filter API support.
type DeleteQueryBuilder struct {
	qb            *QueryBuilder
	deleteBuilder squirrel.DeleteBuilder
	err           error // deferred error surfaced by ToSQL()
}

// check if DeleteQueryBuilder implements dbtypes.DeleteQueryBuilder
var _ dbtypes.DeleteQueryBuilder = (*DeleteQueryBuilder)(nil)

// ========== QueryBuilder Methods ==========

// Per-vendor statement builders, built once at package load instead of on every
// NewQueryBuilder call. squirrel's StatementBuilderType wraps a persistent
// (copy-on-write) map: every fluent call (.Select(), .From(), …) clones the
// receiver rather than mutating it, and the placeholder formats stored here
// (Dollar/Colon/Question) are stateless. Sharing one value per vendor across
// goroutines is therefore safe and avoids a PlaceholderFormat clone + reflect
// allocation per NewQueryBuilder call. Treat these as immutable: never assign back
// through a method (e.g. RunWith) and never store a stateful PlaceholderFormat.
var (
	pgStatementBuilder    = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar)   // PostgreSQL: $1, $2, ...
	oraStatementBuilder   = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Colon)    // Oracle: :1, :2, ...
	qmarkStatementBuilder = squirrel.StatementBuilder.PlaceholderFormat(squirrel.Question) // Default: ?, ?, ...
)

// NewQueryBuilder creates a new query builder for the specified database vendor.
// It selects the vendor's shared, immutable placeholder-format statement builder.
func NewQueryBuilder(vendor dbtypes.Vendor) *QueryBuilder {
	sb := qmarkStatementBuilder // default to question mark placeholders
	switch vendor {
	case dbtypes.PostgreSQL:
		sb = pgStatementBuilder
	case dbtypes.Oracle:
		sb = oraStatementBuilder
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
//
// Returns an error if the SQL is empty, too many aliases are provided, or alias contains dangerous characters.
func (qb *QueryBuilder) Expr(sql string, alias ...string) (dbtypes.RawExpression, error) {
	return dbtypes.Expr(sql, alias...)
}

// MustExpr is like Expr but panics on error.
// Use this only in static initialization or tests where errors indicate programming bugs.
func (qb *QueryBuilder) MustExpr(sql string, alias ...string) dbtypes.RawExpression {
	return dbtypes.MustExpr(sql, alias...)
}

// Columns extracts column metadata from a struct with `db:"column_name"` tags.
// It lazily parses the struct on first use and caches the metadata forever,
// providing vendor-specific column quoting (e.g., Oracle reserved words).
//
// This method delegates to the global column registry, which maintains per-vendor
// caches using sync.Map for lock-free cached reads.
//
// Parameters:
//   - structPtr: Pointer to a struct with `db:"column_name"` tags
//
// Returns:
//   - dbtypes.Columns: Interface providing Col(), Cols(), and All() methods
//
// Panics if:
//   - structPtr is not a pointer to a struct
//   - No fields with `db` tags are found
//   - Any db tag contains dangerous SQL characters
//
// Performance:
//   - First use: ~2µs (reflection + tag parsing)
//   - Cached access: ~50ns (sync.Map read + method call)
//
// Example:
//
//	type User struct {
//	    ID    int64  `db:"id"`
//	    Name  string `db:"name"`
//	    Level string `db:"level"` // Oracle reserved word
//	}
//
//	cols := qb.Columns(&User{})
//	query := qb.Select(cols.Cols("ID", "Name")).From("users") // []string flattened by Select
//	// Oracle: SELECT id, name FROM users
//	// PostgreSQL: SELECT id, name FROM users
func (qb *QueryBuilder) Columns(structPtr any) dbtypes.Columns {
	return colreg.RegisterColumns(qb.vendor, structPtr)
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

// Insert creates an INSERT query builder for the specified table.
// The returned InsertQueryBuilder exposes ToSQL() (idiomatic Go, per S8179)
// consistent with Select/Update/Delete builders.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
func (qb *QueryBuilder) Insert(table string) dbtypes.InsertQueryBuilder {
	return &InsertQueryBuilder{insertBuilder: qb.statementBuilder.Insert(qb.quoteTableForQuery(table))}
}

// InsertWithColumns creates an INSERT query builder with pre-specified columns.
// It applies vendor-specific column quoting to the provided column list.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
func (qb *QueryBuilder) InsertWithColumns(table string, columns ...string) dbtypes.InsertQueryBuilder {
	return &InsertQueryBuilder{
		insertBuilder: qb.statementBuilder.Insert(qb.quoteTableForQuery(table)).Columns(qb.quoteColumnsForDML(columns...)...),
	}
}

// InsertStruct creates an INSERT query by extracting all fields from a struct instance.
// Zero-value ID fields (int64 or string type with field name "ID") are automatically excluded
// to support auto-increment primary keys.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
//
// Example:
//
//	type User struct {
//	    ID    int64  `db:"id"`    // Excluded if zero
//	    Name  string `db:"name"`
//	    Email string `db:"email"`
//	}
//
//	user := User{Name: "Alice", Email: "alice@example.com"}
//	query := qb.InsertStruct("users", &user)
//	// INSERT INTO users (name, email) VALUES (?, ?)
//
// Panics if instance is not a struct or pointer to struct with db tags.
func (qb *QueryBuilder) InsertStruct(table string, instance any) dbtypes.InsertQueryBuilder {
	cols := qb.Columns(instance)
	fieldMap := cols.FieldMap(instance)

	// Filter out zero-value ID field for auto-increment support
	columns := make([]string, 0, len(fieldMap))
	values := make([]any, 0, len(fieldMap))

	for col, val := range fieldMap {
		// Skip zero-value ID fields (common pattern for auto-increment PKs)
		if qb.isZeroValueIDField(col, val) {
			continue
		}
		columns = append(columns, col)
		values = append(values, val)
	}

	return &InsertQueryBuilder{
		insertBuilder: qb.statementBuilder.Insert(qb.quoteTableForQuery(table)).
			Columns(qb.quoteColumnsForDML(columns...)...).
			Values(values...),
	}
}

// InsertFields creates an INSERT query by extracting only specified fields from a struct instance.
// This is useful for partial inserts or when you need explicit control over which fields to include.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
//
// Example:
//
//	user := User{ID: 123, Name: "Alice", Email: "alice@example.com", Status: "active"}
//	query := qb.InsertFields("users", &user, "Name", "Email")
//	// INSERT INTO users (name, email) VALUES (?, ?)
//
// Panics if instance is not a struct or any field name is invalid.
func (qb *QueryBuilder) InsertFields(table string, instance any, fields ...string) dbtypes.InsertQueryBuilder {
	cols := qb.Columns(instance)
	fieldMap := cols.FieldMap(instance)

	// Extract only requested fields
	columns := make([]string, 0, len(fields))
	values := make([]any, 0, len(fields))

	for _, fieldName := range fields {
		col := cols.Col(fieldName)
		if val, ok := fieldMap[col]; ok {
			columns = append(columns, col)
			values = append(values, val)
		} else {
			panic(fmt.Sprintf("field %q not found in struct", fieldName))
		}
	}

	return &InsertQueryBuilder{
		insertBuilder: qb.statementBuilder.Insert(qb.quoteTableForQuery(table)).
			Columns(qb.quoteColumnsForDML(columns...)...).
			Values(values...),
	}
}

// extractTerminalIdentifier extracts the final identifier from a column name,
// handling quoted identifiers and qualified names (e.g., "schema"."table"."id" -> "id").
// Trims backticks, double quotes, and square brackets, then splits on dots.
func extractTerminalIdentifier(column string) string {
	// Trim leading/trailing whitespace
	column = strings.TrimSpace(column)

	// Split on dots to handle qualified names (schema.table.column)
	parts := strings.Split(column, ".")
	lastPart := parts[len(parts)-1]

	// Trim common quoting characters from the terminal identifier
	lastPart = strings.Trim(lastPart, "`\"[] ")

	return lastPart
}

// isZeroValueIDField checks if a column is an ID field with a zero value.
// This is used to skip auto-increment primary keys in INSERT operations.
// Only columns whose terminal identifier is exactly "id" (case-insensitive) are treated as ID columns.
func (qb *QueryBuilder) isZeroValueIDField(column string, value any) bool {
	// Extract terminal identifier and check if it's exactly "id" (case-insensitive)
	terminalID := extractTerminalIdentifier(column)
	isIDColumn := strings.EqualFold(terminalID, "id")

	if !isIDColumn {
		return false
	}

	// Check for zero values
	switch v := value.(type) {
	case int64:
		return v == 0
	case string:
		return v == ""
	case int:
		return v == 0
	case int32:
		return v == 0
	default:
		return false
	}
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
	uqb := &UpdateQueryBuilder{qb: qb}
	// Validate the table identifier before interpolation (all vendors): on
	// PostgreSQL quoteTableForQuery returns the name verbatim, so an unvalidated
	// table is a raw-interpolation (M9) vector. Surface a violation from ToSQL().
	if err := validateTableName(table); err != nil {
		uqb.err = fmt.Errorf("Update: %w", err)
		return uqb
	}
	uqb.updateBuilder = qb.statementBuilder.Update(qb.quoteTableForQuery(table))
	return uqb
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
	dqb := &DeleteQueryBuilder{qb: qb}
	// Validate the table identifier before interpolation (all vendors) — same M9
	// raw-interpolation guard as Update/From. Surface a violation from ToSQL().
	if err := validateTableName(table); err != nil {
		dqb.err = fmt.Errorf("Delete: %w", err)
		return dqb
	}
	dqb.deleteBuilder = qb.statementBuilder.Delete(qb.quoteTableForQuery(table))
	return dqb
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

// BuildRegex creates a vendor-specific regex match expression.
//
// PostgreSQL emits the POSIX-regex operators: ~ (CS), ~* (CI), !~ (NOT CS),
// !~* (NOT CI). Oracle emits REGEXP_LIKE with an optional 'i' match flag,
// wrapped in NOT(...) when negated.
//
// Pattern syntax differs slightly between vendors (POSIX ERE vs Oracle's
// extended POSIX); callers writing vendor-portable regexes should stick to
// the common subset (anchors, character classes, quantifiers).
func (qb *QueryBuilder) BuildRegex(column, pattern string, caseInsensitive, negated bool) squirrel.Sqlizer {
	quotedColumn := qb.quoteColumnForQuery(column)

	switch qb.vendor {
	case dbtypes.PostgreSQL:
		op := "~"
		if negated {
			op = "!~"
		}
		if caseInsensitive {
			op += "*"
		}
		return squirrel.Expr(quotedColumn+" "+op+" ?", pattern)
	case dbtypes.Oracle:
		expr := "REGEXP_LIKE(" + quotedColumn + ", ?"
		args := []any{pattern}
		if caseInsensitive {
			expr += ", ?"
			args = append(args, "i")
		}
		expr += ")"
		if negated {
			expr = "NOT (" + expr + ")"
		}
		return squirrel.Expr(expr, args...)
	default:
		return errorSqlizer{err: fmt.Errorf("regex matching is not supported for vendor %q", qb.vendor)}
	}
}

// BuildJSONContains creates a JSON containment expression.
//
// PostgreSQL emits "column @> ?::jsonb" with the value marshaled to JSON.
// Strings, []byte, and json.RawMessage values are passed through as-is
// (caller-provided JSON); other values are marshaled via encoding/json so
// that callers can pass structs, maps, or slices directly.
//
// Oracle has no clean equivalent (JSON_EQUAL is exact-equality, JSON_EXISTS
// requires a path predicate) so it returns an error filter for now. See
// https://github.com/gaborage/go-bricks/issues/341 for follow-up.
func (qb *QueryBuilder) BuildJSONContains(column string, value any) squirrel.Sqlizer {
	switch qb.vendor {
	case dbtypes.PostgreSQL:
		jsonStr, err := jsonContainsPayload(value)
		if err != nil {
			return errorSqlizer{err: fmt.Errorf("JSONContains: %w", err)}
		}
		quotedColumn := qb.quoteColumnForQuery(column)
		return squirrel.Expr(quotedColumn+" @> ?::jsonb", jsonStr)
	case dbtypes.Oracle:
		return errorSqlizer{err: fmt.Errorf("JSONContains: Oracle support not implemented; see https://github.com/gaborage/go-bricks/issues/341")}
	default:
		return errorSqlizer{err: fmt.Errorf("JSONContains: unsupported vendor %q", qb.vendor)}
	}
}

// jsonContainsPayload converts the caller-supplied value into a JSON string.
//
// Strings, []byte, and json.RawMessage are treated as already-encoded JSON
// payloads but are still validated via json.Valid so malformed input fails at
// query-build time rather than at the database. Typed-nil byte slices map to
// the JSON literal "null" (matching the explicit nil case). Everything else
// routes through encoding/json.
func jsonContainsPayload(value any) (string, error) {
	validateBytes := func(data []byte) (string, error) {
		if data == nil {
			return jsonLiteralNull, nil
		}
		if !json.Valid(data) {
			return "", fmt.Errorf("invalid pre-encoded JSON")
		}
		return string(data), nil
	}

	switch v := value.(type) {
	case nil:
		return jsonLiteralNull, nil
	case string:
		return validateBytes([]byte(v))
	case json.RawMessage:
		return validateBytes([]byte(v))
	case []byte:
		return validateBytes(v)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("marshal value to JSON: %w", err)
		}
		return string(data), nil
	}
}

// BuildCurrentTimestamp returns the current timestamp function for the database vendor
func (qb *QueryBuilder) BuildCurrentTimestamp() string {
	switch qb.vendor {
	case dbtypes.PostgreSQL:
		return sqlFuncNow
	case dbtypes.Oracle:
		return "SYSDATE"
	default:
		return sqlFuncNow
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

// validateTableReference validates the identifier(s) carried by a FROM/JOIN
// table argument BEFORE interpolation. The plain table name (and the alias, when
// a *TableRef carries one) are interpolated verbatim into the SQL string, so both
// must satisfy the safe identifier grammar on ALL vendors (M9). Unsupported types
// fail fast — mirroring quoteTableReference's panic — via the returned error.
func (qb *QueryBuilder) validateTableReference(table any) error {
	switch t := table.(type) {
	case string:
		// Plain string table names may carry an inline alias ("users u").
		return validateTableName(t)
	case *dbtypes.TableRef:
		// TableRef carries name and alias separately; each is a bare identifier.
		if err := validateIdentifier("table", t.Name()); err != nil {
			return err
		}
		if t.HasAlias() {
			return validateIdentifier("table alias", t.Alias())
		}
		return nil
	default:
		// Unsupported type is a programming error, not attacker input — defer to
		// quoteTableReference's fail-fast panic rather than masking it as an error.
		return nil
	}
}

// quoteTableReference handles vendor-specific table quoting for both string names and TableRef instances.
// Returns quoted table name with optional alias (e.g., "customers" c for PostgreSQL, "LEVEL" lvl for Oracle).
// Accepts either string or *TableRef. Panics for invalid types (fail-fast validation).
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
		panic(fmt.Sprintf("unsupported table reference type: %T (must be string or *TableRef)", table))
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
// SECURITY: Table identifiers must be developer-controlled, not user input. They
// are validated against a safe identifier grammar (simple/qualified name with an
// optional alias) on ALL vendors BEFORE interpolation; anything else surfaces as a
// ToSQL() error. The table argument accepts only a string name or *TableRef — it
// is not an expression slot, so there is no Expr()/Raw() escape hatch for tables.
// See ADR-031.
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
		// Validate table-name identifiers BEFORE interpolation (all vendors) so
		// the FROM clause cannot be used as a SQL injection vector (M9).
		if err := sqb.qb.validateTableReference(table); err != nil {
			sqb.err = fmt.Errorf("From: %w", err)
			return sqb
		}
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

// validateJoinTable validates a JOIN table argument BEFORE interpolation (all
// vendors) and quotes it. The table name/alias are interpolated verbatim into the
// JOIN clause, so they must satisfy the safe identifier grammar to close the M9
// injection vector. On failure the first error is captured (deferred to ToSQL,
// mirroring From) and ok is false so the caller short-circuits.
//
// SECURITY: JOIN table identifiers must be developer-controlled, not user input.
// They are validated against the same safe grammar as From() on ALL vendors before
// interpolation. The table argument accepts only a string name or *TableRef (no
// Expr()/Raw() expression slot for tables). See ADR-031.
func (sqb *SelectQueryBuilder) validateJoinTable(method string, table any) (quoted string, ok bool) {
	if err := sqb.qb.validateTableReference(table); err != nil {
		if sqb.err == nil {
			sqb.err = fmt.Errorf("%s: %w", method, err)
		}
		return "", false
	}
	return sqb.qb.quoteTableReference(table), true
}

// JoinOn adds a type-safe JOIN clause to the query using JoinFilter for column comparisons.
// Accepts either a string table name or *TableRef instance with optional alias.
// The table name is automatically quoted according to vendor rules.
//
// SECURITY: see validateJoinTable — JOIN table identifiers are validated on ALL
// vendors before interpolation (M9 / ADR-031).
//
// Example:
//
//	jf := qb.JoinFilter()
//	query.JoinOn(Table("profiles").As("p"), jf.EqColumn("users.id", "p.user_id"))
func (sqb *SelectQueryBuilder) JoinOn(table any, filter dbtypes.JoinFilter) dbtypes.SelectQueryBuilder {
	quotedTable, ok := sqb.validateJoinTable("JoinOn", table)
	if !ok {
		return sqb
	}
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
	quotedTable, ok := sqb.validateJoinTable("LeftJoinOn", table)
	if !ok {
		return sqb
	}
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
	quotedTable, ok := sqb.validateJoinTable("RightJoinOn", table)
	if !ok {
		return sqb
	}
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
	quotedTable, ok := sqb.validateJoinTable("InnerJoinOn", table)
	if !ok {
		return sqb
	}
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
	quotedTable, ok := sqb.validateJoinTable("CrossJoinOn", table)
	if !ok {
		return sqb
	}
	sqb.selectBuilder = sqb.selectBuilder.CrossJoin(quotedTable)
	return sqb
}

// OrderBy adds an ORDER BY clause to the query.
// Column names are automatically quoted according to database vendor rules.
// Accepts both string column names (with optional ASC/DESC) and RawExpression instances (v2.1+).
//
// SECURITY: String ORDER BY arguments must be developer-controlled, not user
// input. They are validated on ALL vendors against a safe grammar — a
// simple/qualified identifier with an optional ASC/DESC [NULLS FIRST|LAST]
// direction — BEFORE interpolation; anything else (functions, multiple tokens,
// comments, semicolons) surfaces as a ToSQL() error. Use qb.Expr() for function
// or computed orderings (e.g. COUNT(*) DESC). See ADR-031.
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

	if sqb.err != nil {
		return sqb
	}

	sqb.selectBuilder = sqb.selectBuilder.OrderBy(processedOrderBys...)
	return sqb
}

// GroupBy adds a GROUP BY clause to the query.
// Column names are automatically quoted according to database vendor rules.
// Accepts both string column names and RawExpression instances (v2.1+).
//
// SECURITY: String GROUP BY arguments must be developer-controlled, not user
// input. They are validated on ALL vendors against a safe identifier grammar
// BEFORE interpolation; anything else (functions, comments, semicolons) surfaces
// as a ToSQL() error. Use qb.Expr() for computed groupings (e.g.
// DATE(created_at)). See ADR-031.
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

	if sqb.err != nil {
		return sqb
	}

	sqb.selectBuilder = sqb.selectBuilder.GroupBy(processedGroupBys...)
	return sqb
}

func (sqb *SelectQueryBuilder) appendClauseValue(processed *[]string, value any, clauseName string, stringFormatter func(string) string) {
	switch v := value.(type) {
	case nil:
		panic(fmt.Sprintf("nil %s in %s", clauseName, clauseName))
	case string:
		// Validate the identifier (with its optional ASC/DESC [NULLS …] direction)
		// BEFORE quoting/interpolation on ALL vendors so a crafted clause argument
		// cannot inject a second statement or comment (M9). Use qb.Expr() for
		// complex expressions that legitimately need raw SQL.
		if err := validateClauseIdentifier(clauseName, v); err != nil {
			if sqb.err == nil {
				sqb.err = err
			}
			return
		}
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
			if clause := buildOraclePaginationClause(int(sqb.limit), int(sqb.offset)); clause != "" { //#nosec G115 -- pagination values are realistic LIMIT/OFFSET, well within int range
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
//
// SECURITY: The column identifier must be developer-controlled, not user input.
// It is validated against a safe identifier grammar on ALL vendors BEFORE
// interpolation; anything else surfaces as a ToSQL() error. The value side is
// parameterized. See ADR-031.
func (uqb *UpdateQueryBuilder) Set(column string, value any) dbtypes.UpdateQueryBuilder {
	// Validate the SET target identifier BEFORE interpolation (all vendors) so the
	// column name cannot be used as a SQL injection vector (M9). The value side is
	// already parameterized by squirrel.
	if err := validateIdentifier("Set column", column); err != nil {
		if uqb.err == nil {
			uqb.err = err
		}
		return uqb
	}
	quotedColumn := uqb.qb.quoteColumnForQuery(column)
	uqb.updateBuilder = uqb.updateBuilder.Set(quotedColumn, value)
	return uqb
}

// SetMap sets multiple columns to values in the UPDATE statement.
// Column names are automatically quoted according to database vendor rules.
//
// SECURITY: Column identifiers (the map keys) must be developer-controlled, not
// user input. Each is validated against a safe identifier grammar on ALL vendors
// BEFORE interpolation; anything else surfaces as a ToSQL() error. See ADR-031.
func (uqb *UpdateQueryBuilder) SetMap(clauses map[string]any) dbtypes.UpdateQueryBuilder {
	quotedClauses := make(map[string]any, len(clauses))
	for k, v := range clauses {
		// Validate each SET target identifier BEFORE interpolation (all vendors, M9).
		if err := validateIdentifier("SetMap column", k); err != nil {
			if uqb.err == nil {
				uqb.err = err
			}
			return uqb
		}
		quotedClauses[uqb.qb.quoteColumnForQuery(k)] = v
	}
	uqb.updateBuilder = uqb.updateBuilder.SetMap(quotedClauses)
	return uqb
}

// SetStruct sets multiple columns from a struct instance in the UPDATE statement.
// If no fields are specified, all struct fields are included.
// If fields are provided, only those fields are updated.
// Column names are automatically quoted according to database vendor rules.
//
// Example (all fields):
//
//	user := User{Name: "Alice", Email: "alice@example.com", Status: "active"}
//	query := qb.Update("users").SetStruct(&user).Where(f.Eq("id", 123))
//	// UPDATE users SET name = ?, email = ?, status = ? WHERE id = ?
//
// Example (selective fields):
//
//	user := User{Name: "Bob", Email: "bob@example.com", Status: "inactive"}
//	query := qb.Update("users").SetStruct(&user, "Name", "Status").Where(f.Eq("id", 456))
//	// UPDATE users SET name = ?, status = ? WHERE id = ?
//
// Panics if instance is not a struct or any field name is invalid.
func (uqb *UpdateQueryBuilder) SetStruct(instance any, fields ...string) dbtypes.UpdateQueryBuilder {
	cols := uqb.qb.Columns(instance)
	fieldMap := cols.FieldMap(instance)

	// If specific fields requested, use only those
	if len(fields) > 0 {
		for _, fieldName := range fields {
			col := cols.Col(fieldName)
			if val, ok := fieldMap[col]; ok {
				quotedCol := uqb.qb.quoteColumnForQuery(col)
				uqb.updateBuilder = uqb.updateBuilder.Set(quotedCol, val)
			} else {
				panic(fmt.Sprintf("field %q not found in struct", fieldName))
			}
		}
	} else {
		// Use all fields
		for col, val := range fieldMap {
			quotedCol := uqb.qb.quoteColumnForQuery(col)
			uqb.updateBuilder = uqb.updateBuilder.Set(quotedCol, val)
		}
	}

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
	if uqb.err != nil {
		return "", nil, uqb.err
	}
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
	quotedOrderBys := make([]string, 0, len(orderBys))
	for _, orderBy := range orderBys {
		// Validate the ORDER BY identifier (with optional ASC/DESC [NULLS …])
		// BEFORE interpolation on ALL vendors so it cannot inject SQL (M9).
		if err := validateClauseIdentifier("orderBy", orderBy); err != nil {
			if dqb.err == nil {
				dqb.err = err
			}
			return dqb
		}
		quotedOrderBys = append(quotedOrderBys, dqb.qb.quoteIdentifierForClause(orderBy))
	}
	dqb.deleteBuilder = dqb.deleteBuilder.OrderBy(quotedOrderBys...)
	return dqb
}

// ToSQL generates the final SQL query and arguments.
func (dqb *DeleteQueryBuilder) ToSQL() (sql string, args []any, err error) {
	if dqb.err != nil {
		return "", nil, dqb.err
	}
	return dqb.deleteBuilder.ToSql()
}

// ========== InsertQueryBuilder Methods ==========

func (iqb *InsertQueryBuilder) Columns(columns ...string) dbtypes.InsertQueryBuilder {
	iqb.insertBuilder = iqb.insertBuilder.Columns(columns...)
	return iqb
}

func (iqb *InsertQueryBuilder) Values(values ...any) dbtypes.InsertQueryBuilder {
	iqb.insertBuilder = iqb.insertBuilder.Values(values...)
	return iqb
}

func (iqb *InsertQueryBuilder) SetMap(clauses map[string]any) dbtypes.InsertQueryBuilder {
	iqb.insertBuilder = iqb.insertBuilder.SetMap(clauses)
	return iqb
}

func (iqb *InsertQueryBuilder) Options(options ...string) dbtypes.InsertQueryBuilder {
	iqb.insertBuilder = iqb.insertBuilder.Options(options...)
	return iqb
}

func (iqb *InsertQueryBuilder) Prefix(sql string, args ...any) dbtypes.InsertQueryBuilder {
	iqb.insertBuilder = iqb.insertBuilder.Prefix(sql, args...)
	return iqb
}

func (iqb *InsertQueryBuilder) Suffix(sql string, args ...any) dbtypes.InsertQueryBuilder {
	iqb.insertBuilder = iqb.insertBuilder.Suffix(sql, args...)
	return iqb
}

// Select uses sb as the source rows for INSERT...SELECT. Squirrel's InsertBuilder.Select
// requires the concrete *SelectQueryBuilder so pagination state (limit/offset) and captured
// filter errors are preserved via buildSelectBuilder()/ValidateForSubquery(). Foreign
// SelectQueryBuilder implementations cannot be plumbed into squirrel's Select clause — for
// those, the error is deferred to ToSQL() rather than panicking.
func (iqb *InsertQueryBuilder) Select(sb dbtypes.SelectQueryBuilder) dbtypes.InsertQueryBuilder {
	if err := dbtypes.ValidateSubquery(sb); err != nil {
		iqb.err = fmt.Errorf("InsertQueryBuilder.Select: %w", err)
		return iqb
	}
	concrete, ok := sb.(*SelectQueryBuilder)
	if !ok {
		iqb.err = fmt.Errorf("InsertQueryBuilder.Select: unsupported subquery type %T", sb)
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.Select(concrete.buildSelectBuilder())
	return iqb
}

func (iqb *InsertQueryBuilder) ToSQL() (sql string, args []any, err error) {
	if iqb.err != nil {
		return "", nil, iqb.err
	}
	return iqb.insertBuilder.ToSql()
}
