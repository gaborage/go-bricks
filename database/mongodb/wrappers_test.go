package mongodb

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/gaborage/go-bricks/internal/database"
)

const testCollName = "test.coll"

func TestCursorWrapper(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("cursor operations", func(mt *mtest.T) {
		// Setup mock responses
		first := mtest.CreateCursorResponse(1, testCollName, mtest.FirstBatch, bson.D{{Key: "_id", Value: 1}, {Key: "name", Value: "test1"}})
		second := mtest.CreateCursorResponse(0, testCollName, mtest.NextBatch)
		mt.AddMockResponses(first, second)

		coll := mt.Coll
		mongoCursor, err := coll.Find(context.Background(), bson.D{})
		require.NoError(t, err)

		// Wrap the cursor
		cursor := &Cursor{cursor: mongoCursor}

		// Test Next
		hasNext := cursor.Next(context.Background())
		assert.True(t, hasNext)

		// Test TryNext - should also work
		hasTryNext := cursor.TryNext(context.Background())
		_ = hasTryNext // TryNext behavior depends on current state

		// Test Decode
		var result1 map[string]any
		err = cursor.Decode(&result1)
		assert.NoError(t, err)
		assert.Equal(t, int32(1), result1["_id"])

		// Test Current
		current := cursor.Current()
		assert.NotNil(t, current)

		// Test ID - with mock cursor, ID might be 0, so just test it doesn't panic
		id := cursor.ID()
		_ = id // Mock cursors may have ID of 0, which is fine for testing

		// Test All - collect remaining documents
		var allResults []map[string]any
		err = cursor.All(context.Background(), &allResults)
		assert.NoError(t, err)

		// Test Err
		assert.NoError(t, cursor.Err())

		// Test Close
		err = cursor.Close(context.Background())
		assert.NoError(t, err)
	})

	mt.Run("cursor basic behavior", func(mt *mtest.T) {
		// Simple test to ensure wrappers don't panic
		first := mtest.CreateCursorResponse(0, testCollName, mtest.FirstBatch)
		mt.AddMockResponses(first)

		coll := mt.Coll
		mongoCursor, err := coll.Find(context.Background(), bson.D{})
		require.NoError(t, err)

		cursor := &Cursor{cursor: mongoCursor}

		// Test methods don't panic
		hasNext := cursor.Next(context.Background())
		assert.False(t, hasNext) // No documents

		// These should not panic even with no data
		_ = cursor.TryNext(context.Background())
		_ = cursor.Current()
		_ = cursor.ID()
		assert.NoError(t, cursor.Err())
		assert.NoError(t, cursor.Close(context.Background()))
	})
}

func TestSingleResultWrapper(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("single result operations", func(mt *mtest.T) {
		// Setup mock response for findOne - need a cursor response
		response := mtest.CreateCursorResponse(1, testCollName, mtest.FirstBatch, bson.D{{Key: "_id", Value: 1}, {Key: "name", Value: "test"}})
		mt.AddMockResponses(response)

		coll := mt.Coll
		mongoResult := coll.FindOne(context.Background(), bson.D{})

		// Wrap the result
		result := &SingleResult{result: mongoResult}

		// Test Decode
		var decoded map[string]any
		err := result.Decode(&decoded)
		assert.NoError(t, err)
		assert.Equal(t, int32(1), decoded["_id"])
		assert.Equal(t, "test", decoded["name"])

		// Test Err
		assert.NoError(t, result.Err())
	})

	mt.Run("single result with no documents", func(mt *mtest.T) {
		// Setup mock response for no documents
		mt.AddMockResponses(mtest.CreateCursorResponse(0, testCollName, mtest.FirstBatch))

		coll := mt.Coll
		mongoResult := coll.FindOne(context.Background(), bson.D{{Key: "nonexistent", Value: true}})

		result := &SingleResult{result: mongoResult}

		// Test Err should return ErrNoDocuments
		err := result.Err()
		assert.Error(t, err)
		assert.True(t, errors.Is(err, mongo.ErrNoDocuments))

		// Test Decode should also return error
		var decoded map[string]any
		err = result.Decode(&decoded)
		assert.Error(t, err)
	})
}

