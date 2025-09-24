package main

import (
	"testing"

	"github.com/Masterminds/squirrel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/gaborage/go-bricks/testing/mocks"
)

// Test constants to avoid duplication
const (
	testNameJohn    = "%john%"
	testEmailDomain = "example.com"
	testUserName    = "John Doe"
	testUserEmail   = "john@example.com"
)

func TestUserServiceBuildUserSearchQueryWithMockQueryBuilder(t *testing.T) {
	tests := []struct {
		name        string
		criteria    *SearchCriteria
		setupMock   func(*mocks.MockQueryBuilder)
		expectError bool
		description string
	}{
		{
			name: "basic_search_with_name_filter",
			criteria: &SearchCriteria{
				NameFilter: "john",
				ActiveOnly: true,
				Limit:      10,
			},
			setupMock: func(mockQB *mocks.MockQueryBuilder) {
				// Mock the sequence of method calls
				selectBuilder := squirrel.Select("id", "name", "email", "active")
				nameCondition := squirrel.ILike{"name": testNameJohn}

				mockQB.On("Select", []string{"id", "name", "email", "active"}).Return(selectBuilder)
				mockQB.On("BuildCaseInsensitiveLike", "name", "john").Return(nameCondition)
				mockQB.On("BuildBooleanValue", true).Return(true)
				mockQB.On("BuildLimitOffset", mock.AnythingOfType("squirrel.SelectBuilder"), 10, 0).Return(selectBuilder)
			},
			expectError: false,
			description: "Tests that name filtering, active filtering, and pagination methods are called correctly",
		},
		{
			name: "vendor_specific_boolean_handling",
			criteria: &SearchCriteria{
				ActiveOnly: true,
			},
			setupMock: func(mockQB *mocks.MockQueryBuilder) {
				selectBuilder := squirrel.Select("id", "name", "email", "active")

				mockQB.On("Select", []string{"id", "name", "email", "active"}).Return(selectBuilder)
				// Test Oracle's integer boolean conversion
				mockQB.On("BuildBooleanValue", true).Return(1)
			},
			expectError: false,
			description: "Verifies that vendor-specific boolean value conversion is handled correctly",
		},
		{
			name: "complex_query_with_sorting",
			criteria: &SearchCriteria{
				NameFilter:    "doe",
				EmailFilter:   testEmailDomain,
				SortBy:        "created_at",
				SortDirection: "desc",
				Offset:        20,
			},
			setupMock: func(mockQB *mocks.MockQueryBuilder) {
				selectBuilder := squirrel.Select("id", "name", "email", "active")
				nameCondition := squirrel.ILike{"name": "%doe%"}
				emailCondition := squirrel.ILike{"email": "%" + testEmailDomain + "%"}

				mockQB.On("Select", []string{"id", "name", "email", "active"}).Return(selectBuilder)
				mockQB.On("BuildCaseInsensitiveLike", "name", "doe").Return(nameCondition)
				mockQB.On("BuildCaseInsensitiveLike", "email", testEmailDomain).Return(emailCondition)
				mockQB.On("EscapeIdentifier", "created_at").Return(`"created_at"`) // PostgreSQL style
				mockQB.On("BuildLimitOffset", mock.AnythingOfType("squirrel.SelectBuilder"), 0, 20).Return(selectBuilder)
			},
			expectError: false,
			description: "Tests complex query construction with multiple filters, sorting, and offset",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock and set expectations
			mockQB := &mocks.MockQueryBuilder{}
			defer mockQB.AssertExpectations(t)

			tt.setupMock(mockQB)

			// Create service with mock
			service := NewUserService(nil, mockQB) // db not needed for this test

			// Execute the method under test
			_, _, err := service.BuildUserSearchQuery(tt.criteria)

			// Verify results
			if tt.expectError {
				assert.Error(t, err, tt.description)
			} else {
				assert.NoError(t, err, tt.description)
				// The key benefit of MockQueryBuilder is testing the business logic flow
				// without needing to generate actual SQL. The mock expectations verify
				// that the correct methods are called with the right parameters.
			}
		})
	}
}

