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

	// Struct-based UPDATE (v2.4+)
	// SetStruct extracts field values from a struct instance for UPDATE.
	// If fields are specified, only those fields are updated.
	// If no fields specified, all struct fields are updated.
	SetStruct(instance any, fields ...string) UpdateQueryBuilder

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

// Columns represents cached metadata for a struct type with `db:"column_name"` tags.
// It provides methods to retrieve vendor-quoted column names with optional table aliasing.
//
// This interface is implemented by the internal columns package and returned by
// QueryBuilder.Columns(). The metadata is lazily parsed on first use and cached forever.
//
// v2.4 Breaking Changes:
//   - Renamed Get() → Col() for consistency with database terminology
//   - Renamed Fields() → Cols() with []string return type (was []any)
//   - All() now returns []string instead of []any
//   - Added As() for immutable aliasing: cols.As("u").Col("ID") → "u.id"
//   - Added FieldMap() and AllFields() for struct-to-query conversion
type Columns interface {
	// Col retrieves the vendor-quoted column name for the given struct field name.
	// If aliased via As(), returns qualified column (e.g., "u.id").
	// Panics if the field name is not found (fail-fast for development-time typos).
	//
	// Example (unaliased):
	//   cols.Col("ID")    // Returns: "id" (PostgreSQL) or "ID" (Oracle)
	//   cols.Col("Level") // Returns: "level" (PostgreSQL) or "LEVEL" (Oracle, quoted)
	//
	// Example (aliased):
	//   u := cols.As("u")
	//   u.Col("ID")       // Returns: "u.id" (PostgreSQL) or "u.\"ID\"" (Oracle)
	Col(fieldName string) string

	// As returns a new Columns instance bound to the specified table alias.
	// The returned instance shares the underlying column metadata (zero-copy).
	// This method is immutable - the original Columns instance remains unchanged.
	//
	// Example:
	//   cols := qb.Columns(&User{})
	//   u := cols.As("u")
	//   p := cols.As("p")
	//   u.Col("ID")  // "u.id"
	//   p.Col("ID")  // "p.id"
	//   cols.Col("ID") // "id" (original unaffected)
	As(alias string) Columns

	// Alias returns the current table alias, or empty string if unaliased.
	//
	// Example:
	//   cols.Alias()        // ""
	//   cols.As("u").Alias() // "u"
	Alias() string

	// Cols retrieves vendor-quoted column names for multiple struct field names.
	// If aliased, returns qualified columns.
	// Panics if any field name is not found.
	//
	// Example:
	//   cols.Cols("ID", "Name", "Email") // ["id", "name", "email"]
	//   cols.As("u").Cols("ID", "Name")  // ["u.id", "u.name"]
	Cols(fieldNames ...string) []string

	// All returns vendor-quoted column names for all columns in the struct,
	// in the order they were declared in the struct definition.
	//
	// Example:
	//   cols.All()        // ["id", "name", "email"] (unaliased)
	//   cols.As("u").All() // ["u.id", "u.name", "u.email"] (aliased)
	All() []string

	// FieldMap extracts field values from a struct instance into a map.
	// The map keys are vendor-quoted column names (respecting alias if set).
	// Only fields with `db` tags are included. Zero values are included.
	//
	// Example:
	//   user := User{ID: 123, Name: "Alice", Email: "alice@example.com"}
	//   cols.FieldMap(&user)
	//   // Returns: {"id": 123, "name": "Alice", "email": "alice@example.com"}
	//
	//   cols.As("u").FieldMap(&user)
	//   // Returns: {"u.id": 123, "u.name": "Alice", "u.email": "alice@example.com"}
	//
	// Panics if instance is not a struct or pointer to struct.
	FieldMap(instance any) map[string]any

	// AllFields extracts all field values from a struct instance as separate slices.
	// Returns (columns, values) suitable for bulk INSERT/UPDATE operations.
	// Only fields with `db` tags are included. Zero values are included.
	//
	// Example:
	//   user := User{ID: 123, Name: "Alice", Email: "alice@example.com"}
	//   cols, vals := cols.AllFields(&user)
	//   // cols: ["id", "name", "email"]
	//   // vals: [123, "Alice", "alice@example.com"]
	//
	// Panics if instance is not a struct or pointer to struct.
	AllFields(instance any) ([]string, []any)
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

	// Column metadata extraction (v2.4+)
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
	//   qb.Select(cols.Col("ID"), cols.Col("Level")).From("users")
	//
	//   // With aliasing:
	//   u := cols.As("u")
	//   qb.Select(u.Col("ID"), u.Col("Level")).From(Table("users").As("u"))
	//
	// Panics if structPtr is not a pointer to a struct with db tags.
	Columns(structPtr any) Columns

	// Query builders
	Select(columns ...any) SelectQueryBuilder
	Insert(table string) squirrel.InsertBuilder
	InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder

	// Struct-based INSERT (v2.4+)
	// InsertStruct extracts all fields from a struct instance and creates an INSERT query.
	// Zero-value ID fields (int64/string) are automatically excluded to support auto-increment.
	InsertStruct(table string, instance any) squirrel.InsertBuilder

	// InsertFields extracts only specified fields from a struct instance for INSERT.
	// Useful for partial inserts or when you need explicit control over included fields.
	InsertFields(table string, instance any, fields ...string) squirrel.InsertBuilder

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
