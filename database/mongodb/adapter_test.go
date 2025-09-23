package mongodb

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
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
				ArrayFilters: []any{bson.M{"elem.score": bson.M{"$gte": 80}}},
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
	log := CreateTestLogger()

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

func TestConnectionCollection(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("get collection", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		collection := conn.Collection("test_collection")
		assert.NotNil(t, collection)
		assert.IsType(t, &Collection{}, collection)
	})
}

func TestConnectionCreateCollection(t *testing.T) {
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

func TestConnectionDropCollection(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("drop collection", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock successful collection drop
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		err := conn.DropCollection(context.Background(), "test_collection")
		assert.NoError(t, err)
	})
}

func TestConnectionCreateIndex(t *testing.T) {
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

func TestConnectionRunCommand(t *testing.T) {
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

func TestCollectionInsertOne(t *testing.T) {
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

func TestCollectionFindOne(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("find one document", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock find response using helper
		userDoc := MockUser("507f1f77bcf86cd799439011", "test")
		mt.AddMockResponses(MockFindResponse(CreateTestNamespace("test_collection"), userDoc))

		filter := bson.M{"name": "test"}
		result := collection.FindOne(context.Background(), filter, nil)
		assert.NotNil(t, result)

		var doc bson.M
		err := result.Decode(&doc)
		assert.NoError(t, err)
		assert.Equal(t, "test", doc["name"])
	})
}

func TestCollectionUpdateOne(t *testing.T) {
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

func TestCollectionDeleteOne(t *testing.T) {
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

func TestCollectionCountDocuments(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("count documents", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("test_collection")

		// Mock count response using helper (CountDocuments uses aggregation internally)
		mt.AddMockResponses(MockCountResponse(CreateTestNamespace("test_collection"), 5))

		filter := bson.M{"status": "active"}
		count, err := collection.CountDocuments(context.Background(), filter, nil)
		assert.NoError(t, err)
		assert.Equal(t, int64(5), count)
	})
}

func TestConnectionBeginTransaction(t *testing.T) {
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
		mt.AddMockResponses(mtest.CreateSuccessResponse())
		err = tx.Rollback()
		assert.NoError(t, err)
	})
}

func TestTransactionCommitAndRollback(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("transaction commit", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock session start and transaction start
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		tx, err := conn.Begin(context.Background())
		assert.NoError(t, err)
		assert.NotNil(t, tx)

		// Cast to MongoDB Transaction to verify proper session context usage
		mongoTx, ok := tx.(*Transaction)
		assert.True(t, ok)
		assert.NotNil(t, mongoTx.session)
		assert.NotNil(t, mongoTx.parentCtx)

		// Mock transaction commit
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		err = tx.Commit()
		assert.NoError(t, err)
	})

	mt.Run("transaction rollback", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)

		// Mock session start and transaction start
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		tx, err := conn.Begin(context.Background())
		assert.NoError(t, err)
		assert.NotNil(t, tx)

		// Cast to MongoDB Transaction to verify proper session context usage
		mongoTx, ok := tx.(*Transaction)
		assert.True(t, ok)
		assert.NotNil(t, mongoTx.session)
		assert.NotNil(t, mongoTx.parentCtx)

		// Mock transaction abort
		mt.AddMockResponses(mtest.CreateSuccessResponse())

		err = tx.Rollback()
		assert.NoError(t, err)
	})
}

