// Package database provides cross-database query building utilities
package database

import (
	"github.com/gaborage/go-bricks/database/internal/builder"
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

// The following methods are already implemented by the embedded builder.QueryBuilder
// and are available through struct embedding:
//
// - Vendor() string
// - Select(columns ...string) squirrel.SelectBuilder
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
