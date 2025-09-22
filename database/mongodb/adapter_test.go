package mongodb

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants to avoid duplication
const (
	testNilInput     = "nil input"
	testEmptyOptions = "empty options"
)

func TestBuildIndexOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.IndexOptions
		expected func(*options.IndexOptions) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.IndexOptions) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.IndexOptions{},
			expected: func(opts *options.IndexOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with name",
			input: &database.IndexOptions{
				Name: database.StringPtr("test_index"),
			},
			expected: func(opts *options.IndexOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with unique",
			input: &database.IndexOptions{
				Unique: database.BoolPtr(true),
			},
			expected: func(opts *options.IndexOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with sparse",
			input: &database.IndexOptions{
				Sparse: database.BoolPtr(true),
			},
			expected: func(opts *options.IndexOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with expire after seconds",
			input: &database.IndexOptions{
				ExpireAfterSeconds: database.Int32Ptr(3600),
			},
			expected: func(opts *options.IndexOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with partial filter expression",
			input: &database.IndexOptions{
				PartialFilterExpression: bson.M{"age": bson.M{"$gt": 18}},
			},
			expected: func(opts *options.IndexOptions) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildIndexOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}

func TestBuildFindOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.FindOptions
		expected func(*options.FindOptions) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.FindOptions) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.FindOptions{},
			expected: func(opts *options.FindOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with skip and limit",
			input: &database.FindOptions{
				Skip:  database.Int64Ptr(10),
				Limit: database.Int64Ptr(5),
			},
			expected: func(opts *options.FindOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with batch size",
			input: &database.FindOptions{
				BatchSize: database.Int32Ptr(100),
			},
			expected: func(opts *options.FindOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with no cursor timeout",
			input: &database.FindOptions{
				NoCursorTimeout: database.BoolPtr(true),
			},
			expected: func(opts *options.FindOptions) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFindOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}

func TestBuildUpdateOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.UpdateOptions
		expected func(*options.UpdateOptions) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.UpdateOptions) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.UpdateOptions{},
			expected: func(opts *options.UpdateOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with upsert",
			input: &database.UpdateOptions{
				Upsert: database.BoolPtr(true),
			},
			expected: func(opts *options.UpdateOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with bypass document validation",
			input: &database.UpdateOptions{
				BypassDocumentValidation: database.BoolPtr(true),
			},
			expected: func(opts *options.UpdateOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with array filters",
			input: &database.UpdateOptions{
				ArrayFilters: []interface{}{bson.M{"elem.score": bson.M{"$gte": 80}}},
			},
			expected: func(opts *options.UpdateOptions) bool {
				return opts != nil // ArrayFilters are currently commented out due to type compatibility
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildUpdateOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}

func setupTestConnection(t *testing.T, mt *mtest.T) *Connection {
	cfg := &config.DatabaseConfig{
		Host:     "localhost",
		Port:     27017,
		Database: "test",
	}
	log := &testLogger{}

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

func TestConnection_Collection(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("get collection", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		collection := conn.Collection("test_collection")
		assert.NotNil(t, collection)
		assert.IsType(t, &Collection{}, collection)
	})
}

func TestConnection_CreateCollection(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("create collection", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock successful collection creation
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		err := conn.CreateCollection(context.Background(), "new_collection", nil)
		assert.NoError(t, err)
	})

	mt.Run("create collection with options", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock successful collection creation
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		opts := &database.CreateCollectionOptions{
			Capped:      database.BoolPtr(true),
			SizeInBytes: database.Int64Ptr(1024),
		}

		err := conn.CreateCollection(context.Background(), "capped_collection", opts)
		assert.NoError(t, err)
	})
}

func TestConnection_DropCollection(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("drop collection", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock successful collection drop
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		err := conn.DropCollection(context.Background(), "test_collection")
		assert.NoError(t, err)
	})
}

func TestConnection_CreateIndex(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("create index", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock successful index creation
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		model := database.IndexModel{
			Keys: bson.D{{Key: "name", Value: 1}},
			Options: &database.IndexOptions{
				Name:   database.StringPtr("name_index"),
				Unique: database.BoolPtr(true),
			},
		}

		err := conn.CreateIndex(context.Background(), "test_collection", model)
		assert.NoError(t, err)
	})
}

func TestConnection_RunCommand(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("run command", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock command response
		mt.AddMockResponses(bson.D{{Key: "ok", Value: 1}, {Key: "result", Value: "success"}})

		result := conn.RunCommand(context.Background(), bson.D{{Key: "ping", Value: 1}})
		assert.NotNil(t, result)

		var response bson.M
		err := result.Decode(&response)
		assert.NoError(t, err)
		assert.Equal(t, int32(1), response["ok"])
	})
}

func TestCollection_InsertOne(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("insert one document", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock successful insert
		mt.AddMockResponses(MockSuccessResponse())

		document := bson.M{"name": "test", "value": 123}
		insertedID, err := collection.InsertOne(context.Background(), document, nil)
		assert.NoError(t, err)
		assert.NotNil(t, insertedID)
	})

	mt.Run("insert one with options", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock successful insert
		mt.AddMockResponses(MockSuccessResponse())

		document := bson.M{"name": "test", "value": 123}
		opts := &database.InsertOneOptions{
			BypassDocumentValidation: database.BoolPtr(true),
		}

		insertedID, err := collection.InsertOne(context.Background(), document, opts)
		assert.NoError(t, err)
		assert.NotNil(t, insertedID)
	})
}

