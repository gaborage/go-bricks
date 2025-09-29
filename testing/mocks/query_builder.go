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

// Select implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Select(columns ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(columns))
	for i, col := range columns {
		callArgs[i] = col
	}
	args := m.MethodCalled("Select", callArgs...)
	return args.Get(0).(types.SelectQueryBuilder)
}

// Insert implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Insert(table string) squirrel.InsertBuilder {
	args := m.MethodCalled("Insert", table)
	return args.Get(0).(squirrel.InsertBuilder)
}

// InsertWithColumns implements types.QueryBuilderInterface
func (m *MockQueryBuilder) InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder {
	callArgs := make([]any, len(columns)+1)
	callArgs[0] = table
	for i, col := range columns {
		callArgs[i+1] = col
	}
	args := m.MethodCalled("InsertWithColumns", callArgs...)
	return args.Get(0).(squirrel.InsertBuilder)
}

// Update implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Update(table string) squirrel.UpdateBuilder {
	args := m.MethodCalled("Update", table)
	return args.Get(0).(squirrel.UpdateBuilder)
}

// Delete implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Delete(table string) squirrel.DeleteBuilder {
	args := m.MethodCalled("Delete", table)
	return args.Get(0).(squirrel.DeleteBuilder)
}

// BuildCaseInsensitiveLike implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer {
	args := m.MethodCalled("BuildCaseInsensitiveLike", column, value)
	return args.Get(0).(squirrel.Sqlizer)
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
func (m *MockQueryBuilder) ExpectInsert(table string, builder squirrel.InsertBuilder) *mock.Call {
	return m.On("Insert", table).Return(builder)
}

// ExpectUpdate sets up an update expectation with the provided builder
func (m *MockQueryBuilder) ExpectUpdate(table string, builder squirrel.UpdateBuilder) *mock.Call {
	return m.On("Update", table).Return(builder)
}

// ExpectDelete sets up a delete expectation with the provided builder
func (m *MockQueryBuilder) ExpectDelete(table string, builder squirrel.DeleteBuilder) *mock.Call {
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

func (m *MockQueryBuilder) CrossJoin(table string, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{table}, args...)
	arguments := m.MethodCalled("CrossJoin", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) From(from ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(from))
	for i, table := range from {
		callArgs[i] = table
	}
	arguments := m.MethodCalled("From", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) Where(pred any, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{pred}, args...)
	arguments := m.MethodCalled("Where", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) GroupBy(groupBys ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(groupBys))
	for i, col := range groupBys {
		callArgs[i] = col
	}
	arguments := m.MethodCalled("GroupBy", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) Having(pred any, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{pred}, args...)
	arguments := m.MethodCalled("Having", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) OrderBy(orderBys ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(orderBys))
	for i, col := range orderBys {
		callArgs[i] = col
	}
	arguments := m.MethodCalled("OrderBy", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) InnerJoin(join string, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{join}, args...)
	arguments := m.MethodCalled("InnerJoin", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) LeftJoin(join string, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{join}, args...)
	arguments := m.MethodCalled("LeftJoin", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) RightJoin(join string, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{join}, args...)
	arguments := m.MethodCalled("RightJoin", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) Join(join string, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{join}, args...)
	arguments := m.MethodCalled("Join", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) Limit(limit uint64) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Limit", limit)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) Offset(offset uint64) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Offset", offset)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) Paginate(limit, offset uint64) types.SelectQueryBuilder {
	arguments := m.MethodCalled("Paginate", limit, offset)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) ToSQL() (sql string, args []any, err error) {
	arguments := m.MethodCalled("ToSQL")

	var outArgs []any
	if v, ok := arguments.Get(1).([]any); ok {
		outArgs = v
	}

	return arguments.String(0), outArgs, arguments.Error(2)
}

func (m *MockQueryBuilder) WhereBetween(column string, lowerBound, upperBound any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereBetween", column, lowerBound, upperBound)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereEq(column string, value any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereEq", column, value)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereGt(column string, value any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereGt", column, value)
	return arguments.Get(0).(types.SelectQueryBuilder)
}
func (m *MockQueryBuilder) WhereGte(column string, value any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereGte", column, value)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereIn(column string, values any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereIn", column, values)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereLike(column, pattern string) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereLike", column, pattern)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereLt(column string, value any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereLt", column, value)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereLte(column string, value any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereLte", column, value)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereNotEq(column string, value any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereNotEq", column, value)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereNotIn(column string, values any) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereNotIn", column, values)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereNotNull(column string) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereNotNull", column)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereNull(column string) types.SelectQueryBuilder {
	arguments := m.MethodCalled("WhereNull", column)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

func (m *MockQueryBuilder) WhereRaw(condition string, args ...any) types.SelectQueryBuilder {
	callArgs := append([]any{condition}, args...)
	arguments := m.MethodCalled("WhereRaw", callArgs...)
	return arguments.Get(0).(types.SelectQueryBuilder)
}

// Compile-time verification that MockQueryBuilder implements the interface
var _ types.QueryBuilderInterface = (*MockQueryBuilder)(nil)
var _ types.SelectQueryBuilder = (*MockQueryBuilder)(nil)
