package mongodb

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ========================================
// FILTER CONDITION TESTS
// ========================================

func TestBuilderFilterConditions(t *testing.T) {
	tests := []BuilderTestCase{
		{
			Name: "simple equality",
			Setup: func() *Builder {
				return NewBuilder().Where("name", "John")
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"name": "John"},
			},
		},
		{
			Name: "multiple conditions",
			Setup: func() *Builder {
				return NewBuilder().
					Where("name", "John").
					Where("age", 25)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"name": "John", "age": 25},
			},
		},
		{
			Name: "not equal condition",
			Setup: func() *Builder {
				return NewBuilder().WhereNe("status", "inactive")
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"status": bson.M{NeOp: "inactive"}},
			},
		},
		{
			Name: "greater than condition",
			Setup: func() *Builder {
				return NewBuilder().WhereGt("age", 18)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"age": bson.M{GtOp: 18}},
			},
		},
		{
			Name: "greater than or equal condition",
			Setup: func() *Builder {
				return NewBuilder().WhereGte("score", 90)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"score": bson.M{GteOp: 90}},
			},
		},
		{
			Name: "less than condition",
			Setup: func() *Builder {
				return NewBuilder().WhereLt("price", 100)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"price": bson.M{LtOp: 100}},
			},
		},
		{
			Name: "less than or equal condition",
			Setup: func() *Builder {
				return NewBuilder().WhereLte("discount", 50)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"discount": bson.M{LteOp: 50}},
			},
		},
		{
			Name: "in condition",
			Setup: func() *Builder {
				return NewBuilder().WhereIn("category", "electronics", "books", "clothing")
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"category": bson.M{InOp: []interface{}{"electronics", "books", "clothing"}}},
			},
		},
		{
			Name: "not in condition",
			Setup: func() *Builder {
				return NewBuilder().WhereNin("status", "deleted", "archived")
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"status": bson.M{NinOp: []interface{}{"deleted", "archived"}}},
			},
		},
		{
			Name: "regex condition",
			Setup: func() *Builder {
				return NewBuilder().WhereRegex("email", TestRegexEmail, TestRegexOptions)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"email": bson.M{RegexOp: TestRegexEmail, OptionsOp: TestRegexOptions}},
			},
		},
		{
			Name: "exists condition",
			Setup: func() *Builder {
				return NewBuilder().WhereExists("optional_field", true)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"optional_field": bson.M{ExistsOp: true}},
			},
		},
		{
			Name: "type condition",
			Setup: func() *Builder {
				return NewBuilder().WhereType("created_at", "date")
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"created_at": bson.M{TypeOp: "date"}},
			},
		},
		{
			Name: "range condition (gte and lte merged)",
			Setup: func() *Builder {
				return NewBuilder().
					WhereGte("price", 100).
					WhereLte("price", 1000)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"price": bson.M{GteOp: 100, LteOp: 1000}},
			},
		},
		{
			Name: "multiple field ranges",
			Setup: func() *Builder {
				return NewBuilder().
					WhereGt("age", 18).
					WhereLt("age", 65).
					WhereGte("score", 80)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{
					"age":   bson.M{GtOp: 18, LtOp: 65},
					"score": bson.M{GteOp: 80},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertBuilderState(t, builder, &tt.Expected)
		})
	}
}

// ========================================
// LOGICAL OPERATOR TESTS
// ========================================

