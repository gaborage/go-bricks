// Package builder provides cross-database query building utilities.
// This package implements vendor-specific SQL generation and identifier handling
// for PostgreSQL, Oracle, and other database backends.
package builder

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Masterminds/squirrel"

	colreg "github.com/gaborage/go-bricks/database/internal/columns"
	"github.com/gaborage/go-bricks/database/internal/sqllex"
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
	// renderer is the vendor's identifier rendering adapter, resolved once here
	// so the quoting funnels below dispatch by method call rather than by
	// re-testing the vendor at each clause. See renderer.go.
	renderer vendorRenderer
}

// SelectQueryBuilder provides a type-safe interface for building SELECT queries
// with proper identifier quoting and vendor-specific optimizations.
type SelectQueryBuilder struct {
	qb            *QueryBuilder
	selectBuilder squirrel.SelectBuilder
	limit         uint64 // 0 means no limit
	offset        uint64 // 0 means no offset
	// lock is the rendered row-lock clause, "" for none; see row_lock.go.
	lock string
	// hasFrom records that From() named at least one table. Oracle has no
	// table-less SELECT, so ToSQL supplies `FROM dual` when it is false.
	hasFrom bool
	err     error // Captured error from filter operations
}

// check if SelectQueryBuilder implements dbtypes.SelectQueryBuilder
var _ dbtypes.SelectQueryBuilder = (*SelectQueryBuilder)(nil)

