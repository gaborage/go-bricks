//go:build integration

package mongodb

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/gaborage/go-bricks/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	cursorAllShouldSucceedMsg = "Cursor.All should succeed"
)

// =============================================================================
// Connection Lifecycle Tests
// =============================================================================

func TestConnectionHealth(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	err := conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed")
}

func TestConnectionStats(t *testing.T) {
	conn, _ := setupTestContainer(t)

	stats, err := conn.Stats()
	assert.NoError(t, err, "Stats retrieval should succeed")
	assert.NotNil(t, stats, "Stats should not be nil")
	assert.Contains(t, stats, "database_type", "Stats should contain database_type")
	assert.Equal(t, "mongodb", stats["database_type"], "Database type should be mongodb")
}

func TestConnectionDatabaseType(t *testing.T) {
	conn, _ := setupTestContainer(t)

	dbType := conn.DatabaseType()
	assert.Equal(t, "mongodb", dbType, "Database type should be mongodb")
}

func TestConnectionClose(t *testing.T) {
	conn, ctx := setupTestContainer(t)

	// Connection should work before close
	err := conn.Health(ctx)
	assert.NoError(t, err, "Health check should succeed before close")

	// Close connection
	err = conn.Close()
	assert.NoError(t, err, "Close should succeed")

	// Health check should fail after close
	err = conn.Health(ctx)
	assert.Error(t, err, "Health check should fail after close")
}

// =============================================================================
// Collection Management Tests
// =============================================================================

func TestConnectionCreateCollection(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := uniqueCollectionName(t, "test_create_collection")
	defer cleanupTestCollection(t, conn, ctx, collName)

	tests := []struct {
		name    string
		opts    *database.CreateCollectionOptions
		wantErr bool
	}{
		{
			name:    "create collection without options",
			opts:    nil,
			wantErr: false,
		},
		{
			name: "create capped collection",
			opts: &database.CreateCollectionOptions{
				Capped:      database.BoolPtr(true),
				SizeInBytes: database.Int64Ptr(1024 * 1024), // 1MB
			},
			wantErr: false,
		},
		{
			name: "create collection with validator",
			opts: &database.CreateCollectionOptions{
				Validator: bson.M{
					"$jsonSchema": bson.M{
						"required":   bson.A{"name", "value"},
						"properties": bson.M{"name": bson.M{"bsonType": "string"}},
					},
				},
			},
			wantErr: false,
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testCollName := collName + "_" + string(rune('0'+i))
			defer cleanupTestCollection(t, conn, ctx, testCollName)

			err := conn.CreateCollection(ctx, testCollName, tt.opts)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)

				// Verify collection exists by inserting a document
				coll := conn.Collection(testCollName)
				_, err := coll.InsertOne(ctx, sampleDocument("test", 1), nil)
				assert.NoError(t, err, "Should be able to insert into created collection")
			}
		})
	}
}

func TestConnectionDropCollection(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_drop_collection"

	// Create a collection first
	err := conn.CreateCollection(ctx, collName, nil)
	require.NoError(t, err, "Failed to create test collection")

	// Drop it
	err = conn.DropCollection(ctx, collName)
	assert.NoError(t, err, "Drop collection should succeed")

	// MongoDB allows dropping non-existent collections (idempotent operation)
	// This should NOT return an error
	err = conn.DropCollection(ctx, "nonexistent_collection")
	assert.NoError(t, err, "MongoDB allows dropping non-existent collections (idempotent)")
}

func TestConnectionCollection(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_get_collection"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Create collection
	createTestCollection(t, conn, ctx, collName)

	// Get collection
	coll := conn.Collection(collName)
	assert.NotNil(t, coll, "Collection should not be nil")
	assert.IsType(t, &Collection{}, coll, "Should return Collection type")

	// Verify we can use the collection
	_, err := coll.InsertOne(ctx, sampleDocument("test", 1), nil)
	assert.NoError(t, err, "Should be able to insert into collection")
}

// =============================================================================
// Index Management Tests
// =============================================================================

