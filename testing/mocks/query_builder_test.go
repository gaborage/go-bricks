package mocks

import (
	"testing"

	"github.com/Masterminds/squirrel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/database/types"
)

// MockSelectQueryBuilder provides a mock implementation of types.SelectQueryBuilder for testing
type MockSelectQueryBuilder struct {
	mock.Mock
}

// MockFilterFactory provides a mock implementation of types.FilterFactory for testing
type MockFilterFactory struct {
	mock.Mock
}

func (m *MockFilterFactory) Eq(column string, value any) types.Filter {
	args := m.MethodCalled("Eq", column, value)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) NotEq(column string, value any) types.Filter {
	args := m.MethodCalled("NotEq", column, value)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Lt(column string, value any) types.Filter {
	args := m.MethodCalled("Lt", column, value)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Lte(column string, value any) types.Filter {
	args := m.MethodCalled("Lte", column, value)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Gt(column string, value any) types.Filter {
	args := m.MethodCalled("Gt", column, value)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Gte(column string, value any) types.Filter {
	args := m.MethodCalled("Gte", column, value)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) In(column string, values any) types.Filter {
	args := m.MethodCalled("In", column, values)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) NotIn(column string, values any) types.Filter {
	args := m.MethodCalled("NotIn", column, values)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Like(column, pattern string) types.Filter {
	args := m.MethodCalled("Like", column, pattern)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Null(column string) types.Filter {
	args := m.MethodCalled("Null", column)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) NotNull(column string) types.Filter {
	args := m.MethodCalled("NotNull", column)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Between(column string, lowerBound, upperBound any) types.Filter {
	args := m.MethodCalled("Between", column, lowerBound, upperBound)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) And(filters ...types.Filter) types.Filter {
	callArgs := make([]any, len(filters))
	for i, filter := range filters {
		callArgs[i] = filter
	}
	args := m.MethodCalled("And", callArgs...)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Or(filters ...types.Filter) types.Filter {
	callArgs := make([]any, len(filters))
	for i, filter := range filters {
		callArgs[i] = filter
	}
	args := m.MethodCalled("Or", callArgs...)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Not(filter types.Filter) types.Filter {
	args := m.MethodCalled("Not", filter)
	return args.Get(0).(types.Filter)
}

func (m *MockFilterFactory) Raw(condition string, args ...any) types.Filter {
	callArgs := append([]any{condition}, args...)
	mockArgs := m.MethodCalled("Raw", callArgs...)
	return mockArgs.Get(0).(types.Filter)
}

var _ types.FilterFactory = (*MockFilterFactory)(nil)

// MockFilter provides a mock implementation of types.Filter for testing
type MockFilter struct {
	mock.Mock
}

func (m *MockFilter) ToSQL() (sql string, args []any, err error) {
	mockArgs := m.MethodCalled("ToSQL")
	var outArgs []any
	if v := mockArgs.Get(1); v != nil {
		if cast, ok := v.([]any); ok {
			outArgs = cast
		}
	}
	return mockArgs.String(0), outArgs, mockArgs.Error(2)
}

//nolint:revive // ToSql is required by squirrel.Sqlizer interface (lowercase 's')
func (m *MockFilter) ToSql() (sql string, args []any, err error) {
	mockArgs := m.MethodCalled("ToSql")
	var outArgs []any
	if v := mockArgs.Get(1); v != nil {
		if cast, ok := v.([]any); ok {
			outArgs = cast
		}
	}
	return mockArgs.String(0), outArgs, mockArgs.Error(2)
}

var _ types.Filter = (*MockFilter)(nil)

// MockUpdateQueryBuilder provides a mock implementation of types.UpdateQueryBuilder for testing
type MockUpdateQueryBuilder struct {
	mock.Mock
}

func (m *MockUpdateQueryBuilder) Set(column string, value any) types.UpdateQueryBuilder {
	args := m.MethodCalled("Set", column, value)
	return args.Get(0).(types.UpdateQueryBuilder)
}

func (m *MockUpdateQueryBuilder) SetMap(clauses map[string]any) types.UpdateQueryBuilder {
	args := m.MethodCalled("SetMap", clauses)
	return args.Get(0).(types.UpdateQueryBuilder)
}

func (m *MockUpdateQueryBuilder) Where(filter types.Filter) types.UpdateQueryBuilder {
	args := m.MethodCalled("Where", filter)
	return args.Get(0).(types.UpdateQueryBuilder)
}

func (m *MockUpdateQueryBuilder) ToSQL() (sql string, args []any, err error) {
	mockArgs := m.MethodCalled("ToSQL")
	return mockArgs.String(0), mockArgs.Get(1).([]any), mockArgs.Error(2)
}

var _ types.UpdateQueryBuilder = (*MockUpdateQueryBuilder)(nil)

// MockDeleteQueryBuilder provides a mock implementation of types.DeleteQueryBuilder for testing
type MockDeleteQueryBuilder struct {
	mock.Mock
}

func (m *MockDeleteQueryBuilder) Where(filter types.Filter) types.DeleteQueryBuilder {
	args := m.MethodCalled("Where", filter)
	return args.Get(0).(types.DeleteQueryBuilder)
}

func (m *MockDeleteQueryBuilder) Limit(limit uint64) types.DeleteQueryBuilder {
	args := m.MethodCalled("Limit", limit)
	return args.Get(0).(types.DeleteQueryBuilder)
}

func (m *MockDeleteQueryBuilder) OrderBy(orderBys ...string) types.DeleteQueryBuilder {
	callArgs := make([]any, len(orderBys))
	for i, col := range orderBys {
		callArgs[i] = col
	}
	args := m.MethodCalled("OrderBy", callArgs...)
	return args.Get(0).(types.DeleteQueryBuilder)
}

func (m *MockDeleteQueryBuilder) ToSQL() (sql string, args []any, err error) {
	mockArgs := m.MethodCalled("ToSQL")
	return mockArgs.String(0), mockArgs.Get(1).([]any), mockArgs.Error(2)
}

var _ types.DeleteQueryBuilder = (*MockDeleteQueryBuilder)(nil)

func (m *MockSelectQueryBuilder) From(from ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(from))
	for i, table := range from {
		callArgs[i] = table
	}
	args := m.MethodCalled("From", callArgs...)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) JoinOn(table string, filter types.JoinFilter) types.SelectQueryBuilder {
	args := m.MethodCalled("JoinOn", table, filter)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) LeftJoinOn(table string, filter types.JoinFilter) types.SelectQueryBuilder {
	args := m.MethodCalled("LeftJoinOn", table, filter)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) RightJoinOn(table string, filter types.JoinFilter) types.SelectQueryBuilder {
	args := m.MethodCalled("RightJoinOn", table, filter)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) InnerJoinOn(table string, filter types.JoinFilter) types.SelectQueryBuilder {
	args := m.MethodCalled("InnerJoinOn", table, filter)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) CrossJoinOn(table string) types.SelectQueryBuilder {
	args := m.MethodCalled("CrossJoinOn", table)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Where(filter types.Filter) types.SelectQueryBuilder {
	args := m.MethodCalled("Where", filter)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) GroupBy(groupBys ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(groupBys))
	for i, col := range groupBys {
		callArgs[i] = col
	}
	args := m.MethodCalled("GroupBy", callArgs...)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Having(pred any, rest ...any) types.SelectQueryBuilder {
	callArgs := append([]any{pred}, rest...)
	args := m.MethodCalled("Having", callArgs...)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) OrderBy(orderBys ...string) types.SelectQueryBuilder {
	callArgs := make([]any, len(orderBys))
	for i, col := range orderBys {
		callArgs[i] = col
	}
	args := m.MethodCalled("OrderBy", callArgs...)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Limit(limit uint64) types.SelectQueryBuilder {
	args := m.MethodCalled("Limit", limit)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Offset(offset uint64) types.SelectQueryBuilder {
	args := m.MethodCalled("Offset", offset)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Paginate(limit, offset uint64) types.SelectQueryBuilder {
	args := m.MethodCalled("Paginate", limit, offset)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) ToSQL() (sql string, args []any, err error) {
	mockArgs := m.MethodCalled("ToSQL")
	return mockArgs.String(0), mockArgs.Get(1).([]any), mockArgs.Error(2)
}

// Compile-time verification that MockSelectQueryBuilder implements the interface
var _ types.SelectQueryBuilder = (*MockSelectQueryBuilder)(nil)

// ExampleUserService demonstrates how to use MockQueryBuilder for testing
// business logic that constructs queries without actually generating SQL.
type ExampleUserService struct {
	qb types.QueryBuilderInterface
}

func NewExampleUserService(qb types.QueryBuilderInterface) *ExampleUserService {
	return &ExampleUserService{qb: qb}
}

// BuildUserSearchQuery constructs a query for searching users by name
func (s *ExampleUserService) BuildUserSearchQuery(searchTerm string) types.SelectQueryBuilder {
	query := s.qb.Select("id", "name", "email").From("users")

	if searchTerm != "" {
		f := s.qb.Filter()
		query = query.Where(f.Like("name", searchTerm))
	}

	return query
}

// IsPostgreSQL checks if the service is using PostgreSQL
func (s *ExampleUserService) IsPostgreSQL() bool {
	return s.qb.Vendor() == "postgresql"
}

func TestMockQueryBuilderBasicUsage(t *testing.T) {
	// Create and configure mock
	mockQB := &MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Set expectations
	mockQB.ExpectVendor("postgresql")

	// Test the service
	service := NewExampleUserService(mockQB)
	isPostgreSQL := service.IsPostgreSQL()

	assert.True(t, isPostgreSQL)
}

func TestMockQueryBuilderQueryConstruction(t *testing.T) {
	// Create and configure mocks
	mockQB := &MockQueryBuilder{}
	mockSelectBuilder := &MockSelectQueryBuilder{}
	mockFilterFactory := &MockFilterFactory{}
	mockFilter := &MockFilter{}
	defer mockQB.AssertExpectations(t)
	defer mockSelectBuilder.AssertExpectations(t)
	defer mockFilterFactory.AssertExpectations(t)

	// Set expectations
	mockQB.On("Select", "id", "name", "email").Return(mockSelectBuilder)
	mockSelectBuilder.On("From", "users").Return(mockSelectBuilder)
	mockQB.On("Filter").Return(mockFilterFactory)
	mockFilterFactory.On("Like", "name", "john").Return(mockFilter)
	mockSelectBuilder.On("Where", mockFilter).Return(mockSelectBuilder)

	// Test the service
	service := NewExampleUserService(mockQB)
	query := service.BuildUserSearchQuery("john")

	// Verify the query is constructed (in real usage, you'd verify business logic)
	assert.NotNil(t, query)
}

func TestMockQueryBuilderEmptySearchTerm(t *testing.T) {
	// Create and configure mocks
	mockQB := &MockQueryBuilder{}
	mockSelectBuilder := &MockSelectQueryBuilder{}
	defer mockQB.AssertExpectations(t)
	defer mockSelectBuilder.AssertExpectations(t)

	// Set expectations - only Select should be called for empty search
	mockQB.On("Select", "id", "name", "email").Return(mockSelectBuilder)
	mockSelectBuilder.On("From", "users").Return(mockSelectBuilder)
	// BuildCaseInsensitiveLike should NOT be called

	// Test the service
	service := NewExampleUserService(mockQB)
	query := service.BuildUserSearchQuery("")

	// Verify the query is constructed
	assert.NotNil(t, query)
}

func TestMockQueryBuilderHelperMethods(t *testing.T) {
	mockQB := &MockQueryBuilder{}
	mockSelectBuilder := &MockSelectQueryBuilder{}
	mockUpdateBuilder := &MockUpdateQueryBuilder{}
	mockDeleteBuilder := &MockDeleteQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Test helper methods
	insertBuilder := squirrel.Insert("users")
	likeCondition := squirrel.ILike{"name": "%test%"}

	// Use helper methods to set expectations
	mockQB.ExpectVendor("postgresql")
	mockQB.ExpectSelect([]string{"*"}, mockSelectBuilder)
	mockQB.ExpectInsert("users", insertBuilder)
	mockQB.ExpectUpdate("users", mockUpdateBuilder)
	mockQB.ExpectDelete("users", mockDeleteBuilder)
	mockQB.ExpectCaseInsensitiveLike("name", "test", likeCondition)
	mockQB.ExpectCurrentTimestamp("NOW()")
	mockQB.ExpectUUIDGeneration("gen_random_uuid()")
	mockQB.ExpectBooleanValue(true, true)
	mockQB.ExpectEscapeIdentifier("table_name", `"table_name"`)

	// Call the methods
	assert.Equal(t, "postgresql", mockQB.Vendor())
	assert.Equal(t, mockSelectBuilder, mockQB.Select("*"))
	assert.Equal(t, insertBuilder, mockQB.Insert("users"))
	assert.Equal(t, mockUpdateBuilder, mockQB.Update("users"))
	assert.Equal(t, mockDeleteBuilder, mockQB.Delete("users"))
	assert.Equal(t, likeCondition, mockQB.BuildCaseInsensitiveLike("name", "test"))
	assert.Equal(t, "NOW()", mockQB.BuildCurrentTimestamp())
	assert.Equal(t, "gen_random_uuid()", mockQB.BuildUUIDGeneration())
	assert.Equal(t, true, mockQB.BuildBooleanValue(true))
	assert.Equal(t, `"table_name"`, mockQB.EscapeIdentifier("table_name"))
}

func TestMockQueryBuilderVendorSpecificBehavior(t *testing.T) {
	// Test Oracle-specific behavior
	oracleQB := &MockQueryBuilder{}
	defer oracleQB.AssertExpectations(t)

	// Oracle returns uppercase escaped identifiers
	oracleQB.ExpectVendor("oracle")
	oracleQB.ExpectEscapeIdentifier("table_name", `"TABLE_NAME"`)
	oracleQB.ExpectCurrentTimestamp("SYSDATE")
	oracleQB.ExpectBooleanValue(true, 1)

	// Test behavior
	assert.Equal(t, "oracle", oracleQB.Vendor())
	assert.Equal(t, `"TABLE_NAME"`, oracleQB.EscapeIdentifier("table_name"))
	assert.Equal(t, "SYSDATE", oracleQB.BuildCurrentTimestamp())
	assert.Equal(t, 1, oracleQB.BuildBooleanValue(true))

	// Test PostgreSQL-specific behavior
	postgresQB := &MockQueryBuilder{}
	defer postgresQB.AssertExpectations(t)

	postgresQB.ExpectVendor("postgresql")
	postgresQB.ExpectEscapeIdentifier("table_name", `"table_name"`)
	postgresQB.ExpectCurrentTimestamp("NOW()")
	postgresQB.ExpectBooleanValue(true, true)

	assert.Equal(t, "postgresql", postgresQB.Vendor())
	assert.Equal(t, `"table_name"`, postgresQB.EscapeIdentifier("table_name"))
	assert.Equal(t, "NOW()", postgresQB.BuildCurrentTimestamp())
	assert.Equal(t, true, postgresQB.BuildBooleanValue(true))
}

// Example of testing error scenarios
func TestMockQueryBuilderErrorHandling(t *testing.T) {
	mockQB := &MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Mock an upsert operation that returns an error
	mockQB.On("BuildUpsert",
		"users",
		[]string{"id"},
		map[string]any{"id": 1, "name": "test"},
		map[string]any{"name": "updated"},
	).Return("", []any(nil), assert.AnError)

	sql, args, err := mockQB.BuildUpsert(
		"users",
		[]string{"id"},
		map[string]any{"id": 1, "name": "test"},
		map[string]any{"name": "updated"},
	)

	assert.Empty(t, sql)
	assert.Nil(t, args)
	assert.Error(t, err)
}

// Benchmark to demonstrate that mocking eliminates SQL generation overhead
func BenchmarkMockQueryBuilder(b *testing.B) {
	mockQB := &MockQueryBuilder{}
	selectBuilder := squirrel.Select("id", "name")

	// Set up expectations
	mockQB.On("Select", mock.Anything).Return(selectBuilder)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mockQB.Select("id", "name")
	}
}

// ========== Nil-Safe Mock ToSQL Tests ==========

func TestMockFilterNilSafeToSQL(t *testing.T) {
	mockFilter := &MockFilter{}

	t.Run("ToSQL with nil args", func(t *testing.T) {
		// Configure mock to return nil for args (common pattern)
		mockFilter.On("ToSQL").Return("status = ?", nil, nil)

		// This should not panic
		sql, args, err := mockFilter.ToSQL()

		assert.Equal(t, "status = ?", sql)
		assert.Nil(t, args) // nil args is valid, not a panic
		assert.NoError(t, err)
		mockFilter.AssertExpectations(t)
	})

	t.Run("ToSql with nil args", func(t *testing.T) {
		mockFilter2 := &MockFilter{}
		// Configure mock to return nil for args
		mockFilter2.On("ToSql").Return("id = ?", nil, nil)

		// This should not panic
		sql, args, err := mockFilter2.ToSql()

		assert.Equal(t, "id = ?", sql)
		assert.Nil(t, args)
		assert.NoError(t, err)
		mockFilter2.AssertExpectations(t)
	})

	t.Run("ToSQL with valid []any args", func(t *testing.T) {
		mockFilter3 := &MockFilter{}
		expectedArgs := []any{"active", 100}
		mockFilter3.On("ToSQL").Return("status = ? AND age > ?", expectedArgs, nil)

		sql, args, err := mockFilter3.ToSQL()

		assert.Equal(t, "status = ? AND age > ?", sql)
		assert.Equal(t, expectedArgs, args)
		assert.NoError(t, err)
		mockFilter3.AssertExpectations(t)
	})
}
