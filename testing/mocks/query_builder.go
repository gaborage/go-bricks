package mocks

import (
	"github.com/Masterminds/squirrel"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/database/types"
)

// MockQueryBuilder provides a testify-based mock implementation of the QueryBuilderInterface.
// It allows for sophisticated testing scenarios with expectation setting and behavior verification
// for services that construct SQL queries using the query builder.
//
// Example usage:
//
//	mockQB := &mocks.MockQueryBuilder{}
//	mockQB.On("Vendor").Return("postgresql")
//	mockQB.On("Select", "id", "name").Return(mockSelectBuilder)
//	mockQB.On("BuildCaseInsensitiveLike", "name", "john").Return(squirrel.ILike{"name": "%john%"})
//
//	// Use mockQB in your tests
//	result := service.BuildUserQuery(mockQB, criteria)
type MockQueryBuilder struct {
	mock.Mock
}

// Vendor implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Vendor() string {
	args := m.MethodCalled("Vendor")
	return args.String(0)
}

// Filter implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Filter() types.FilterFactory {
	args := m.MethodCalled("Filter")
	return arg[types.FilterFactory](args, "Filter", 0)
}

// JoinFilter implements types.QueryBuilderInterface
func (m *MockQueryBuilder) JoinFilter() types.JoinFilterFactory {
	args := m.MethodCalled("JoinFilter")
	return arg[types.JoinFilterFactory](args, "JoinFilter", 0)
}

// Expr implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Expr(sql string, alias ...string) (types.RawExpression, error) {
	callArgs := make([]any, len(alias)+1)
	callArgs[0] = sql
	for i, a := range alias {
		callArgs[i+1] = a
	}
	args := m.MethodCalled("Expr", callArgs...)
	return arg[types.RawExpression](args, "Expr", 0), args.Error(1)
}

// MustExpr implements types.QueryBuilderInterface
func (m *MockQueryBuilder) MustExpr(sql string, alias ...string) types.RawExpression {
	expr, err := m.Expr(sql, alias...)
	if err != nil {
		panic(err)
	}
	return expr
}

// Columns implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Columns(structPtr any) types.Columns {
	args := m.MethodCalled("Columns", structPtr)
	return arg[types.Columns](args, "Columns", 0)
}

// Select implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Select(columns ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("Select", columns...)
	return arg[types.SelectQueryBuilder](args, "Select", 0)
}

// Insert implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Insert(table string) types.InsertQueryBuilder {
	args := m.MethodCalled("Insert", table)
	return arg[types.InsertQueryBuilder](args, "Insert", 0)
}

// InsertWithColumns implements types.QueryBuilderInterface
func (m *MockQueryBuilder) InsertWithColumns(table string, columns ...string) types.InsertQueryBuilder {
	callArgs := make([]any, len(columns)+1)
	callArgs[0] = table
	for i, col := range columns {
		callArgs[i+1] = col
	}
	args := m.MethodCalled("InsertWithColumns", callArgs...)
	return arg[types.InsertQueryBuilder](args, "InsertWithColumns", 0)
}

// InsertStruct implements types.QueryBuilderInterface
func (m *MockQueryBuilder) InsertStruct(table string, instance any) types.InsertQueryBuilder {
	args := m.MethodCalled("InsertStruct", table, instance)
	return arg[types.InsertQueryBuilder](args, "InsertStruct", 0)
}

// InsertFields implements types.QueryBuilderInterface
func (m *MockQueryBuilder) InsertFields(table string, instance any, fields ...string) types.InsertQueryBuilder {
	callArgs := make([]any, len(fields)+2)
	callArgs[0] = table
	callArgs[1] = instance
	for i, field := range fields {
		callArgs[i+2] = field
	}
	args := m.MethodCalled("InsertFields", callArgs...)
	return arg[types.InsertQueryBuilder](args, "InsertFields", 0)
}

