package mongodb

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// =============================================================================
// Query Filter Methods Tests
// =============================================================================

func TestNewBuilder(t *testing.T) {
	b := NewBuilder()
	require.NotNil(t, b)
	assert.Empty(t, b.ToFilter())
	assert.False(t, b.HasFilter())
	assert.False(t, b.HasSort())
	assert.False(t, b.HasProjection())
	assert.False(t, b.HasPagination())
}

func TestBuilderWhereConditions(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*Builder) *Builder
		expected bson.M
	}{
		{
			name:     "where_eq_string",
			setup:    func(b *Builder) *Builder { return b.WhereEq("status", "active") },
			expected: bson.M{"status": "active"},
		},
		{
			name:     "where_eq_int",
			setup:    func(b *Builder) *Builder { return b.WhereEq("count", 42) },
			expected: bson.M{"count": 42},
		},
		{
			name:     "where_ne",
			setup:    func(b *Builder) *Builder { return b.WhereNe("status", "deleted") },
			expected: bson.M{"status": bson.M{"$ne": "deleted"}},
		},
		{
			name:     "where_gt",
			setup:    func(b *Builder) *Builder { return b.WhereGt("age", 18) },
			expected: bson.M{"age": bson.M{"$gt": 18}},
		},
		{
			name:     "where_gte",
			setup:    func(b *Builder) *Builder { return b.WhereGte("age", 21) },
			expected: bson.M{"age": bson.M{"$gte": 21}},
		},
		{
			name:     "where_lt",
			setup:    func(b *Builder) *Builder { return b.WhereLt("price", 100) },
			expected: bson.M{"price": bson.M{"$lt": 100}},
		},
		{
			name:     "where_lte",
			setup:    func(b *Builder) *Builder { return b.WhereLte("price", 50) },
			expected: bson.M{"price": bson.M{"$lte": 50}},
		},
		{
			name:     "where_in",
			setup:    func(b *Builder) *Builder { return b.WhereIn("status", "active", "pending") },
			expected: bson.M{"status": bson.M{"$in": []any{"active", "pending"}}},
		},
		{
			name:     "where_nin",
			setup:    func(b *Builder) *Builder { return b.WhereNin("status", "deleted", "archived") },
			expected: bson.M{"status": bson.M{"$nin": []any{"deleted", "archived"}}},
		},
		{
			name:     "where_regex",
			setup:    func(b *Builder) *Builder { return b.WhereRegex("name", "^test", "i") },
			expected: bson.M{"name": bson.M{"$regex": "^test", "$options": "i"}},
		},
		{
			name:     "where_exists_true",
			setup:    func(b *Builder) *Builder { return b.WhereExists("email", true) },
			expected: bson.M{"email": bson.M{"$exists": true}},
		},
		{
			name:     "where_exists_false",
			setup:    func(b *Builder) *Builder { return b.WhereExists("deleted_at", false) },
			expected: bson.M{"deleted_at": bson.M{"$exists": false}},
		},
		{
			name:     "where_type",
			setup:    func(b *Builder) *Builder { return b.WhereType("age", "int") },
			expected: bson.M{"age": bson.M{"$type": "int"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder()
			result := tt.setup(b)
			assert.Equal(t, tt.expected, result.ToFilter())
			assert.True(t, result.HasFilter())
		})
	}
}

func TestBuilderWhereConditionChaining(t *testing.T) {
	b := NewBuilder().
		WhereEq("status", "active").
		WhereGt("age", 18).
		WhereLt("age", 65)

	filter := b.ToFilter()
	assert.Equal(t, "active", filter["status"])
	// age should have merged conditions
	ageCondition, ok := filter["age"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, 18, ageCondition["$gt"])
	assert.Equal(t, 65, ageCondition["$lt"])
}

func TestBuilderAddFieldConditionMerging(t *testing.T) {
	// Test that multiple conditions on same field are merged
	b := NewBuilder().
		WhereGt("price", 10).
		WhereLt("price", 100)

	filter := b.ToFilter()
	priceCondition, ok := filter["price"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, 10, priceCondition["$gt"])
	assert.Equal(t, 100, priceCondition["$lt"])
}

