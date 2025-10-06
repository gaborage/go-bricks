package mongodb

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Test Logger Implementation
// Shared test logger types to avoid duplication across test files

// testLogger implements logger.Logger for testing
type testLogger struct{}

func (l *testLogger) Info() logger.LogEvent                     { return &testLogEvent{} }
func (l *testLogger) Error() logger.LogEvent                    { return &testLogEvent{} }
func (l *testLogger) Debug() logger.LogEvent                    { return &testLogEvent{} }
func (l *testLogger) Warn() logger.LogEvent                     { return &testLogEvent{} }
func (l *testLogger) Fatal() logger.LogEvent                    { return &testLogEvent{} }
func (l *testLogger) WithContext(_ any) logger.Logger           { return l }
func (l *testLogger) WithFields(_ map[string]any) logger.Logger { return l }

// testLogEvent implements logger.LogEvent for testing
type testLogEvent struct{}

func (e *testLogEvent) Str(_, _ string) logger.LogEvent               { return e }
func (e *testLogEvent) Int(_ string, _ int) logger.LogEvent           { return e }
func (e *testLogEvent) Int64(_ string, _ int64) logger.LogEvent       { return e }
func (e *testLogEvent) Uint64(_ string, _ uint64) logger.LogEvent     { return e }
func (e *testLogEvent) Dur(_ string, _ time.Duration) logger.LogEvent { return e }
func (e *testLogEvent) Interface(_ string, _ any) logger.LogEvent     { return e }
func (e *testLogEvent) Bytes(_ string, _ []byte) logger.LogEvent      { return e }
func (e *testLogEvent) Err(_ error) logger.LogEvent                   { return e }
func (e *testLogEvent) Msg(_ string) {
	// No-op implementation for testing
}
func (e *testLogEvent) Msgf(_ string, _ ...any) {
	// No-op implementation for testing
}

// MockResponseHelpers provides reusable mock responses for MongoDB testing
// These helpers encapsulate common MongoDB wire protocol response patterns
// and ensure consistency across all tests.

// MockSuccessResponse creates a basic success response for commands
func MockSuccessResponse() bson.D {
	return bson.D{{Key: "ok", Value: 1}}
}

// MockInsertResponse creates a mock response for insert operations
func MockInsertResponse(insertedID any) bson.D {
	return bson.D{
		{Key: "ok", Value: 1},
		{Key: "insertedId", Value: insertedID},
	}
}

// MockUpdateResponse creates a mock response for update operations
func MockUpdateResponse(matched, modified int64, upsertedID any) bson.D {
	response := bson.D{
		{Key: "ok", Value: 1},
		{Key: "n", Value: matched},          // matchedCount in wire protocol
		{Key: "nModified", Value: modified}, // modifiedCount in wire protocol
	}

	if upsertedID != nil {
		response = append(response, bson.E{Key: "upserted", Value: []bson.M{
			{"_id": upsertedID},
		}})
	}

	return response
}

// MockDeleteResponse creates a mock response for delete operations
func MockDeleteResponse(deletedCount int64) bson.D {
	return bson.D{
		{Key: "ok", Value: 1},
		{Key: "n", Value: deletedCount}, // deletedCount in wire protocol
	}
}

// MockCountResponse creates a cursor response for count operations
// CountDocuments uses aggregation internally, so it returns a cursor
func MockCountResponse(namespace string, count int64) bson.D {
	return mtest.CreateCursorResponse(1, namespace, mtest.FirstBatch,
		bson.D{{Key: "n", Value: count}})
}

// MockFindResponse creates a cursor response for find operations
func MockFindResponse(namespace string, documents ...bson.D) bson.D {
	return mtest.CreateCursorResponse(1, namespace, mtest.FirstBatch, documents...)
}

// MockEmptyCollectionList creates an empty cursor for ListCollectionNames
func MockEmptyCollectionList(dbName string) bson.D {
	return mtest.CreateCursorResponse(0, dbName+".$cmd.listCollections", mtest.FirstBatch)
}