// Update implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Update(table string) types.UpdateQueryBuilder {
	args := m.MethodCalled("Update", table)
	return arg[types.UpdateQueryBuilder](args, "Update", 0)
}

// Delete implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Delete(table string) types.DeleteQueryBuilder {
	args := m.MethodCalled("Delete", table)
	return arg[types.DeleteQueryBuilder](args, "Delete", 0)
}

// BuildCaseInsensitiveLike implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer {
	args := m.MethodCalled("BuildCaseInsensitiveLike", column, value)
	return arg[squirrel.Sqlizer](args, "BuildCaseInsensitiveLike", 0)
}

// BuildUpsert implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	arguments := m.MethodCalled("BuildUpsert", table, conflictColumns, insertColumns, updateColumns)
	argsVal, ok := arguments.Get(1).([]any)
	if !ok {
		argsVal = nil
	}
	return arguments.String(0), argsVal, arguments.Error(2)
}

// BuildCurrentTimestamp implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildCurrentTimestamp() string {
	args := m.MethodCalled("BuildCurrentTimestamp")
	return args.String(0)
}

// BuildUUIDGeneration implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildUUIDGeneration() string {
	args := m.MethodCalled("BuildUUIDGeneration")
	return args.String(0)
}

// BuildBooleanValue implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildBooleanValue(value bool) any {
	args := m.MethodCalled("BuildBooleanValue", value)
	return args.Get(0)
}

// EscapeIdentifier implements types.QueryBuilderInterface
func (m *MockQueryBuilder) EscapeIdentifier(identifier string) string {
	args := m.MethodCalled("EscapeIdentifier", identifier)
	return args.String(0)
}

// Helper methods for common testing scenarios

// ExpectVendor sets up a vendor expectation
func (m *MockQueryBuilder) ExpectVendor(vendor string) *mock.Call {
	return m.On("Vendor").Return(vendor)
}

// ExpectSelect sets up a select expectation with the provided builder
func (m *MockQueryBuilder) ExpectSelect(columns []string, builder types.SelectQueryBuilder) *mock.Call {
	callArgs := make([]any, len(columns))
	for i, col := range columns {
		callArgs[i] = col
	}
	return m.On("Select", callArgs...).Return(builder)
}

// ExpectInsert sets up an insert expectation with the provided builder
func (m *MockQueryBuilder) ExpectInsert(table string, builder types.InsertQueryBuilder) *mock.Call {
	return m.On("Insert", table).Return(builder)
}

// ExpectUpdate sets up an update expectation with the provided builder
func (m *MockQueryBuilder) ExpectUpdate(table string, builder types.UpdateQueryBuilder) *mock.Call {
	return m.On("Update", table).Return(builder)
}

// ExpectDelete sets up a delete expectation with the provided builder
func (m *MockQueryBuilder) ExpectDelete(table string, builder types.DeleteQueryBuilder) *mock.Call {
	return m.On("Delete", table).Return(builder)
}

// ExpectCaseInsensitiveLike sets up a case-insensitive like expectation
func (m *MockQueryBuilder) ExpectCaseInsensitiveLike(column, value string, sqlizer squirrel.Sqlizer) *mock.Call {
	return m.On("BuildCaseInsensitiveLike", column, value).Return(sqlizer)
}

// ExpectCurrentTimestamp sets up a current timestamp expectation
func (m *MockQueryBuilder) ExpectCurrentTimestamp(timestamp string) *mock.Call {
	return m.On("BuildCurrentTimestamp").Return(timestamp)
}

// ExpectUUIDGeneration sets up a UUID generation expectation
func (m *MockQueryBuilder) ExpectUUIDGeneration(uuidFunc string) *mock.Call {
	return m.On("BuildUUIDGeneration").Return(uuidFunc)
}

// ExpectBooleanValue sets up a boolean value conversion expectation
func (m *MockQueryBuilder) ExpectBooleanValue(input bool, output any) *mock.Call {
	return m.On("BuildBooleanValue", input).Return(output)
}

