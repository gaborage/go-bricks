// Package types contains the core database interface definitions for go-bricks.
// These interfaces are separate from the main database package to avoid import cycles
// and to make them easily accessible for mocking and testing.
//
//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Masterminds/squirrel"
)

// Filter represents a composable WHERE clause filter that can be combined with AND/OR/NOT operators.
// Filters are created through FilterFactory methods obtained from QueryBuilder.Filter().
//
// Filter embeds squirrel.Sqlizer for compatibility with Squirrel's query builder, and adds
// ToSQL() as a convenience method with idiomatic Go naming (uppercase SQL).
//
// This is an interface to support mocking and testing. The concrete implementation is in
// database/internal/builder package.
type Filter interface {
	squirrel.Sqlizer

	// ToSQL is a convenience method with idiomatic Go naming.
	// It should delegate to ToSql() in implementations.
	ToSQL() (sql string, args []any, err error)
}

// FilterFactory provides methods for creating composable, type-safe query filters.
// Filters maintain vendor-specific quoting rules and can be combined with AND/OR/NOT logic.
type FilterFactory interface {
	// Comparison operators
	Eq(column string, value any) Filter
	NotEq(column string, value any) Filter
	Lt(column string, value any) Filter
	Lte(column string, value any) Filter
	Gt(column string, value any) Filter
	Gte(column string, value any) Filter
	In(column string, values any) Filter
	NotIn(column string, values any) Filter
	Like(column, pattern string) Filter
	Null(column string) Filter
	NotNull(column string) Filter
	Between(column string, lowerBound, upperBound any) Filter

	// Logical operators
	And(filters ...Filter) Filter
	Or(filters ...Filter) Filter
	Not(filter Filter) Filter

	// Raw escape hatch
	Raw(condition string, args ...any) Filter

	// Subquery support (v2.1+)
	Exists(subquery SelectQueryBuilder) Filter
	NotExists(subquery SelectQueryBuilder) Filter
	InSubquery(column string, subquery SelectQueryBuilder) Filter
}

// JoinFilter represents a filter specifically for JOIN ON conditions where columns are compared
// to other columns (not to values). This follows Single Responsibility Principle by separating
// column-to-column comparisons (JOIN) from column-to-value comparisons (WHERE).
//
// JoinFilters are created through JoinFilterFactory methods obtained from QueryBuilder.JoinFilter().
//
// Example:
//
//	jf := qb.JoinFilter()
//	query := qb.Select("*").From("users").JoinOn("profiles", jf.EqColumn("users.id", "profiles.user_id"))
//
// This is an interface to support mocking and testing. The concrete implementation is in
// database/internal/builder package.
type JoinFilter interface {
	squirrel.Sqlizer

	// ToSQL is a convenience method with idiomatic Go naming.
	// It should delegate to ToSql() in implementations.
	ToSQL() (sql string, args []any, err error)
}

// JoinFilterFactory provides methods for creating type-safe JOIN ON filters.
// JoinFilters support both column-to-column comparisons and column-to-value comparisons,
// enabling mixed JOIN conditions without requiring Raw() for common cases.
//
// Column-to-value methods accept RawExpression for complex SQL expressions:
//
//	jf.Eq("amount", qb.Expr("TO_NUMBER(?)"), 100)  // Expression support
//	jf.Eq("status", "active")                      // Simple value with placeholder
type JoinFilterFactory interface {
	// Column-to-column comparison operators
	EqColumn(leftColumn, rightColumn string) JoinFilter
	NotEqColumn(leftColumn, rightColumn string) JoinFilter
	LtColumn(leftColumn, rightColumn string) JoinFilter
	LteColumn(leftColumn, rightColumn string) JoinFilter
	GtColumn(leftColumn, rightColumn string) JoinFilter
	GteColumn(leftColumn, rightColumn string) JoinFilter

	// Column-to-value comparison operators (v2.2+)
	// These methods accept any value type, including RawExpression for complex SQL.
	// Regular values generate placeholders; RawExpression values are inserted verbatim.
	Eq(column string, value any) JoinFilter
	NotEq(column string, value any) JoinFilter
	Lt(column string, value any) JoinFilter
	Lte(column string, value any) JoinFilter
	Gt(column string, value any) JoinFilter
	Gte(column string, value any) JoinFilter
	In(column string, values any) JoinFilter
	NotIn(column string, values any) JoinFilter
	Like(column, pattern string) JoinFilter
	Null(column string) JoinFilter
	NotNull(column string) JoinFilter
	Between(column string, lowerBound, upperBound any) JoinFilter

	// Logical operators for complex JOIN conditions
	And(filters ...JoinFilter) JoinFilter
	Or(filters ...JoinFilter) JoinFilter

	// Raw escape hatch for complex JOIN conditions
	Raw(condition string, args ...any) JoinFilter
}