// InsertQueryBuilder wraps squirrel.InsertBuilder so the public API exposes
// idiomatic ToSQL() (uppercase, per S8179) instead of squirrel's ToSql().
//
// All chaining methods return the dbtypes.InsertQueryBuilder interface so users
// see a consistent ToSQL()-based surface across SELECT/INSERT/UPDATE/DELETE.
type InsertQueryBuilder struct {
	// qb is the owning builder, held for the vendor's column quoting: Columns and
	// SetMap have to reach the same funnel InsertWithColumns uses, and the insert
	// builder carries no vendor of its own — which is why the doors diverged
	// (#1154). Set on BOTH newInsertBuilder branches, the failure one included:
	// Columns and SetMap re-check only the identifiers just handed to them, so a
	// builder whose TABLE failed validation still reaches this field.
	qb            *QueryBuilder
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
		renderer:         rendererFor(vendor),
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

// appendSelectColumn flattens one Select argument into the rendered column list.
// A string is an identifier argument and is validated against the select grammar
// before it is interpolated; a RawExpression is the sanctioned escape hatch and
// passes through untouched. Panics stay reserved for a genuine programming error
// — an unsupported TYPE — while bad identifier CONTENT is returned as an error
// and deferred to ToSQL(), the split ADR-031 established.
func (qb *QueryBuilder) appendSelectColumn(processed *[]string, col any) error {
	switch v := col.(type) {
	case nil:
		panic("nil column in Select")
	case string:
		normalized, err := validateSelectIdentifier(v)
		if err != nil {
			return err
		}
		*processed = append(*processed, qb.quoteColumnsForSelect(normalized)...)
	case dbtypes.RawExpression:
		// A struct literal never passed through Expr(), so the alias grammar and
		// the empty-SQL check run here too — the door is where the value is
		// interpolated, and the two construction paths converge only if it does.
		if err := v.Validate(); err != nil {
			return err
		}
		if v.Alias != "" {
			*processed = append(*processed, fmt.Sprintf("%s AS %s", v.SQL, v.Alias))
		} else {
			*processed = append(*processed, v.SQL)
		}
	case []string:
		return appendSelectColumnsOf(qb, processed, v)
	case []dbtypes.RawExpression:
		return appendSelectColumnsOf(qb, processed, v)
	case []any:
		return appendSelectColumnsOf(qb, processed, v)
	default:
		panic(fmt.Sprintf("unsupported column type in Select: %T (must be string or RawExpression)", col))
	}
	return nil
}

// appendSelectColumnsOf flattens a slice of Select arguments, stopping at the first
// violation so the error a caller sees names the first bad column rather than the
// last — the same first-violation-wins rule the fluent builders follow.
func appendSelectColumnsOf[T any](qb *QueryBuilder, processed *[]string, cols []T) error {
	for _, item := range cols {
		if err := qb.appendSelectColumn(processed, item); err != nil {
			return err
		}
	}
	return nil
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

	var firstErr error
	for _, col := range columns {
		// Stop at the first violation, matching appendSelectColumnsOf one level
		// down: the builder is already doomed to return this error, so quoting the
		// remaining columns is work whose output nothing reads.
		if err := qb.appendSelectColumn(&processedColumns, col); err != nil {
			firstErr = err
			break
		}
	}

	selectBuilder := qb.statementBuilder.Select(processedColumns...)
	return &SelectQueryBuilder{
		qb:            qb,
		selectBuilder: selectBuilder,
		err:           firstErr,
	}
}

// Insert creates an INSERT query builder for the specified table.
// The returned InsertQueryBuilder exposes ToSQL() (idiomatic Go, per S8179)
// consistent with Select/Update/Delete builders.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
func (qb *QueryBuilder) Insert(table string) dbtypes.InsertQueryBuilder {
	return qb.newInsertBuilder("Insert", table)
}

// newInsertBuilder starts an INSERT on table, validating the table identifier
// BEFORE interpolation — the same M9 guard From/Update/Delete apply, since
// quoteTableForQuery returns the name verbatim on PostgreSQL and a table sits
// first in the statement, where a trailing comment takes the rest of it. A
// violation is deferred to ToSQL(), so a caller that keeps building on the
// returned builder still fails there; the callers below stop early only to avoid
// mutating a builder that carries no statement.
func (qb *QueryBuilder) newInsertBuilder(context, table string) *InsertQueryBuilder {
	normalized, err := validateTableName(table)
	if err != nil {
		return &InsertQueryBuilder{qb: qb, err: fmt.Errorf("%s: %w", context, err)}
	}
	return &InsertQueryBuilder{qb: qb, insertBuilder: qb.statementBuilder.Insert(qb.quoteTableForQuery(normalized))}
}

// InsertWithColumns creates an INSERT query builder with pre-specified columns.
// It applies vendor-specific column quoting to the provided column list.
// Table names are automatically quoted according to database vendor rules to handle reserved words.
func (qb *QueryBuilder) InsertWithColumns(table string, columns ...string) dbtypes.InsertQueryBuilder {
	iqb := qb.newInsertBuilder("InsertWithColumns", table)
	if iqb.err != nil {
		return iqb
	}
	normalized, err := validateIdentifiers("insert column", columns)
	if err != nil {
		iqb.failClause(err)
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.Columns(qb.quoteColumnsForDML(normalized...)...)
	return iqb
}

// validateIdentifiers checks a column list against the identifier grammar,
// reporting the FIRST violation so the error names the first bad column rather
// than the last — the same first-violation-wins rule the fluent builders follow.
//
// It returns the NORMALIZED list, and callers must render that rather than their
// own input: validating a trimmed value while rendering the untrimmed one is what
// let `Select("t.* ")` render as `t."*"` (ADR-082), and returning the value is
// what stops the two from disagreeing again (#1158).
func validateIdentifiers(context string, columns []string) (normalized []string, err error) {
	normalized = make([]string, 0, len(columns))
	for _, col := range columns {
		trimmed, colErr := validateIdentifier(context, col)
		if colErr != nil {
			return nil, colErr
		}
		normalized = append(normalized, trimmed)
	}
	return normalized, nil
}

// InsertStruct creates an INSERT query by extracting all fields from a struct instance.
// Zero-value ID fields (int64, string, int, or int32 whose db-tag column name resolves to
// exactly "id", case-insensitive) are automatically excluded to support auto-increment primary keys.
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

	// Sorted: identical input must render byte-identical SQL (#1157).
	for _, col := range sortedKeys(fieldMap) {
		val := fieldMap[col]
		// Skip zero-value ID fields (common pattern for auto-increment PKs)
		if qb.isZeroValueIDField(col, val) {
			continue
		}
		columns = append(columns, col)
		values = append(values, val)
	}

	iqb := qb.newInsertBuilder("InsertStruct", table)
	if iqb.err != nil {
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.
		Columns(qb.quoteColumnsForDML(columns...)...).
		Values(values...)
	return iqb
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
		val, ok := fieldMap[col]
		if !ok {
			panic(fmt.Sprintf("field %q not found in struct", fieldName))
		}
		columns = append(columns, col)
		values = append(values, val)
	}

	iqb := qb.newInsertBuilder("InsertFields", table)
	if iqb.err != nil {
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.
		Columns(qb.quoteColumnsForDML(columns...)...).
		Values(values...)
	return iqb
}

// extractTerminalIdentifier extracts the final identifier from a column name,
// handling quoted identifiers and qualified names (e.g., "schema"."table"."id" -> "id").
// Trims backticks, double quotes, and square brackets, then splits on dots.
func extractTerminalIdentifier(column string) string {
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
	normalized, err := validateTableName(table)
	if err != nil {
		uqb.failClause(fmt.Errorf("Update: %w", err))
		return uqb
	}
	uqb.updateBuilder = qb.statementBuilder.Update(qb.quoteTableForQuery(normalized))
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
	normalized, err := validateTableName(table)
	if err != nil {
		dqb.failClause(fmt.Errorf("Delete: %w", err))
		return dqb
	}
	dqb.deleteBuilder = qb.statementBuilder.Delete(qb.quoteTableForQuery(normalized))
	return dqb
}

// BuildCaseInsensitiveLike creates a case-insensitive LIKE expression.
// The implementation varies by database vendor.
func (qb *QueryBuilder) BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer {
	likeValue := "%" + value + "%"

	// Rendered once, before the vendor switch. Two of these three branches used to
	// use the caller's column verbatim as a squirrel map key, reaching SQL without
	// even the vendor quoting — so this door bypassed the funnel entirely rather
	// than merely forgetting to check. A fallible funnel makes the compiler
	// enumerate every door that CALLS it; it cannot point at one that does not.
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return errorSqlizer{err: err}
	}

	return qb.renderer.CaseInsensitiveLike(quotedColumn, likeValue)
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
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return errorSqlizer{err: err}
	}

	return qb.renderer.Regex(quotedColumn, pattern, caseInsensitive, negated)
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
	// The column is handed over as a deferred quoter, not a quoted string: a
	// vendor that cannot render this predicate never quotes a column it will
	// not name, which is what the Oracle and unknown-vendor arms did here.
	return qb.renderer.JSONContains(value, func() (string, error) {
		return qb.quoteColumnForQuery(column)
	})
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
			return "", errors.New("invalid pre-encoded JSON")
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
	return qb.renderer.CurrentTimestamp()
}