// ExpectEscapeIdentifier sets up an identifier escaping expectation
func (m *MockQueryBuilder) ExpectEscapeIdentifier(input, output string) *mock.Call {
	return m.On("EscapeIdentifier", input).Return(output)
}

// JoinOn implements types.SelectQueryBuilder
func (m *MockQueryBuilder) JoinOn(table any, filter types.JoinFilter) types.SelectQueryBuilder {
	arguments := m.MethodCalled("JoinOn", table, filter)
	return arg[types.SelectQueryBuilder](arguments, "JoinOn", 0)
}

// LeftJoinOn implements types.SelectQueryBuilder
func (m *MockQueryBuilder) LeftJoinOn(table any, filter types.JoinFilter) types.SelectQueryBuilder {
	arguments := m.MethodCalled("LeftJoinOn", table, filter)
	return arg[types.SelectQueryBuilder](arguments, "LeftJoinOn", 0)
}

// RightJoinOn implements types.SelectQueryBuilder
func (m *MockQueryBuilder) RightJoinOn(table any, filter types.JoinFilter) types.SelectQueryBuilder {
	arguments := m.MethodCalled("RightJoinOn", table, filter)
	return arg[types.SelectQueryBuilder](arguments, "RightJoinOn", 0)
}

// InnerJoinOn implements types.SelectQueryBuilder
func (m *MockQueryBuilder) InnerJoinOn(table any, filter types.JoinFilter) types.SelectQueryBuilder {
	arguments := m.MethodCalled("InnerJoinOn", table, filter)
	return arg[types.SelectQueryBuilder](arguments, "InnerJoinOn", 0)
}

// CrossJoinOn implements types.SelectQueryBuilder
func (m *MockQueryBuilder) CrossJoinOn(table any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("CrossJoinOn", table)
	return arg[types.SelectQueryBuilder](arguments, "CrossJoinOn", 0)
}

func (m *MockQueryBuilder) From(from ...any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("From", from...)
	return arg[types.SelectQueryBuilder](arguments, "From", 0)
}

func (m *MockQueryBuilder) GroupBy(groupBys ...any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("GroupBy", groupBys...)
	return arg[types.SelectQueryBuilder](arguments, "GroupBy", 0)
}

func (m *MockQueryBuilder) Having(pred any, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{pred}, args...)
	arguments := m.MethodCalled("Having", callArgs...)
	return arg[types.SelectQueryBuilder](arguments, "Having", 0)
}

func (m *MockQueryBuilder) OrderBy(orderBys ...any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("OrderBy", orderBys...)
	return arg[types.SelectQueryBuilder](arguments, "OrderBy", 0)
}

func (m *MockQueryBuilder) Limit(limit uint64) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Limit", limit)
	return arg[types.SelectQueryBuilder](arguments, "Limit", 0)
}

func (m *MockQueryBuilder) Offset(offset uint64) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Offset", offset)
	return arg[types.SelectQueryBuilder](arguments, "Offset", 0)
}

func (m *MockQueryBuilder) Paginate(limit, offset uint64) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Paginate", limit, offset)
	return arg[types.SelectQueryBuilder](arguments, "Paginate", 0)
}

func (m *MockQueryBuilder) ToSQL() (sql string, args []any, err error) {
	arguments := m.MethodCalled("ToSQL")

	var outArgs []any
	if v, ok := arguments.Get(1).([]any); ok {
		outArgs = v
	}

	return arguments.String(0), outArgs, arguments.Error(2)
}

// Where implements types.SelectQueryBuilder
func (m *MockQueryBuilder) Where(filter types.Filter) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Where", filter)
	return arg[types.SelectQueryBuilder](arguments, "Where", 0)
}

// Compile-time verification that MockQueryBuilder implements the interface
var (
	_ types.QueryBuilderInterface = (*MockQueryBuilder)(nil)
	_ types.SelectQueryBuilder    = (*MockQueryBuilder)(nil)
)