// SelectQueryBuilder defines the interface for enhanced SELECT query building with type safety.
// This interface extends basic squirrel.SelectBuilder functionality with additional methods
// for composable filters, JOIN operations, and vendor-specific query features.
type SelectQueryBuilder interface {
	// Core SELECT builder methods
	// From accepts either string table names or *TableRef instances with optional aliases
	From(from ...any) SelectQueryBuilder

	// Type-safe JOIN methods with JoinFilter (v2.0+)
	// Each accepts either string table name or *TableRef instance with optional alias
	JoinOn(table any, filter JoinFilter) SelectQueryBuilder
	LeftJoinOn(table any, filter JoinFilter) SelectQueryBuilder
	RightJoinOn(table any, filter JoinFilter) SelectQueryBuilder
	InnerJoinOn(table any, filter JoinFilter) SelectQueryBuilder
	CrossJoinOn(table any) SelectQueryBuilder

	GroupBy(groupBys ...any) SelectQueryBuilder
	Having(pred any, rest ...any) SelectQueryBuilder
	OrderBy(orderBys ...any) SelectQueryBuilder
	Limit(limit uint64) SelectQueryBuilder
	Offset(offset uint64) SelectQueryBuilder
	Paginate(limit, offset uint64) SelectQueryBuilder

	// Composable WHERE clause
	Where(filter Filter) SelectQueryBuilder

	// SQL generation
	ToSQL() (sql string, args []any, err error)
}

// UpdateQueryBuilder defines the interface for UPDATE query building with type-safe filtering.
// This interface wraps squirrel.UpdateBuilder with Filter API support and vendor-specific quoting.
type UpdateQueryBuilder interface {
	// Data modification
	Set(column string, value any) UpdateQueryBuilder
	SetMap(clauses map[string]any) UpdateQueryBuilder

	// Filtering
	Where(filter Filter) UpdateQueryBuilder

	// SQL generation
	ToSQL() (sql string, args []any, err error)
}

// DeleteQueryBuilder defines the interface for DELETE query building with type-safe filtering.
// This interface wraps squirrel.DeleteBuilder with Filter API support.
type DeleteQueryBuilder interface {
	// Filtering
	Where(filter Filter) DeleteQueryBuilder

	// Batch operations
	Limit(limit uint64) DeleteQueryBuilder
	OrderBy(orderBys ...string) DeleteQueryBuilder

	// SQL generation
	ToSQL() (sql string, args []any, err error)
}

// Database vendor identifiers shared across the database packages.
type Vendor = string

const (
	PostgreSQL Vendor = "postgresql"
	Oracle     Vendor = "oracle"
	MongoDB    Vendor = "mongodb"
)

// Row represents a single result set row with basic scanning behaviour.
type Row interface {
	Scan(dest ...any) error
	Err() error
}

type sqlRowAdapter struct {
	row *sql.Row
}

// NewRowFromSQL wraps the provided *sql.Row in a Row.
// If row is nil, NewRowFromSQL returns nil.
func NewRowFromSQL(row *sql.Row) Row {
	if row == nil {
		return nil
	}
	return &sqlRowAdapter{row: row}
}

func (r *sqlRowAdapter) Scan(dest ...any) error {
	if r == nil || r.row == nil {
		return errors.New("sqlRowAdapter: underlying sql.Row is nil")
	}
	return r.row.Scan(dest...)
}

func (r *sqlRowAdapter) Err() error {
	if r == nil || r.row == nil {
		return errors.New("sqlRowAdapter: underlying sql.Row is nil")
	}
	return r.row.Err()
}

