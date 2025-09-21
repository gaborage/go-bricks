package mongodb

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/stretchr/testify/assert"
)

// Test constants to avoid duplication
const (
	matchOp = "$match"
	limitOp = "$limit"
	sortOp  = "$sort"
	groupOp = "$group"
)

func TestBuilder_Where(t *testing.T) {
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

func TestBuilder_LogicalOperators(t *testing.T) {
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
			"$not": bson.M{"age": bson.M{"$lt": 18}},
		}

		assert.Equal(t, expected, builder.ToFilter())
	})
}

func TestBuilder_Sorting(t *testing.T) {
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

func TestBuilder_Pagination(t *testing.T) {
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

func TestBuilder_Projection(t *testing.T) {
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
			Exclude("password")

		opts := builder.ToFindOptions()

		expected := bson.M{
			"name":     1,
			"email":    1,
			"password": 0,
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

func TestBuilder_AggregationPipeline(t *testing.T) {
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

func TestBuilder_StaticHelpers(t *testing.T) {
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
			{"$match": bson.M{"status": "active"}},
			{"$sort": bson.D{{Key: "created_at", Value: -1}}},
		}

		builder := Pipeline(stages...)
		pipeline := builder.ToPipeline()

		assert.Equal(t, stages, pipeline)
	})
}

func TestBuilder_Clone(t *testing.T) {
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

func TestBuilder_ComplexQueries(t *testing.T) {
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
		matchStage, ok := pipeline[0]["$match"]
		assert.True(t, ok)
		assert.NotNil(t, matchStage)

		sortStage, ok := pipeline[1]["$sort"]
		assert.True(t, ok)
		assert.NotNil(t, sortStage)
	})

	t.Run("user analytics aggregation", func(t *testing.T) {
		builder := NewBuilder().
			AddMatch(bson.M{"created_at": bson.M{"$gte": "2023-01-01"}}).
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
		assert.Contains(t, pipeline[0], "$match")
		assert.Contains(t, pipeline[1], "$group")
		assert.Contains(t, pipeline[2], "$sort")
		assert.Contains(t, pipeline[3], "$limit")
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
