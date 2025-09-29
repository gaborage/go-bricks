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

func (m *MockSelectQueryBuilder) From(from ...string) types.SelectQueryBuilder {
	args := m.MethodCalled("From", from)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Join(join string, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("Join", join, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) LeftJoin(join string, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("LeftJoin", join, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) RightJoin(join string, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("RightJoin", join, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) InnerJoin(join string, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("InnerJoin", join, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) CrossJoin(join string, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("CrossJoin", join, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Where(pred any, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("Where", pred, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) GroupBy(groupBys ...string) types.SelectQueryBuilder {
	args := m.MethodCalled("GroupBy", groupBys)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) Having(pred any, rest ...any) types.SelectQueryBuilder {
	args := m.MethodCalled("Having", pred, rest)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) OrderBy(orderBys ...string) types.SelectQueryBuilder {
	args := m.MethodCalled("OrderBy", orderBys)
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

func (m *MockSelectQueryBuilder) WhereEq(column string, value any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereEq", column, value)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereNotEq(column string, value any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereNotEq", column, value)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereLt(column string, value any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereLt", column, value)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereLte(column string, value any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereLte", column, value)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereGt(column string, value any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereGt", column, value)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereGte(column string, value any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereGte", column, value)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereIn(column string, values any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereIn", column, values)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereNotIn(column string, values any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereNotIn", column, values)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereLike(column, pattern string) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereLike", column, pattern)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereNull(column string) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereNull", column)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereNotNull(column string) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereNotNull", column)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereBetween(column string, lowerBound, upperBound any) types.SelectQueryBuilder {
	args := m.MethodCalled("WhereBetween", column, lowerBound, upperBound)
	return args.Get(0).(types.SelectQueryBuilder)
}

func (m *MockSelectQueryBuilder) WhereRaw(condition string, args ...any) types.SelectQueryBuilder {
	mockArgs := m.MethodCalled("WhereRaw", condition, args)
	return mockArgs.Get(0).(types.SelectQueryBuilder)
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
		condition := s.qb.BuildCaseInsensitiveLike("name", searchTerm)
		query = query.Where(condition)
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
	defer mockQB.AssertExpectations(t)
	defer mockSelectBuilder.AssertExpectations(t)

	// Create mock condition
	likeCondition := squirrel.Like{"name": "%john%"}

	// Set expectations
	mockQB.On("Select", []string{"id", "name", "email"}).Return(mockSelectBuilder)
	mockQB.On("BuildCaseInsensitiveLike", "name", "john").Return(likeCondition)
	mockSelectBuilder.On("From", []string{"users"}).Return(mockSelectBuilder)
	mockSelectBuilder.On("Where", likeCondition, []any(nil)).Return(mockSelectBuilder)

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
	mockQB.On("Select", []string{"id", "name", "email"}).Return(mockSelectBuilder)
	mockSelectBuilder.On("From", []string{"users"}).Return(mockSelectBuilder)
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
	defer mockQB.AssertExpectations(t)

	// Test helper methods
	insertBuilder := squirrel.Insert("users")
	updateBuilder := squirrel.Update("users")
	deleteBuilder := squirrel.Delete("users")
	likeCondition := squirrel.ILike{"name": "%test%"}

	// Use helper methods to set expectations
	mockQB.ExpectVendor("postgresql")
	mockQB.ExpectSelect([]string{"*"}, mockSelectBuilder)
	mockQB.ExpectInsert("users", insertBuilder)
	mockQB.ExpectUpdate("users", updateBuilder)
	mockQB.ExpectDelete("users", deleteBuilder)
	mockQB.ExpectCaseInsensitiveLike("name", "test", likeCondition)
	mockQB.ExpectCurrentTimestamp("NOW()")
	mockQB.ExpectUUIDGeneration("gen_random_uuid()")
	mockQB.ExpectBooleanValue(true, true)
	mockQB.ExpectEscapeIdentifier("table_name", `"table_name"`)

	// Call the methods
	assert.Equal(t, "postgresql", mockQB.Vendor())
	assert.Equal(t, mockSelectBuilder, mockQB.Select("*"))
	assert.Equal(t, insertBuilder, mockQB.Insert("users"))
	assert.Equal(t, updateBuilder, mockQB.Update("users"))
	assert.Equal(t, deleteBuilder, mockQB.Delete("users"))
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