// Statement defines the interface for prepared statements
type Statement interface {
	// Query execution
	Query(ctx context.Context, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, args ...any) Row
	Exec(ctx context.Context, args ...any) (sql.Result, error)

	// Statement management
	Close() error
}

// Tx defines the interface for database transactions
type Tx interface {
	// Query execution within transaction
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Prepared statements within transaction
	Prepare(ctx context.Context, query string) (Statement, error)

	// Transaction control
	Commit() error
	Rollback() error
}

// Interface defines the common database operations supported by the framework.
// This is the main interface that applications and modules should depend on
// for database operations, allowing for easy mocking and testing.
type Interface interface {
	// Query execution
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)

	// Prepared statements
	Prepare(ctx context.Context, query string) (Statement, error)

	// Transaction support
	Begin(ctx context.Context) (Tx, error)
	BeginTx(ctx context.Context, opts *sql.TxOptions) (Tx, error)

	// Health and diagnostics
	Health(ctx context.Context) error
	Stats() (map[string]any, error)

	// Connection management
	Close() error

	// Database-specific features
	DatabaseType() string

	// Migration support
	GetMigrationTable() string
	CreateMigrationTable(ctx context.Context) error
}

// ColumnMetadata represents cached metadata for a struct type with `db:"column_name"` tags.
// It provides methods to retrieve vendor-quoted column names for use in query building.
//
// This interface is implemented by the internal columns package and returned by
// QueryBuilder.Columns(). The metadata is lazily parsed on first use and cached forever.
type ColumnMetadata interface {
	// Get retrieves the vendor-quoted column name for the given struct field name.
	// Panics if the field name is not found (fail-fast for development-time typos).
	//
	// Example:
	//   cols.Get("ID")    // Returns: "id" (PostgreSQL) or `"ID"` (Oracle)
	//   cols.Get("Level") // Returns: "level" (PostgreSQL) or `"LEVEL"` (Oracle, quoted)
	Get(fieldName string) string

	// Fields retrieves vendor-quoted column names for multiple struct field names.
	// Panics if any field name is not found.
	//
	// Example:
	//   qb.Select(cols.Fields("ID", "Name", "Email")...).From("users")
	Fields(fieldNames ...string) []any

	// All returns vendor-quoted column names for all columns in the struct,
	// in the order they were declared in the struct definition.
	//
	// Example:
	//   qb.Select(cols.All()...).From("users") // SELECT all columns
	All() []any
}

// QueryBuilderInterface defines the interface for vendor-specific SQL query building.
// This interface allows for dependency injection and mocking of query builders,
// enabling unit testing of business logic that constructs queries without
// actually generating SQL strings.
type QueryBuilderInterface interface {
	// Vendor information
	Vendor() string

	// Filter factories
	Filter() FilterFactory
	JoinFilter() JoinFilterFactory

	// Expression builder (v2.1+)
	Expr(sql string, alias ...string) RawExpression

	// Column metadata extraction (v2.3+)
	// Extracts column metadata from structs with `db:"column_name"` tags.
	// Lazily parses struct on first use, caches forever.
	// Returns vendor-specific quoted column names (e.g., Oracle reserved words).
	//
	// Example:
	//   type User struct {
	//       ID    int64  `db:"id"`
	//       Level string `db:"level"` // Oracle reserved word
	//   }
	//   cols := qb.Columns(&User{})
	//   qb.Select(cols.Get("ID"), cols.Get("Level")).From("users")
	//
	// Panics if structPtr is not a pointer to a struct with db tags.
	Columns(structPtr any) ColumnMetadata

	// Query builders
	Select(columns ...any) SelectQueryBuilder
	Insert(table string) squirrel.InsertBuilder
	InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder
	Update(table string) UpdateQueryBuilder
	Delete(table string) DeleteQueryBuilder

	// Vendor-specific helpers
	BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer
	BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error)

	// Database function builders
	BuildCurrentTimestamp() string
	BuildUUIDGeneration() string
	BuildBooleanValue(value bool) any

	// Identifier escaping
	EscapeIdentifier(identifier string) string
}
