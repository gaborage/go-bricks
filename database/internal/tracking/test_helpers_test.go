package tracking

// Shared SQL query constants for the tracking package's tests.
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
)