// MockCollectionExists creates a cursor indicating a collection exists
func MockCollectionExists(dbName, collection string) bson.D {
	return mtest.CreateCursorResponse(1, dbName+"."+collection,
		mtest.FirstBatch, bson.D{{Key: "name", Value: collection}})
}

// MockServerStatus creates a mock server status response
func MockServerStatus(currentConnections, availableConnections int) bson.D {
	return bson.D{
		{Key: "ok", Value: 1},
		{Key: "connections", Value: bson.D{
			{Key: "current", Value: currentConnections},
			{Key: "available", Value: availableConnections},
		}},
	}
}

// MockDatabaseStats creates a mock database statistics response
func MockDatabaseStats(collections, dataSize int) bson.D {
	return bson.D{
		{Key: "ok", Value: 1},
		{Key: "collections", Value: collections},
		{Key: "dataSize", Value: dataSize},
	}
}

// MockCommandResponse creates a generic command response
func MockCommandResponse(fields ...bson.E) bson.D {
	response := bson.D{{Key: "ok", Value: 1}}
	return append(response, fields...)
}

// MockErrorResponse creates an error response for testing error conditions
func MockErrorResponse(code int, message string) bson.D {
	return bson.D{
		{Key: "ok", Value: 0},
		{Key: "code", Value: code},
		{Key: "errmsg", Value: message},
	}
}

// Common document builders for testing

// MockUser creates a sample user document for testing
func MockUser(id, name string) bson.D {
	return bson.D{
		{Key: "_id", Value: id},
		{Key: "name", Value: name},
		{Key: "email", Value: name + "@example.com"},
	}
}

// MockProduct creates a sample product document for testing
func MockProduct(id, name string, price float64) bson.D {
	return bson.D{
		{Key: "_id", Value: id},
		{Key: "name", Value: name},
		{Key: "price", Value: price},
		{Key: "category", Value: "electronics"},
	}
}

// Test namespace helpers

// CreateTestNamespace creates a consistent namespace for test operations
func CreateTestNamespace(collection string) string {
	return "test." + collection
}

// GetTestDatabase returns the standard test database name
func GetTestDatabase() string {
	return "test"
}

// MongoDB Operator Constants - centralized to avoid duplication
const (
	MatchOp   = "$match"
	SortOp    = "$sort"
	LimitOp   = "$limit"
	SkipOp    = "$skip"
	ProjectOp = "$project"
	GroupOp   = "$group"
	LookupOp  = "$lookup"
	UnwindOp  = "$unwind"
	AndOp     = "$and"
	OrOp      = "$or"
	NorOp     = "$nor"
	NotOp     = "$not"
	NeOp      = "$ne"
	GtOp      = "$gt"
	GteOp     = "$gte"
	LtOp      = "$lt"
	LteOp     = "$lte"
	InOp      = "$in"
	NinOp     = "$nin"
	RegexOp   = "$regex"
	OptionsOp = "$options"
	ExistsOp  = "$exists"
	TypeOp    = "$type"
	SliceOp   = "$slice"
	ElemMatch = "$elemMatch"
)

// Test Data Constants
const (
	TestDate         = "2023-01-01"
	TestUserID       = "user123"
	TestProductID    = "prod456"
	TestOrderID      = "order789"
	TestCategory     = "electronics"
	TestStatus       = "active"
	TestPrice        = 99.99
	TestEmail        = "test@example.com"
	TestRegexEmail   = ".*@example\\.com$"
	TestRegexOptions = "i"
)

// Test Data Fixtures
var (
	TestProducts = []TestProduct{
		{ID: "1", Name: "Laptop", Price: 999.99, Category: "electronics", InStock: true},
		{ID: "2", Name: "Mouse", Price: 29.99, Category: "electronics", InStock: true},
		{ID: "3", Name: "Book", Price: 19.99, Category: "books", InStock: false},
	}

	TestUsers = []TestUser{
		{ID: "1", Name: "John Doe", Email: "john@example.com", Role: "admin", Status: "active"},
		{ID: "2", Name: "Jane Smith", Email: "jane@example.com", Role: "user", Status: "active"},
		{ID: "3", Name: "Bob Wilson", Email: "bob@example.com", Role: "moderator", Status: "inactive"},
	}

	TestOrders = []TestOrder{
		{ID: "1", CustomerID: "user1", Amount: 299.99, Status: "completed"},
		{ID: "2", CustomerID: "user2", Amount: 149.99, Status: "pending"},
		{ID: "3", CustomerID: "user1", Amount: 399.99, Status: "completed"},
	}
)

