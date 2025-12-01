package mongodb

import (
	"fmt"
	"maps"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/gaborage/go-bricks/internal/database"
)

// Builder provides a fluent interface for building MongoDB queries and aggregation pipelines
// This follows the Squirrel pattern but for MongoDB's document-oriented operations.
type Builder struct {
	match      bson.M
	sort       bson.D
	skip       *int64
	limit      *int64
	projection bson.M
	pipeline   []bson.M
}

// NewBuilder creates a new MongoDB query builder
func NewBuilder() *Builder {
	return &Builder{
		match:      make(bson.M),
		sort:       make(bson.D, 0),
		projection: make(bson.M),
		pipeline:   make([]bson.M, 0),
	}
}

// Match methods for building filter conditions

// WhereEq adds an equality condition
func (b *Builder) WhereEq(field string, value any) *Builder {
	b.match[field] = value
	return b
}

// WhereNe adds a "not equal" condition
func (b *Builder) WhereNe(field string, value any) *Builder {
	b.addFieldCondition(field, "$ne", value)
	return b
}

// WhereGt adds a "greater than" condition
func (b *Builder) WhereGt(field string, value any) *Builder {
	b.addFieldCondition(field, "$gt", value)
	return b
}

// addFieldCondition adds a condition to a field, merging with existing conditions if needed
func (b *Builder) addFieldCondition(field, operator string, value any) {
	if existing, exists := b.match[field]; exists {
		// If there's already a condition for this field, merge them
		if existingMap, ok := existing.(bson.M); ok {
			existingMap[operator] = value
		} else {
			// If existing value is not a map, create a new condition map
			b.match[field] = bson.M{operator: value}
		}
	} else {
		// No existing condition, create new one
		b.match[field] = bson.M{operator: value}
	}
}

// WhereGte adds a "greater than or equal" condition
func (b *Builder) WhereGte(field string, value any) *Builder {
	b.addFieldCondition(field, "$gte", value)
	return b
}

// WhereLt adds a "less than" condition
func (b *Builder) WhereLt(field string, value any) *Builder {
	b.addFieldCondition(field, "$lt", value)
	return b
}

// WhereLte adds a "less than or equal" condition
func (b *Builder) WhereLte(field string, value any) *Builder {
	b.addFieldCondition(field, "$lte", value)
	return b
}

// WhereIn adds an "in" condition
func (b *Builder) WhereIn(field string, values ...any) *Builder {
	b.match[field] = bson.M{"$in": values}
	return b
}

// WhereNin adds a "not in" condition
func (b *Builder) WhereNin(field string, values ...any) *Builder {
	b.match[field] = bson.M{"$nin": values}
	return b
}

// WhereRegex adds a regex pattern condition
func (b *Builder) WhereRegex(field, pattern, options string) *Builder {
	b.match[field] = bson.M{"$regex": pattern, "$options": options}
	return b
}

// WhereExists adds an existence check condition
func (b *Builder) WhereExists(field string, exists bool) *Builder {
	b.addFieldCondition(field, "$exists", exists)
	return b
}

// WhereType adds a type check condition
func (b *Builder) WhereType(field string, bsonType any) *Builder {
	b.addFieldCondition(field, "$type", bsonType)
	return b
}

// Logical operators

// WhereAnd adds an AND condition with multiple filters
func (b *Builder) WhereAnd(filters ...bson.M) *Builder {
	if len(filters) > 0 {
		b.match["$and"] = filters
	}
	return b
}

// WhereOr adds an OR condition with multiple filters
func (b *Builder) WhereOr(filters ...bson.M) *Builder {
	if len(filters) > 0 {
		b.match["$or"] = filters
	}
	return b
}

// WhereNor adds a NOR condition with multiple filters
func (b *Builder) WhereNor(filters ...bson.M) *Builder {
	if len(filters) > 0 {
		b.match["$nor"] = filters
	}
	return b
}

// WhereNot adds a NOT condition
func (b *Builder) WhereNot(filter bson.M) *Builder {
	if b.hasLogicalOperators(filter) {
		b.addToNorCondition(filter)
		return b
	}

	b.processFieldConditions(filter)
	return b
}

// hasLogicalOperators checks if filter contains MongoDB logical operators
func (b *Builder) hasLogicalOperators(filter bson.M) bool {
	for k := range filter {
		if k != "" && k[0] == '$' {
			return true
		}
	}
	return false
}

// addToNorCondition adds a filter to the $nor array
func (b *Builder) addToNorCondition(filter bson.M) {
	if existing, ok := b.match["$nor"].([]bson.M); ok {
		b.match["$nor"] = append(existing, filter)
	} else {
		b.match["$nor"] = []bson.M{filter}
	}
}