// =============================================================================
// Logical Operator Tests
// =============================================================================

func TestBuilderLogicalOperators(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*Builder) *Builder
		checkKey string
		checkLen int
	}{
		{
			name: "where_and",
			setup: func(b *Builder) *Builder {
				return b.WhereAnd(
					bson.M{"status": "active"},
					bson.M{"verified": true},
				)
			},
			checkKey: "$and",
			checkLen: 2,
		},
		{
			name: "where_or",
			setup: func(b *Builder) *Builder {
				return b.WhereOr(
					bson.M{"status": "active"},
					bson.M{"status": "pending"},
				)
			},
			checkKey: "$or",
			checkLen: 2,
		},
		{
			name: "where_nor",
			setup: func(b *Builder) *Builder {
				return b.WhereNor(
					bson.M{"status": "deleted"},
					bson.M{"status": "archived"},
				)
			},
			checkKey: "$nor",
			checkLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder()
			result := tt.setup(b)
			filter := result.ToFilter()

			conditions, ok := filter[tt.checkKey].([]bson.M)
			require.True(t, ok)
			assert.Len(t, conditions, tt.checkLen)
		})
	}
}

func TestBuilderWhereAndEmpty(t *testing.T) {
	b := NewBuilder().WhereAnd()
	assert.Empty(t, b.ToFilter())
}

func TestBuilderWhereOrEmpty(t *testing.T) {
	b := NewBuilder().WhereOr()
	assert.Empty(t, b.ToFilter())
}

func TestBuilderWhereNorEmpty(t *testing.T) {
	b := NewBuilder().WhereNor()
	assert.Empty(t, b.ToFilter())
}

func TestBuilderWhereNot(t *testing.T) {
	tests := []struct {
		name   string
		filter bson.M
		check  func(t *testing.T, result bson.M)
	}{
		{
			name:   "simple_value_negation",
			filter: bson.M{"status": "active"},
			check: func(t *testing.T, result bson.M) {
				statusCond, ok := result["status"].(bson.M)
				require.True(t, ok)
				assert.Equal(t, "active", statusCond["$ne"])
			},
		},
		{
			name:   "logical_operator_to_nor",
			filter: bson.M{"$or": []bson.M{{"a": 1}, {"b": 2}}},
			check: func(t *testing.T, result bson.M) {
				_, ok := result["$nor"]
				assert.True(t, ok)
			},
		},
		{
			name:   "complex_condition_single_operator",
			filter: bson.M{"age": bson.M{"$gt": 18}},
			check: func(t *testing.T, result bson.M) {
				ageCond, ok := result["age"].(bson.M)
				require.True(t, ok)
				_, hasNot := ageCond["$not"]
				assert.True(t, hasNot)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder().WhereNot(tt.filter)
			tt.check(t, b.ToFilter())
		})
	}
}

func TestBuilderWhereNotMultiOperator(t *testing.T) {
	// Multi-operator conditions get split into $nor
	b := NewBuilder().WhereNot(bson.M{"age": bson.M{"$gt": 18, "$lt": 65}})
	filter := b.ToFilter()

	norConditions, ok := filter["$nor"].([]bson.M)
	require.True(t, ok)
	assert.Len(t, norConditions, 2)
}

// =============================================================================
// Sorting Tests
// =============================================================================

func TestBuilderSorting(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*Builder) *Builder
		expectedField string
		expectedOrder int
	}{
		{
			name:          "order_by_asc",
			setup:         func(b *Builder) *Builder { return b.OrderBy("name") },
			expectedField: "name",
			expectedOrder: 1,
		},
		{
			name:          "order_by_asc_alias",
			setup:         func(b *Builder) *Builder { return b.OrderByAsc("name") },
			expectedField: "name",
			expectedOrder: 1,
		},
		{
			name:          "order_by_desc",
			setup:         func(b *Builder) *Builder { return b.OrderByDesc("created_at") },
			expectedField: "created_at",
			expectedOrder: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder()
			result := tt.setup(b)

			assert.True(t, result.HasSort())
			fields := result.SortFields()
			require.Len(t, fields, 1)
			assert.Equal(t, tt.expectedField, fields[0])
		})
	}
}

