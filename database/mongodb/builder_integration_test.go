package mongodb

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants to avoid duplication
const (
	matchOperator = "$match"
	groupOperator = "$group"
)

func TestBuilder_IntegrationWithAdapter(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("find with builder", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("products")

		// Create a query using the builder
		builder := NewBuilder().
			Where("category", "electronics").
			WhereGte("price", 100).
			WhereLte("price", 1000).
			OrderByDesc("rating").
			OrderBy("price").
			Skip(10).
			Limit(5).
			Select("name", "price", "rating")

		// Mock the response
		productDocs := []bson.D{
			MockProduct("1", "Laptop", 999.99),
			MockProduct("2", "Tablet", 299.99),
		}
		mt.AddMockResponses(MockFindResponse(TestNamespace("products"), productDocs...))

		// Execute the query
		filter := builder.ToFilter()
		options := builder.ToFindOptions().Build()

		cursor, err := collection.Find(context.Background(), filter, options)
		require.NoError(t, err)
		assert.NotNil(t, cursor)
		defer cursor.Close(context.Background())

		// Check that the filter contains the expected conditions
		assert.Equal(t, "electronics", filter["category"])
		assert.IsType(t, bson.M{}, filter["price"])

		priceFilter := filter["price"].(bson.M)
		assert.Equal(t, 100, priceFilter["$gte"])
		assert.Equal(t, 1000, priceFilter["$lte"])

		// Verify options
		assert.Equal(t, int64(10), *options.Skip)
		assert.Equal(t, int64(5), *options.Limit)
		assert.NotNil(t, options.Sort)
		assert.NotNil(t, options.Projection)
	})

	mt.Run("aggregation with builder", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("orders")

		// Create an aggregation pipeline using the builder
		builder := NewBuilder().
			AddMatch(bson.M{"status": "completed"}).
			AddGroup(bson.M{
				"_id":          "$customer_id",
				"total_amount": bson.M{"$sum": "$amount"},
				"order_count":  bson.M{"$sum": 1},
			}).
			AddSort(bson.D{{Key: "total_amount", Value: -1}}).
			AddLimit(10)

		// Mock the aggregation response
		aggregateResults := []bson.D{
			{
				{Key: "_id", Value: "customer1"},
				{Key: "total_amount", Value: 1500.0},
				{Key: "order_count", Value: 3},
			},
			{
				{Key: "_id", Value: "customer2"},
				{Key: "total_amount", Value: 800.0},
				{Key: "order_count", Value: 2},
			},
		}
		mt.AddMockResponses(MockFindResponse(TestNamespace("orders"), aggregateResults...))

		// Execute the aggregation
		pipeline := builder.ToPipeline()
		cursor, err := collection.Aggregate(context.Background(), pipeline, nil)
		require.NoError(t, err)
		assert.NotNil(t, cursor)
		defer cursor.Close(context.Background())

		// Verify the pipeline structure
		assert.Len(t, pipeline, 4)
		assert.Contains(t, pipeline[0], matchOperator)
		assert.Contains(t, pipeline[1], groupOperator)
		assert.Contains(t, pipeline[2], "$sort")
		assert.Contains(t, pipeline[3], "$limit")

		// Verify match stage
		matchStage := pipeline[0][matchOperator].(bson.M)
		assert.Equal(t, "completed", matchStage["status"])

		// Verify group stage
		groupStage := pipeline[1][groupOperator].(bson.M)
		assert.Equal(t, "$customer_id", groupStage["_id"])
		assert.Contains(t, groupStage, "total_amount")
		assert.Contains(t, groupStage, "order_count")
	})

	mt.Run("complex query with logical operators", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("users")

		// Build a complex query with AND/OR conditions
		builder := NewBuilder().
			WhereOr(
				bson.M{"role": "admin"},
				bson.M{"role": "moderator"},
			).
			WhereAnd(
				bson.M{"status": "active"},
				bson.M{"email_verified": true},
			).
			WhereRegex("email", ".*@company\\.com$", "i").
			OrderBy("last_login").
			Limit(20)

		// Mock response
		userDocs := []bson.D{
			MockUser("1", "admin"),
			MockUser("2", "moderator"),
		}
		mt.AddMockResponses(MockFindResponse(TestNamespace("users"), userDocs...))

		// Execute query
		filter := builder.ToFilter()
		options := builder.ToFindOptions().Build()

		cursor, err := collection.Find(context.Background(), filter, options)
		require.NoError(t, err)
		assert.NotNil(t, cursor)
		defer cursor.Close(context.Background())

		// Verify complex filter structure
		assert.Contains(t, filter, "$or")
		assert.Contains(t, filter, "$and")
		assert.Contains(t, filter, "email")

		// Verify OR condition
		orConditions := filter["$or"].([]bson.M)
		assert.Len(t, orConditions, 2)
		assert.Equal(t, "admin", orConditions[0]["role"])
		assert.Equal(t, "moderator", orConditions[1]["role"])

		// Verify AND condition
		andConditions := filter["$and"].([]bson.M)
		assert.Len(t, andConditions, 2)

		// Verify email regex
		emailFilter := filter["email"].(bson.M)
		assert.Equal(t, ".*@company\\.com$", emailFilter["$regex"])
		assert.Equal(t, "i", emailFilter["$options"])
	})

	mt.Run("update with builder filter", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("products")

		// Create a filter using the builder
		builder := NewBuilder().
			Where("category", "electronics").
			WhereLt("stock", 10)

		// Mock update response
		mt.AddMockResponses(MockUpdateResponse(5, 5, nil))

		// Execute update
		filter := builder.ToFilter()
		update := bson.M{"$set": bson.M{"needs_restock": true}}

		result, err := collection.UpdateMany(context.Background(), filter, update, nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, int64(5), result.MatchedCount())
		assert.Equal(t, int64(5), result.ModifiedCount())

		// Verify filter
		assert.Equal(t, "electronics", filter["category"])
		stockFilter := filter["stock"].(bson.M)
		assert.Equal(t, 10, stockFilter["$lt"])
	})

	mt.Run("count with builder filter", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("orders")

		// Create a filter for counting
		builder := NewBuilder().
			Where("status", "pending").
			WhereGte("created_at", "2023-01-01")

		// Mock count response
		mt.AddMockResponses(MockCountResponse(TestNamespace("orders"), 42))

		// Execute count
		filter := builder.ToFilter()
		count, err := collection.CountDocuments(context.Background(), filter, nil)
		require.NoError(t, err)
		assert.Equal(t, int64(42), count)

		// Verify filter
		assert.Equal(t, "pending", filter["status"])
		dateFilter := filter["created_at"].(bson.M)
		assert.Equal(t, "2023-01-01", dateFilter["$gte"])
	})
}

