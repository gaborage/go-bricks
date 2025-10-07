package database

import (
	"context"
	"reflect"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// DocumentInterface defines operations for document-oriented databases like MongoDB
// This interface provides native document database operations alongside the SQL-compatible Interface
type DocumentInterface interface {
	// Collection operations
	Collection(name string) DocumentCollection

	// Database-level operations
	CreateCollection(ctx context.Context, name string, opts *CreateCollectionOptions) error
	DropCollection(ctx context.Context, name string) error

	// Index operations
	CreateIndex(ctx context.Context, collection string, model IndexModel) error
	DropIndex(ctx context.Context, collection string, name string) error
	ListIndexes(ctx context.Context, collection string) ([]IndexModel, error)

	// Aggregation operations
	Aggregate(ctx context.Context, collection string, pipeline any, opts *AggregateOptions) (DocumentCursor, error)

	// Database administration
	RunCommand(ctx context.Context, command any) DocumentResult
}

// DocumentCollection defines operations on a specific collection
type DocumentCollection interface {
	// Single document operations
	InsertOne(ctx context.Context, document any, opts *InsertOneOptions) (any, error)
	FindOne(ctx context.Context, filter any, opts *FindOneOptions) DocumentResult
	UpdateOne(ctx context.Context, filter any, update any, opts *UpdateOptions) (DocumentUpdateResult, error)
	ReplaceOne(ctx context.Context, filter any, replacement any, opts *ReplaceOptions) (DocumentUpdateResult, error)
	DeleteOne(ctx context.Context, filter any, opts *DeleteOptions) (DocumentDeleteResult, error)

	// Multiple document operations
	InsertMany(ctx context.Context, documents []any, opts *InsertManyOptions) ([]any, error)
	Find(ctx context.Context, filter any, opts *FindOptions) (DocumentCursor, error)
	UpdateMany(ctx context.Context, filter any, update any, opts *UpdateOptions) (DocumentUpdateResult, error)
	DeleteMany(ctx context.Context, filter any, opts *DeleteOptions) (DocumentDeleteResult, error)

	// Count operations
	CountDocuments(ctx context.Context, filter any, opts *CountOptions) (int64, error)
	EstimatedDocumentCount(ctx context.Context, opts *EstimatedCountOptions) (int64, error)

	// Aggregation operations
	Aggregate(ctx context.Context, pipeline any, opts *AggregateOptions) (DocumentCursor, error)
	Distinct(ctx context.Context, fieldName string, filter any, opts *DistinctOptions) ([]any, error)

	// Index operations
	CreateIndex(ctx context.Context, model IndexModel) error
	CreateIndexes(ctx context.Context, models []IndexModel) error
	DropIndex(ctx context.Context, name string) error
	ListIndexes(ctx context.Context) (DocumentCursor, error)

	// Bulk operations
	BulkWrite(ctx context.Context, models []WriteModel, opts *BulkWriteOptions) (DocumentBulkWriteResult, error)

	// Watch changes (if supported)
	Watch(ctx context.Context, pipeline any, opts *ChangeStreamOptions) (ChangeStream, error)
}

// DocumentCursor represents a cursor for iterating over query results
type DocumentCursor interface {
	// Iteration
	Next(ctx context.Context) bool
	TryNext(ctx context.Context) bool
	Decode(val any) error
	All(ctx context.Context, results any) error

	// Cursor management
	Close(ctx context.Context) error
	Err() error
	ID() int64

	// Current document access
	Current() bson.Raw
}

// DocumentResult represents a single document result
type DocumentResult interface {
	Decode(v any) error
	Err() error
}

// DocumentUpdateResult represents the result of an update operation
type DocumentUpdateResult interface {
	MatchedCount() int64
	ModifiedCount() int64
	UpsertedCount() int64
	UpsertedID() any
}

// DocumentDeleteResult represents the result of a delete operation
type DocumentDeleteResult interface {
	DeletedCount() int64
}

// DocumentBulkWriteResult represents the result of a bulk write operation
type DocumentBulkWriteResult interface {
	InsertedCount() int64
	MatchedCount() int64
	ModifiedCount() int64
	DeletedCount() int64
	UpsertedCount() int64
	UpsertedIDs() map[int64]any
}

// ChangeStream represents a change stream for watching collection changes
type ChangeStream interface {
	Next(ctx context.Context) bool
	TryNext(ctx context.Context) bool
	Decode(val any) error
	Err() error
	Close(ctx context.Context) error
	ResumeToken() bson.Raw
}

// WriteModel represents a write operation for bulk writes
type WriteModel interface {
	GetModel() any
}

// IndexModel represents an index specification
type IndexModel struct {
	Keys    any
	Options *IndexOptions
}

// Options structs for various operations
type InsertOneOptions struct {
	BypassDocumentValidation *bool
}

type InsertManyOptions struct {
	BypassDocumentValidation *bool
	Ordered                  *bool
}

type FindOneOptions struct {
	Sort                any
	Skip                *int64
	Projection          any
	MaxTime             *time.Duration
	ShowRecordID        *bool
	AllowPartialResults *bool
}

type FindOptions struct {
	Sort                any
	Skip                *int64
	Limit               *int64
	Projection          any
	MaxTime             *time.Duration
	ShowRecordID        *bool
	AllowPartialResults *bool
	BatchSize           *int32
	NoCursorTimeout     *bool
}

type UpdateOptions struct {
	ArrayFilters             []any
	BypassDocumentValidation *bool
	Upsert                   *bool
}

type ReplaceOptions struct {
	BypassDocumentValidation *bool
	Upsert                   *bool
}

type DeleteOptions struct {
	// Currently no specific options for delete operations
}

type CountOptions struct {
	Skip    *int64
	Limit   *int64
	MaxTime *time.Duration
}

type EstimatedCountOptions struct {
	MaxTime *time.Duration
}

type AggregateOptions struct {
	AllowDiskUse             *bool
	BatchSize                *int32
	BypassDocumentValidation *bool
	MaxTime                  *time.Duration
	UseCursor                *bool
}

type DistinctOptions struct {
	MaxTime *time.Duration
}

type BulkWriteOptions struct {
	BypassDocumentValidation *bool
	Ordered                  *bool
}

type ChangeStreamOptions struct {
	BatchSize            *int32
	FullDocument         *string
	MaxAwaitTime         *time.Duration
	ResumeAfter          any
	StartAtOperationTime any
	StartAfter           any
}

type CreateCollectionOptions struct {
	Capped                       *bool
	SizeInBytes                  *int64
	MaxDocuments                 *int64
	StorageEngine                any
	Validator                    any
	ValidationLevel              *string
	ValidationAction             *string
	IndexOptionDefaults          any
	ViewOn                       *string
	Pipeline                     any
	Collation                    *Collation
	ChangeStreamPreAndPostImages *ChangeStreamPreAndPostImages
}

type IndexOptions struct {
	Background              *bool
	ExpireAfterSeconds      *int32
	Name                    *string
	Sparse                  *bool
	StorageEngine           any
	Unique                  *bool
	Version                 *int32
	DefaultLanguage         *string
	LanguageOverride        *string
	TextVersion             *int32
	Weights                 any
	SphereVersion           *int32
	Bits                    *int32
	Max                     *float64
	Min                     *float64
	BucketSize              *float64
	PartialFilterExpression any
	Collation               *Collation
	WildcardProjection      any
	Hidden                  *bool
}

type Collation struct {
	Locale          *string
	CaseLevel       *bool
	CaseFirst       *string
	Strength        *int32
	NumericOrdering *bool
	Alternate       *string
	MaxVariable     *string
	Backwards       *bool
}

type ChangeStreamPreAndPostImages struct {
	Enabled *bool
}

// Utility functions for creating common filter types
func Filter() FilterBuilder {
	return FilterBuilder{filter: make(bson.M)}
}

// FilterBuilder provides a fluent interface for building MongoDB filters
type FilterBuilder struct {
	filter bson.M
}

func (fb FilterBuilder) Eq(field string, value any) FilterBuilder {
	fb.filter[field] = value
	return fb
}

func (fb FilterBuilder) Ne(field string, value any) FilterBuilder {
	fb.filter[field] = bson.M{"$ne": value}
	return fb
}

func (fb FilterBuilder) Gt(field string, value any) FilterBuilder {
	fb.filter[field] = bson.M{"$gt": value}
	return fb
}

func (fb FilterBuilder) Gte(field string, value any) FilterBuilder {
	fb.filter[field] = bson.M{"$gte": value}
	return fb
}

func (fb FilterBuilder) Lt(field string, value any) FilterBuilder {
	fb.filter[field] = bson.M{"$lt": value}
	return fb
}

func (fb FilterBuilder) Lte(field string, value any) FilterBuilder {
	fb.filter[field] = bson.M{"$lte": value}
	return fb
}

func (fb FilterBuilder) In(field string, values ...any) FilterBuilder {
	fb.filter[field] = bson.M{"$in": values}
	return fb
}

func (fb FilterBuilder) Nin(field string, values ...any) FilterBuilder {
	fb.filter[field] = bson.M{"$nin": values}
	return fb
}

func (fb FilterBuilder) Regex(field, pattern, options string) FilterBuilder {
	fb.filter[field] = bson.M{"$regex": pattern, "$options": options}
	return fb
}

func (fb FilterBuilder) Exists(field string, exists bool) FilterBuilder {
	fb.filter[field] = bson.M{"$exists": exists}
	return fb
}

func (fb FilterBuilder) Type(field string, bsonType any) FilterBuilder {
	fb.filter[field] = bson.M{"$type": bsonType}
	return fb
}

func (fb FilterBuilder) And(filters ...any) FilterBuilder {
	fb.filter["$and"] = filters
	return fb
}

func (fb FilterBuilder) Or(filters ...any) FilterBuilder {
	fb.filter["$or"] = filters
	return fb
}

func (fb FilterBuilder) Nor(filters ...any) FilterBuilder {
	fb.filter["$nor"] = filters
	return fb
}

func (fb FilterBuilder) Not(filter any) FilterBuilder {
	fb.filter["$not"] = filter
	return fb
}

func (fb FilterBuilder) Build() bson.M {
	return fb.filter
}

// Projection builder for specifying which fields to include/exclude
func Projection() ProjectionBuilder {
	return ProjectionBuilder{projection: make(bson.M)}
}

type ProjectionBuilder struct {
	projection bson.M
}

func (pb ProjectionBuilder) Include(fields ...string) ProjectionBuilder {
	for _, field := range fields {
		pb.projection[field] = 1
	}
	return pb
}

func (pb ProjectionBuilder) Exclude(fields ...string) ProjectionBuilder {
	for _, field := range fields {
		pb.projection[field] = 0
	}
	return pb
}

func (pb ProjectionBuilder) Slice(field string, limit int) ProjectionBuilder {
	pb.projection[field] = bson.M{"$slice": limit}
	return pb
}

func (pb ProjectionBuilder) SliceWithSkip(field string, skip, limit int) ProjectionBuilder {
	pb.projection[field] = bson.M{"$slice": []int{skip, limit}}
	return pb
}

func (pb ProjectionBuilder) ElemMatch(field string, condition any) ProjectionBuilder {
	pb.projection[field] = bson.M{"$elemMatch": condition}
	return pb
}

func (pb ProjectionBuilder) Build() bson.M {
	return pb.projection
}

// Sort builder for specifying sort order
func Sort() SortBuilder {
	return SortBuilder{sort: make(bson.D, 0)}
}

type SortBuilder struct {
	sort bson.D
}

func (sb SortBuilder) Asc(fields ...string) SortBuilder {
	for _, field := range fields {
		sb.sort = append(sb.sort, bson.E{Key: field, Value: 1})
	}
	return sb
}

func (sb SortBuilder) Desc(fields ...string) SortBuilder {
	for _, field := range fields {
		sb.sort = append(sb.sort, bson.E{Key: field, Value: -1})
	}
	return sb
}

func (sb SortBuilder) Build() bson.D {
	return sb.sort
}

// MongoDB update operators
const (
	updateOpSet         = "$set"
	updateOpUnset       = "$unset"
	updateOpInc         = "$inc"
	updateOpPush        = "$push"
	updateOpPull        = "$pull"
	updateOpAddToSet    = "$addToSet"
	updateOpCurrentDate = "$currentDate"
)

// Update builder for creating update documents
func Update() UpdateBuilder {
	return UpdateBuilder{update: make(bson.M)}
}

type UpdateBuilder struct {
	update bson.M
}

func (ub UpdateBuilder) Set(field string, value any) UpdateBuilder {
	if ub.update[updateOpSet] == nil {
		ub.update[updateOpSet] = make(bson.M)
	}
	ub.update[updateOpSet].(bson.M)[field] = value
	return ub
}

func (ub UpdateBuilder) Unset(fields ...string) UpdateBuilder {
	if ub.update[updateOpUnset] == nil {
		ub.update[updateOpUnset] = make(bson.M)
	}
	for _, field := range fields {
		ub.update[updateOpUnset].(bson.M)[field] = ""
	}
	return ub
}

func (ub UpdateBuilder) Inc(field string, value any) UpdateBuilder {
	if ub.update[updateOpInc] == nil {
		ub.update[updateOpInc] = make(bson.M)
	}
	ub.update[updateOpInc].(bson.M)[field] = value
	return ub
}

func (ub UpdateBuilder) Push(field string, value any) UpdateBuilder {
	if ub.update[updateOpPush] == nil {
		ub.update[updateOpPush] = make(bson.M)
	}
	ub.update[updateOpPush].(bson.M)[field] = value
	return ub
}

func (ub UpdateBuilder) Pull(field string, condition any) UpdateBuilder {
	if ub.update[updateOpPull] == nil {
		ub.update[updateOpPull] = make(bson.M)
	}
	ub.update[updateOpPull].(bson.M)[field] = condition
	return ub
}

func (ub UpdateBuilder) AddToSet(field string, value any) UpdateBuilder {
	if ub.update[updateOpAddToSet] == nil {
		ub.update[updateOpAddToSet] = make(bson.M)
	}
	ub.update[updateOpAddToSet].(bson.M)[field] = value
	return ub
}

func (ub UpdateBuilder) CurrentDate(fields ...string) UpdateBuilder {
	if ub.update[updateOpCurrentDate] == nil {
		ub.update[updateOpCurrentDate] = make(bson.M)
	}
	for _, field := range fields {
		ub.update[updateOpCurrentDate].(bson.M)[field] = true
	}
	return ub
}

func (ub UpdateBuilder) Build() bson.M {
	return ub.update
}

// Helper functions for common operations
func Int64Ptr(v int64) *int64       { return &v }
func Int32Ptr(v int32) *int32       { return &v }
func BoolPtr(v bool) *bool          { return &v }
func StringPtr(v string) *string    { return &v }
func Float64Ptr(v float64) *float64 { return &v }

// Type assertions for checking if an Interface also implements DocumentInterface
func AsDocumentInterface(db Interface) (DocumentInterface, bool) {
	if docDB, ok := db.(DocumentInterface); ok {
		return docDB, true
	}
	return nil, false
}

// Helper to get the underlying type name for reflection-based operations
func GetTypeName(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if name := t.Name(); name != "" {
		return name
	}
	return t.String()
}