func TestBuilderMultipleSorts(t *testing.T) {
	b := NewBuilder().
		OrderByDesc("created_at").
		OrderBy("name")

	assert.True(t, b.HasSort())
	fields := b.SortFields()
	require.Len(t, fields, 2)
	assert.Equal(t, "created_at", fields[0])
	assert.Equal(t, "name", fields[1])
}

// =============================================================================
// Pagination Tests
// =============================================================================

func TestBuilderPagination(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*Builder) *Builder
		expectedSkip  *int64
		expectedLimit *int64
	}{
		{
			name:          "skip_only",
			setup:         func(b *Builder) *Builder { return b.Skip(10) },
			expectedSkip:  ptrInt64(10),
			expectedLimit: nil,
		},
		{
			name:          "limit_only",
			setup:         func(b *Builder) *Builder { return b.Limit(20) },
			expectedSkip:  nil,
			expectedLimit: ptrInt64(20),
		},
		{
			name:          "offset_alias",
			setup:         func(b *Builder) *Builder { return b.Offset(15) },
			expectedSkip:  ptrInt64(15),
			expectedLimit: nil,
		},
		{
			name:          "skip_and_limit",
			setup:         func(b *Builder) *Builder { return b.Skip(10).Limit(20) },
			expectedSkip:  ptrInt64(10),
			expectedLimit: ptrInt64(20),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder()
			result := tt.setup(b)

			assert.True(t, result.HasPagination())

			skipVal := result.SkipValue()
			limitVal := result.LimitValue()

			if tt.expectedSkip != nil {
				require.NotNil(t, skipVal)
				assert.Equal(t, *tt.expectedSkip, *skipVal)
			} else {
				assert.Nil(t, skipVal)
			}

			if tt.expectedLimit != nil {
				require.NotNil(t, limitVal)
				assert.Equal(t, *tt.expectedLimit, *limitVal)
			} else {
				assert.Nil(t, limitVal)
			}
		})
	}
}

func TestBuilderNoPagination(t *testing.T) {
	b := NewBuilder()
	assert.False(t, b.HasPagination())
	assert.Nil(t, b.SkipValue())
	assert.Nil(t, b.LimitValue())
}

// =============================================================================
// Projection Tests
// =============================================================================

func TestBuilderProjection(t *testing.T) {
	tests := []struct {
		name           string
		setup          func(*Builder) *Builder
		expectedFields []string
	}{
		{
			name:           "select_single",
			setup:          func(b *Builder) *Builder { return b.Select("name") },
			expectedFields: []string{"name"},
		},
		{
			name:           "select_multiple",
			setup:          func(b *Builder) *Builder { return b.Select("name", "email", "age") },
			expectedFields: []string{"name", "email", "age"},
		},
		{
			name:           "exclude_single",
			setup:          func(b *Builder) *Builder { return b.Exclude("password") },
			expectedFields: []string{"password"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder()
			result := tt.setup(b)

			assert.True(t, result.HasProjection())
			fields := result.ProjectionFields()
			assert.Len(t, fields, len(tt.expectedFields))
		})
	}
}

func TestBuilderProjectSlice(t *testing.T) {
	b := NewBuilder().ProjectSlice("comments", 5)

	assert.True(t, b.HasProjection())
	state := b.State()
	sliceVal, ok := state.Projection["comments"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, 5, sliceVal["$slice"])
}

func TestBuilderProjectSliceWithSkip(t *testing.T) {
	b := NewBuilder().ProjectSliceWithSkip("comments", 10, 5)

	state := b.State()
	sliceVal, ok := state.Projection["comments"].(bson.M)
	require.True(t, ok)
	sliceArr, ok := sliceVal["$slice"].([]int)
	require.True(t, ok)
	assert.Equal(t, []int{10, 5}, sliceArr)
}