// Test Data Types
type TestProduct struct {
	ID       string
	Name     string
	Price    float64
	Category string
	InStock  bool
}

type TestUser struct {
	ID     string
	Name   string
	Email  string
	Role   string
	Status string
}

type TestOrder struct {
	ID         string
	CustomerID string
	Amount     float64
	Status     string
}

// Builder Test Helpers

// BuilderExpectation encapsulates expected test outcomes
type BuilderExpectation struct {
	Filter     bson.M
	Pipeline   []bson.M
	Options    *database.FindOptions
	Sort       bson.D
	Projection bson.M
	Skip       *int64
	Limit      *int64
}

// BuilderTestCase represents a table-driven test case
type BuilderTestCase struct {
	Name     string
	Setup    func() *Builder
	Expected BuilderExpectation
}

// Assertion Helpers

// AssertFilter verifies that a builder's filter matches expected values
func AssertFilter(t *testing.T, builder *Builder, expected bson.M) {
	t.Helper()
	actual := builder.ToFilter()
	assert.Equal(t, expected, actual, "Filter mismatch")
}

// AssertPipeline verifies that a builder's pipeline matches expected stages
func AssertPipeline(t *testing.T, builder *Builder, expected []bson.M) {
	t.Helper()
	actual := builder.ToPipeline()
	assert.Equal(t, expected, actual, "Pipeline mismatch")
}

// AssertPipelineContains checks if pipeline contains specific stages
func AssertPipelineContains(t *testing.T, pipeline []bson.M, operators ...string) {
	t.Helper()
	for _, op := range operators {
		found := false
		for _, stage := range pipeline {
			if _, exists := stage[op]; exists {
				found = true
				break
			}
		}
		assert.True(t, found, "Pipeline missing operator: %s", op)
	}
}

// AssertProjection verifies builder projection settings
func AssertProjection(t *testing.T, builder *Builder, expected bson.M) {
	t.Helper()
	opts := builder.ToFindOptions()
	assert.Equal(t, expected, opts.projection, "Projection mismatch")
}

// AssertSort verifies builder sort settings
func AssertSort(t *testing.T, builder *Builder, expected bson.D) {
	t.Helper()
	opts := builder.ToFindOptions()
	assert.Equal(t, expected, opts.sort, "Sort mismatch")
}

// AssertPagination verifies builder pagination settings
func AssertPagination(t *testing.T, builder *Builder, expectedSkip, expectedLimit *int64) {
	t.Helper()
	opts := builder.ToFindOptions()
	if expectedSkip != nil {
		require.NotNil(t, opts.skip, "Expected skip to be set")
		assert.Equal(t, *expectedSkip, *opts.skip, "Skip mismatch")
	} else {
		assert.Nil(t, opts.skip, "Expected skip to be nil")
	}

	if expectedLimit != nil {
		require.NotNil(t, opts.limit, "Expected limit to be set")
		assert.Equal(t, *expectedLimit, *opts.limit, "Limit mismatch")
	} else {
		assert.Nil(t, opts.limit, "Expected limit to be nil")
	}
}

// AssertFindOptions verifies complete find options
func AssertFindOptions(t *testing.T, builder *Builder, expected *BuilderExpectation) {
	t.Helper()
	opts := builder.ToFindOptions().Build()

	if expected.Sort != nil {
		assert.Equal(t, expected.Sort, opts.Sort, "Sort options mismatch")
	}
	if expected.Skip != nil {
		require.NotNil(t, opts.Skip, "Expected skip to be set")
		assert.Equal(t, *expected.Skip, *opts.Skip, "Skip options mismatch")
	}
	if expected.Limit != nil {
		require.NotNil(t, opts.Limit, "Expected limit to be set")
		assert.Equal(t, *expected.Limit, *opts.Limit, "Limit options mismatch")
	}
	if expected.Projection != nil {
		assert.Equal(t, expected.Projection, opts.Projection, "Projection options mismatch")
	}
}

