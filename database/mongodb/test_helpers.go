package mongodb

import (
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

// MockResponseHelpers provides reusable mock responses for MongoDB testing
// These helpers encapsulate common MongoDB wire protocol response patterns
// and ensure consistency across all tests.

// MockSuccessResponse creates a basic success response for commands
func MockSuccessResponse() bson.D {
	return bson.D{{Key: "ok", Value: 1}}
}

// MockInsertResponse creates a mock response for insert operations
func MockInsertResponse(insertedID interface{}) bson.D {
	return bson.D{
		{Key: "ok", Value: 1},
		{Key: "insertedId", Value: insertedID},
	}
}

// MockUpdateResponse creates a mock response for update operations
func MockUpdateResponse(matched, modified int64, upsertedID interface{}) bson.D {
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
func MockEmptyCollectionList(database string) bson.D {
	return mtest.CreateCursorResponse(0, database+".$cmd.listCollections", mtest.FirstBatch)
}

// MockCollectionExists creates a cursor indicating a collection exists
func MockCollectionExists(database, collection string) bson.D {
	return mtest.CreateCursorResponse(1, database+"."+collection,
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

// TestNamespace creates a consistent namespace for test operations
func TestNamespace(collection string) string {
	return "test." + collection
}

// TestDatabase returns the standard test database name
func TestDatabase() string {
	return "test"
}