func TestBuilderProjectElemMatch(t *testing.T) {
	condition := bson.M{"score": bson.M{"$gte": 90}}
	b := NewBuilder().ProjectElemMatch("grades", condition)

	state := b.State()
	elemMatch, ok := state.Projection["grades"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, condition, elemMatch["$elemMatch"])
}

// =============================================================================
// Aggregation Pipeline Tests
// =============================================================================

func TestBuilderAggregationStages(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*Builder) *Builder
		stageKey   string
		stageValue any
	}{
		{
			name:       "add_match",
			setup:      func(b *Builder) *Builder { return b.AddMatch(bson.M{"status": "active"}) },
			stageKey:   "$match",
			stageValue: bson.M{"status": "active"},
		},
		{
			name:       "add_limit",
			setup:      func(b *Builder) *Builder { return b.AddLimit(10) },
			stageKey:   "$limit",
			stageValue: int64(10),
		},
		{
			name:       "add_skip",
			setup:      func(b *Builder) *Builder { return b.AddSkip(5) },
			stageKey:   "$skip",
			stageValue: int64(5),
		},
		{
			name:       "add_project",
			setup:      func(b *Builder) *Builder { return b.AddProject(bson.M{"name": 1}) },
			stageKey:   "$project",
			stageValue: bson.M{"name": 1},
		},
		{
			name:       "add_group",
			setup:      func(b *Builder) *Builder { return b.AddGroup(bson.M{"_id": "$status"}) },
			stageKey:   "$group",
			stageValue: bson.M{"_id": "$status"},
		},
		{
			name:       "add_unwind",
			setup:      func(b *Builder) *Builder { return b.AddUnwind("$tags") },
			stageKey:   "$unwind",
			stageValue: "$tags",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewBuilder()
			result := tt.setup(b)
			pipeline := result.ToPipeline()

			require.Len(t, pipeline, 1)
			assert.Equal(t, tt.stageValue, pipeline[0][tt.stageKey])
		})
	}
}

func TestBuilderAddSort(t *testing.T) {
	sortOrder := bson.D{{Key: "created_at", Value: -1}, {Key: "name", Value: 1}}
	b := NewBuilder().AddSort(sortOrder)

	pipeline := b.ToPipeline()
	require.Len(t, pipeline, 1)
	assert.Equal(t, sortOrder, pipeline[0]["$sort"])
}

func TestBuilderAddLookup(t *testing.T) {
	b := NewBuilder().AddLookup("orders", "user_id", "_id", "user_orders")

	pipeline := b.ToPipeline()
	require.Len(t, pipeline, 1)

	lookup, ok := pipeline[0]["$lookup"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, "orders", lookup["from"])
	assert.Equal(t, "user_id", lookup["localField"])
	assert.Equal(t, "_id", lookup["foreignField"])
	assert.Equal(t, "user_orders", lookup["as"])
}

func TestBuilderAddStageCustom(t *testing.T) {
	customStage := bson.M{"$count": "total"}
	b := NewBuilder().AddStage(customStage)

	pipeline := b.ToPipeline()
	require.Len(t, pipeline, 1)
	assert.Equal(t, "total", pipeline[0]["$count"])
}

func TestBuilderToPipelineWithImplicitStages(t *testing.T) {
	b := NewBuilder().
		WhereEq("status", "active").
		OrderByDesc("created_at").
		Skip(10).
		Limit(20).
		Select("name", "email")

	pipeline := b.ToPipeline()

	// Should have: $match, $sort, $skip, $limit, $project
	assert.Len(t, pipeline, 5)
}

func TestBuilderToPipelineEmpty(t *testing.T) {
	b := NewBuilder()
	pipeline := b.ToPipeline()
	assert.Empty(t, pipeline)
}

// =============================================================================
// State & Utility Methods Tests
// =============================================================================

func TestBuilderClone(t *testing.T) {
	original := NewBuilder().
		WhereEq("status", "active").
		OrderBy("name").
		Skip(10).
		Limit(20).
		Select("name")

	clone := original.Clone()

	// Modify original
	original.WhereEq("type", "user")

	// Clone should not be affected
	originalFilter := original.ToFilter()
	cloneFilter := clone.ToFilter()

	assert.Contains(t, originalFilter, "type")
	assert.NotContains(t, cloneFilter, "type")
}