// CompareBSON performs deep comparison of BSON documents
func CompareBSON(t *testing.T, expected, actual any) {
	t.Helper()
	if !reflect.DeepEqual(expected, actual) {
		t.Errorf("BSON documents differ:\nExpected: %+v\nActual: %+v", expected, actual)
	}
}

// AssertBuilderState verifies internal builder state for testing
func AssertBuilderState(t *testing.T, builder *Builder, expected *BuilderExpectation) {
	t.Helper()

	// Check filter
	if expected.Filter != nil {
		AssertFilter(t, builder, expected.Filter)
	}

	// Check pipeline
	if expected.Pipeline != nil {
		AssertPipeline(t, builder, expected.Pipeline)
	}

	// Check find options
	AssertFindOptions(t, builder, expected)
}

// Builder Creation Helpers

// NewTestBuilder creates a builder with common test data
func NewTestBuilder() *Builder {
	return NewBuilder()
}

// BuilderWithTestData creates a builder pre-populated with test conditions
func BuilderWithTestData() *Builder {
	return NewBuilder().
		WhereEq("category", TestCategory).
		WhereEq("status", TestStatus).
		WhereGte("price", 10.0)
}

// ComplexQueryBuilder creates a builder with complex test conditions
func ComplexQueryBuilder() *Builder {
	return NewBuilder().
		WhereIn("category", "electronics", "books", "clothing").
		WhereAnd(
			bson.M{"price": bson.M{GteOp: 50}},
			bson.M{"price": bson.M{LteOp: 1000}},
		).
		WhereExists("featured_image", true).
		OrderByDesc("created_at").
		OrderBy("price").
		Skip(10).
		Limit(20).
		Select("name", "price", "category")
}

// ECommerceSearchBuilder creates a typical e-commerce search builder
func ECommerceSearchBuilder() *Builder {
	return NewBuilder().
		WhereIn("category", "electronics", "computers", "phones").
		WhereAnd(
			bson.M{"price": bson.M{GteOp: 50}},
			bson.M{"price": bson.M{LteOp: 2000}},
		).
		WhereEq("in_stock", true).
		WhereRegex("name", "samsung|apple|sony", "i").
		OrderByDesc("rating").
		OrderBy("price").
		Skip(0).
		Limit(24).
		Select("name", "price", "rating", "image_url", "category")
}

// UserAnalyticsBuilder creates a user analytics aggregation builder
func UserAnalyticsBuilder() *Builder {
	return NewBuilder().
		AddMatch(bson.M{
			"created_at": bson.M{GteOp: TestDate},
			"status":     bson.M{NeOp: "deleted"},
		}).
		AddGroup(bson.M{
			"_id": bson.M{
				"department": "$department",
				"role":       "$role",
			},
			"user_count":   bson.M{"$sum": 1},
			"avg_age":      bson.M{"$avg": "$age"},
			"active_users": bson.M{"$sum": bson.M{"$cond": []any{bson.M{"$eq": []any{"$status", "active"}}, 1, 0}}},
			"total_logins": bson.M{"$sum": "$login_count"},
		}).
		AddSort(bson.D{{Key: "user_count", Value: -1}}).
		AddLimit(20)
}

// Mock Document Builders with Test Data

// MockProductFromTestData creates a product document from test fixture
func MockProductFromTestData(product TestProduct) bson.D {
	return bson.D{
		{Key: "_id", Value: product.ID},
		{Key: "name", Value: product.Name},
		{Key: "price", Value: product.Price},
		{Key: "category", Value: product.Category},
		{Key: "in_stock", Value: product.InStock},
	}
}

// MockUserFromTestData creates a user document from test fixture
func MockUserFromTestData(user *TestUser) bson.D {
	return bson.D{
		{Key: "_id", Value: user.ID},
		{Key: "name", Value: user.Name},
		{Key: "email", Value: user.Email},
		{Key: "role", Value: user.Role},
		{Key: "status", Value: user.Status},
	}
}