func TestBuilder_RealWorldExamples(t *testing.T) {
	t.Run("e-commerce product search", func(t *testing.T) {
		// Simulate a typical e-commerce product search
		builder := NewBuilder().
			WhereIn("category", "electronics", "computers", "phones").
			WhereAnd(
				bson.M{"price": bson.M{"$gte": 50}},
				bson.M{"price": bson.M{"$lte": 2000}},
			).
			Where("in_stock", true).
			WhereRegex("name", "samsung|apple|sony", "i").
			OrderByDesc("rating").
			OrderBy("price").
			Skip(0).
			Limit(24).
			Select("name", "price", "rating", "image_url", "category")

		filter := builder.ToFilter()
		options := builder.ToFindOptions().Build()

		// Verify the search criteria
		assert.Contains(t, filter, "category")
		assert.Contains(t, filter, "$and")
		assert.Equal(t, true, filter["in_stock"])

		// Verify pagination
		assert.Equal(t, int64(0), *options.Skip)
		assert.Equal(t, int64(24), *options.Limit)

		// Verify sorting (rating desc, then price asc)
		sort := options.Sort.(bson.D)
		assert.Equal(t, "rating", sort[0].Key)
		assert.Equal(t, -1, sort[0].Value)
		assert.Equal(t, "price", sort[1].Key)
		assert.Equal(t, 1, sort[1].Value)
	})

	t.Run("user analytics pipeline", func(t *testing.T) {
		// Simulate user analytics aggregation
		builder := NewBuilder().
			AddMatch(bson.M{
				"created_at": bson.M{"$gte": "2023-01-01"},
				"status":     bson.M{"$ne": "deleted"},
			}).
			AddGroup(bson.M{
				"_id": bson.M{
					"department": "$department",
					"role":       "$role",
				},
				"user_count":   bson.M{"$sum": 1},
				"avg_age":      bson.M{"$avg": "$age"},
				"active_users": bson.M{"$sum": bson.M{"$cond": []interface{}{bson.M{"$eq": []interface{}{"$status", "active"}}, 1, 0}}},
				"total_logins": bson.M{"$sum": "$login_count"},
			}).
			AddSort(bson.D{{Key: "user_count", Value: -1}}).
			AddLimit(20).
			AddProject(bson.M{
				"department":          "$_id.department",
				"role":                "$_id.role",
				"user_count":          1,
				"avg_age":             bson.M{"$round": []interface{}{"$avg_age", 1}},
				"active_percentage":   bson.M{"$multiply": []interface{}{bson.M{"$divide": []interface{}{"$active_users", "$user_count"}}, 100}},
				"avg_logins_per_user": bson.M{"$divide": []interface{}{"$total_logins", "$user_count"}},
				"_id":                 0,
			})

		pipeline := builder.ToPipeline()

		// Verify pipeline structure
		assert.Len(t, pipeline, 5) // match, group, sort, limit, project

		// Verify match stage
		matchStage := pipeline[0]["$match"].(bson.M)
		assert.Contains(t, matchStage, "created_at")
		assert.Contains(t, matchStage, "status")

		// Verify group stage
		groupStage := pipeline[1][groupOperator].(bson.M)
		assert.Contains(t, groupStage, "_id")
		assert.Contains(t, groupStage, "user_count")
		assert.Contains(t, groupStage, "avg_age")

		// Verify project stage (final stage)
		projectStage := pipeline[4]["$project"].(bson.M)
		assert.Contains(t, projectStage, "department")
		assert.Contains(t, projectStage, "active_percentage")
	})

	t.Run("content management query", func(t *testing.T) {
		// Simulate CMS article search with complex criteria
		builder := NewBuilder().
			WhereOr(
				bson.M{"status": "published"},
				bson.M{
					"status":    "draft",
					"author_id": "current_user_id",
				},
			).
			WhereAnd(
				bson.M{"published_at": bson.M{"$lte": "now"}},
				bson.M{"archived": bson.M{"$ne": true}},
			).
			WhereIn("category", "technology", "science", "business").
			WhereExists("featured_image", true).
			OrderByDesc("published_at").
			Skip(30).
			Limit(15).
			Exclude("content", "raw_html", "internal_notes")

		filter := builder.ToFilter()
		options := builder.ToFindOptions().Build()

		// Verify complex OR condition
		assert.Contains(t, filter, "$or")
		orConditions := filter["$or"].([]bson.M)
		assert.Len(t, orConditions, 2)

		// Verify AND conditions
		assert.Contains(t, filter, "$and")

		// Verify field exclusions
		projection := options.Projection.(bson.M)
		assert.Equal(t, 0, projection["content"])
		assert.Equal(t, 0, projection["raw_html"])
		assert.Equal(t, 0, projection["internal_notes"])

		// Verify pagination for page 3 (skip 30, limit 15)
		assert.Equal(t, int64(30), *options.Skip)
		assert.Equal(t, int64(15), *options.Limit)
	})
}
