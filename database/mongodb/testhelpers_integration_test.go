//go:build integration

package mongodb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/testing/containers"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// uniqueCollectionName generates a unique collection name for tests to prevent cross-test pollution
func uniqueCollectionName(t *testing.T, prefix string) string {
	t.Helper()
	// Use test name and unix nano timestamp for uniqueness
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// setupTestContainer starts a MongoDB testcontainer and returns the connection
// The container is automatically cleaned up when the test finishes
func setupTestContainer(t *testing.T) (*Connection, context.Context) {
	t.Helper()

	// Create context with timeout to prevent indefinite hangs
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)

	// Register cleanup to cancel context and close connection
	t.Cleanup(func() {
		cancel()
	})

	// Start MongoDB container with default configuration
	mongoContainer := containers.MustStartMongoDBContainer(ctx, t, nil).WithCleanup(t)

	// Create logger for tests (disabled output)
	log := logger.New("disabled", true)

	// Create config using connection string from container
	cfg := &config.DatabaseConfig{
		ConnectionString: mongoContainer.ConnectionString(),
		Database:         "testdb",
	}

	// Create MongoDB connection
	conn, err := NewConnection(cfg, log)
	require.NoError(t, err, "Failed to create MongoDB connection")

	// Register cleanup to close connection before container terminates
	t.Cleanup(func() {
		if conn != nil {
			_ = conn.Close()
		}
	})

	// Verify connection works
	err = conn.Health(ctx)
	require.NoError(t, err, "Failed to ping MongoDB")

	return conn.(*Connection), ctx
}

// cleanupTestCollection drops the test collection and ignores "namespace not found" errors
func cleanupTestCollection(t *testing.T, conn *Connection, ctx context.Context, collectionName string) {
	t.Helper()

	err := conn.DropCollection(ctx, collectionName)
	if err != nil && !isNamespaceNotFoundError(err) {
		t.Logf("Warning: failed to cleanup collection %s: %v", collectionName, err)
	}
}

// isNamespaceNotFoundError checks if the error is a "namespace not found" error
func isNamespaceNotFoundError(err error) bool {
	return err != nil && (
	// MongoDB error codes for namespace not found
	err.Error() == "ns not found" ||
		// Check for the error message pattern
		containsString(err.Error(), "namespace") && containsString(err.Error(), "not found"))
}

// containsString is a simple helper to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) &&
		(s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			findSubstring(s, substr)))
}

// findSubstring checks if substr exists in s
func findSubstring(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// createTestCollection creates a collection with optional sample documents
func createTestCollection(t *testing.T, conn *Connection, ctx context.Context, collectionName string, sampleDocs ...any) {
	t.Helper()

	// Create collection
	err := conn.CreateCollection(ctx, collectionName, nil)
	require.NoError(t, err, "Failed to create test collection")

	// Insert sample documents if provided
	if len(sampleDocs) > 0 {
		coll := conn.Collection(collectionName)
		for _, doc := range sampleDocs {
			_, err := coll.InsertOne(ctx, doc, nil)
			require.NoError(t, err, "Failed to insert sample document")
		}
	}
}

// sampleDocument returns a simple test document
func sampleDocument(name string, value int) bson.M {
	return bson.M{
		"name":   name,
		"value":  value,
		"active": true,
	}
}

// sampleDocuments returns multiple test documents
func sampleDocuments(count int) []any {
	docs := make([]any, count)
	for i := 0; i < count; i++ {
		docs[i] = sampleDocument(bsonString("doc", i), i)
	}
	return docs
}

// bsonString creates a formatted string for BSON documents
func bsonString(prefix string, num int) string {
	return fmt.Sprintf("%s%d", prefix, num)
}

// assertDocumentCount checks that a collection has the expected number of documents
func assertDocumentCount(t *testing.T, coll database.DocumentCollection, ctx context.Context, expected int64) {
	t.Helper()

	count, err := coll.CountDocuments(ctx, bson.M{}, nil)
	require.NoError(t, err, "Failed to count documents")
	require.Equal(t, expected, count, "Unexpected document count")
}

// assertDocumentExists checks that a document exists with the given filter
func assertDocumentExists(t *testing.T, coll database.DocumentCollection, ctx context.Context, filter bson.M) {
	t.Helper()

	result := coll.FindOne(ctx, filter, nil)
	require.NotNil(t, result, "FindOne returned nil")

	var doc bson.M
	err := result.Decode(&doc)
	require.NoError(t, err, "Document should exist")
	require.NotEmpty(t, doc, "Document should not be empty")
}

// assertDocumentNotExists checks that no document exists with the given filter
func assertDocumentNotExists(t *testing.T, coll database.DocumentCollection, ctx context.Context, filter bson.M) {
	t.Helper()

	count, err := coll.CountDocuments(ctx, filter, nil)
	require.NoError(t, err, "Failed to count documents")
	require.Equal(t, int64(0), count, "Document should not exist")
}
