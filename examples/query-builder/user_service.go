package main

import (
	"fmt"

	"github.com/gaborage/go-bricks/database"
)

// UserService demonstrates a service that uses QueryBuilder for constructing
// complex queries based on business logic. This is a realistic example of
// where MockQueryBuilder would be valuable for unit testing.
type UserService struct {
	db database.Interface
	qb database.QueryBuilderInterface
}

// NewUserService creates a new UserService with database and query builder dependencies
func NewUserService(db database.Interface, qb database.QueryBuilderInterface) *UserService {
	return &UserService{
		db: db,
		qb: qb,
	}
}

// SearchCriteria defines parameters for user search
type SearchCriteria struct {
	NameFilter    string
	EmailFilter   string
	ActiveOnly    bool
	SortBy        string
	SortDirection string
	Limit         int
	Offset        int
}

// User represents a user record
type User struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Active bool   `json:"active"`
}

// BuildUserSearchQuery constructs a complex user search query based on criteria.
// This method contains business logic that benefits from unit testing with mocks.
func (s *UserService) BuildUserSearchQuery(criteria *SearchCriteria) (sql string, args []any, err error) {
	// Start with basic SELECT
	query := s.qb.Select("id", "name", "email", "active").From("users")

	// Apply name filter with vendor-specific case-insensitive matching
	if criteria.NameFilter != "" {
		nameCondition := s.qb.BuildCaseInsensitiveLike("name", criteria.NameFilter)
		query = query.Where(nameCondition)
	}

	// Apply email filter
	if criteria.EmailFilter != "" {
		emailCondition := s.qb.BuildCaseInsensitiveLike("email", criteria.EmailFilter)
		query = query.Where(emailCondition)
	}

	// Apply active filter with vendor-specific boolean handling
	if criteria.ActiveOnly {
		activeValue := s.qb.BuildBooleanValue(true)
		query = query.Where("active = ?", activeValue)
	}

	// Apply sorting with vendor-specific identifier escaping
	if criteria.SortBy != "" {
		sortColumn := s.qb.EscapeIdentifier(criteria.SortBy)
		direction := "ASC"
		if criteria.SortDirection == "desc" {
			direction = "DESC"
		}
		query = query.OrderBy(fmt.Sprintf("%s %s", sortColumn, direction))
	}

	// Apply pagination with vendor-specific LIMIT/OFFSET handling
	if criteria.Limit > 0 || criteria.Offset > 0 {
		query = s.qb.BuildLimitOffset(query, criteria.Limit, criteria.Offset)
	}

	return query.ToSql()
}

// CreateUserUpsertQuery demonstrates vendor-specific upsert logic
func (s *UserService) CreateUserUpsertQuery(user User) (sql string, args []any, err error) {
	insertData := map[string]any{
		"id":     user.ID,
		"name":   user.Name,
		"email":  user.Email,
		"active": s.qb.BuildBooleanValue(user.Active),
	}

	updateData := map[string]any{
		"name":   user.Name,
		"email":  user.Email,
		"active": s.qb.BuildBooleanValue(user.Active),
	}

	// This method contains complex vendor-specific logic that benefits from mocking
	if s.qb.Vendor() == "postgresql" {
		// PostgreSQL supports ON CONFLICT
		return s.qb.BuildUpsert("users", []string{"id"}, insertData, updateData)
	}

	// For other vendors, fall back to separate INSERT logic
	insertQuery := s.qb.InsertWithColumns("users", "id", "name", "email", "active")
	insertQuery = insertQuery.Values(user.ID, user.Name, user.Email, s.qb.BuildBooleanValue(user.Active))

	return insertQuery.ToSql()
}

// GetOptimalTimestampFunction returns the best timestamp function for the vendor
func (s *UserService) GetOptimalTimestampFunction() string {
	// Business logic that depends on vendor-specific functions
	return s.qb.BuildCurrentTimestamp()
}

// AnalyzeQueryComplexity demonstrates business logic that makes decisions
// based on vendor capabilities
func (s *UserService) AnalyzeQueryComplexity(criteria *SearchCriteria) string {
	complexity := "simple"

	// This business logic benefits from mocking the query builder
	// to test different vendor behaviors
	switch s.qb.Vendor() {
	case "postgresql":
		if criteria.NameFilter != "" || criteria.EmailFilter != "" {
			complexity = "moderate" // PostgreSQL has efficient ILIKE
		}
		if criteria.Limit > 1000 {
			complexity = "complex" // Large result sets
		}
	case "oracle":
		if criteria.NameFilter != "" || criteria.EmailFilter != "" {
			complexity = "complex" // Oracle requires UPPER() transformations
		}
	default:
		complexity = "unknown"
	}

	return complexity
}

// Main function demonstrates real usage (not typically tested with mocks)
func main() {
	// In a real application, you would initialize these from your app configuration
	fmt.Println("Example UserService - see user_service_test.go for MockQueryBuilder usage")

	// Create a real query builder for demonstration
	qb := database.NewQueryBuilder("postgresql")

	// Show how a query would be constructed
	service := &UserService{qb: qb}

	criteria := &SearchCriteria{
		NameFilter:    "john",
		ActiveOnly:    true,
		SortBy:        "name",
		SortDirection: "asc",
		Limit:         10,
		Offset:        0,
	}

	sql, args, err := service.BuildUserSearchQuery(criteria)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}

	fmt.Printf("Generated SQL: %s\n", sql)
	fmt.Printf("Arguments: %v\n", args)
	fmt.Printf("Complexity: %s\n", service.AnalyzeQueryComplexity(criteria))
	fmt.Printf("Timestamp function: %s\n", service.GetOptimalTimestampFunction())
}