func TestBuilderReset(t *testing.T) {
	b := NewBuilder().
		WhereEq("status", "active").
		OrderBy("name").
		Skip(10).
		Limit(20).
		Select("name")

	b.Reset()

	assert.False(t, b.HasFilter())
	assert.False(t, b.HasSort())
	assert.False(t, b.HasProjection())
	assert.False(t, b.HasPagination())
}

func TestBuilderState(t *testing.T) {
	b := NewBuilder().
		WhereEq("status", "active").
		OrderBy("name").
		Skip(10).
		Limit(20)

	state := b.State()

	assert.Equal(t, bson.M{"status": "active"}, state.Match)
	require.NotNil(t, state.Skip)
	assert.Equal(t, int64(10), *state.Skip)
	require.NotNil(t, state.Limit)
	assert.Equal(t, int64(20), *state.Limit)
}

func TestBuilderString(t *testing.T) {
	b := NewBuilder().
		WhereEq("status", "active").
		Skip(10).
		Limit(20)

	str := b.String()

	assert.Contains(t, str, "MongoBuilder")
	assert.Contains(t, str, "status")
	assert.Contains(t, str, "10")
	assert.Contains(t, str, "20")
}

func TestBuilderToJSON(t *testing.T) {
	b := NewBuilder().WhereEq("status", "active")

	jsonStr := b.ToJSON()

	assert.Contains(t, jsonStr, "status")
	assert.Contains(t, jsonStr, "active")
}

func TestBuilderToFilter(t *testing.T) {
	b := NewBuilder()
	assert.Equal(t, bson.M{}, b.ToFilter())

	b.WhereEq("id", 1)
	assert.Equal(t, bson.M{"id": 1}, b.ToFilter())
}

func TestBuilderToFindOptions(t *testing.T) {
	b := NewBuilder().
		OrderByDesc("created_at").
		Skip(10).
		Limit(20).
		Select("name", "email")

	opts := b.ToFindOptions()
	require.NotNil(t, opts)

	findOpts := opts.Build()
	require.NotNil(t, findOpts)
	require.NotNil(t, findOpts.Skip)
	assert.Equal(t, int64(10), *findOpts.Skip)
	require.NotNil(t, findOpts.Limit)
	assert.Equal(t, int64(20), *findOpts.Limit)
}

func TestBuilderToUpdateDocument(t *testing.T) {
	b := NewBuilder()
	update := bson.M{"$set": bson.M{"status": "inactive"}}

	result := b.ToUpdateDocument(update)
	assert.Equal(t, update, result)
}

func TestBuilderWithOptions(t *testing.T) {
	b := NewBuilder()
	opts := BuilderOptions{PipelineOrder: []string{"match", "sort"}}

	result := b.WithOptions(opts)
	assert.Same(t, b, result) // Should return same builder
}

// =============================================================================
// Static Helper Functions Tests
// =============================================================================

func TestStaticSelect(t *testing.T) {
	b := Select("name", "email")

	assert.True(t, b.HasProjection())
	fields := b.ProjectionFields()
	assert.Len(t, fields, 2)
}

func TestStaticWhere(t *testing.T) {
	b := Where("status", "active")

	assert.True(t, b.HasFilter())
	assert.Equal(t, bson.M{"status": "active"}, b.ToFilter())
}

func TestStaticMatch(t *testing.T) {
	filter := bson.M{"status": "active", "verified": true}
	b := Match(filter)

	assert.True(t, b.HasFilter())
	result := b.ToFilter()
	assert.Equal(t, "active", result["status"])
	assert.Equal(t, true, result["verified"])
}

func TestStaticPipeline(t *testing.T) {
	stages := []bson.M{
		{"$match": bson.M{"status": "active"}},
		{"$limit": 10},
	}
	b := Pipeline(stages...)

	pipeline := b.ToPipeline()
	assert.Len(t, pipeline, 2)
}