// BuildUUIDGeneration returns the UUID generation function for the database vendor
func (qb *QueryBuilder) BuildUUIDGeneration() string {
	return qb.renderer.UUIDGeneration()
}

// BuildBooleanValue converts a Go boolean to the appropriate database representation
func (qb *QueryBuilder) BuildBooleanValue(value bool) any {
	return qb.renderer.BooleanValue(value)
}

// EscapeIdentifier escapes a database identifier (table/column name) according to vendor rules
func (qb *QueryBuilder) EscapeIdentifier(identifier string) string {
	// Quote-aware split: a dot inside a quoted segment belongs to the name, so
	// `"my.col"` is one identifier and must not be torn into `"my` and `col"`
	// and quoted half by half (#1151).
	//
	// This function is EXPORTED — consumers call it to quote a dynamic identifier
	// before embedding it in raw SQL — so unlike the internal renderer it is a
	// trust boundary, not a post-validation step: a malformed identifier is a
	// live input here, not an unreachable branch. It stays safe by escaping, and
	// sqllex.SplitIdentifierSegments hands back the whole string when it cannot parse it.
	parts := sqllex.SplitIdentifierSegments(identifier)
	for i, part := range parts {
		if sqllex.IsQuotedIdentifier(part) {
			// Already a well-formed quoted identifier; re-quoting would rename it.
			continue
		}
		// All vendors now preserve case for quoted identifiers, and an interior
		// quote is doubled so it cannot end the identifier early.
		parts[i] = sqllex.QuoteIdentifierLiteral(part)
	}

	return strings.Join(parts, ".")
}

// quoteColumnsForSelect renders a SELECT column list through the vendor renderer.
//
// The wildcard skip lives here, not in an adapter: `*` and `t.*` are not
// identifiers on any vendor, so which columns are RENDERABLE is the builder's
// question, and only how each one is spelled is the renderer's.
func (qb *QueryBuilder) quoteColumnsForSelect(columns ...string) []string {
	quoted := make([]string, 0, len(columns))
	for _, col := range columns {
		if col == "*" || strings.HasSuffix(col, ".*") {
			quoted = append(quoted, col)
			continue
		}
		quoted = append(quoted, qb.renderer.QuoteColumn(col))
	}
	return quoted
}

// quoteColumnsForDML renders a DML column list through the vendor renderer.
// A list is rendered element-wise on every vendor, so the loop is the builder's
// and the seam only answers for one column at a time.
func (qb *QueryBuilder) quoteColumnsForDML(columns ...string) []string {
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = qb.renderer.QuoteColumn(col)
	}
	return quoted
}