// processFieldConditions handles field-wise negation for complex conditions
func (b *Builder) processFieldConditions(filter bson.M) {
	for field, condition := range filter {
		switch cond := condition.(type) {
		case bson.M, bson.D:
			b.processComplexCondition(field, cond)
		default:
			b.addFieldCondition(field, "$ne", condition)
		}
	}
}

// processComplexCondition handles bson.M and bson.D conditions
func (b *Builder) processComplexCondition(field string, cond any) {
	condMap := b.convertToMap(cond)
	if condMap == nil {
		b.addFieldCondition(field, "$not", cond)
		return
	}

	if len(condMap) > 1 {
		b.splitMultiOperatorCondition(field, condMap)
	} else {
		b.addFieldCondition(field, "$not", cond)
	}
}

// convertToMap converts bson.D or bson.M to bson.M
func (b *Builder) convertToMap(cond any) bson.M {
	switch typedCond := cond.(type) {
	case bson.M:
		return typedCond
	case bson.D:
		condMap := bson.M{}
		for _, elem := range typedCond {
			condMap[elem.Key] = elem.Value
		}
		return condMap
	default:
		return nil
	}
}

// splitMultiOperatorCondition splits multi-operator conditions into $nor predicates
func (b *Builder) splitMultiOperatorCondition(field string, condMap bson.M) {
	predicates := make([]bson.M, 0, len(condMap))
	for op, val := range condMap {
		predicates = append(predicates, bson.M{field: bson.M{op: val}})
	}

	if existing, ok := b.match["$nor"].([]bson.M); ok {
		b.match["$nor"] = append(existing, predicates...)
	} else {
		b.match["$nor"] = predicates
	}
}

// Sorting methods

// OrderBy adds a sort field in ascending order
func (b *Builder) OrderBy(field string) *Builder {
	b.sort = append(b.sort, bson.E{Key: field, Value: 1})
	return b
}

// OrderByDesc adds a sort field in descending order
func (b *Builder) OrderByDesc(field string) *Builder {
	b.sort = append(b.sort, bson.E{Key: field, Value: -1})
	return b
}

// OrderByAsc adds a sort field in ascending order (alias for OrderBy)
func (b *Builder) OrderByAsc(field string) *Builder {
	return b.OrderBy(field)
}

// Pagination methods

// Skip sets the number of documents to skip
func (b *Builder) Skip(count int64) *Builder {
	b.skip = &count
	return b
}

// Limit sets the maximum number of documents to return
func (b *Builder) Limit(count int64) *Builder {
	b.limit = &count
	return b
}

// Offset is an alias for Skip (SQL-like naming)
func (b *Builder) Offset(count int64) *Builder {
	return b.Skip(count)
}

// Projection methods

// Select includes specific fields in the result
func (b *Builder) Select(fields ...string) *Builder {
	for _, field := range fields {
		b.projection[field] = 1
	}
	return b
}

// Exclude excludes specific fields from the result
func (b *Builder) Exclude(fields ...string) *Builder {
	for _, field := range fields {
		b.projection[field] = 0
	}
	return b
}

// ProjectSlice projects an array slice
func (b *Builder) ProjectSlice(field string, limit int) *Builder {
	b.projection[field] = bson.M{"$slice": limit}
	return b
}

// ProjectSliceWithSkip projects an array slice with skip
func (b *Builder) ProjectSliceWithSkip(field string, skip, limit int) *Builder {
	b.projection[field] = bson.M{"$slice": []int{skip, limit}}
	return b
}

// ProjectElemMatch projects elements that match a condition
func (b *Builder) ProjectElemMatch(field string, condition any) *Builder {
	b.projection[field] = bson.M{"$elemMatch": condition}
	return b
}

// Aggregation pipeline methods

// AddStage adds a custom aggregation stage
func (b *Builder) AddStage(stage bson.M) *Builder {
	b.pipeline = append(b.pipeline, stage)
	return b
}

// AddMatch adds a $match stage
func (b *Builder) AddMatch(filter bson.M) *Builder {
	return b.AddStage(bson.M{"$match": filter})
}

// AddSort adds a $sort stage
func (b *Builder) AddSort(sort bson.D) *Builder {
	return b.AddStage(bson.M{"$sort": sort})
}

// AddLimit adds a $limit stage
func (b *Builder) AddLimit(limit int64) *Builder {
	return b.AddStage(bson.M{"$limit": limit})
}

// AddSkip adds a $skip stage
func (b *Builder) AddSkip(skip int64) *Builder {
	return b.AddStage(bson.M{"$skip": skip})
}