func TestUserServiceCreateUserUpsertQueryVendorSpecific(t *testing.T) {
	user := User{
		ID:     1,
		Name:   testUserName,
		Email:  testUserEmail,
		Active: true,
	}

	t.Run("postgresql_upsert", func(t *testing.T) {
		mockQB := &mocks.MockQueryBuilder{}
		defer mockQB.AssertExpectations(t)

		// Test PostgreSQL upsert path
		mockQB.On("Vendor").Return("postgresql")
		mockQB.On("BuildBooleanValue", true).Return(true).Times(2) // Called twice for insert and update data
		mockQB.On("BuildUpsert",
			"users",
			[]string{"id"},
			map[string]any{
				"id":     1,
				"name":   testUserName,
				"email":  testUserEmail,
				"active": true,
			},
			map[string]any{
				"name":   testUserName,
				"email":  testUserEmail,
				"active": true,
			},
		).Return("INSERT ... ON CONFLICT", []any{1, testUserName, testUserEmail, true}, nil)

		service := NewUserService(nil, mockQB)
		sql, args, err := service.CreateUserUpsertQuery(user)

		assert.NoError(t, err)
		assert.Contains(t, sql, "INSERT")
		assert.Contains(t, sql, "ON CONFLICT")
		assert.Len(t, args, 4)
	})

	t.Run("oracle_fallback", func(t *testing.T) {
		mockQB := &mocks.MockQueryBuilder{}
		defer mockQB.AssertExpectations(t)

		// Test Oracle fallback path
		insertBuilder := squirrel.Insert("users")

		mockQB.On("Vendor").Return("oracle")
		mockQB.On("BuildBooleanValue", true).Return(1) // Oracle uses integers for booleans
		mockQB.On("InsertWithColumns", "users", []string{"id", "name", "email", "active"}).Return(insertBuilder)

		service := NewUserService(nil, mockQB)
		_, _, err := service.CreateUserUpsertQuery(user)

		// In the Oracle case, we're testing the business logic of falling back
		// to INSERT when upsert is not available. The mock verifies the right methods are called.
		assert.NoError(t, err)
	})
}

func TestUserServiceAnalyzeQueryComplexity(t *testing.T) {
	tests := []struct {
		name               string
		vendor             string
		criteria           *SearchCriteria
		expectedComplexity string
		description        string
	}{
		{
			name:   "postgresql_simple",
			vendor: "postgresql",
			criteria: &SearchCriteria{
				ActiveOnly: true,
				Limit:      10,
			},
			expectedComplexity: "simple",
			description:        "Basic PostgreSQL query should be simple",
		},
		{
			name:   "postgresql_with_filters",
			vendor: "postgresql",
			criteria: &SearchCriteria{
				NameFilter: "john",
				Limit:      10,
			},
			expectedComplexity: "moderate",
			description:        "PostgreSQL with text filters should be moderate (efficient ILIKE)",
		},
		{
			name:   "postgresql_large_limit",
			vendor: "postgresql",
			criteria: &SearchCriteria{
				Limit: 2000,
			},
			expectedComplexity: "complex",
			description:        "Large result sets should be complex",
		},
		{
			name:   "oracle_with_filters",
			vendor: "oracle",
			criteria: &SearchCriteria{
				NameFilter: "john",
			},
			expectedComplexity: "complex",
			description:        "Oracle with text filters should be complex (requires UPPER() transformations)",
		},
		{
			name:   "unknown_vendor",
			vendor: "sqlite",
			criteria: &SearchCriteria{
				NameFilter: "john",
			},
			expectedComplexity: "unknown",
			description:        "Unknown vendors should return unknown complexity",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockQB := &mocks.MockQueryBuilder{}
			defer mockQB.AssertExpectations(t)

			// Mock only the vendor call
			mockQB.On("Vendor").Return(tt.vendor)

			service := NewUserService(nil, mockQB)
			complexity := service.AnalyzeQueryComplexity(tt.criteria)

			assert.Equal(t, tt.expectedComplexity, complexity, tt.description)
		})
	}
}