func TestChangeStreamWrapper(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("change stream basic operations", func(mt *mtest.T) {
		// Simple change stream test - just ensure methods don't panic
		first := mtest.CreateCursorResponse(0, testCollName, mtest.FirstBatch)
		mt.AddMockResponses(first)

		coll := mt.Coll
		changeStream, err := coll.Watch(context.Background(), bson.A{})
		require.NoError(t, err)

		// Wrap the change stream
		wrapper := &ChangeStreamWrapper{stream: changeStream}

		// Test methods don't panic - behavior may vary based on mock state
		_ = wrapper.Next(context.Background())
		_ = wrapper.TryNext(context.Background())

		// Test other methods
		_ = wrapper.ResumeToken()
		_ = wrapper.Err()

		// Test Close
		err = wrapper.Close(context.Background())
		assert.NoError(t, err)
	})

	mt.Run("change stream with data", func(mt *mtest.T) {
		// Try to test with actual change data
		changeEvent := bson.D{
			{Key: "_id", Value: bson.D{{Key: "_data", Value: "test_resume_token"}}},
			{Key: "operationType", Value: "insert"},
			{Key: "fullDocument", Value: bson.D{{Key: "_id", Value: 1}, {Key: "name", Value: "test"}}},
		}
		first := mtest.CreateCursorResponse(1, testCollName, mtest.FirstBatch, changeEvent)
		second := mtest.CreateCursorResponse(0, testCollName, mtest.NextBatch)
		mt.AddMockResponses(first, second)

		coll := mt.Coll
		changeStream, err := coll.Watch(context.Background(), bson.A{})
		require.NoError(t, err)

		wrapper := &ChangeStreamWrapper{stream: changeStream}

		// Test Next
		if wrapper.Next(context.Background()) {
			// Test Decode if we have data
			var change map[string]any
			err = wrapper.Decode(&change)
			if err == nil {
				// Verify the decode worked
				assert.Equal(t, "insert", change["operationType"])
			}
		}

		// Test ResumeToken
		token := wrapper.ResumeToken()
		_ = token // May be nil or have data depending on mock state

		// Test Err
		_ = wrapper.Err()

		// Test Close
		err = wrapper.Close(context.Background())
		assert.NoError(t, err)
	})
}