// AddProject adds a $project stage
func (b *Builder) AddProject(projection bson.M) *Builder {
	return b.AddStage(bson.M{"$project": projection})
}

// AddGroup adds a $group stage
func (b *Builder) AddGroup(group bson.M) *Builder {
	return b.AddStage(bson.M{"$group": group})
}

// AddUnwind adds an $unwind stage
func (b *Builder) AddUnwind(field string) *Builder {
	return b.AddStage(bson.M{"$unwind": field})
}

// AddLookup adds a $lookup stage for joins
func (b *Builder) AddLookup(from, localField, foreignField, as string) *Builder {
	return b.AddStage(bson.M{
		"$lookup": bson.M{
			"from":         from,
			"localField":   localField,
			"foreignField": foreignField,
			"as":           as,
		},
	})
}

// Build methods for different query types

// ToFilter builds a filter document for find operations
func (b *Builder) ToFilter() bson.M {
	if len(b.match) == 0 {
		return bson.M{}
	}
	return b.match
}

// ToFindOptions builds options for find operations
func (b *Builder) ToFindOptions() *FindOptionsBuilder {
	opts := &FindOptionsBuilder{}

	if len(b.sort) > 0 {
		opts.Sort(b.sort)
	}
	if b.skip != nil {
		opts.Skip(*b.skip)
	}
	if b.limit != nil {
		opts.Limit(*b.limit)
	}
	if len(b.projection) > 0 {
		opts.Projection(b.projection)
	}

	return opts
}

// ToPipeline builds an aggregation pipeline
func (b *Builder) ToPipeline() []bson.M {
	pipeline := make([]bson.M, 0)

	// Add existing custom stages first
	pipeline = append(pipeline, b.pipeline...)

	// Add implicit stages from builder state
	if len(b.match) > 0 {
		pipeline = append(pipeline, bson.M{"$match": b.match})
	}

	if len(b.sort) > 0 {
		pipeline = append(pipeline, bson.M{"$sort": b.sort})
	}

	if b.skip != nil {
		pipeline = append(pipeline, bson.M{"$skip": *b.skip})
	}

	if b.limit != nil {
		pipeline = append(pipeline, bson.M{"$limit": *b.limit})
	}

	if len(b.projection) > 0 {
		pipeline = append(pipeline, bson.M{"$project": b.projection})
	}

	return pipeline
}

// ToUpdateDocument builds an update document
func (b *Builder) ToUpdateDocument(update bson.M) bson.M {
	return update
}

// Debug methods

// ToJSON returns a JSON representation of the current query state
func (b *Builder) ToJSON() string {
	state := bson.M{
		"match":      b.match,
		"sort":       b.sort,
		"skip":       b.skip,
		"limit":      b.limit,
		"projection": b.projection,
		"pipeline":   b.pipeline,
	}

	// Convert to JSON for debugging - fall back to String() on marshal error
	jsonBytes, err := bson.MarshalExtJSON(state, true, false)
	if err != nil {
		return b.String() // Fallback to simpler representation
	}
	return string(jsonBytes)
}

// String returns a string representation of the query
func (b *Builder) String() string {
	var skipVal, limitVal any
	if b.skip != nil {
		skipVal = *b.skip
	}
	if b.limit != nil {
		limitVal = *b.limit
	}
	return fmt.Sprintf("MongoBuilder{match: %v, sort: %v, skip: %v, limit: %v}",
		b.match, b.sort, skipVal, limitVal)
}

// Clone creates a copy of the builder
func (b *Builder) Clone() *Builder {
	clone := &Builder{
		match:      make(bson.M),
		sort:       make(bson.D, len(b.sort)),
		projection: make(bson.M),
		pipeline:   make([]bson.M, len(b.pipeline)),
	}

	// Deep copy match
	maps.Copy(clone.match, b.match)

	// Copy sort
	copy(clone.sort, b.sort)

	// Copy skip/limit
	if b.skip != nil {
		skip := *b.skip
		clone.skip = &skip
	}
	if b.limit != nil {
		limit := *b.limit
		clone.limit = &limit
	}

	// Deep copy projection
	maps.Copy(clone.projection, b.projection)

	// Deep copy pipeline
	copy(clone.pipeline, b.pipeline)

	return clone
}

// Reset clears all builder state for reuse
func (b *Builder) Reset() *Builder {
	b.match = make(bson.M)
	b.sort = make(bson.D, 0)
	b.skip = nil
	b.limit = nil
	b.projection = make(bson.M)
	b.pipeline = make([]bson.M, 0)
	return b
}