// quoteColumnForQuery validates a column identifier and renders it for the vendor.
//
// It returns an error rather than a bare string because it is the single point
// every column argument reaches before becoming SQL — the Filter and JoinFilter
// doors, the comparison helpers, and UPDATE's SET targets all funnel here. Making
// the funnel fallible is what stops a door from forgetting: a new one cannot
// interpolate a column without handling the failure, where a per-door guard is
// something a reviewer has to notice is missing (ADR-082).
//
// The check matters most on PostgreSQL, where the renderer returns the column
// verbatim, so an unvalidated argument was interpolated as written.
func (qb *QueryBuilder) quoteColumnForQuery(column string) (string, error) {
	trimmed, err := validateIdentifier("column", column)
	if err != nil {
		return "", err
	}
	return qb.renderer.QuoteColumn(trimmed), nil
}

// quoteTableForQuery renders a FROM/JOIN table argument through the vendor renderer.
func (qb *QueryBuilder) quoteTableForQuery(table string) string {
	return qb.renderer.QuoteTable(table)
}

// validateTableReference validates the identifier(s) carried by a FROM/JOIN
// table argument BEFORE interpolation. The plain table name (and the alias, when
// a *TableRef carries one) are interpolated verbatim into the SQL string, so both
// must satisfy the safe identifier grammar on ALL vendors (M9). Unsupported types
// fail fast — mirroring quoteTableReference's panic — via the returned error.
func (qb *QueryBuilder) validateTableReference(table any) (normalizedTableRef, error) {
	switch t := table.(type) {
	case string:
		// Plain string table names may carry an inline alias ("users u").
		normalized, err := validateTableName(t)
		return normalizedTableRef{name: normalized, supported: true}, err
	case *dbtypes.TableRef:
		// TableRef carries name and alias separately; each is a bare identifier.
		name, err := validateIdentifier("table", t.Name())
		if err != nil {
			return normalizedTableRef{}, err
		}
		ref := normalizedTableRef{name: name, supported: true}
		if t.HasAlias() {
			if ref.alias, err = validateIdentifier("table alias", t.Alias()); err != nil {
				return normalizedTableRef{}, err
			}
		}
		return ref, nil
	default:
		// Unsupported type is a programming error, not attacker input — defer to
		// quoteTableReference's fail-fast panic rather than masking it as an error.
		return normalizedTableRef{typeName: fmt.Sprintf("%T", table)}, nil
	}
}

// normalizedTableRef is a validated table reference: the identifier(s) the
// renderer must interpolate, already trimmed. It exists so validation and
// rendering cannot disagree about which string they mean — the class #1158
// closes. name carries the whole string form ("users u", inline alias included);
// alias is set only for a TableRef, which keeps its parts separate. orig is
// retained solely so an unsupported type still reaches quoteTableReference's
// fail-fast panic with the caller's own value.
type normalizedTableRef struct {
	name  string
	alias string
	// typeName is the caller's own type, captured once so the renderer can name
	// it in the fail-fast panic without re-deriving the classification — and
	// without carrying an `any`, which would make this struct only conditionally
	// comparable (an `any` holding a slice panics on == or map-key use).
	typeName  string
	supported bool
}

// quoteTableReference handles vendor-specific table quoting for both string names and TableRef instances.
// Returns quoted table name with optional alias (e.g., "customers" c for PostgreSQL, "LEVEL" lvl for Oracle).
// Accepts either string or *TableRef. Panics for invalid types (fail-fast validation).
func (qb *QueryBuilder) quoteTableReference(ref normalizedTableRef) string {
	if !ref.supported {
		panic(fmt.Sprintf("unsupported table reference type: %s (must be string or *TableRef)", ref.typeName))
	}
	quotedName := qb.quoteTableForQuery(ref.name)
	if ref.alias != "" {
		// Quote table name, preserve alias case (no quotes on alias for standard SQL)
		return quotedName + " " + ref.alias
	}
	return quotedName
}

// quoteIdentifierForClause renders an ORDER BY / GROUP BY item through the
// vendor renderer, which keeps the direction and NULLS keywords outside the
// quoted identifier.
func (qb *QueryBuilder) quoteIdentifierForClause(identifier string) string {
	return qb.renderer.QuoteIdentifierForClause(identifier)
}

// Eq creates an equality condition with proper column quoting for the database vendor
func (qb *QueryBuilder) Eq(column string, value any) (squirrel.Eq, error) {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return nil, err
	}
	return squirrel.Eq{quotedColumn: value}, nil
}

// NotEq creates a not-equal condition with proper column quoting for the database vendor
func (qb *QueryBuilder) NotEq(column string, value any) (squirrel.NotEq, error) {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return nil, err
	}
	return squirrel.NotEq{quotedColumn: value}, nil
}

// Lt creates a less-than condition with proper column quoting for the database vendor
func (qb *QueryBuilder) Lt(column string, value any) (squirrel.Lt, error) {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return nil, err
	}
	return squirrel.Lt{quotedColumn: value}, nil
}