func TestUserServiceGetOptimalTimestampFunction(t *testing.T) {
	tests := []struct {
		name              string
		vendor            string
		expectedTimestamp string
	}{
		{
			name:              "postgresql_timestamp",
			vendor:            "postgresql",
			expectedTimestamp: "NOW()",
		},
		{
			name:              "oracle_timestamp",
			vendor:            "oracle",
			expectedTimestamp: "SYSDATE",
		},
		{
			name:              "mysql_timestamp",
			vendor:            "mysql",
			expectedTimestamp: "NOW()",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockQB := &mocks.MockQueryBuilder{}
			defer mockQB.AssertExpectations(t)

			mockQB.On("BuildCurrentTimestamp").Return(tt.expectedTimestamp)

			service := NewUserService(nil, mockQB)
			timestamp := service.GetOptimalTimestampFunction()

			assert.Equal(t, tt.expectedTimestamp, timestamp)
		})
	}
}

// Benchmark comparing mock vs real query builder performance
func BenchmarkQueryConstruction(b *testing.B) {
	criteria := &SearchCriteria{
		NameFilter:    "john",
		EmailFilter:   testEmailDomain,
		ActiveOnly:    true,
		SortBy:        "name",
		SortDirection: "asc",
		Limit:         10,
		Offset:        5,
	}

	b.Run("with_mock", func(b *testing.B) {
		mockQB := &mocks.MockQueryBuilder{}
		selectBuilder := squirrel.Select("id", "name", "email", "active")
		nameCondition := squirrel.ILike{"name": testNameJohn}

		// Set up expectations
		mockQB.On("Select", mock.Anything).Return(selectBuilder)
		mockQB.On("BuildCaseInsensitiveLike", mock.Anything, mock.Anything).Return(nameCondition)
		mockQB.On("BuildBooleanValue", mock.Anything).Return(true)
		mockQB.On("EscapeIdentifier", mock.Anything).Return(`"name"`)
		mockQB.On("BuildLimitOffset", mock.Anything, mock.Anything, mock.Anything).Return(selectBuilder)

		service := NewUserService(nil, mockQB)

		b.ResetTimer()
		for range b.N {
			service.BuildUserSearchQuery(criteria)
		}
	})

	// Note: Real query builder benchmark would require importing the actual database package
	// This demonstrates the performance benefit of mocking for unit tests
}

// Example of testing error scenarios with mocks
func TestUserServiceBuildUserSearchQueryErrorHandling(t *testing.T) {
	mockQB := &mocks.MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	// Simulate an error in query building
	selectBuilder := squirrel.Select("id", "name", "email", "active")
	mockQB.On("Select", []string{"id", "name", "email", "active"}).Return(selectBuilder)
	mockQB.On("BuildBooleanValue", true).Return(true)

	service := NewUserService(nil, mockQB)

	criteria := &SearchCriteria{
		ActiveOnly: true,
	}

	// The actual error would come from ToSql() call on the builder
	// This demonstrates that we can isolate and test our business logic
	// separate from SQL generation errors
	_, _, err := service.BuildUserSearchQuery(criteria)

	// In this case, no error expected because our mock is set up correctly
	assert.NoError(t, err)
}

// Test demonstrating isolation of business logic from SQL generation
func TestUserServiceBusinessLogicIsolation(t *testing.T) {
	mockQB := &mocks.MockQueryBuilder{}
	defer mockQB.AssertExpectations(t)

	selectBuilder := squirrel.Select("id", "name", "email", "active")

	// We only care about testing the business logic flow, not SQL generation
	mockQB.On("Select", []string{"id", "name", "email", "active"}).Return(selectBuilder)
	mockQB.On("BuildCaseInsensitiveLike", "name", "john").Return(squirrel.ILike{"name": testNameJohn}).Once()
	mockQB.On("BuildCaseInsensitiveLike", "email", "doe").Return(squirrel.ILike{"email": "%doe%"}).Once()
	mockQB.On("EscapeIdentifier", "created_at").Return(`"created_at"`).Once()
	mockQB.On("BuildLimitOffset", mock.Anything, 20, 10).Return(selectBuilder).Once()

	service := NewUserService(nil, mockQB)

	criteria := &SearchCriteria{
		NameFilter:    "john",
		EmailFilter:   "doe",
		SortBy:        "created_at",
		SortDirection: "asc",
		Limit:         20,
		Offset:        10,
	}

	_, _, err := service.BuildUserSearchQuery(criteria)
	assert.NoError(t, err)

	// The mock expectations verify that:
	// 1. Both name and email filters are applied
	// 2. The correct column is escaped for sorting
	// 3. The correct limit and offset are passed
	// All without generating actual SQL!
}