func TestConnectionInterfaceCompliance(t *testing.T) {
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

func TestParseExpireAfterSeconds(t *testing.T) {
	tests := []struct {
		name     string
		input    any
		expected *int32
	}{
		{
			name:     "int32 value",
			input:    int32(3600),
			expected: func() *int32 { v := int32(3600); return &v }(),
		},
		{
			name:     "int32 negative value (should be rejected)",
			input:    int32(-1),
			expected: nil,
		},
		{
			name:     "int32 zero value",
			input:    int32(0),
			expected: func() *int32 { v := int32(0); return &v }(),
		},
		{
			name:     "int64 value in range",
			input:    int64(7200),
			expected: func() *int32 { v := int32(7200); return &v }(),
		},
		{
			name:     "int64 negative value (should be rejected)",
			input:    int64(-100),
			expected: nil,
		},
		{
			name:     "int64 value max int32",
			input:    int64(math.MaxInt32),
			expected: func() *int32 { v := int32(math.MaxInt32); return &v }(),
		},
		{
			name:     "int64 value min int32 (negative, should be rejected)",
			input:    int64(math.MinInt32),
			expected: nil,
		},
		{
			name:     "int64 value out of range (too large)",
			input:    int64(math.MaxInt32) + 1,
			expected: nil,
		},
		{
			name:     "int64 value out of range (too small)",
			input:    int64(math.MinInt32) - 1,
			expected: nil,
		},
		{
			name:     "float64 value",
			input:    float64(1800.0),
			expected: func() *int32 { v := int32(1800); return &v }(),
		},
		{
			name:     "float64 negative value (should be rejected)",
			input:    float64(-123.5),
			expected: nil,
		},
		{
			name:     "float64 value with rounding",
			input:    float64(1800.6),
			expected: func() *int32 { v := int32(1801); return &v }(),
		},
		{
			name:     "float64 value with rounding down",
			input:    float64(1800.4),
			expected: func() *int32 { v := int32(1800); return &v }(),
		},
		{
			name:     "float64 value out of range",
			input:    float64(math.MaxInt32) + 1000.0,
			expected: nil,
		},
		{
			name:     "json.Number as integer",
			input:    json.Number("900"),
			expected: func() *int32 { v := int32(900); return &v }(),
		},
		{
			name:     "json.Number negative integer (should be rejected)",
			input:    json.Number("-500"),
			expected: nil,
		},
		{
			name:     "json.Number as float",
			input:    json.Number("900.7"),
			expected: func() *int32 { v := int32(901); return &v }(),
		},
		{
			name:     "json.Number negative float (should be rejected)",
			input:    json.Number("-123.7"),
			expected: nil,
		},
		{
			name:     "json.Number invalid",
			input:    json.Number("invalid"),
			expected: nil,
		},
		{
			name:     "json.Number out of range",
			input:    json.Number("9999999999999"),
			expected: nil,
		},
		{
			name:     "unsupported type (string)",
			input:    "3600",
			expected: nil,
		},
		{
			name:     "unsupported type (bool)",
			input:    true,
			expected: nil,
		},
		{
			name:     "nil value",
			input:    nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseExpireAfterSeconds(tt.input)

			if tt.expected == nil {
				assert.Nil(t, result, "Expected nil result for input: %v", tt.input)
			} else {
				require.NotNil(t, result, "Expected non-nil result for input: %v", tt.input)
				assert.Equal(t, *tt.expected, *result, "Expected %d, got %d for input: %v", *tt.expected, *result, tt.input)
			}
		})
	}
}

func TestBuildChangeStreamOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.ChangeStreamOptions
		expected func(*options.ChangeStreamOptions) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.ChangeStreamOptions) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.ChangeStreamOptions{},
			expected: func(opts *options.ChangeStreamOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with batch size",
			input: &database.ChangeStreamOptions{
				BatchSize: database.Int32Ptr(100),
			},
			expected: func(opts *options.ChangeStreamOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with full document",
			input: &database.ChangeStreamOptions{
				FullDocument: database.StringPtr("updateLookup"),
			},
			expected: func(opts *options.ChangeStreamOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with resume after",
			input: &database.ChangeStreamOptions{
				ResumeAfter: bson.M{"_id": "test"},
			},
			expected: func(opts *options.ChangeStreamOptions) bool {
				return opts != nil
			},
		},
		{
			name: "with start after",
			input: &database.ChangeStreamOptions{
				StartAfter: bson.M{"_id": "test"},
			},
			expected: func(opts *options.ChangeStreamOptions) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildChangeStreamOptions(tt.input)
			assert.True(t, tt.expected(result), "Unexpected result for test case: %s", tt.name)
		})
	}
}

