// Package types contains the core database interface definitions for go-bricks.
// These interfaces are separate from the main database package to avoid import cycles
// and to make them easily accessible for mocking and testing.
//
//nolint:revive // Package name "types" is intentionally generic to avoid circular
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
}

// SelectQueryBuilder defines the interface for enhanced SELECT query building with type safety.
// This interface extends basic squirrel.SelectBuilder functionality with additional methods
// for composable filters, JOIN operations, and vendor-specific query features.
type SelectQueryBuilder interface {
	// Core SELECT builder methods
	From(from ...string) SelectQueryBuilder
	Join(join string, rest ...any) SelectQueryBuilder
	LeftJoin(join string, rest ...any) SelectQueryBuilder
	RightJoin(join string, rest ...any) SelectQueryBuilder
	InnerJoin(join string, rest ...any) SelectQueryBuilder
	CrossJoin(join string, rest ...any) SelectQueryBuilder
	GroupBy(groupBys ...string) SelectQueryBuilder
	Having(pred any, rest ...any) SelectQueryBuilder
	OrderBy(orderBys ...string) SelectQueryBuilder
	Limit(limit uint64) SelectQueryBuilder
	Offset(offset uint64) SelectQueryBuilder
	Paginate(limit, offset uint64) SelectQueryBuilder

	// Composable WHERE clause
	Where(filter Filter) SelectQueryBuilder

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

// QueryBuilderInterface defines the interface for vendor-specific SQL query building.
// This interface allows for dependency injection and mocking of query builders,
// enabling unit testing of business logic that constructs queries without
// actually generating SQL strings.
type QueryBuilderInterface interface {
	// Vendor information
	Vendor() string

	// Filter factory
	Filter() FilterFactory

	// Query builders
	Select(columns ...string) SelectQueryBuilder
	Insert(table string) squirrel.InsertBuilder
	InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder
	Update(table string) squirrel.UpdateBuilder
	Delete(table string) squirrel.DeleteBuilder

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