// BuilderState represents the internal state of a Builder for testing
type BuilderState struct {
	Match      bson.M
	Sort       bson.D
	Skip       *int64
	Limit      *int64
	Projection bson.M
	Pipeline   []bson.M
}

// State returns the current builder state for testing purposes
func (b *Builder) State() BuilderState {
	state := BuilderState{
		Match:      make(bson.M),
		Sort:       make(bson.D, len(b.sort)),
		Projection: make(bson.M),
		Pipeline:   make([]bson.M, len(b.pipeline)),
	}

	// Copy match
	maps.Copy(state.Match, b.match)

	// Copy sort
	copy(state.Sort, b.sort)

	// Copy skip/limit
	if b.skip != nil {
		skip := *b.skip
		state.Skip = &skip
	}
	if b.limit != nil {
		limit := *b.limit
		state.Limit = &limit
	}

	// Copy projection
	maps.Copy(state.Projection, b.projection)

	// Copy pipeline
	copy(state.Pipeline, b.pipeline)

	return state
}

// BuilderOptions configures Builder behavior
type BuilderOptions struct {
	PipelineOrder []string // Order of pipeline stages: "match", "sort", "skip", "limit", "project"
}

// WithOptions configures builder behavior
func (b *Builder) WithOptions(_ BuilderOptions) *Builder {
	// For now, we'll store options but the main implementation can be enhanced later
	// This is a placeholder for future configurability
	return b
}

// HasFilter returns true if the builder has filter conditions
func (b *Builder) HasFilter() bool {
	return len(b.match) > 0
}

// HasSort returns true if the builder has sort conditions
func (b *Builder) HasSort() bool {
	return len(b.sort) > 0
}

// HasProjection returns true if the builder has projection settings
func (b *Builder) HasProjection() bool {
	return len(b.projection) > 0
}

// HasPagination returns true if the builder has skip or limit set
func (b *Builder) HasPagination() bool {
	return b.skip != nil || b.limit != nil
}

// SkipValue returns the current skip value
func (b *Builder) SkipValue() *int64 {
	if b.skip == nil {
		return nil
	}
	skip := *b.skip
	return &skip
}

// LimitValue returns the current limit value
func (b *Builder) LimitValue() *int64 {
	if b.limit == nil {
		return nil
	}
	limit := *b.limit
	return &limit
}

// ProjectionFields returns the fields being projected
func (b *Builder) ProjectionFields() []string {
	fields := make([]string, 0, len(b.projection))
	for field := range b.projection {
		fields = append(fields, field)
	}
	return fields
}

// SortFields returns the fields being sorted
func (b *Builder) SortFields() []string {
	fields := make([]string, 0, len(b.sort))
	for _, elem := range b.sort {
		fields = append(fields, elem.Key)
	}
	return fields
}

// FindOptionsBuilder helps build find options
type FindOptionsBuilder struct {
	sort       any
	skip       *int64
	limit      *int64
	projection any
}

// Sort sets the sort option
func (f *FindOptionsBuilder) Sort(sort any) *FindOptionsBuilder {
	f.sort = sort
	return f
}

// Skip sets the skip option
func (f *FindOptionsBuilder) Skip(skip int64) *FindOptionsBuilder {
	f.skip = &skip
	return f
}

// Limit sets the limit option
func (f *FindOptionsBuilder) Limit(limit int64) *FindOptionsBuilder {
	f.limit = &limit
	return f
}

// Projection sets the projection option
func (f *FindOptionsBuilder) Projection(projection any) *FindOptionsBuilder {
	f.projection = projection
	return f
}

// Build builds the find options for the database package
func (f *FindOptionsBuilder) Build() *database.FindOptions {
	opts := &database.FindOptions{}

	if f.sort != nil {
		opts.Sort = f.sort
	}
	if f.skip != nil {
		opts.Skip = f.skip
	}
	if f.limit != nil {
		opts.Limit = f.limit
	}
	if f.projection != nil {
		opts.Projection = f.projection
	}

	return opts
}

// Static helper functions following Squirrel pattern

// Select creates a new builder with initial field selection
func Select(fields ...string) *Builder {
	return NewBuilder().Select(fields...)
}

// Where creates a new builder with initial where condition
func Where(field string, value any) *Builder {
	return NewBuilder().WhereEq(field, value)
}

// Match creates a new builder with initial match condition
func Match(filter bson.M) *Builder {
	builder := NewBuilder()
	maps.Copy(builder.match, filter)
	return builder
}

// Pipeline creates a new builder with an initial pipeline stage
func Pipeline(stages ...bson.M) *Builder {
	builder := NewBuilder()
	builder.pipeline = append(builder.pipeline, stages...)
	return builder
}