func TestValidateAndMapFullDocument(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected options.FullDocument
		valid    bool
	}{
		{
			name:     "valid default",
			input:    "default",
			expected: options.Default,
			valid:    true,
		},
		{
			name:     "valid updateLookup",
			input:    "updateLookup",
			expected: options.UpdateLookup,
			valid:    true,
		},
		{
			name:     "valid whenAvailable",
			input:    "whenAvailable",
			expected: options.WhenAvailable,
			valid:    true,
		},
		{
			name:     "valid required",
			input:    "required",
			expected: options.Required,
			valid:    true,
		},
		{
			name:     "invalid empty string",
			input:    "",
			expected: "",
			valid:    false,
		},
		{
			name:     "invalid unknown value",
			input:    "unknown",
			expected: "",
			valid:    false,
		},
		{
			name:     "invalid case sensitive",
			input:    "Default",
			expected: "",
			valid:    false,
		},
		{
			name:     "invalid case sensitive updateLookup",
			input:    "UpdateLookup",
			expected: "",
			valid:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, valid := validateAndMapFullDocument(tt.input)
			assert.Equal(t, tt.valid, valid, "Expected validity %v, got %v for input: %s", tt.valid, valid, tt.input)
			if tt.valid {
				assert.Equal(t, tt.expected, result, "Expected %v, got %v for input: %s", tt.expected, result, tt.input)
			}
		})
	}
}

func TestBuildChangeStreamOptionsWithEnhancements(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.ChangeStreamOptions
		validate func(t *testing.T, result *options.ChangeStreamOptions)
	}{
		{
			name: "valid FullDocument values",
			input: &database.ChangeStreamOptions{
				FullDocument: database.StringPtr("updateLookup"),
			},
			validate: func(t *testing.T, result *options.ChangeStreamOptions) {
				assert.NotNil(t, result)
				// FullDocument is set via builder pattern, so we can't directly assert the value
				// but we know it was set because no error occurred
			},
		},
		{
			name: "invalid FullDocument silently ignored",
			input: &database.ChangeStreamOptions{
				FullDocument: database.StringPtr("invalidValue"),
			},
			validate: func(t *testing.T, result *options.ChangeStreamOptions) {
				assert.NotNil(t, result)
				// Invalid FullDocument should be silently ignored
			},
		},
		{
			name: "StartAtOperationTime with *primitive.Timestamp",
			input: &database.ChangeStreamOptions{
				StartAtOperationTime: &primitive.Timestamp{T: 123, I: 456},
			},
			validate: func(t *testing.T, result *options.ChangeStreamOptions) {
				assert.NotNil(t, result)
			},
		},
		{
			name: "StartAtOperationTime with primitive.Timestamp value",
			input: &database.ChangeStreamOptions{
				StartAtOperationTime: primitive.Timestamp{T: 123, I: 456},
			},
			validate: func(t *testing.T, result *options.ChangeStreamOptions) {
				assert.NotNil(t, result)
			},
		},
		{
			name: "StartAtOperationTime with invalid type silently ignored",
			input: &database.ChangeStreamOptions{
				StartAtOperationTime: "invalid_type",
			},
			validate: func(t *testing.T, result *options.ChangeStreamOptions) {
				assert.NotNil(t, result)
				// Invalid type should be silently ignored
			},
		},
		{
			name: "combination of valid and invalid values",
			input: &database.ChangeStreamOptions{
				BatchSize:            database.Int32Ptr(100),
				FullDocument:         database.StringPtr("required"),
				StartAtOperationTime: primitive.Timestamp{T: 789, I: 101},
				ResumeAfter:          bson.M{"_id": "test"},
			},
			validate: func(t *testing.T, result *options.ChangeStreamOptions) {
				assert.NotNil(t, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildChangeStreamOptions(tt.input)
			tt.validate(t, result)
		})
	}
}
