package tracking

// Shared test query constants for use across all test files in the tracking package.
// These constants eliminate duplication and provide a single source of truth for test SQL queries.
//
// Usage: Import these constants in *_test.go files instead of defining local duplicates.
const (
	// SELECT queries
	TestQuerySelectUsers       = "SELECT * FROM users"
	TestQuerySelectUsersParams = "SELECT * FROM users WHERE id = $1"
	TestQuerySelectOrders      = "SELECT * FROM orders"
	TestQuerySelectOne         = "SELECT 1"

	// INSERT queries
	TestQueryInsertUsers       = "INSERT INTO users VALUES (1)"
	TestQueryInsertUsersParams = "INSERT INTO users (name) VALUES ($1)"

	// UPDATE queries
	TestQueryUpdateUsers = "UPDATE users SET name = 'test'"

	// DELETE queries
	TestQueryDeleteUsers = "DELETE FROM users WHERE id = 1"
)
