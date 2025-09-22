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
	matchOp       = "$match"
	limitOp       = "$limit"
	sortOp        = "$sort"
	groupOp       = "$group"
	referenceDate = "2023-01-01"
)

func TestBuilderWhere(t *testing.T) {
	tests := []struct {
		name     string
		build    func() *Builder
		expected bson.M
	}{
		{
			name: "simple equality",
			build: func() *Builder {
				return NewBuilder().Where("name", "John")
			},
			expected: bson.M{"name": "John"},
		},
		{
			name: "multiple conditions",
			build: func() *Builder {
				return NewBuilder().
					Where("name", "John").
					Where("age", 25)
			},
			expected: bson.M{"name": "John", "age": 25},
		},
		{
			name: "not equal",
			build: func() *Builder {
				return NewBuilder().WhereNe("status", "inactive")
			},
			expected: bson.M{"status": bson.M{"$ne": "inactive"}},
		},
		{
			name: "greater than",
			build: func() *Builder {
				return NewBuilder().WhereGt("age", 18)
			},
			expected: bson.M{"age": bson.M{"$gt": 18}},
		},
		{
			name: "greater than or equal",
			build: func() *Builder {
				return NewBuilder().WhereGte("score", 90)
			},
			expected: bson.M{"score": bson.M{"$gte": 90}},
		},
		{
			name: "less than",
			build: func() *Builder {
				return NewBuilder().WhereLt("price", 100)
			},
			expected: bson.M{"price": bson.M{"$lt": 100}},
		},
		{
			name: "less than or equal",
			build: func() *Builder {
				return NewBuilder().WhereLte("discount", 50)
			},
			expected: bson.M{"discount": bson.M{"$lte": 50}},
		},
		{
			name: "in condition",
			build: func() *Builder {
				return NewBuilder().WhereIn("category", "electronics", "books", "clothing")
			},
			expected: bson.M{"category": bson.M{"$in": []interface{}{"electronics", "books", "clothing"}}},
		},
		{
			name: "not in condition",
			build: func() *Builder {
				return NewBuilder().WhereNin("status", "deleted", "archived")
			},
			expected: bson.M{"status": bson.M{"$nin": []interface{}{"deleted", "archived"}}},
		},
		{
			name: "regex condition",
			build: func() *Builder {
				return NewBuilder().WhereRegex("email", ".*@example\\.com$", "i")
			},
			expected: bson.M{"email": bson.M{"$regex": ".*@example\\.com$", "$options": "i"}},
		},
		{
			name: "exists condition",
			build: func() *Builder {
				return NewBuilder().WhereExists("optional_field", true)
			},
			expected: bson.M{"optional_field": bson.M{"$exists": true}},
		},
		{
			name: "type condition",
			build: func() *Builder {
				return NewBuilder().WhereType("created_at", "date")
			},
			expected: bson.M{"created_at": bson.M{"$type": "date"}},
		},
		{
			name: "range condition (gte and lte)",
			build: func() *Builder {
				return NewBuilder().
					WhereGte("price", 100).
					WhereLte("price", 1000)
			},
			expected: bson.M{"price": bson.M{"$gte": 100, "$lte": 1000}},
		},
		{
			name: "multiple range conditions",
			build: func() *Builder {
				return NewBuilder().
					WhereGt("age", 18).
					WhereLt("age", 65).
					WhereGte("score", 80)
			},
			expected: bson.M{
				"age":   bson.M{"$gt": 18, "$lt": 65},
				"score": bson.M{"$gte": 80},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := tt.build()
			result := builder.ToFilter()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestBuilderLogicalOperators(t *testing.T) {
	t.Run("AND condition", func(t *testing.T) {
		builder := NewBuilder().WhereAnd(
			bson.M{"age": bson.M{"$gte": 18}},
			bson.M{"status": "active"},
		)

		expected := bson.M{
			"$and": []bson.M{
				{"age": bson.M{"$gte": 18}},
				{"status": "active"},
			},
		}

		assert.Equal(t, expected, builder.ToFilter())
	})

	t.Run("OR condition", func(t *testing.T) {
		builder := NewBuilder().WhereOr(
			bson.M{"category": "electronics"},
			bson.M{"category": "books"},
		)

		expected := bson.M{
			"$or": []bson.M{
				{"category": "electronics"},
				{"category": "books"},
			},
		}

		assert.Equal(t, expected, builder.ToFilter())
	})

	t.Run("NOR condition", func(t *testing.T) {
		builder := NewBuilder().WhereNor(
			bson.M{"status": "deleted"},
			bson.M{"status": "archived"},
		)

		expected := bson.M{
			"$nor": []bson.M{
				{"status": "deleted"},
				{"status": "archived"},
			},
		}

		assert.Equal(t, expected, builder.ToFilter())
	})

	t.Run("NOT condition", func(t *testing.T) {
		builder := NewBuilder().WhereNot(bson.M{"age": bson.M{"$lt": 18}})

		expected := bson.M{
			"age": bson.M{"$not": bson.M{"$lt": 18}},
		}

		assert.Equal(t, expected, builder.ToFilter())
	})
}

func TestBuilderSorting(t *testing.T) {
	t.Run("single sort ascending", func(t *testing.T) {
		builder := NewBuilder().OrderBy("name")
		opts := builder.ToFindOptions()

		expected := bson.D{{Key: "name", Value: 1}}
		assert.Equal(t, expected, opts.sort)
	})

	t.Run("single sort descending", func(t *testing.T) {
		builder := NewBuilder().OrderByDesc("created_at")
		opts := builder.ToFindOptions()

		expected := bson.D{{Key: "created_at", Value: -1}}
		assert.Equal(t, expected, opts.sort)
	})

	t.Run("multiple sort fields", func(t *testing.T) {
		builder := NewBuilder().
			OrderBy("category").
			OrderByDesc("price").
			OrderBy("name")

		opts := builder.ToFindOptions()

		expected := bson.D{
			{Key: "category", Value: 1},
			{Key: "price", Value: -1},
			{Key: "name", Value: 1},
		}

		assert.Equal(t, expected, opts.sort)
	})
}

func TestBuilderPagination(t *testing.T) {
	t.Run("skip and limit", func(t *testing.T) {
		builder := NewBuilder().Skip(10).Limit(5)
		opts := builder.ToFindOptions()

		assert.Equal(t, int64(10), *opts.skip)
		assert.Equal(t, int64(5), *opts.limit)
	})

	t.Run("offset alias", func(t *testing.T) {
		builder := NewBuilder().Offset(20).Limit(10)
		opts := builder.ToFindOptions()

		assert.Equal(t, int64(20), *opts.skip)
		assert.Equal(t, int64(10), *opts.limit)
	})
}

func TestBuilderProjection(t *testing.T) {
	t.Run("select fields", func(t *testing.T) {
		builder := NewBuilder().Select("name", "email", "age")
		opts := builder.ToFindOptions()

		expected := bson.M{
			"name":  1,
			"email": 1,
			"age":   1,
		}

		assert.Equal(t, expected, opts.projection)
	})

	t.Run("exclude fields", func(t *testing.T) {
		builder := NewBuilder().Exclude("password", "internal_notes")
		opts := builder.ToFindOptions()

		expected := bson.M{
			"password":       0,
			"internal_notes": 0,
		}

		assert.Equal(t, expected, opts.projection)
	})

	t.Run("mixed select and exclude", func(t *testing.T) {
		builder := NewBuilder().
			Select("name", "email").
			Exclude("_id")

		opts := builder.ToFindOptions()

		expected := bson.M{
			"name":  1,
			"email": 1,
			"_id":   0,
		}

		assert.Equal(t, expected, opts.projection)
	})

	t.Run("array slice projection", func(t *testing.T) {
		builder := NewBuilder().ProjectSlice("comments", 5)
		opts := builder.ToFindOptions()

		expected := bson.M{
			"comments": bson.M{"$slice": 5},
		}

		assert.Equal(t, expected, opts.projection)
	})

	t.Run("array slice with skip", func(t *testing.T) {
		builder := NewBuilder().ProjectSliceWithSkip("items", 10, 5)
		opts := builder.ToFindOptions()

		expected := bson.M{
			"items": bson.M{"$slice": []int{10, 5}},
		}

		assert.Equal(t, expected, opts.projection)
	})

	t.Run("elem match projection", func(t *testing.T) {
		condition := bson.M{"score": bson.M{"$gte": 80}}
		builder := NewBuilder().ProjectElemMatch("grades", condition)
		opts := builder.ToFindOptions()

		expected := bson.M{
			"grades": bson.M{"$elemMatch": condition},
		}

		assert.Equal(t, expected, opts.projection)
	})
}

func TestBuilderAggregationPipeline(t *testing.T) {
	t.Run("custom stages", func(t *testing.T) {
		builder := NewBuilder().
			AddMatch(bson.M{"status": "active"}).
			AddSort(bson.D{{Key: "created_at", Value: -1}}).
			AddLimit(10)

		pipeline := builder.ToPipeline()

		expected := []bson.M{
			{matchOp: bson.M{"status": "active"}},
			{sortOp: bson.D{{Key: "created_at", Value: -1}}},
			{limitOp: int64(10)},
		}

		assert.Equal(t, expected, pipeline)
	})

	t.Run("implicit stages from builder state", func(t *testing.T) {
		builder := NewBuilder().
			Where("category", "electronics").
			OrderBy("price").
			Skip(10).
			Limit(5).
			Select("name", "price")

		pipeline := builder.ToPipeline()

		expected := []bson.M{
			{matchOp: bson.M{"category": "electronics"}},
			{sortOp: bson.D{{Key: "price", Value: 1}}},
			{"$skip": int64(10)},
			{limitOp: int64(5)},
			{"$project": bson.M{"name": 1, "price": 1}},
		}

		assert.Equal(t, expected, pipeline)
	})

	t.Run("lookup stage", func(t *testing.T) {
		builder := NewBuilder().AddLookup("categories", "category_id", "_id", "category_info")

		pipeline := builder.ToPipeline()

		expected := []bson.M{
			{
				"$lookup": bson.M{
					"from":         "categories",
					"localField":   "category_id",
					"foreignField": "_id",
					"as":           "category_info",
				},
			},
		}

		assert.Equal(t, expected, pipeline)
	})

	t.Run("group stage", func(t *testing.T) {
		groupStage := bson.M{
			"_id":   "$category",
			"total": bson.M{"$sum": "$price"},
			"count": bson.M{"$sum": 1},
		}

		builder := NewBuilder().AddGroup(groupStage)

		pipeline := builder.ToPipeline()

		expected := []bson.M{
			{groupOp: groupStage},
		}

		assert.Equal(t, expected, pipeline)
	})

	t.Run("unwind stage", func(t *testing.T) {
		builder := NewBuilder().AddUnwind("$tags")

		pipeline := builder.ToPipeline()

		expected := []bson.M{
			{"$unwind": "$tags"},
		}

		assert.Equal(t, expected, pipeline)
	})
}

func TestBuilderStaticHelpers(t *testing.T) {
	t.Run("Select helper", func(t *testing.T) {
		builder := Select("name", "email")
		opts := builder.ToFindOptions()

		expected := bson.M{"name": 1, "email": 1}
		assert.Equal(t, expected, opts.projection)
	})

	t.Run("Where helper", func(t *testing.T) {
		builder := Where("status", "active")
		filter := builder.ToFilter()

		expected := bson.M{"status": "active"}
		assert.Equal(t, expected, filter)
	})

	t.Run("Match helper", func(t *testing.T) {
		filter := bson.M{"age": bson.M{"$gte": 18}, "status": "active"}
		builder := Match(filter)

		result := builder.ToFilter()
		assert.Equal(t, filter, result)
	})

	t.Run("Pipeline helper", func(t *testing.T) {
		stages := []bson.M{
			{matchOp: bson.M{"status": "active"}},
			{"$sort": bson.D{{Key: "created_at", Value: -1}}},
		}

		builder := Pipeline(stages...)
		pipeline := builder.ToPipeline()

		assert.Equal(t, stages, pipeline)
	})
}

func TestBuilderClone(t *testing.T) {
	t.Run("clone preserves state", func(t *testing.T) {
		original := NewBuilder().
			Where("status", "active").
			OrderBy("name").
			Skip(10).
			Limit(5).
			Select("name", "email")

		clone := original.Clone()

		// Verify clone has same state
		assert.Equal(t, original.ToFilter(), clone.ToFilter())
		assert.Equal(t, original.ToPipeline(), clone.ToPipeline())

		// Verify independence - modifying clone doesn't affect original
		clone.Where("category", "electronics")

		assert.NotEqual(t, original.ToFilter(), clone.ToFilter())
	})
}

func TestBuilderComplexQueries(t *testing.T) {
	t.Run("e-commerce product search", func(t *testing.T) {
		builder := NewBuilder().
			WhereAnd(
				bson.M{"category": bson.M{"$in": []interface{}{"electronics", "computers"}}},
				bson.M{"price": bson.M{"$gte": 100, "$lte": 1000}},
				bson.M{"stock": bson.M{"$gt": 0}},
			).
			WhereRegex("name", "laptop|computer", "i").
			OrderByDesc("rating").
			OrderBy("price").
			Skip(20).
			Limit(10).
			Select("name", "price", "rating", "image_url")

		filter := builder.ToFilter()
		pipeline := builder.ToPipeline()

		// Verify filter structure
		assert.Contains(t, filter, "$and")
		assert.Contains(t, filter, "name")

		// Verify pipeline has all expected stages
		assert.Len(t, pipeline, 5) // match, sort, skip, limit, project

		// Verify pipeline order
		matchStage, ok := pipeline[0][matchOp]
		assert.True(t, ok)
		assert.NotNil(t, matchStage)

		sortStage, ok := pipeline[1]["$sort"]
		assert.True(t, ok)
		assert.NotNil(t, sortStage)
	})

	t.Run("user analytics aggregation", func(t *testing.T) {
		builder := NewBuilder().
			AddMatch(bson.M{"created_at": bson.M{"$gte": referenceDate}}).
			AddGroup(bson.M{
				"_id":          "$department",
				"total_users":  bson.M{"$sum": 1},
				"avg_age":      bson.M{"$avg": "$age"},
				"active_users": bson.M{"$sum": bson.M{"$cond": []interface{}{bson.M{"$eq": []interface{}{"$status", "active"}}, 1, 0}}},
			}).
			AddSort(bson.D{{Key: "total_users", Value: -1}}).
			AddLimit(5)

		pipeline := builder.ToPipeline()

		assert.Len(t, pipeline, 4)
		assert.Contains(t, pipeline[0], matchOp)
		assert.Contains(t, pipeline[1], "$group")
		assert.Contains(t, pipeline[2], "$sort")
		assert.Contains(t, pipeline[3], limitOp)
	})
}

func TestFindOptionsBuilder(t *testing.T) {
	t.Run("build complete options", func(t *testing.T) {
		sort := bson.D{{Key: "name", Value: 1}}
		projection := bson.M{"name": 1, "email": 1}

		builder := &FindOptionsBuilder{}
		opts := builder.
			Sort(sort).
			Skip(10).
			Limit(5).
			Projection(projection).
			Build()

		assert.Equal(t, sort, opts.Sort)
		assert.Equal(t, int64(10), *opts.Skip)
		assert.Equal(t, int64(5), *opts.Limit)
		assert.Equal(t, projection, opts.Projection)
	})
}

// Test constants to avoid duplication

func TestBuilderIntegrationWithAdapter(t *testing.T) {
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
		assert.Contains(t, pipeline[0], matchOp)
		assert.Contains(t, pipeline[1], groupOp)
		assert.Contains(t, pipeline[2], "$sort")
		assert.Contains(t, pipeline[3], limitOp)

		// Verify match stage
		matchStage := pipeline[0][matchOp].(bson.M)
		assert.Equal(t, "completed", matchStage["status"])

		// Verify group stage
		groupStage := pipeline[1][groupOp].(bson.M)
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
			WhereGte("created_at", referenceDate)

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
		assert.Equal(t, referenceDate, dateFilter["$gte"])
	})
}

func TestBuilderRealWorldExamples(t *testing.T) {
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
				"created_at": bson.M{"$gte": referenceDate},
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
		groupStage := pipeline[1][groupOp].(bson.M)
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