// MockOrderFromTestData creates an order document from test fixture
func MockOrderFromTestData(order TestOrder) bson.D {
	return bson.D{
		{Key: "_id", Value: order.ID},
		{Key: "customer_id", Value: order.CustomerID},
		{Key: "amount", Value: order.Amount},
		{Key: "status", Value: order.Status},
	}
}

// Utility Functions for Common Test Patterns

// Int64Ptr creates an int64 pointer for testing
func Int64Ptr(v int64) *int64 {
	return &v
}

// BoolPtr creates a bool pointer for testing
func BoolPtr(v bool) *bool {
	return &v
}

// StringPtr creates a string pointer for testing
func StringPtr(v string) *string {
	return &v
}

// Connection Test Setup Helpers
// Shared connection setup functions to reduce duplication across test files

// DefaultTestConfig returns a standard test database configuration
func DefaultTestConfig() *config.DatabaseConfig {
	return &config.DatabaseConfig{
		Host:     "localhost",
		Port:     27017,
		Database: "test",
	}
}

// SetupMockConnection creates a test connection with mocked MongoDB functions
func SetupMockConnection(t *testing.T, mt *mtest.T, cfg *config.DatabaseConfig, log logger.Logger) *Connection {
	t.Helper()

	// Mock successful connection
	originalConnect := connectMongoDB
	originalPing := pingMongoDB
	t.Cleanup(func() {
		connectMongoDB = originalConnect
		pingMongoDB = originalPing
	})

	connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
		return mt.Client, nil
	}
	pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
		return nil
	}

	conn, err := NewConnection(cfg, log)
	require.NoError(t, err)
	return conn.(*Connection)
}

// MockConnectionFuncs mocks the global connection functions and returns restore function
func MockConnectionFuncs(client *mongo.Client, pingErr error) (restore func()) {
	originalConnect := connectMongoDB
	originalPing := pingMongoDB

	connectMongoDB = func(_ context.Context, _ *options.ClientOptions) (*mongo.Client, error) {
		return client, nil
	}
	pingMongoDB = func(_ context.Context, _ *mongo.Client) error {
		return pingErr
	}

	return func() {
		connectMongoDB = originalConnect
		pingMongoDB = originalPing
	}
}

// Connection Test Case Structures
// Following the pattern from builder_test.go for table-driven tests

// ConnectionTestCase represents a table-driven test case for connection tests
type ConnectionTestCase struct {
	Name          string
	Config        *config.DatabaseConfig
	ExpectedURI   string
	ExpectedError bool
	MockSetup     func() func() // Returns cleanup function
	Validation    func(*testing.T, *Connection, error)
}

// TLSTestCase represents a test case for TLS configuration
type TLSTestCase struct {
	Name                 string
	SSLMode              string
	ExpectError          bool
	ExpectedInsecureSkip *bool
	ExpectedServerName   *string
	ExpectNilConfig      bool
}

// Connection Assertion Helpers
// Following the pattern from builder assertion helpers

// AssertConnectionState verifies connection properties
func AssertConnectionState(t *testing.T, conn *Connection, _ string) {
	t.Helper()
	assert.Equal(t, "mongodb", conn.DatabaseType())
	assert.Equal(t, "schema_migrations", conn.GetMigrationTable())
	// Add more state assertions as needed
}

// AssertTLSConfig verifies TLS configuration properties
func AssertTLSConfig(t *testing.T, tlsConfig any, expected *TLSTestCase) {
	t.Helper()

	if expected.ExpectNilConfig {
		assert.Nil(t, tlsConfig)
		return
	}

	require.NotNil(t, tlsConfig)
	// Additional TLS-specific assertions would go here
}

// AssertMongoURI verifies that the generated URI matches expected format
func AssertMongoURI(t *testing.T, expected, actual string) {
	t.Helper()
	assert.Equal(t, expected, actual, "MongoDB URI mismatch")
}

// Helper Functions for Connection Testing

// CreateTestLogger returns a standard test logger instance
func CreateTestLogger() logger.Logger {
	return &testLogger{}
}

// Note: setupTestConnection is defined in adapter_test.go and connection_test.go
// The builder tests will use the existing setupTestConnection function from those files
