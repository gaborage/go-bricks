// Package database provides cross-database query building utilities
package database

import (
	"github.com/gaborage/go-bricks/database/internal/builder"
	"github.com/gaborage/go-bricks/database/types"
)

// QueryBuilder provides vendor-specific SQL query building over the embedded
// internal builder.
type QueryBuilder struct {
	*builder.QueryBuilder
}

// NewQueryBuilder creates a new query builder for the specified database vendor.
func NewQueryBuilder(vendor string) *QueryBuilder {
	return &QueryBuilder{
		QueryBuilder: builder.NewQueryBuilder(vendor),
	}
}

// Select creates a SELECT query builder that returns the interface type.
// This method overrides the embedded builder to provide the correct interface.
func (qb *QueryBuilder) Select(columns ...any) types.SelectQueryBuilder {
	return qb.QueryBuilder.Select(columns...)
}

// Interface compliance check: ensure *QueryBuilder implements types.QueryBuilderInterface
var _ types.QueryBuilderInterface = (*QueryBuilder)(nil)

// The following methods are already implemented by the embedded builder.QueryBuilder
// and are available through struct embedding:
//
// - Vendor() string
// - Filter() types.FilterFactory
// - JoinFilter() types.JoinFilterFactory
// - Expr(sql string, alias ...string) (types.RawExpression, error)
// - MustExpr(sql string, alias ...string) types.RawExpression
// - Columns(structPtr any) types.Columns
// - Insert(table string) types.InsertQueryBuilder
// - InsertWithColumns(table string, columns ...string) types.InsertQueryBuilder
// - InsertStruct(table string, instance any) types.InsertQueryBuilder
// - InsertFields(table string, instance any, fields ...string) types.InsertQueryBuilder
// - Update(table string) types.UpdateQueryBuilder
// - Delete(table string) types.DeleteQueryBuilder
// - BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer
// - BuildRegex(column, pattern string, caseInsensitive, negated bool) squirrel.Sqlizer
// - BuildJSONContains(column string, value any) squirrel.Sqlizer
// - BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error)
// - BuildCurrentTimestamp() string
// - BuildUUIDGeneration() string
// - BuildBooleanValue(value bool) any
// - EscapeIdentifier(identifier string) string
