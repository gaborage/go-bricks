// Package database provides cross-database query building utilities
package database

import (
	"github.com/gaborage/go-bricks/database/internal/builder"
	"github.com/gaborage/go-bricks/database/types"
)

// QueryBuilder provides vendor-specific SQL query building.
// This is a compatibility wrapper around the internal implementation.
type QueryBuilder struct {
	*builder.QueryBuilder
}

// NewQueryBuilder creates a new query builder for the specified database vendor.
// This function maintains backward compatibility while using the improved internal implementation.
func NewQueryBuilder(vendor string) *QueryBuilder {
	return &QueryBuilder{
		QueryBuilder: builder.NewQueryBuilder(vendor),
	}
}

// Select creates a SELECT query builder that returns the interface type.
// This method overrides the embedded builder to provide the correct interface.
func (qb *QueryBuilder) Select(columns ...string) types.SelectQueryBuilder {
	return qb.QueryBuilder.Select(columns...)
}

// Filter returns a FilterFactory for creating composable WHERE clause filters.
// This method overrides the embedded builder to provide the correct interface.
func (qb *QueryBuilder) Filter() types.FilterFactory {
	return qb.QueryBuilder.Filter()
}

// Interface compliance check: ensure *QueryBuilder implements types.QueryBuilderInterface
var _ types.QueryBuilderInterface = (*QueryBuilder)(nil)

// The following methods are already implemented by the embedded builder.QueryBuilder
// and are available through struct embedding:
//
// - Vendor() string
// - Insert(table string) squirrel.InsertBuilder
// - InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder
// - Update(table string) squirrel.UpdateBuilder
// - Delete(table string) squirrel.DeleteBuilder
// - BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer
// - BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error)
// - BuildCurrentTimestamp() string
// - BuildUUIDGeneration() string
// - BuildBooleanValue(value bool) any
// - EscapeIdentifier(identifier string) string