// =============================================================================
// FindOptionsBuilder Tests
// =============================================================================

func TestFindOptionsBuilder(t *testing.T) {
	opts := &FindOptionsBuilder{}

	opts.Sort(bson.D{{Key: "name", Value: 1}}).
		Skip(10).
		Limit(20).
		Projection(bson.M{"name": 1})

	findOpts := opts.Build()

	assert.NotNil(t, findOpts.Sort)
	require.NotNil(t, findOpts.Skip)
	assert.Equal(t, int64(10), *findOpts.Skip)
	require.NotNil(t, findOpts.Limit)
	assert.Equal(t, int64(20), *findOpts.Limit)
	assert.NotNil(t, findOpts.Projection)
}

func TestFindOptionsBuilderEmpty(t *testing.T) {
	opts := &FindOptionsBuilder{}
	findOpts := opts.Build()

	assert.Nil(t, findOpts.Sort)
	assert.Nil(t, findOpts.Skip)
	assert.Nil(t, findOpts.Limit)
	assert.Nil(t, findOpts.Projection)
}

// =============================================================================
// Internal Method Edge Cases Tests
// =============================================================================

func TestBuilderConvertToMapWithBsonD(t *testing.T) {
	// Test bson.D conversion path in convertToMap
	b := NewBuilder()
	bsonDCondition := bson.D{{Key: "$gt", Value: 18}}

	// WhereNot with single operator bson.D should use convertToMap
	b.WhereNot(bson.M{"age": bsonDCondition})

	filter := b.ToFilter()
	ageCond, ok := filter["age"].(bson.M)
	require.True(t, ok)
	_, hasNot := ageCond["$not"]
	assert.True(t, hasNot)
}

func TestBuilderAddToNorConditionAppend(t *testing.T) {
	// Test appending to existing $nor array
	b := NewBuilder().
		WhereNor(bson.M{"status": "deleted"}).
		WhereNot(bson.M{"$or": []bson.M{{"a": 1}}})

	filter := b.ToFilter()
	norConditions, ok := filter["$nor"].([]bson.M)
	require.True(t, ok)
	// Should have both the original WhereNor condition and the appended WhereNot
	assert.GreaterOrEqual(t, len(norConditions), 2)
}

func TestBuilderProcessComplexConditionNilMap(t *testing.T) {
	// Test when convertToMap returns nil (non-bson.M/bson.D type)
	// This is an edge case where condition is neither bson.M nor bson.D
	b := NewBuilder()

	// Create a filter with a slice value (not bson.M or bson.D)
	// This triggers the nil path in processComplexCondition
	filter := bson.M{"tags": []string{"a", "b"}}
	b.WhereNot(filter)

	result := b.ToFilter()
	// Should have negated the condition
	tagsCond, ok := result["tags"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, []string{"a", "b"}, tagsCond["$ne"])
}

func TestBuilderSplitMultiOperatorAppendToExisting(t *testing.T) {
	// Test appending multi-operator split to existing $nor
	b := NewBuilder().
		WhereNor(bson.M{"status": "deleted"}).
		WhereNot(bson.M{"price": bson.M{"$gt": 10, "$lt": 100}})

	filter := b.ToFilter()
	norConditions, ok := filter["$nor"].([]bson.M)
	require.True(t, ok)
	// Should have 3 conditions: original + 2 from split
	assert.GreaterOrEqual(t, len(norConditions), 3)
}

func TestBuilderAddFieldConditionNewMap(t *testing.T) {
	// Test the else branch where existing value is not a map
	b := NewBuilder().
		WhereEq("status", "active"). // Sets status to string
		WhereNe("status", "deleted") // Should create new condition map

	filter := b.ToFilter()
	// Since WhereEq sets a raw value, WhereNe should replace with map
	statusCond, ok := filter["status"].(bson.M)
	require.True(t, ok)
	assert.Equal(t, "deleted", statusCond["$ne"])
}

// =============================================================================
// Helper Functions
// =============================================================================

func ptrInt64(v int64) *int64 {
	return &v
}
