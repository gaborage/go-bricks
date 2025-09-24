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
//	mockQB.On("Select", "id", "name").Return(squirrel.SelectBuilder{})
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
func (m *MockQueryBuilder) Select(columns ...string) squirrel.SelectBuilder {
	args := m.MethodCalled("Select", columns)
	return args.Get(0).(squirrel.SelectBuilder)
}

// Insert implements types.QueryBuilderInterface
func (m *MockQueryBuilder) Insert(table string) squirrel.InsertBuilder {
	args := m.MethodCalled("Insert", table)
	return args.Get(0).(squirrel.InsertBuilder)
}

// InsertWithColumns implements types.QueryBuilderInterface
func (m *MockQueryBuilder) InsertWithColumns(table string, columns ...string) squirrel.InsertBuilder {
	args := m.MethodCalled("InsertWithColumns", table, columns)
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

// BuildLimitOffset implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildLimitOffset(query squirrel.SelectBuilder, limit, offset int) squirrel.SelectBuilder {
	args := m.MethodCalled("BuildLimitOffset", query, limit, offset)
	return args.Get(0).(squirrel.SelectBuilder)
}

// BuildUpsert implements types.QueryBuilderInterface
func (m *MockQueryBuilder) BuildUpsert(table string, conflictColumns []string, insertColumns, updateColumns map[string]any) (query string, args []any, err error) {
	arguments := m.MethodCalled("BuildUpsert", table, conflictColumns, insertColumns, updateColumns)
	return arguments.String(0), arguments.Get(1).([]any), arguments.Error(2)
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
func (m *MockQueryBuilder) ExpectSelect(columns []string, builder squirrel.SelectBuilder) *mock.Call {
	return m.On("Select", columns).Return(builder)
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

// Compile-time verification that MockQueryBuilder implements the interface
var _ types.QueryBuilderInterface = (*MockQueryBuilder)(nil)