// LtOrEq creates a less-than-or-equal condition with proper column quoting for the database vendor
func (qb *QueryBuilder) LtOrEq(column string, value any) (squirrel.LtOrEq, error) {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return nil, err
	}
	return squirrel.LtOrEq{quotedColumn: value}, nil
}

// Gt creates a greater-than condition with proper column quoting for the database vendor
func (qb *QueryBuilder) Gt(column string, value any) (squirrel.Gt, error) {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return nil, err
	}
	return squirrel.Gt{quotedColumn: value}, nil
}

// GtOrEq creates a greater-than-or-equal condition with proper column quoting for the database vendor
func (qb *QueryBuilder) GtOrEq(column string, value any) (squirrel.GtOrEq, error) {
	quotedColumn, err := qb.quoteColumnForQuery(column)
	if err != nil {
		return nil, err
	}
	return squirrel.GtOrEq{quotedColumn: value}, nil
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
	sqb.hasFrom = true

	// Quote all tables and join with commas for multi-table FROM clause
	quotedTables := make([]string, len(from))
	for i, table := range from {
		// Validate table-name identifiers BEFORE interpolation (all vendors) so
		// the FROM clause cannot be used as a SQL injection vector (M9).
		ref, err := sqb.qb.validateTableReference(table)
		if err != nil {
			sqb.failClause(fmt.Errorf("From: %w", err))
			return sqb
		}
		quotedTables[i] = sqb.qb.quoteTableReference(ref)
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

// SubqueryColumn appends a scalar subquery to the projection; see
// dbtypes.SelectQueryBuilder. The subquery renders with question-mark
// placeholders so the outer statement's final pass numbers every argument once,
// exactly as buildExistsFilter does for EXISTS.
func (sqb *SelectQueryBuilder) SubqueryColumn(sub dbtypes.SelectQueryBuilder, alias string) dbtypes.SelectQueryBuilder {
	if sqb.err != nil {
		return sqb
	}
	if !sqllex.IsUnquotedIdentifier(alias) {
		sqb.err = fmt.Errorf("subquery column: %w: %q", dbtypes.ErrInvalidAlias, alias)
		return sqb
	}
	subSQL, subArgs, err := renderSubquery(sub)
	if err != nil {
		sqb.err = fmt.Errorf("subquery column %s: %w", alias, err)
		return sqb
	}
	sqb.selectBuilder = sqb.selectBuilder.Column(squirrel.Alias(squirrel.Expr(subSQL, subArgs...), alias))
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
	ref, err := sqb.qb.validateTableReference(table)
	if err != nil {
		sqb.failClause(fmt.Errorf("%s: %w", method, err))
		return "", false
	}
	return sqb.qb.quoteTableReference(ref), true
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
		sqb.failClause(fmt.Errorf("JoinOn filter error: %w", err))
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
		sqb.failClause(fmt.Errorf("LeftJoinOn filter error: %w", err))
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
		sqb.failClause(fmt.Errorf("RightJoinOn filter error: %w", err))
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
		sqb.failClause(fmt.Errorf("InnerJoinOn filter error: %w", err))
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

// failClause records the FIRST violation and leaves any later one alone — the
// deferred-error rule ADR-031 established. The builder is doomed from here, which
// is why every clause door consults sqb.err before reading the next value.
func (sqb *SelectQueryBuilder) failClause(err error) {
	if sqb.err == nil {
		sqb.err = err
	}
}

// appendClauseString validates an identifier argument (with its optional
// ASC/DESC [NULLS …] direction) BEFORE quoting/interpolation on ALL vendors, so a
// crafted clause argument cannot inject a second statement or comment (M9). Use
// qb.Expr() for complex expressions that legitimately need raw SQL.
func (sqb *SelectQueryBuilder) appendClauseString(processed *[]string, value, clauseName string, stringFormatter func(string) string) {
	normalized, err := validateClauseIdentifier(clauseName, value)
	if err != nil {
		sqb.failClause(err)
		return
	}
	*processed = append(*processed, stringFormatter(normalized))
}

// appendClauseExpr runs the same consumption-time check Select applies: a
// RawExpression struct literal never passed through Expr() (#1153).
func (sqb *SelectQueryBuilder) appendClauseExpr(processed *[]string, expr dbtypes.RawExpression) {
	if err := expr.Validate(); err != nil {
		sqb.failClause(err)
		return
	}
	*processed = append(*processed, expr.SQL)
}

// appendClauseValuesOf flattens a slice of clause arguments. The per-value guard
// in appendClauseValue stops the walk at the first violation.
func appendClauseValuesOf[T any](sqb *SelectQueryBuilder, processed *[]string, values []T, clauseName string, stringFormatter func(string) string) {
	for _, item := range values {
		sqb.appendClauseValue(processed, item, clauseName, stringFormatter)
	}
}

// appendClauseValue flattens one GroupBy/OrderBy argument into the rendered
// clause list. A violation is deferred to ToSQL() rather than panicked, the split
// ADR-031 established; panics stay reserved for a programming error — an
// unsupported TYPE. Once an error is deferred the builder returns early for every
// later value: the statement is already lost, so a trailing bad argument must
// surface as that deferred error rather than as a panic from this function's
// default branch.
func (sqb *SelectQueryBuilder) appendClauseValue(processed *[]string, value any, clauseName string, stringFormatter func(string) string) {
	if sqb.err != nil {
		return
	}

	switch v := value.(type) {
	case nil:
		panic(fmt.Sprintf("nil %s in %s", clauseName, clauseName))
	case string:
		sqb.appendClauseString(processed, v, clauseName, stringFormatter)
	case dbtypes.RawExpression:
		sqb.appendClauseExpr(processed, v)
	case []string:
		appendClauseValuesOf(sqb, processed, v, clauseName, stringFormatter)
	case []dbtypes.RawExpression:
		appendClauseValuesOf(sqb, processed, v, clauseName, stringFormatter)
	case []any:
		appendClauseValuesOf(sqb, processed, v, clauseName, stringFormatter)
	default:
		panic(fmt.Sprintf("unsupported %s type: %T (must be string or RawExpression)", clauseName, value))
	}
}

// Having adds a HAVING clause to the query.
//
// Prefer a qb.Expr() RawExpression — `Having(qb.MustExpr("SUM(amount) > ?"), 100)`,
// or qb.Expr when you handle its error —
// which is the sanctioned path for the aggregate comparisons HAVING exists for
// and the same spelling Select, GroupBy and OrderBy take. HAVING is a predicate,
// not an identifier, so neither form is validated against the identifier grammar
// (ADR-082): a string predicate is a raw-SQL door on par with f.Raw/jf.Raw/
// database.Raw and requires the same inline
// `// SECURITY: Manual SQL review completed - <what was verified>` annotation at
// every call site. The RawExpression form is exempt for consistency with Select,
// GroupBy and OrderBy — NOT because it is safer. RawExpression.Validate() checks
// only that SQL is non-empty and that the Alias is free of dangerous characters;
// it never inspects the SQL body, so that body carries the same injection risk as
// the string form and must be reviewed as raw SQL. Its audit hook is its own
// name: `git grep -nE 'MustExpr\(|[.]Expr\('`.
func (sqb *SelectQueryBuilder) Having(pred any, rest ...any) dbtypes.SelectQueryBuilder {
	if expr, ok := pred.(dbtypes.RawExpression); ok {
		// The alias is judged BEFORE Validate(): for HAVING no alias is ever legal,
		// so an alias that also trips Validate's own alias rule must still report
		// ErrAliasInHaving. Validating first would return that other error instead
		// and make the sentinel a function of the alias's CONTENT.
		if expr.Alias != "" {
			sqb.failClause(fmt.Errorf("%w: %s", dbtypes.ErrAliasInHaving, expr.Alias))
			return sqb
		}
		if err := expr.Validate(); err != nil {
			sqb.failClause(err)
			return sqb
		}
		sqb.selectBuilder = sqb.selectBuilder.Having(expr.SQL, rest...)
		return sqb
	}
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
		return errors.New("subquery cannot be nil")
	}
	if sqb.lock != "" {
		// A row lock belongs to the statement that holds the transaction, never
		// to a nested SELECT: inside EXISTS/IN it is invalid on PostgreSQL and
		// meaningless on Oracle, and buildSelectBuilder would render it.
		return errors.New("subquery cannot carry a row lock (ForUpdate/ForUpdateNoWait)")
	}

	return sqb.err
}

// buildSelectBuilder returns the underlying squirrel.SelectBuilder with
// pagination and the row lock applied, in that order: squirrel renders LIMIT/
// OFFSET before any suffix, and the Oracle OFFSET/FETCH clause is itself a
// suffix, so appending the lock last puts it after pagination on both vendors.
func (sqb *SelectQueryBuilder) buildSelectBuilder() squirrel.SelectBuilder {
	builder := sqb.selectBuilder
	if !sqb.hasFrom && sqb.qb.vendor == dbtypes.Oracle {
		// Oracle has no table-less SELECT: `SELECT 1` and a projection made only
		// of scalar subqueries both need a row source, and dual is the vendor's
		// one-row table for exactly that.
		builder = builder.From("dual")
	}
	return sqb.withRowLock(sqb.applyPagination(builder))
}

// applyPagination renders LIMIT/OFFSET per vendor.
func (sqb *SelectQueryBuilder) applyPagination(builder squirrel.SelectBuilder) squirrel.SelectBuilder {
	if sqb.limit == 0 && sqb.offset == 0 {
		return builder
	}

	if sqb.qb.vendor == dbtypes.Oracle {
		// Oracle 12c+ uses OFFSET...FETCH syntax
		if clause := buildOraclePaginationClause(sqb.limit, sqb.offset); clause != "" {
			builder = builder.Suffix(clause)
		}
		return builder
	}

	// Standard SQL LIMIT/OFFSET for PostgreSQL and others
	if sqb.limit > 0 {
		builder = builder.Limit(sqb.limit)
	}
	if sqb.offset > 0 {
		builder = builder.Offset(sqb.offset)
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
	if err := sqb.validateRowLock(); err != nil {
		return "", nil, err
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
	// The validation lives in the column funnel setColumn goes through, so there is
	// no separate pre-check here: a second validateIdentifier on the same string
	// could never fail once the funnel's had passed.
	uqb.setColumn(column, value)
	return uqb
}

// SetExpr assigns an argument-carrying expression; see dbtypes.UpdateQueryBuilder.
func (uqb *UpdateQueryBuilder) SetExpr(column string, expr dbtypes.RawExpression, args ...any) dbtypes.UpdateQueryBuilder {
	quoted, err := uqb.qb.quoteColumnForQuery(column)
	if err != nil {
		uqb.failClause(err)
		return uqb
	}
	cell, err := valueCell(column, expr, args...)
	if err != nil {
		uqb.failClause(err)
		return uqb
	}
	uqb.updateBuilder = uqb.updateBuilder.Set(quoted, cell)
	return uqb
}

// SetMap sets multiple columns to values in the UPDATE statement.
// Column names are automatically quoted according to database vendor rules.
//
// SECURITY: Column identifiers (the map keys) must be developer-controlled, not
// user input. Each is validated against a safe identifier grammar on ALL vendors
// BEFORE interpolation; anything else surfaces as a ToSQL() error. See ADR-031.
func (uqb *UpdateQueryBuilder) SetMap(clauses map[string]any) dbtypes.UpdateQueryBuilder {
	// Sorted so that WHICH invalid column is reported is deterministic when several
	// are invalid, as InsertQueryBuilder.SetMap already does.
	keys := sortedKeys(clauses)
	quotedKeys := make([]string, len(keys))
	for i, k := range keys {
		// Validated by the column funnel, as every other column door is.
		quoted, quoteErr := uqb.qb.quoteColumnForQuery(k)
		if quoteErr != nil {
			uqb.failClause(quoteErr)
			return uqb
		}
		quotedKeys[i] = quoted
	}
	values, err := valueCells(valuesByKeyOrder(clauses, keys), keyLabel(keys))
	if err != nil {
		uqb.failClause(err)
		return uqb
	}
	// Applied one Set per key, in rendered order — the order squirrel's SetMap
	// would emit (#1185) — rather than through a map keyed by the rendering: two
	// keys that differ only in padding render alike, and a map would keep one
	// assignment and drop the other silently. Both reach SQL, as in
	// InsertQueryBuilder.SetMap, so the database reports the duplicate.
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return quotedKeys[order[a]] < quotedKeys[order[b]] })
	for _, i := range order {
		uqb.updateBuilder = uqb.updateBuilder.Set(quotedKeys[i], values[i])
	}
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
// setColumn renders one SET target through the column funnel and records it,
// failClause records the FIRST violation and leaves any later one alone — the
// same deferred-error rule the other builders follow (ADR-031).
func (uqb *UpdateQueryBuilder) failClause(err error) {
	if uqb.err == nil {
		uqb.err = err
	}
}

// reporting whether the caller may continue. Hoisted out of SetStruct's two
// branches so neither has to nest the funnel's failure inside its own loop.
func (uqb *UpdateQueryBuilder) setColumn(column string, value any) (ok bool) {
	quoted, err := uqb.qb.quoteColumnForQuery(column)
	if err != nil {
		// Defense in depth: every caller feeds this a column parsed from a db tag,
		// and the tag parser rejects an unsafe identifier by panicking, so no test
		// can reach this branch today.
		uqb.failClause(err)
		return false
	}
	cell, err := valueCell(column, value)
	if err != nil {
		uqb.failClause(err)
		return false
	}
	uqb.updateBuilder = uqb.updateBuilder.Set(quoted, cell)
	return true
}

func (uqb *UpdateQueryBuilder) SetStruct(instance any, fields ...string) dbtypes.UpdateQueryBuilder {
	cols := uqb.qb.Columns(instance)
	fieldMap := cols.FieldMap(instance)

	if len(fields) > 0 {
		for _, fieldName := range fields {
			col := cols.Col(fieldName)
			val, ok := fieldMap[col]
			if !ok {
				panic(fmt.Sprintf("field %q not found in struct", fieldName))
			}
			if !uqb.setColumn(col, val) {
				return uqb
			}
		}
	} else {
		// Sorted, as InsertStruct (#1157).
		for _, col := range sortedKeys(fieldMap) {
			if !uqb.setColumn(col, fieldMap[col]) {
				return uqb
			}
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
// failClause records the FIRST violation and leaves any later one alone — the
// same deferred-error rule the other builders follow (ADR-031).
func (dqb *DeleteQueryBuilder) failClause(err error) {
	if dqb.err == nil {
		dqb.err = err
	}
}

func (dqb *DeleteQueryBuilder) OrderBy(orderBys ...string) dbtypes.DeleteQueryBuilder {
	quotedOrderBys := make([]string, 0, len(orderBys))
	for _, orderBy := range orderBys {
		// Validate the ORDER BY identifier (with optional ASC/DESC [NULLS …])
		// BEFORE interpolation on ALL vendors so it cannot inject SQL (M9).
		normalized, err := validateClauseIdentifier("orderBy", orderBy)
		if err != nil {
			dqb.failClause(err)
			return dqb
		}
		quotedOrderBys = append(quotedOrderBys, dqb.qb.quoteIdentifierForClause(normalized))
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
	// Validate each column identifier BEFORE interpolation (all vendors, M9), the
	// same guard UpdateQueryBuilder.SetMap has applied since ADR-031.
	normalized, err := validateIdentifiers("insert column", columns)
	if err != nil {
		iqb.failClause(err)
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.Columns(iqb.qb.quoteColumnsForDML(normalized...)...)
	return iqb
}

func (iqb *InsertQueryBuilder) Values(values ...any) dbtypes.InsertQueryBuilder {
	cells, err := valueCells(values, positionLabel)
	if err != nil {
		iqb.failClause(err)
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.Values(cells...)
	return iqb
}

func (iqb *InsertQueryBuilder) SetMap(clauses map[string]any) dbtypes.InsertQueryBuilder {
	// Mirrors UpdateQueryBuilder.SetMap, which has validated its keys since
	// ADR-031. That the two disagreed is the defect ADR-082 names: one shape, two
	// builders, opposite safety, nothing in either signature to tell them apart.
	// sortedKeys keeps the reported column deterministic when several are invalid.
	keys := sortedKeys(clauses)
	normalized, err := validateIdentifiers("SetMap column", keys)
	if err != nil {
		iqb.failClause(err)
		return iqb
	}
	// Not squirrel's SetMap: it sorts the keys it is handed, so quoting first
	// would order Oracle's columns by the leading quote ("level" ahead of id)
	// rather than by name. Sorting the caller's names first and quoting in that
	// order keeps the column order both vendors emit today. The columns are the
	// NORMALIZED names; values follow the caller's own keys, so two keys that
	// differ only in padding stay two columns and the database reports the
	// duplicate rather than one value being silently dropped.
	values, err := valueCells(valuesByKeyOrder(clauses, keys), keyLabel(keys))
	if err != nil {
		iqb.failClause(err)
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.
		Columns(iqb.qb.quoteColumnsForDML(normalized...)...).
		Values(values...)
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
		iqb.failClause(fmt.Errorf("InsertQueryBuilder.Select: %w", err))
		return iqb
	}
	concrete, ok := sb.(*SelectQueryBuilder)
	if !ok {
		iqb.failClause(fmt.Errorf("InsertQueryBuilder.Select: unsupported subquery type %T", sb))
		return iqb
	}
	iqb.insertBuilder = iqb.insertBuilder.Select(concrete.buildSelectBuilder())
	return iqb
}

// failClause records the FIRST violation and leaves any later one alone — the
// same deferred-error rule SelectQueryBuilder follows (ADR-031).
func (iqb *InsertQueryBuilder) failClause(err error) {
	if iqb.err == nil {
		iqb.err = err
	}
}

func (iqb *InsertQueryBuilder) ToSQL() (sql string, args []any, err error) {
	if iqb.err != nil {
		return "", nil, iqb.err
	}
	return iqb.insertBuilder.ToSql()
}