func TestConnectionCreateIndex(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_create_index"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)

	tests := []struct {
		name    string
		model   database.IndexModel
		wantErr bool
	}{
		{
			name: "create simple index",
			model: database.IndexModel{
				Keys: bson.D{{Key: "name", Value: 1}},
				Options: &database.IndexOptions{
					Name: database.StringPtr("name_idx"),
				},
			},
			wantErr: false,
		},
		{
			name: "create unique index",
			model: database.IndexModel{
				Keys: bson.D{{Key: "email", Value: 1}},
				Options: &database.IndexOptions{
					Name:   database.StringPtr("email_unique_idx"),
					Unique: database.BoolPtr(true),
				},
			},
			wantErr: false,
		},
		{
			name: "create sparse index",
			model: database.IndexModel{
				Keys: bson.D{{Key: "optional_field", Value: 1}},
				Options: &database.IndexOptions{
					Name:   database.StringPtr("optional_sparse_idx"),
					Sparse: database.BoolPtr(true),
				},
			},
			wantErr: false,
		},
		{
			name: "create TTL index",
			model: database.IndexModel{
				Keys: bson.D{{Key: "created_at", Value: 1}},
				Options: &database.IndexOptions{
					Name:               database.StringPtr("ttl_idx"),
					ExpireAfterSeconds: database.Int32Ptr(3600),
				},
			},
			wantErr: false,
		},
		{
			name: "create compound index",
			model: database.IndexModel{
				Keys: bson.D{
					{Key: "category", Value: 1},
					{Key: "priority", Value: -1},
				},
				Options: &database.IndexOptions{
					Name: database.StringPtr("compound_idx"),
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conn.CreateIndex(ctx, collName, tt.model)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConnectionListIndexes(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_list_indexes"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)

	// Create a few indexes
	indexModels := []database.IndexModel{
		{
			Keys: bson.D{{Key: "name", Value: 1}},
			Options: &database.IndexOptions{
				Name: database.StringPtr("name_idx"),
			},
		},
		{
			Keys: bson.D{{Key: "email", Value: 1}},
			Options: &database.IndexOptions{
				Name:   database.StringPtr("email_unique_idx"),
				Unique: database.BoolPtr(true),
			},
		},
	}

	for _, model := range indexModels {
		err := conn.CreateIndex(ctx, collName, model)
		require.NoError(t, err, "Failed to create test index")
	}

	// List indexes
	indexes, err := conn.ListIndexes(ctx, collName)
	assert.NoError(t, err, "ListIndexes should succeed")
	assert.GreaterOrEqual(t, len(indexes), 3, "Should have at least 3 indexes (_id + 2 custom)")

	// Verify index names
	indexNames := make([]string, len(indexes))
	for i, idx := range indexes {
		if idx.Options != nil && idx.Options.Name != nil {
			indexNames[i] = *idx.Options.Name
		}
	}
	assert.Contains(t, indexNames, "_id_", "Should contain default _id index")
	assert.Contains(t, indexNames, "name_idx", "Should contain name_idx")
	assert.Contains(t, indexNames, "email_unique_idx", "Should contain email_unique_idx")
}

func TestConnectionDropIndex(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_drop_index"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)

	// Create an index
	indexModel := database.IndexModel{
		Keys: bson.D{{Key: "name", Value: 1}},
		Options: &database.IndexOptions{
			Name: database.StringPtr("name_idx"),
		},
	}
	err := conn.CreateIndex(ctx, collName, indexModel)
	require.NoError(t, err, "Failed to create test index")

	// Drop the index
	err = conn.DropIndex(ctx, collName, "name_idx")
	assert.NoError(t, err, "DropIndex should succeed")

	// Verify index is gone
	indexes, err := conn.ListIndexes(ctx, collName)
	assert.NoError(t, err, "ListIndexes should succeed")

	for _, idx := range indexes {
		if idx.Options != nil && idx.Options.Name != nil {
			assert.NotEqual(t, "name_idx", *idx.Options.Name, "Index should be dropped")
		}
	}

	// Dropping non-existent index should return error
	err = conn.DropIndex(ctx, collName, "nonexistent_idx")
	assert.Error(t, err, "Dropping non-existent index should return error")
}

// =============================================================================
// CRUD Operations Tests
// =============================================================================

func TestCollectionInsertOne(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_insert_one"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)
	coll := conn.Collection(collName)

	doc := sampleDocument("test_insert", 42)
	insertedID, err := coll.InsertOne(ctx, doc, nil)
	assert.NoError(t, err, "InsertOne should succeed")
	assert.NotNil(t, insertedID, "Inserted ID should not be nil")

	// Verify document was inserted
	assertDocumentCount(t, coll, ctx, 1)
}

func TestCollectionInsertMany(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_insert_many"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)
	coll := conn.Collection(collName)

	docs := sampleDocuments(5)
	insertedIDs, err := coll.InsertMany(ctx, docs, nil)
	assert.NoError(t, err, "InsertMany should succeed")
	assert.Len(t, insertedIDs, 5, "Should have 5 inserted IDs")

	// Verify documents were inserted
	assertDocumentCount(t, coll, ctx, 5)
}

func TestCollectionFindOne(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_find_one"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert test data
	testDoc := sampleDocument("find_test", 123)
	createTestCollection(t, conn, ctx, collName, testDoc)
	coll := conn.Collection(collName)

	// Find the document
	filter := bson.M{"name": "find_test"}
	result := coll.FindOne(ctx, filter, nil)
	assert.NotNil(t, result, "FindOne result should not be nil")

	var doc bson.M
	err := result.Decode(&doc)
	assert.NoError(t, err, "Decode should succeed")
	assert.Equal(t, "find_test", doc["name"], "Document name should match")
	assert.Equal(t, int32(123), doc["value"], "Document value should match")

	// Find non-existent document
	filter = bson.M{"name": "nonexistent"}
	result = coll.FindOne(ctx, filter, nil)
	assert.NotNil(t, result, "FindOne should return result even if not found")

	err = result.Decode(&doc)
	assert.Error(t, err, "Decode should fail for non-existent document")
	assert.True(t, errors.Is(err, mongo.ErrNoDocuments), "Should be ErrNoDocuments")
}

func TestCollectionFind(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_find"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert multiple test documents
	docs := []any{
		sampleDocument("doc1", 10),
		sampleDocument("doc2", 20),
		sampleDocument("doc3", 30),
	}
	createTestCollection(t, conn, ctx, collName, docs...)
	coll := conn.Collection(collName)

	// Find all documents
	cursor, err := coll.Find(ctx, bson.M{}, nil)
	assert.NoError(t, err, "Find should succeed")
	assert.NotNil(t, cursor, "Cursor should not be nil")

	var results []bson.M
	err = cursor.All(ctx, &results)
	assert.NoError(t, err, cursorAllShouldSucceedMsg)
	assert.Len(t, results, 3, "Should find 3 documents")

	// Find with filter
	filter := bson.M{"value": bson.M{"$gte": 20}}
	cursor, err = coll.Find(ctx, filter, nil)
	assert.NoError(t, err, "Find with filter should succeed")

	results = nil
	err = cursor.All(ctx, &results)
	assert.NoError(t, err, cursorAllShouldSucceedMsg)
	assert.Len(t, results, 2, "Should find 2 documents with value >= 20")
}

func TestCollectionUpdateOne(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_update_one"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert test document
	testDoc := sampleDocument("update_test", 100)
	createTestCollection(t, conn, ctx, collName, testDoc)
	coll := conn.Collection(collName)

	// Update the document
	filter := bson.M{"name": "update_test"}
	update := bson.M{"$set": bson.M{"value": 200}}
	result, err := coll.UpdateOne(ctx, filter, update, nil)
	assert.NoError(t, err, "UpdateOne should succeed")
	assert.NotNil(t, result, "Update result should not be nil")
	assert.Equal(t, int64(1), result.MatchedCount(), "Should match 1 document")
	assert.Equal(t, int64(1), result.ModifiedCount(), "Should modify 1 document")

	// Verify update
	assertDocumentExists(t, coll, ctx, bson.M{"name": "update_test", "value": 200})
}

func TestCollectionUpdateMany(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_update_many"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert multiple test documents
	docs := sampleDocuments(5)
	createTestCollection(t, conn, ctx, collName, docs...)
	coll := conn.Collection(collName)

	// Update all documents
	filter := bson.M{"active": true}
	update := bson.M{"$set": bson.M{"updated": true}}
	result, err := coll.UpdateMany(ctx, filter, update, nil)
	assert.NoError(t, err, "UpdateMany should succeed")
	assert.NotNil(t, result, "Update result should not be nil")
	assert.Equal(t, int64(5), result.MatchedCount(), "Should match 5 documents")
	assert.Equal(t, int64(5), result.ModifiedCount(), "Should modify 5 documents")

	// Verify all documents were updated
	count, err := coll.CountDocuments(ctx, bson.M{"updated": true}, nil)
	assert.NoError(t, err, "CountDocuments should succeed")
	assert.Equal(t, int64(5), count, "All documents should be updated")
}

func TestCollectionDeleteOne(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_delete_one"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert test documents
	docs := sampleDocuments(3)
	createTestCollection(t, conn, ctx, collName, docs...)
	coll := conn.Collection(collName)

	// Delete one document
	filter := bson.M{"name": "doc0"}
	result, err := coll.DeleteOne(ctx, filter, nil)
	assert.NoError(t, err, "DeleteOne should succeed")
	assert.NotNil(t, result, "Delete result should not be nil")
	assert.Equal(t, int64(1), result.DeletedCount(), "Should delete 1 document")

	// Verify deletion
	assertDocumentCount(t, coll, ctx, 2)
	assertDocumentNotExists(t, coll, ctx, filter)
}

func TestCollectionDeleteMany(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_delete_many"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert test documents
	docs := sampleDocuments(5)
	createTestCollection(t, conn, ctx, collName, docs...)
	coll := conn.Collection(collName)

	// Delete multiple documents
	filter := bson.M{"active": true}
	result, err := coll.DeleteMany(ctx, filter, nil)
	assert.NoError(t, err, "DeleteMany should succeed")
	assert.NotNil(t, result, "Delete result should not be nil")
	assert.Equal(t, int64(5), result.DeletedCount(), "Should delete 5 documents")

	// Verify deletion
	assertDocumentCount(t, coll, ctx, 0)
}

func TestCollectionCountDocuments(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_count_documents"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert test documents
	docs := sampleDocuments(10)
	createTestCollection(t, conn, ctx, collName, docs...)
	coll := conn.Collection(collName)

	// Count all documents
	count, err := coll.CountDocuments(ctx, bson.M{}, nil)
	assert.NoError(t, err, "CountDocuments should succeed")
	assert.Equal(t, int64(10), count, "Should count 10 documents")

	// Count with filter
	filter := bson.M{"active": true}
	count, err = coll.CountDocuments(ctx, filter, nil)
	assert.NoError(t, err, "CountDocuments with filter should succeed")
	assert.Equal(t, int64(10), count, "Should count 10 active documents")
}

// =============================================================================
// Transaction Tests
// =============================================================================

func TestTransactionCommitSuccess(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_transaction_commit"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)

	// Begin transaction
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "Begin transaction should succeed")
	require.NotNil(t, tx, "Transaction should not be nil")

	// Get transaction context (MongoDB-specific)
	mongoTx, ok := tx.(*Transaction)
	require.True(t, ok, "Transaction should be MongoDB Transaction type")
	require.NotNil(t, mongoTx.session, "Session should not be nil")

	// Note: MongoDB transactions require session context, but our interface
	// doesn't expose this. For now, we'll test commit/rollback behavior.

	// Commit transaction
	err = tx.Commit()
	assert.NoError(t, err, "Commit should succeed")
}

func TestTransactionRollbackSuccess(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_transaction_rollback"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)

	// Begin transaction
	tx, err := conn.Begin(ctx)
	require.NoError(t, err, "Begin transaction should succeed")
	require.NotNil(t, tx, "Transaction should not be nil")

	// Rollback transaction
	err = tx.Rollback()
	assert.NoError(t, err, "Rollback should succeed")
}

// =============================================================================
// Aggregation Tests
// =============================================================================

func TestCollectionAggregate(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_aggregate"
	defer cleanupTestCollection(t, conn, ctx, collName)

	// Insert test documents with categories
	docs := []any{
		bson.M{"category": "A", "value": 10},
		bson.M{"category": "A", "value": 20},
		bson.M{"category": "B", "value": 15},
		bson.M{"category": "B", "value": 25},
		bson.M{"category": "C", "value": 30},
	}
	createTestCollection(t, conn, ctx, collName, docs...)
	coll := conn.Collection(collName)

	// Aggregation pipeline: group by category and sum values
	pipeline := bson.A{
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$category"},
			{Key: "total", Value: bson.D{{Key: "$sum", Value: "$value"}}},
		}}},
		bson.D{{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}},
	}

	cursor, err := coll.Aggregate(ctx, pipeline, nil)
	assert.NoError(t, err, "Aggregate should succeed")
	assert.NotNil(t, cursor, "Cursor should not be nil")

	var results []bson.M
	err = cursor.All(ctx, &results)
	assert.NoError(t, err, cursorAllShouldSucceedMsg)
	assert.Len(t, results, 3, "Should have 3 groups")

	// Verify aggregation results
	assert.Equal(t, "A", results[0]["_id"], "First group should be A")
	assert.Equal(t, int32(30), results[0]["total"], "Group A total should be 30")
	assert.Equal(t, "B", results[1]["_id"], "Second group should be B")
	assert.Equal(t, int32(40), results[1]["total"], "Group B total should be 40")
	assert.Equal(t, "C", results[2]["_id"], "Third group should be C")
	assert.Equal(t, int32(30), results[2]["total"], "Group C total should be 30")
}

// =============================================================================
// Bulk Operations Tests
// =============================================================================

func TestCollectionBulkWrite(t *testing.T) {
	conn, ctx := setupTestContainer(t)
	collName := "test_bulk_write"
	defer cleanupTestCollection(t, conn, ctx, collName)

	createTestCollection(t, conn, ctx, collName)
	coll := conn.Collection(collName)

	// Create bulk write models
	models := []database.WriteModel{
		&InsertOneModel{Document: sampleDocument("bulk1", 100)},
		&InsertOneModel{Document: sampleDocument("bulk2", 200)},
		&UpdateOneModel{
			Filter: bson.M{"name": "bulk1"},
			Update: bson.M{"$set": bson.M{"value": 150}},
		},
		&DeleteOneModel{
			Filter: bson.M{"name": "bulk2"},
		},
	}

	result, err := coll.BulkWrite(ctx, models, nil)
	assert.NoError(t, err, "BulkWrite should succeed")
	assert.NotNil(t, result, "Bulk write result should not be nil")
	assert.Equal(t, int64(2), result.InsertedCount(), "Should insert 2 documents")
	assert.Equal(t, int64(1), result.ModifiedCount(), "Should modify 1 document")
	assert.Equal(t, int64(1), result.DeletedCount(), "Should delete 1 document")

	// Verify final state: 1 document with value 150
	assertDocumentCount(t, coll, ctx, 1)
	assertDocumentExists(t, coll, ctx, bson.M{"name": "bulk1", "value": 150})
}

// =============================================================================
// Interface Compliance Tests
// =============================================================================

func TestConnectionInterfaceCompliance(t *testing.T) {
	conn, _ := setupTestContainer(t)

	// Verify Connection implements database.Interface
	var _ database.Interface = conn

	// Verify Connection implements database.DocumentInterface
	var _ database.DocumentInterface = conn

	// Test AsDocumentInterface helper
	docDB, ok := database.AsDocumentInterface(conn)
	assert.True(t, ok, "Connection should implement DocumentInterface")
	assert.NotNil(t, docDB, "DocumentInterface should not be nil")
}