func TestCollection_FindOne(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("find one document", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock find response using helper
		userDoc := MockUser("507f1f77bcf86cd799439011", "test")
		mt.AddMockResponses(MockFindResponse(TestNamespace("test_collection"), userDoc))

		filter := bson.M{"name": "test"}
		result := collection.FindOne(context.Background(), filter, nil)
		assert.NotNil(t, result)

		var doc bson.M
		err := result.Decode(&doc)
		assert.NoError(t, err)
		assert.Equal(t, "test", doc["name"])
	})
}

func TestCollection_UpdateOne(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("update one document", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock update response using helper with correct wire protocol fields
		mt.AddMockResponses(MockUpdateResponse(1, 1, nil))

		filter := bson.M{"name": "test"}
		update := bson.M{"$set": bson.M{"value": 456}}

		result, err := collection.UpdateOne(context.Background(), filter, update, nil)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, int64(1), result.MatchedCount())
		assert.Equal(t, int64(1), result.ModifiedCount())
	})
}

func TestCollection_DeleteOne(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("delete one document", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock delete response using helper with correct wire protocol fields
		mt.AddMockResponses(MockDeleteResponse(1))

		filter := bson.M{"name": "test"}
		result, err := collection.DeleteOne(context.Background(), filter, nil)
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, int64(1), result.DeletedCount())
	})
}

func TestCollection_CountDocuments(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("count documents", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock count response using helper (CountDocuments uses aggregation internally)
		mt.AddMockResponses(MockCountResponse(TestNamespace("test_collection"), 5))

		filter := bson.M{"status": "active"}
		count, err := collection.CountDocuments(context.Background(), filter, nil)
		assert.NoError(t, err)
		assert.Equal(t, int64(5), count)
	})
}

func TestConnection_BeginTransaction(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("begin transaction", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock session start
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		tx, err := conn.Begin(context.Background())
		assert.NoError(t, err)
		assert.NotNil(t, tx)
		assert.IsType(t, &Transaction{}, tx)

		// Clean up transaction to avoid session leak
		err = tx.Rollback()
		assert.NoError(t, err)
	})
}

func TestConnection_InterfaceCompliance(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("interface compliance", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Test that Connection implements both interfaces
		var dbInterface database.Interface = conn
		var docInterface database.DocumentInterface = conn

		assert.NotNil(t, dbInterface)
		assert.NotNil(t, docInterface)

		// Test AsDocumentInterface helper
		docDB, ok := database.AsDocumentInterface(conn)
		assert.True(t, ok)
		assert.NotNil(t, docDB)
	})
}