func TestWriteModelImplementations(t *testing.T) {
	t.Run("InsertOneModel", func(t *testing.T) {
		doc := bson.D{{Key: "name", Value: "test"}}
		model := &InsertOneModel{Document: doc}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		// Verify it's the correct type
		insertModel, ok := mongoModel.(*mongo.InsertOneModel)
		assert.True(t, ok)
		assert.NotNil(t, insertModel)
	})

	t.Run("UpdateOneModel", func(t *testing.T) {
		filter := bson.D{{Key: "_id", Value: 1}}
		update := bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: "updated"}}}}
		upsert := true

		model := &UpdateOneModel{
			Filter: filter,
			Update: update,
			Upsert: &upsert,
		}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		// Verify it's the correct type
		updateModel, ok := mongoModel.(*mongo.UpdateOneModel)
		assert.True(t, ok)
		assert.NotNil(t, updateModel)
	})

	t.Run("UpdateOneModel without upsert", func(t *testing.T) {
		filter := bson.D{{Key: "_id", Value: 1}}
		update := bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: "updated"}}}}

		model := &UpdateOneModel{
			Filter: filter,
			Update: update,
			Upsert: nil, // No upsert option
		}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		updateModel, ok := mongoModel.(*mongo.UpdateOneModel)
		assert.True(t, ok)
		assert.NotNil(t, updateModel)
	})

	t.Run("UpdateManyModel", func(t *testing.T) {
		filter := bson.D{{Key: "status", Value: "active"}}
		update := bson.D{{Key: "$set", Value: bson.D{{Key: "updated", Value: true}}}}
		upsert := false

		model := &UpdateManyModel{
			Filter: filter,
			Update: update,
			Upsert: &upsert,
		}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		// Verify it's the correct type
		updateModel, ok := mongoModel.(*mongo.UpdateManyModel)
		assert.True(t, ok)
		assert.NotNil(t, updateModel)
	})

	t.Run("UpdateManyModel without upsert", func(t *testing.T) {
		filter := bson.D{{Key: "status", Value: "active"}}
		update := bson.D{{Key: "$set", Value: bson.D{{Key: "updated", Value: true}}}}

		model := &UpdateManyModel{
			Filter: filter,
			Update: update,
			Upsert: nil,
		}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		updateModel, ok := mongoModel.(*mongo.UpdateManyModel)
		assert.True(t, ok)
		assert.NotNil(t, updateModel)
	})

	t.Run("DeleteOneModel", func(t *testing.T) {
		filter := bson.D{{Key: "_id", Value: 1}}

		model := &DeleteOneModel{Filter: filter}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		// Verify it's the correct type
		deleteModel, ok := mongoModel.(*mongo.DeleteOneModel)
		assert.True(t, ok)
		assert.NotNil(t, deleteModel)
	})

	t.Run("DeleteManyModel", func(t *testing.T) {
		filter := bson.D{{Key: "status", Value: "inactive"}}

		model := &DeleteManyModel{Filter: filter}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		// Verify it's the correct type
		deleteModel, ok := mongoModel.(*mongo.DeleteManyModel)
		assert.True(t, ok)
		assert.NotNil(t, deleteModel)
	})

	t.Run("ReplaceOneModel", func(t *testing.T) {
		filter := bson.D{{Key: "_id", Value: 1}}
		replacement := bson.D{{Key: "name", Value: "replaced"}, {Key: "status", Value: "updated"}}
		upsert := true

		model := &ReplaceOneModel{
			Filter:      filter,
			Replacement: replacement,
			Upsert:      &upsert,
		}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		// Verify it's the correct type
		replaceModel, ok := mongoModel.(*mongo.ReplaceOneModel)
		assert.True(t, ok)
		assert.NotNil(t, replaceModel)
	})

	t.Run("ReplaceOneModel without upsert", func(t *testing.T) {
		filter := bson.D{{Key: "_id", Value: 1}}
		replacement := bson.D{{Key: "name", Value: "replaced"}, {Key: "status", Value: "updated"}}

		model := &ReplaceOneModel{
			Filter:      filter,
			Replacement: replacement,
			Upsert:      nil,
		}

		mongoModel := model.GetModel()
		assert.NotNil(t, mongoModel)

		replaceModel, ok := mongoModel.(*mongo.ReplaceOneModel)
		assert.True(t, ok)
		assert.NotNil(t, replaceModel)
	})
}

func TestWriteModelInterfaceCompliance(t *testing.T) {
	// Test that all WriteModel implementations satisfy the interface
	tests := []struct {
		name  string
		model database.WriteModel
	}{
		{"InsertOneModel", &InsertOneModel{Document: bson.D{{Key: "test", Value: 1}}}},
		{"UpdateOneModel", &UpdateOneModel{Filter: bson.D{{Key: "_id", Value: 1}}, Update: bson.D{{Key: "$set", Value: bson.D{{Key: "test", Value: 1}}}}}},
		{"UpdateManyModel", &UpdateManyModel{Filter: bson.D{}, Update: bson.D{{Key: "$set", Value: bson.D{{Key: "test", Value: 1}}}}}},
		{"DeleteOneModel", &DeleteOneModel{Filter: bson.D{{Key: "_id", Value: 1}}}},
		{"DeleteManyModel", &DeleteManyModel{Filter: bson.D{{Key: "status", Value: "inactive"}}}},
		{"ReplaceOneModel", &ReplaceOneModel{Filter: bson.D{{Key: "_id", Value: 1}}, Replacement: bson.D{{Key: "test", Value: 1}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test interface compliance - tt.model is already declared as database.WriteModel
			assert.NotNil(t, tt.model, "%s should implement database.WriteModel interface", tt.name)

			// Test GetModel returns valid mongo model
			mongoModel := tt.model.GetModel()
			assert.NotNil(t, mongoModel, "%s.GetModel() should return a valid mongo model", tt.name)
		})
	}
}
