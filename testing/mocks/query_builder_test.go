package mocks

import (
	"testing"

	"github.com/Masterminds/squirrel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// ExampleUserService demonstrates how to use MockQueryBuilder for testing
// business logic that constructs queries without actually generating SQL.
type ExampleUserService struct {
	qb QueryBuilderInterface
}

// QueryBuilderInterface defines methods needed by the service (in real code, this would import from database)
type QueryBuilderInterface interface {
	Vendor() string
	Select(columns ...string) squirrel.SelectBuilder
	BuildCaseInsensitiveLike(column, value string) squirrel.Sqlizer
}

func NewExampleUserService(qb QueryBuilderInterface) *ExampleUserService {
	return &ExampleUserService{qb: qb}
}

// BuildUserSearchQuery constructs a query for searching users by name
func (s *ExampleUserService) BuildUserSearchQuery(searchTerm string) squirrel.SelectBuilder {
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
	// Create and configure mock
	mockQB := &MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Create mock squirrel builders
	selectBuilder := squirrel.Select("id", "name", "email")
	likeCondition := squirrel.Like{"name": "%john%"}

	// Set expectations
	mockQB.On("Select", []string{"id", "name", "email"}).Return(selectBuilder)
	mockQB.On("BuildCaseInsensitiveLike", "name", "john").Return(likeCondition)

	// Test the service
	service := NewExampleUserService(mockQB)
	query := service.BuildUserSearchQuery("john")

	// Verify the query is constructed (in real usage, you'd verify business logic)
	assert.NotNil(t, query)
}

func TestMockQueryBuilderEmptySearchTerm(t *testing.T) {
	// Create and configure mock
	mockQB := &MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Create mock squirrel builder
	selectBuilder := squirrel.Select("id", "name", "email")

	// Set expectations - only Select should be called for empty search
	mockQB.On("Select", []string{"id", "name", "email"}).Return(selectBuilder)
	// BuildCaseInsensitiveLike should NOT be called

	// Test the service
	service := NewExampleUserService(mockQB)
	query := service.BuildUserSearchQuery("")

	// Verify the query is constructed
	assert.NotNil(t, query)
}

func TestMockQueryBuilderHelperMethods(t *testing.T) {
	mockQB := &MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Test helper methods
	selectBuilder := squirrel.Select("*")
	insertBuilder := squirrel.Insert("users")
	updateBuilder := squirrel.Update("users")
	deleteBuilder := squirrel.Delete("users")
	likeCondition := squirrel.ILike{"name": "%test%"}

	// Use helper methods to set expectations
	mockQB.ExpectVendor("postgresql")
	mockQB.ExpectSelect([]string{"*"}, selectBuilder)
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
	assert.Equal(t, selectBuilder, mockQB.Select("*"))
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