func TestBuilderLogicalOperators(t *testing.T) {
	tests := []BuilderTestCase{
		{
			Name: "AND condition",
			Setup: func() *Builder {
				return NewBuilder().WhereAnd(
					bson.M{"age": bson.M{GteOp: 18}},
					bson.M{"status": TestStatus},
				)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{
					AndOp: []bson.M{
						{"age": bson.M{GteOp: 18}},
						{"status": TestStatus},
					},
				},
			},
		},
		{
			Name: "OR condition",
			Setup: func() *Builder {
				return NewBuilder().WhereOr(
					bson.M{"category": "electronics"},
					bson.M{"category": "books"},
				)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{
					OrOp: []bson.M{
						{"category": "electronics"},
						{"category": "books"},
					},
				},
			},
		},
		{
			Name: "NOR condition",
			Setup: func() *Builder {
				return NewBuilder().WhereNor(
					bson.M{"status": "deleted"},
					bson.M{"status": "archived"},
				)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{
					NorOp: []bson.M{
						{"status": "deleted"},
						{"status": "archived"},
					},
				},
			},
		},
		{
			Name: "NOT condition",
			Setup: func() *Builder {
				return NewBuilder().WhereNot(bson.M{"age": bson.M{LtOp: 18}})
			},
			Expected: BuilderExpectation{
				Filter: bson.M{
					"age": bson.M{NotOp: bson.M{LtOp: 18}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertBuilderState(t, builder, &tt.Expected)
		})
	}
}

// ========================================
// SORTING TESTS
// ========================================

func TestBuilderSorting(t *testing.T) {
	tests := []BuilderTestCase{
		{
			Name: "single sort ascending",
			Setup: func() *Builder {
				return NewBuilder().OrderBy("name")
			},
			Expected: BuilderExpectation{
				Sort: bson.D{{Key: "name", Value: 1}},
			},
		},
		{
			Name: "single sort descending",
			Setup: func() *Builder {
				return NewBuilder().OrderByDesc("created_at")
			},
			Expected: BuilderExpectation{
				Sort: bson.D{{Key: "created_at", Value: -1}},
			},
		},
		{
			Name: "multiple sort fields",
			Setup: func() *Builder {
				return NewBuilder().
					OrderBy("category").
					OrderByDesc("price").
					OrderBy("name")
			},
			Expected: BuilderExpectation{
				Sort: bson.D{
					{Key: "category", Value: 1},
					{Key: "price", Value: -1},
					{Key: "name", Value: 1},
				},
			},
		},
		{
			Name: "order by alias (OrderByAsc)",
			Setup: func() *Builder {
				return NewBuilder().OrderByAsc("title")
			},
			Expected: BuilderExpectation{
				Sort: bson.D{{Key: "title", Value: 1}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertSort(t, builder, tt.Expected.Sort)
		})
	}
}

// ========================================
// PAGINATION TESTS
// ========================================

func TestBuilderPagination(t *testing.T) {
	tests := []BuilderTestCase{
		{
			Name: "skip and limit",
			Setup: func() *Builder {
				return NewBuilder().Skip(10).Limit(5)
			},
			Expected: BuilderExpectation{
				Skip:  Int64Ptr(10),
				Limit: Int64Ptr(5),
			},
		},
		{
			Name: "offset alias for skip",
			Setup: func() *Builder {
				return NewBuilder().Offset(20).Limit(10)
			},
			Expected: BuilderExpectation{
				Skip:  Int64Ptr(20),
				Limit: Int64Ptr(10),
			},
		},
		{
			Name: "skip without limit",
			Setup: func() *Builder {
				return NewBuilder().Skip(15)
			},
			Expected: BuilderExpectation{
				Skip: Int64Ptr(15),
			},
		},
		{
			Name: "limit without skip",
			Setup: func() *Builder {
				return NewBuilder().Limit(25)
			},
			Expected: BuilderExpectation{
				Limit: Int64Ptr(25),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertPagination(t, builder, tt.Expected.Skip, tt.Expected.Limit)
		})
	}
}

// ========================================
// PROJECTION TESTS
// ========================================

func TestBuilderProjection(t *testing.T) {
	tests := []BuilderTestCase{
		{
			Name: "select fields",
			Setup: func() *Builder {
				return NewBuilder().Select("name", "email", "age")
			},
			Expected: BuilderExpectation{
				Projection: bson.M{
					"name":  1,
					"email": 1,
					"age":   1,
				},
			},
		},
		{
			Name: "exclude fields",
			Setup: func() *Builder {
				return NewBuilder().Exclude("password", "internal_notes")
			},
			Expected: BuilderExpectation{
				Projection: bson.M{
					"password":       0,
					"internal_notes": 0,
				},
			},
		},
		{
			Name: "mixed select and exclude",
			Setup: func() *Builder {
				return NewBuilder().
					Select("name", "email").
					Exclude("_id")
			},
			Expected: BuilderExpectation{
				Projection: bson.M{
					"name":  1,
					"email": 1,
					"_id":   0,
				},
			},
		},
		{
			Name: "array slice projection",
			Setup: func() *Builder {
				return NewBuilder().ProjectSlice("comments", 5)
			},
			Expected: BuilderExpectation{
				Projection: bson.M{
					"comments": bson.M{SliceOp: 5},
				},
			},
		},
		{
			Name: "array slice with skip",
			Setup: func() *Builder {
				return NewBuilder().ProjectSliceWithSkip("items", 10, 5)
			},
			Expected: BuilderExpectation{
				Projection: bson.M{
					"items": bson.M{SliceOp: []int{10, 5}},
				},
			},
		},
		{
			Name: "elem match projection",
			Setup: func() *Builder {
				condition := bson.M{"score": bson.M{GteOp: 80}}
				return NewBuilder().ProjectElemMatch("grades", condition)
			},
			Expected: BuilderExpectation{
				Projection: bson.M{
					"grades": bson.M{ElemMatch: bson.M{"score": bson.M{GteOp: 80}}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertProjection(t, builder, tt.Expected.Projection)
		})
	}
}

// ========================================
// AGGREGATION PIPELINE TESTS
// ========================================

func TestBuilderAggregationPipeline(t *testing.T) {
	const priceField = "$price"
	tests := []BuilderTestCase{
		{
			Name: "custom stages",
			Setup: func() *Builder {
				return NewBuilder().
					AddMatch(bson.M{"status": TestStatus}).
					AddSort(bson.D{{Key: "created_at", Value: -1}}).
					AddLimit(10)
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{MatchOp: bson.M{"status": TestStatus}},
					{SortOp: bson.D{{Key: "created_at", Value: -1}}},
					{LimitOp: int64(10)},
				},
			},
		},
		{
			Name: "implicit stages from builder state",
			Setup: func() *Builder {
				return NewBuilder().
					Where("category", TestCategory).
					OrderBy("price").
					Skip(10).
					Limit(5).
					Select("name", "price")
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{MatchOp: bson.M{"category": TestCategory}},
					{SortOp: bson.D{{Key: "price", Value: 1}}},
					{SkipOp: int64(10)},
					{LimitOp: int64(5)},
					{ProjectOp: bson.M{"name": 1, "price": 1}},
				},
			},
		},
		{
			Name: "lookup stage",
			Setup: func() *Builder {
				return NewBuilder().AddLookup("categories", "category_id", "_id", "category_info")
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{
						LookupOp: bson.M{
							"from":         "categories",
							"localField":   "category_id",
							"foreignField": "_id",
							"as":           "category_info",
						},
					},
				},
			},
		},
		{
			Name: "group stage",
			Setup: func() *Builder {
				groupStage := bson.M{
					"_id":   "$category",
					"total": bson.M{"$sum": priceField},
					"count": bson.M{"$sum": 1},
				}
				return NewBuilder().AddGroup(groupStage)
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{GroupOp: bson.M{
						"_id":   "$category",
						"total": bson.M{"$sum": priceField},
						"count": bson.M{"$sum": 1},
					}},
				},
			},
		},
		{
			Name: "unwind stage",
			Setup: func() *Builder {
				return NewBuilder().AddUnwind("$tags")
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{UnwindOp: "$tags"},
				},
			},
		},
		{
			Name: "project stage",
			Setup: func() *Builder {
				projection := bson.M{"name": 1, "category": 1, "price_rounded": bson.M{"$round": priceField}}
				return NewBuilder().AddProject(projection)
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{ProjectOp: bson.M{"name": 1, "category": 1, "price_rounded": bson.M{"$round": priceField}}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertPipeline(t, builder, tt.Expected.Pipeline)
		})
	}
}

// ========================================
// STATIC HELPER TESTS
// ========================================

func TestBuilderStaticHelpers(t *testing.T) {
	tests := []BuilderTestCase{
		{
			Name: "Select helper",
			Setup: func() *Builder {
				return Select("name", "email")
			},
			Expected: BuilderExpectation{
				Projection: bson.M{"name": 1, "email": 1},
			},
		},
		{
			Name: "Where helper",
			Setup: func() *Builder {
				return Where("status", TestStatus)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"status": TestStatus},
			},
		},
		{
			Name: "Match helper",
			Setup: func() *Builder {
				filter := bson.M{"age": bson.M{GteOp: 18}, "status": TestStatus}
				return Match(filter)
			},
			Expected: BuilderExpectation{
				Filter: bson.M{"age": bson.M{GteOp: 18}, "status": TestStatus},
			},
		},
		{
			Name: "Pipeline helper",
			Setup: func() *Builder {
				stages := []bson.M{
					{MatchOp: bson.M{"status": TestStatus}},
					{SortOp: bson.D{{Key: "created_at", Value: -1}}},
				}
				return Pipeline(stages...)
			},
			Expected: BuilderExpectation{
				Pipeline: []bson.M{
					{MatchOp: bson.M{"status": TestStatus}},
					{SortOp: bson.D{{Key: "created_at", Value: -1}}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			builder := tt.Setup()
			AssertBuilderState(t, builder, &tt.Expected)
		})
	}
}

// ========================================
// BUILDER UTILITY TESTS
// ========================================

func TestBuilderUtilityMethods(t *testing.T) {
	t.Run("clone preserves state", func(t *testing.T) {
		original := BuilderWithTestData()
		clone := original.Clone()

		// Verify clone has same state
		AssertFilter(t, clone, original.ToFilter())
		AssertPipeline(t, clone, original.ToPipeline())

		// Verify independence
		clone.Where("category", "books")
		assert.NotEqual(t, original.ToFilter(), clone.ToFilter())
	})

	t.Run("reset clears all state", func(t *testing.T) {
		builder := BuilderWithTestData()
		assert.True(t, builder.HasFilter())

		builder.Reset()
		assert.False(t, builder.HasFilter())
		assert.False(t, builder.HasSort())
		assert.False(t, builder.HasProjection())
		assert.False(t, builder.HasPagination())
	})

	t.Run("state inspection methods", func(t *testing.T) {
		builder := NewBuilder().
			Where("status", TestStatus).
			OrderBy("name").
			Skip(10).
			Limit(5).
			Select("name", "email")

		assert.True(t, builder.HasFilter())
		assert.True(t, builder.HasSort())
		assert.True(t, builder.HasProjection())
		assert.True(t, builder.HasPagination())

		assert.Equal(t, int64(10), *builder.GetSkip())
		assert.Equal(t, int64(5), *builder.GetLimit())

		projectionFields := builder.GetProjectionFields()
		assert.ElementsMatch(t, []string{"name", "email"}, projectionFields)

		sortFields := builder.GetSortFields()
		assert.Equal(t, []string{"name"}, sortFields)
	})

	t.Run("get state returns complete state", func(t *testing.T) {
		builder := BuilderWithTestData().
			OrderBy("name").
			Skip(10).
			Limit(5).
			Select("title")

		state := builder.GetState()

		assert.NotEmpty(t, state.Match)
		assert.NotEmpty(t, state.Sort)
		assert.NotNil(t, state.Skip)
		assert.NotNil(t, state.Limit)
		assert.NotEmpty(t, state.Projection)
	})
}

// ========================================
// FINDOPTIONS BUILDER TESTS
// ========================================

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

// ========================================
// COMPLEX REAL-WORLD SCENARIO TESTS
// ========================================

func TestBuilderRealWorldScenarios(t *testing.T) {
	t.Run("e-commerce product search", func(t *testing.T) {
		builder := ECommerceSearchBuilder()

		filter := builder.ToFilter()
		pipeline := builder.ToPipeline()
		options := builder.ToFindOptions().Build()

		// Verify search criteria
		assert.Contains(t, filter, "category")
		assert.Contains(t, filter, AndOp)
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

		// Verify pipeline structure
		AssertPipelineContains(t, pipeline, MatchOp, SortOp, SkipOp, LimitOp, ProjectOp)
	})

	t.Run("user analytics aggregation", func(t *testing.T) {
		builder := UserAnalyticsBuilder()
		pipeline := builder.ToPipeline()

		// Verify pipeline structure
		assert.Len(t, pipeline, 4)
		AssertPipelineContains(t, pipeline, MatchOp, GroupOp, SortOp, LimitOp)

		// Verify match stage
		matchStage := pipeline[0][MatchOp].(bson.M)
		assert.Contains(t, matchStage, "created_at")
		assert.Contains(t, matchStage, "status")

		// Verify group stage
		groupStage := pipeline[1][GroupOp].(bson.M)
		assert.Contains(t, groupStage, "_id")
		assert.Contains(t, groupStage, "user_count")
		assert.Contains(t, groupStage, "avg_age")
	})

	t.Run("content management complex query", func(t *testing.T) {
		builder := NewBuilder().
			WhereOr(
				bson.M{"status": "published"},
				bson.M{
					"status":    "draft",
					"author_id": "current_user_id",
				},
			).
			WhereAnd(
				bson.M{"published_at": bson.M{LteOp: "now"}},
				bson.M{"archived": bson.M{NeOp: true}},
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
		assert.Contains(t, filter, OrOp)
		orConditions := filter[OrOp].([]bson.M)
		assert.Len(t, orConditions, 2)

		// Verify AND conditions
		assert.Contains(t, filter, AndOp)

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

// ========================================
// INTEGRATION TESTS WITH MONGODB
// ========================================

func TestBuilderIntegrationWithMongoDB(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().ClientType(mtest.Mock))

	mt.Run("find with builder", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("products")

		builder := ComplexQueryBuilder()

		// Mock response using test fixtures
		productDocs := []bson.D{
			MockProductFromTestData(TestProducts[0]),
			MockProductFromTestData(TestProducts[1]),
		}
		mt.AddMockResponses(MockFindResponse(CreateTestNamespace("products"), productDocs...))

		// Execute query
		filter := builder.ToFilter()
		options := builder.ToFindOptions().Build()

		cursor, err := collection.Find(context.Background(), filter, options)
		require.NoError(t, err)
		assert.NotNil(t, cursor)
		defer cursor.Close(context.Background())

		// Verify filter structure
		assert.Contains(t, filter, "category")
		assert.Contains(t, filter, AndOp)
		assert.Contains(t, filter, "featured_image")

		// Verify options
		assert.Equal(t, int64(10), *options.Skip)
		assert.Equal(t, int64(20), *options.Limit)
		assert.NotNil(t, options.Sort)
		assert.NotNil(t, options.Projection)
	})

	mt.Run("aggregation with builder", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("orders")

		builder := UserAnalyticsBuilder()

		// Mock aggregation response
		aggregateResults := []bson.D{
			MockOrderFromTestData(TestOrders[0]),
			MockOrderFromTestData(TestOrders[1]),
		}
		mt.AddMockResponses(MockFindResponse(CreateTestNamespace("orders"), aggregateResults...))

		// Execute aggregation
		pipeline := builder.ToPipeline()
		cursor, err := collection.Aggregate(context.Background(), pipeline, nil)
		require.NoError(t, err)
		assert.NotNil(t, cursor)
		defer cursor.Close(context.Background())

		// Verify pipeline structure
		assert.Len(t, pipeline, 4)
		AssertPipelineContains(t, pipeline, MatchOp, GroupOp, SortOp, LimitOp)

		// Verify match stage
		matchStage := pipeline[0][MatchOp].(bson.M)
		assert.Equal(t, TestDate, matchStage["created_at"].(bson.M)[GteOp])
	})

	mt.Run("update with builder filter", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("products")

		builder := NewBuilder().
			Where("category", TestCategory).
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
		assert.Equal(t, TestCategory, filter["category"])
		stockFilter := filter["stock"].(bson.M)
		assert.Equal(t, 10, stockFilter[LtOp])
	})

	mt.Run("count with builder filter", func(mt *mtest.T) {
		conn := setupTestConnection(t, mt)
		collection := conn.Collection("orders")

		builder := NewBuilder().
			Where("status", "pending").
			WhereGte("created_at", TestDate)

		// Mock count response
		mt.AddMockResponses(MockCountResponse(CreateTestNamespace("orders"), 42))

		// Execute count
		filter := builder.ToFilter()
		count, err := collection.CountDocuments(context.Background(), filter, nil)
		require.NoError(t, err)
		assert.Equal(t, int64(42), count)

		// Verify filter
		assert.Equal(t, "pending", filter["status"])
		dateFilter := filter["created_at"].(bson.M)
		assert.Equal(t, TestDate, dateFilter[GteOp])
	})
}

// ========================================
// EDGE CASE AND ERROR HANDLING TESTS
// ========================================

func TestBuilderEdgeCases(t *testing.T) {
	t.Run("empty builder produces empty filter", func(t *testing.T) {
		builder := NewBuilder()
		filter := builder.ToFilter()
		assert.Empty(t, filter)
	})

	t.Run("empty builder produces empty pipeline", func(t *testing.T) {
		builder := NewBuilder()
		pipeline := builder.ToPipeline()
		assert.Empty(t, pipeline)
	})

	t.Run("nil values handled gracefully", func(t *testing.T) {
		builder := NewBuilder()

		// These should not panic
		builder.Where("field", nil)
		builder.WhereIn("array_field") // No values
		builder.Select()               // No fields
		builder.Exclude()              // No fields

		filter := builder.ToFilter()
		assert.Contains(t, filter, "field")
		assert.Nil(t, filter["field"])
	})

	t.Run("zero values handled correctly", func(t *testing.T) {
		builder := NewBuilder().
			Skip(0).
			Limit(0).
			WhereGt("count", 0)

		assert.True(t, builder.HasPagination())
		assert.Equal(t, int64(0), *builder.GetSkip())
		assert.Equal(t, int64(0), *builder.GetLimit())
	})

	t.Run("string representation", func(t *testing.T) {
		builder := NewBuilder().
			Where("status", TestStatus).
			Skip(10).
			Limit(5)

		str := builder.String()
		assert.Contains(t, str, "MongoBuilder")
		assert.Contains(t, str, TestStatus)
	})

	t.Run("JSON representation", func(t *testing.T) {
		builder := NewBuilder().Where("test", "value")
		json := builder.ToJSON()
		assert.NotEmpty(t, json)
		assert.Contains(t, json, "test")
		assert.Contains(t, json, "value")
	})
}

// ========================================
// CONCURRENCY TESTS
// ========================================

func TestBuilderConcurrency(t *testing.T) {
	t.Run("concurrent builder usage", func(t *testing.T) {
		const numGoroutines = 10
		const numOperations = 100

		builders := make([]*Builder, numGoroutines)
		for i := 0; i < numGoroutines; i++ {
			builders[i] = NewBuilder()
		}

		// Start concurrent operations
		done := make(chan bool, numGoroutines)
		for i := 0; i < numGoroutines; i++ {
			go func(builderIndex int) {
				defer func() { done <- true }()
				builder := builders[builderIndex]

				for j := 0; j < numOperations; j++ {
					builder.
						Where("field"+string(rune(j)), j).
						OrderBy("sort" + string(rune(j))).
						Skip(int64(j)).
						Limit(int64(j + 1))
				}
			}(i)
		}

		// Wait for all goroutines to complete
		for i := 0; i < numGoroutines; i++ {
			<-done
		}

		// Verify each builder has the expected state
		for i, builder := range builders {
			assert.True(t, builder.HasFilter(), "Builder %d should have filters", i)
			assert.True(t, builder.HasSort(), "Builder %d should have sort", i)
			assert.True(t, builder.HasPagination(), "Builder %d should have pagination", i)
		}
	})
}

// ========================================
// COMPREHENSIVE COVERAGE TESTS
// ========================================

func TestBuilderComprehensiveCoverage(t *testing.T) {
	t.Run("WhereEq alias for Where", func(t *testing.T) {
		builder := NewBuilder().WhereEq("status", TestStatus)
		filter := builder.ToFilter()
		assert.Equal(t, TestStatus, filter["status"])
	})

	t.Run("ToUpdateDocument method", func(t *testing.T) {
		builder := NewBuilder()
		update := bson.M{"$set": bson.M{"status": TestStatus}}
		result := builder.ToUpdateDocument(update)
		assert.Equal(t, update, result)
	})

	t.Run("WithOptions method", func(t *testing.T) {
		builder := NewBuilder()
		opts := BuilderOptions{
			PipelineOrder: []string{"match", "sort", "limit"},
		}
		result := builder.WithOptions(opts)
		assert.Equal(t, builder, result) // Should return same builder
	})

	t.Run("AddStage with custom stage", func(t *testing.T) {
		customStage := bson.M{"$facet": bson.M{"count": []bson.M{{"$count": "total"}}}}
		builder := NewBuilder().AddStage(customStage)
		pipeline := builder.ToPipeline()
		assert.Len(t, pipeline, 1)
		assert.Equal(t, customStage, pipeline[0])
	})

	t.Run("AddSkip aggregation stage", func(t *testing.T) {
		builder := NewBuilder().AddSkip(25)
		pipeline := builder.ToPipeline()
		expected := []bson.M{{SkipOp: int64(25)}}
		assert.Equal(t, expected, pipeline)
	})

	t.Run("complex logical operators combination", func(t *testing.T) {
		builder := NewBuilder().
			WhereAnd(
				bson.M{"type": "premium"},
				bson.M{"active": true},
			).
			WhereOr(
				bson.M{"region": "US"},
				bson.M{"region": "EU"},
			).
			WhereNor(
				bson.M{"banned": true},
				bson.M{"suspended": true},
			)

		filter := builder.ToFilter()
		assert.Contains(t, filter, AndOp)
		assert.Contains(t, filter, OrOp)
		assert.Contains(t, filter, NorOp)
	})

	t.Run("addFieldCondition edge cases", func(t *testing.T) {
		builder := NewBuilder()

		// Test existing simple value gets converted to condition map
		builder.Where("price", 100)
		builder.addFieldCondition("price", GtOp, 50)

		filter := builder.ToFilter()
		priceCondition := filter["price"].(bson.M)
		assert.Equal(t, bson.M{GtOp: 50}, priceCondition)
	})

	t.Run("WhereNot with operator fields", func(t *testing.T) {
		// Test NOT with operator keys (should use $nor)
		builder := NewBuilder().WhereNot(bson.M{
			OrOp: []bson.M{
				{"status": "deleted"},
				{"status": "archived"},
			},
		})

		filter := builder.ToFilter()
		assert.Contains(t, filter, NorOp)
		norConditions := filter[NorOp].([]bson.M)
		assert.Len(t, norConditions, 1)
		assert.Contains(t, norConditions[0], OrOp)
	})

	t.Run("WhereNot with field equality", func(t *testing.T) {
		// Test NOT with simple field (should use $ne)
		builder := NewBuilder().WhereNot(bson.M{"status": "active"})
		filter := builder.ToFilter()
		assert.Equal(t, bson.M{"status": bson.M{NeOp: "active"}}, filter)
	})

	t.Run("WhereNot with existing $nor", func(t *testing.T) {
		// Test appending to existing $nor
		builder := NewBuilder().
			WhereNot(bson.M{OrOp: []bson.M{{"a": 1}}}).
			WhereNot(bson.M{AndOp: []bson.M{{"b": 2}}})

		filter := builder.ToFilter()
		norConditions := filter[NorOp].([]bson.M)
		assert.Len(t, norConditions, 2)
	})

	t.Run("WhereNot with multi-operator field conditions", func(t *testing.T) {
		// Test NOT with multi-operator field conditions (should use $nor with split predicates)
		builder := NewBuilder().WhereNot(bson.M{
			"price": bson.M{
				"$gt": 10,
				"$lt": 100,
			},
		})

		filter := builder.ToFilter()
		assert.Contains(t, filter, NorOp)
		norConditions := filter[NorOp].([]bson.M)
		assert.Len(t, norConditions, 2)

		// Verify the split predicates
		expectedPredicates := []bson.M{
			{"price": bson.M{"$gt": 10}},
			{"price": bson.M{"$lt": 100}},
		}
		assert.ElementsMatch(t, expectedPredicates, norConditions)
	})

	t.Run("WhereNot with single-operator field conditions", func(t *testing.T) {
		// Test NOT with single-operator field conditions (should use $not)
		builder := NewBuilder().WhereNot(bson.M{
			"status": bson.M{"$in": []string{"active", "pending"}},
		})

		filter := builder.ToFilter()
		assert.Equal(t, bson.M{
			"status": bson.M{
				"$not": bson.M{"$in": []string{"active", "pending"}},
			},
		}, filter)
	})

	t.Run("WhereNot with mixed multi and single operator conditions", func(t *testing.T) {
		// Test NOT with both multi-operator and single-operator fields
		builder := NewBuilder().WhereNot(bson.M{
			"price": bson.M{
				"$gt":  10,
				"$lte": 100,
			},
			"status": bson.M{"$ne": "deleted"},
			"name":   "test",
		})

		filter := builder.ToFilter()
		assert.Contains(t, filter, NorOp)
		assert.Contains(t, filter, "status")
		assert.Contains(t, filter, "name")

		// Multi-operator field should be split into $nor predicates
		norConditions := filter[NorOp].([]bson.M)
		assert.Len(t, norConditions, 2)
		expectedPredicates := []bson.M{
			{"price": bson.M{"$gt": 10}},
			{"price": bson.M{"$lte": 100}},
		}
		assert.ElementsMatch(t, expectedPredicates, norConditions)

		// Single-operator field should use $not
		assert.Equal(t, bson.M{"$not": bson.M{"$ne": "deleted"}}, filter["status"])
		// Simple equality should use $ne
		assert.Equal(t, bson.M{"$ne": "test"}, filter["name"])
	})

	t.Run("empty logical operators", func(t *testing.T) {
		builder := NewBuilder()

		// These should not add anything to the filter
		builder.WhereAnd() // empty
		builder.WhereOr()  // empty
		builder.WhereNor() // empty

		filter := builder.ToFilter()
		assert.Empty(t, filter)
	})

	t.Run("pipeline order with mixed stages", func(t *testing.T) {
		builder := NewBuilder()

		// Add custom stages first
		builder.AddMatch(bson.M{"type": "user"})
		builder.AddGroup(bson.M{"_id": "$department", "count": bson.M{"$sum": 1}})

		// Add builder state
		builder.Where("active", true)
		builder.OrderBy("created_at")
		builder.Skip(5)
		builder.Limit(10)
		builder.Select("name", "email")

		pipeline := builder.ToPipeline()

		// Verify order: custom stages first, then implicit stages
		assert.Len(t, pipeline, 7)
		assert.Contains(t, pipeline[0], MatchOp)   // custom match
		assert.Contains(t, pipeline[1], GroupOp)   // custom group
		assert.Contains(t, pipeline[2], MatchOp)   // implicit match
		assert.Contains(t, pipeline[3], SortOp)    // implicit sort
		assert.Contains(t, pipeline[4], SkipOp)    // implicit skip
		assert.Contains(t, pipeline[5], LimitOp)   // implicit limit
		assert.Contains(t, pipeline[6], ProjectOp) // implicit project
	})

	t.Run("builder state methods edge cases", func(t *testing.T) {
		builder := NewBuilder()

		// Test empty state
		assert.False(t, builder.HasFilter())
		assert.False(t, builder.HasSort())
		assert.False(t, builder.HasProjection())
		assert.False(t, builder.HasPagination())
		assert.Nil(t, builder.GetSkip())
		assert.Nil(t, builder.GetLimit())
		assert.Empty(t, builder.GetProjectionFields())
		assert.Empty(t, builder.GetSortFields())

		// Test with data
		builder.Where("test", "value")
		builder.OrderBy("field1")
		builder.OrderByDesc("field2")
		builder.Select("a", "b", "c")
		builder.Skip(10)
		builder.Limit(5)

		assert.True(t, builder.HasFilter())
		assert.True(t, builder.HasSort())
		assert.True(t, builder.HasProjection())
		assert.True(t, builder.HasPagination())

		assert.Equal(t, int64(10), *builder.GetSkip())
		assert.Equal(t, int64(5), *builder.GetLimit())

		projectionFields := builder.GetProjectionFields()
		assert.ElementsMatch(t, []string{"a", "b", "c"}, projectionFields)

		sortFields := builder.GetSortFields()
		assert.ElementsMatch(t, []string{"field1", "field2"}, sortFields)
	})

	t.Run("clone deep copy verification", func(t *testing.T) {
		original := NewBuilder().
			Where("field1", "value1").
			WhereGt("field2", 100).
			OrderBy("sort1").
			Skip(10).
			Limit(5).
			Select("proj1", "proj2").
			AddMatch(bson.M{"custom": "stage"})

		clone := original.Clone()

		// Modify clone and verify original is unaffected
		clone.Where("field1", "modified")
		clone.OrderBy("newsort")
		clone.Skip(20)
		clone.Select("newproj")

		originalState := original.GetState()
		cloneState := clone.GetState()

		// Verify original state is preserved
		assert.Equal(t, "value1", originalState.Match["field1"])
		assert.Equal(t, int64(10), *originalState.Skip)
		assert.Len(t, originalState.Sort, 1)
		assert.Equal(t, "sort1", originalState.Sort[0].Key)

		// Verify clone state is different
		assert.Equal(t, "modified", cloneState.Match["field1"])
		assert.Equal(t, int64(20), *cloneState.Skip)
		assert.Len(t, cloneState.Sort, 2) // original + new
	})

	t.Run("FindOptionsBuilder edge cases", func(t *testing.T) {
		builder := &FindOptionsBuilder{}

		// Test with nil values
		opts := builder.Build()
		assert.Nil(t, opts.Sort)
		assert.Nil(t, opts.Skip)
		assert.Nil(t, opts.Limit)
		assert.Nil(t, opts.Projection)

		// Test method chaining
		result := builder.
			Sort(bson.D{{Key: "test", Value: 1}}).
			Skip(5).
			Limit(10).
			Projection(bson.M{"field": 1})

		assert.Equal(t, builder, result) // Should return same instance
	})
}

// ========================================
// ERROR CONDITIONS AND VALIDATION TESTS
// ========================================

func TestBuilderErrorConditionsAndValidation(t *testing.T) {
	t.Run("JSON serialization with complex types", func(t *testing.T) {
		builder := NewBuilder().
			Where("complex", bson.M{"nested": bson.M{"deep": "value"}}).
			AddMatch(bson.M{"aggregation": true})

		json := builder.ToJSON()
		assert.NotEmpty(t, json)
		assert.Contains(t, json, "complex")
		assert.Contains(t, json, "aggregation")
	})

	t.Run("string representation with all fields", func(t *testing.T) {
		builder := NewBuilder().
			Where("field", "value").
			OrderBy("sort").
			Skip(10).
			Limit(5)

		str := builder.String()
		assert.Contains(t, str, "MongoBuilder")
		assert.Contains(t, str, "field")
		assert.Contains(t, str, "value")
		assert.Contains(t, str, "10") // skip
		assert.Contains(t, str, "5")  // limit
	})

	t.Run("large pipeline generation", func(t *testing.T) {
		builder := NewBuilder()

		// Add many stages to test performance and correctness
		for i := 0; i < 50; i++ {
			builder.AddMatch(bson.M{"iteration": i})
		}

		builder.Where("final", "filter")
		builder.OrderBy("timestamp")
		builder.Skip(100)
		builder.Limit(20)

		pipeline := builder.ToPipeline()
		assert.Len(t, pipeline, 54) // 50 custom + 4 implicit

		// Verify first and last stages
		assert.Contains(t, pipeline[0], MatchOp)
		assert.Equal(t, 0, pipeline[0][MatchOp].(bson.M)["iteration"])

		assert.Contains(t, pipeline[49], MatchOp)
		assert.Equal(t, 49, pipeline[49][MatchOp].(bson.M)["iteration"])

		// Verify implicit stages at the end
		assert.Contains(t, pipeline[50], MatchOp) // final filter
		assert.Contains(t, pipeline[51], SortOp)  // sort
		assert.Contains(t, pipeline[52], SkipOp)  // skip
		assert.Contains(t, pipeline[53], LimitOp) // limit
	})

	t.Run("builder reuse after reset", func(t *testing.T) {
		builder := NewBuilder()

		// Use builder multiple times with reset
		for i := 0; i < 3; i++ {
			builder.Reset()
			builder.Where("iteration", i)
			builder.OrderBy("field")
			builder.Skip(int64(i * 10))

			state := builder.GetState()
			assert.Equal(t, i, state.Match["iteration"])
			assert.Equal(t, int64(i*10), *state.Skip)
			assert.Len(t, state.Sort, 1)
		}
	})

	t.Run("mixed projection operations", func(t *testing.T) {
		builder := NewBuilder().
			Select("include1", "include2").
			Exclude("exclude1", "exclude2").
			ProjectSlice("array1", 5).
			ProjectSliceWithSkip("array2", 2, 3).
			ProjectElemMatch("array3", bson.M{"score": bson.M{GtOp: 80}})

		projection := builder.ToFindOptions().projection
		projectionMap := projection.(bson.M)
		assert.Equal(t, 1, projectionMap["include1"])
		assert.Equal(t, 1, projectionMap["include2"])
		assert.Equal(t, 0, projectionMap["exclude1"])
		assert.Equal(t, 0, projectionMap["exclude2"])
		assert.Equal(t, bson.M{SliceOp: 5}, projectionMap["array1"])
		assert.Equal(t, bson.M{SliceOp: []int{2, 3}}, projectionMap["array2"])
		assert.Equal(t, bson.M{ElemMatch: bson.M{"score": bson.M{GtOp: 80}}}, projectionMap["array3"])
	})
}
