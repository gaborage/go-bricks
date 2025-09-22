package mongodb

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/gaborage/go-bricks/internal/database"
	"github.com/gaborage/go-bricks/logger"
)

// Ensure Connection implements both interfaces
var _ database.Interface = (*Connection)(nil)
var _ database.DocumentInterface = (*Connection)(nil)

// Collection returns a DocumentCollection for the specified collection name
func (c *Connection) Collection(name string) database.DocumentCollection {
	return &Collection{
		collection: c.database.Collection(name),
		logger:     c.logger,
	}
}

// CreateCollection creates a new collection with the specified options
func (c *Connection) CreateCollection(ctx context.Context, name string, opts *database.CreateCollectionOptions) error {
	var mongoOpts *options.CreateCollectionOptions
	if opts != nil {
		mongoOpts = options.CreateCollection()
		if opts.Capped != nil {
			mongoOpts.SetCapped(*opts.Capped)
		}
		if opts.SizeInBytes != nil {
			mongoOpts.SetSizeInBytes(*opts.SizeInBytes)
		}
		if opts.MaxDocuments != nil {
			mongoOpts.SetMaxDocuments(*opts.MaxDocuments)
		}
		if opts.Validator != nil {
			mongoOpts.SetValidator(opts.Validator)
		}
		if opts.ValidationLevel != nil {
			mongoOpts.SetValidationLevel(*opts.ValidationLevel)
		}
		if opts.ValidationAction != nil {
			mongoOpts.SetValidationAction(*opts.ValidationAction)
		}
	}

	err := c.database.CreateCollection(ctx, name, mongoOpts)
	if err != nil {
		return fmt.Errorf("failed to create collection %s: %w", name, err)
	}

	c.logger.Info().Str("collection", name).Msg("Created MongoDB collection")
	return nil
}

// DropCollection drops the specified collection
func (c *Connection) DropCollection(ctx context.Context, name string) error {
	err := c.database.Collection(name).Drop(ctx)
	if err != nil {
		return fmt.Errorf("failed to drop collection %s: %w", name, err)
	}

	c.logger.Info().Str("collection", name).Msg("Dropped MongoDB collection")
	return nil
}

// CreateIndex creates an index on the specified collection
func (c *Connection) CreateIndex(ctx context.Context, collection string, model database.IndexModel) error {
	coll := c.database.Collection(collection)

	mongoModel := mongo.IndexModel{
		Keys: model.Keys,
	}

	mongoModel.Options = buildIndexOptions(model.Options)

	_, err := coll.Indexes().CreateOne(ctx, mongoModel)
	if err != nil {
		return fmt.Errorf("failed to create index on collection %s: %w", collection, err)
	}

	indexName := "unnamed"
	if model.Options != nil && model.Options.Name != nil {
		indexName = *model.Options.Name
	}
	c.logger.Info().Str("collection", collection).Str("index", indexName).Msg("Created MongoDB index")
	return nil
}

// DropIndex drops an index from the specified collection
func (c *Connection) DropIndex(ctx context.Context, collection, name string) error {
	coll := c.database.Collection(collection)
	_, err := coll.Indexes().DropOne(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to drop index %s from collection %s: %w", name, collection, err)
	}

	c.logger.Info().Str("collection", collection).Str("index", name).Msg("Dropped MongoDB index")
	return nil
}

// parseIndexOptions extracts index options from a BSON document
func parseIndexOptions(index bson.M) *database.IndexOptions {
	opts := &database.IndexOptions{}

	if name, ok := index["name"].(string); ok {
		opts.Name = &name
	}
	if unique, ok := index["unique"].(bool); ok {
		opts.Unique = &unique
	}
	if sparse, ok := index["sparse"].(bool); ok {
		opts.Sparse = &sparse
	}
	if expireValue, exists := index["expireAfterSeconds"]; exists {
		if expireAfterSeconds := parseExpireAfterSeconds(expireValue); expireAfterSeconds != nil {
			opts.ExpireAfterSeconds = expireAfterSeconds
		}
	}
	if partialFilterExpression, ok := index["partialFilterExpression"]; ok {
		opts.PartialFilterExpression = partialFilterExpression
	}

	return opts
}

// buildIndexModel creates an IndexModel from a BSON document
func buildIndexModel(index bson.M) database.IndexModel {
	return database.IndexModel{
		Keys:    index["key"],
		Options: parseIndexOptions(index),
	}
}

// ListIndexes lists all indexes on the specified collection
func (c *Connection) ListIndexes(ctx context.Context, collection string) ([]database.IndexModel, error) {
	coll := c.database.Collection(collection)
	cursor, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list indexes for collection %s: %w", collection, err)
	}
	defer cursor.Close(ctx)

	var indexes []database.IndexModel
	for cursor.Next(ctx) {
		var index bson.M
		if err := cursor.Decode(&index); err != nil {
			return nil, fmt.Errorf("failed to decode index info: %w", err)
		}

		indexes = append(indexes, buildIndexModel(index))
	}

	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("cursor error while listing indexes: %w", err)
	}

	return indexes, nil
}

// isInt32InRange checks if a numeric value fits within MongoDB's valid expireAfterSeconds range
func isInt32InRange(value float64) bool {
	// MongoDB requires expireAfterSeconds to be in range [0, 2147483647]
	return value >= 0 && value <= math.MaxInt32
}

// safeConvertToInt32 converts a numeric value to int32 with range checking
func safeConvertToInt32(value float64) *int32 {
	if !isInt32InRange(value) {
		return nil
	}
	result := int32(value)
	return &result
}

// parseExpireAfterSeconds safely parses expireAfterSeconds from various numeric types
// MongoDB servers can return this value as int32, int64, float64, or json.Number
// MongoDB requires expireAfterSeconds to be in range [0, 2147483647]
func parseExpireAfterSeconds(value interface{}) *int32 {
	switch v := value.(type) {
	case int32:
		// Validate that int32 values are in MongoDB's valid range
		if v < 0 {
			return nil
		}
		return &v
	case int64:
		return safeConvertToInt32(float64(v))
	case float64:
		return safeConvertToInt32(math.Round(v))
	case json.Number:
		return parseJSONNumber(v)
	default:
		return nil
	}
}

// parseJSONNumber handles json.Number parsing with int/float fallback
func parseJSONNumber(num json.Number) *int32 {
	if intVal, err := num.Int64(); err == nil {
		return safeConvertToInt32(float64(intVal))
	}
	if floatVal, err := num.Float64(); err == nil {
		return safeConvertToInt32(math.Round(floatVal))
	}
	return nil
}

// Aggregate performs aggregation on the specified collection
func (c *Connection) Aggregate(ctx context.Context, collection string, pipeline interface{}, opts *database.AggregateOptions) (database.DocumentCursor, error) {
	coll := c.database.Collection(collection)

	var mongoOpts *options.AggregateOptions
	if opts != nil {
		mongoOpts = options.Aggregate()
		if opts.AllowDiskUse != nil {
			mongoOpts.SetAllowDiskUse(*opts.AllowDiskUse)
		}
		if opts.BatchSize != nil {
			mongoOpts.SetBatchSize(*opts.BatchSize)
		}
		if opts.BypassDocumentValidation != nil {
			mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
		}
		if opts.MaxTime != nil {
			mongoOpts.SetMaxTime(*opts.MaxTime)
		}
	}

	cursor, err := coll.Aggregate(ctx, pipeline, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to execute aggregation on collection %s: %w", collection, err)
	}

	return &Cursor{cursor: cursor}, nil
}

// RunCommand executes a database command
func (c *Connection) RunCommand(ctx context.Context, command interface{}) database.DocumentResult {
	result := c.database.RunCommand(ctx, command)
	return &SingleResult{result: result}
}

// Collection wraps a MongoDB collection to implement DocumentCollection interface
type Collection struct {
	collection *mongo.Collection
	logger     logger.Logger
}

// Ensure Collection implements DocumentCollection interface
var _ database.DocumentCollection = (*Collection)(nil)

// InsertOne inserts a single document
func (c *Collection) InsertOne(ctx context.Context, document interface{}, opts *database.InsertOneOptions) (interface{}, error) {
	var mongoOpts *options.InsertOneOptions
	if opts != nil {
		mongoOpts = options.InsertOne()
		if opts.BypassDocumentValidation != nil {
			mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
		}
	}

	result, err := c.collection.InsertOne(ctx, document, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to insert document: %w", err)
	}

	return result.InsertedID, nil
}

// FindOne finds a single document
func (c *Collection) FindOne(ctx context.Context, filter interface{}, opts *database.FindOneOptions) database.DocumentResult {
	var mongoOpts *options.FindOneOptions
	if opts != nil {
		mongoOpts = options.FindOne()
		if opts.Sort != nil {
			mongoOpts.SetSort(opts.Sort)
		}
		if opts.Skip != nil {
			mongoOpts.SetSkip(*opts.Skip)
		}
		if opts.Projection != nil {
			mongoOpts.SetProjection(opts.Projection)
		}
		if opts.MaxTime != nil {
			mongoOpts.SetMaxTime(*opts.MaxTime)
		}
		if opts.ShowRecordID != nil {
			mongoOpts.SetShowRecordID(*opts.ShowRecordID)
		}
		if opts.AllowPartialResults != nil {
			mongoOpts.SetAllowPartialResults(*opts.AllowPartialResults)
		}
	}

	result := c.collection.FindOne(ctx, filter, mongoOpts)
	return &SingleResult{result: result}
}

// UpdateOne updates a single document
func (c *Collection) UpdateOne(ctx context.Context, filter, update interface{}, opts *database.UpdateOptions) (database.DocumentUpdateResult, error) {
	mongoOpts := buildUpdateOptions(opts)

	result, err := c.collection.UpdateOne(ctx, filter, update, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to update document: %w", err)
	}

	return &UpdateResult{result: result}, nil
}

// ReplaceOne replaces a single document
func (c *Collection) ReplaceOne(ctx context.Context, filter, replacement interface{}, opts *database.ReplaceOptions) (database.DocumentUpdateResult, error) {
	var mongoOpts *options.ReplaceOptions
	if opts != nil {
		mongoOpts = options.Replace()
		if opts.BypassDocumentValidation != nil {
			mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
		}
		if opts.Upsert != nil {
			mongoOpts.SetUpsert(*opts.Upsert)
		}
	}

	result, err := c.collection.ReplaceOne(ctx, filter, replacement, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to replace document: %w", err)
	}

	return &UpdateResult{result: result}, nil
}

// DeleteOne deletes a single document
func (c *Collection) DeleteOne(ctx context.Context, filter interface{}, _ *database.DeleteOptions) (database.DocumentDeleteResult, error) {
	result, err := c.collection.DeleteOne(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to delete document: %w", err)
	}

	return &DeleteResult{result: result}, nil
}

// InsertMany inserts multiple documents
func (c *Collection) InsertMany(ctx context.Context, documents []interface{}, opts *database.InsertManyOptions) ([]interface{}, error) {
	var mongoOpts *options.InsertManyOptions
	if opts != nil {
		mongoOpts = options.InsertMany()
		if opts.BypassDocumentValidation != nil {
			mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
		}
		if opts.Ordered != nil {
			mongoOpts.SetOrdered(*opts.Ordered)
		}
	}

	result, err := c.collection.InsertMany(ctx, documents, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to insert documents: %w", err)
	}

	return result.InsertedIDs, nil
}

// Find finds multiple documents
func (c *Collection) Find(ctx context.Context, filter interface{}, opts *database.FindOptions) (database.DocumentCursor, error) {
	mongoOpts := buildFindOptions(opts)

	cursor, err := c.collection.Find(ctx, filter, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to find documents: %w", err)
	}

	return &Cursor{cursor: cursor}, nil
}

// UpdateMany updates multiple documents
func (c *Collection) UpdateMany(ctx context.Context, filter, update interface{}, opts *database.UpdateOptions) (database.DocumentUpdateResult, error) {
	mongoOpts := buildUpdateOptions(opts)

	result, err := c.collection.UpdateMany(ctx, filter, update, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to update documents: %w", err)
	}

	return &UpdateResult{result: result}, nil
}

// DeleteMany deletes multiple documents
func (c *Collection) DeleteMany(ctx context.Context, filter interface{}, _ *database.DeleteOptions) (database.DocumentDeleteResult, error) {
	result, err := c.collection.DeleteMany(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to delete documents: %w", err)
	}

	return &DeleteResult{result: result}, nil
}

// CountDocuments counts documents matching the filter
func (c *Collection) CountDocuments(ctx context.Context, filter interface{}, opts *database.CountOptions) (int64, error) {
	var mongoOpts *options.CountOptions
	if opts != nil {
		mongoOpts = options.Count()
		if opts.Skip != nil {
			mongoOpts.SetSkip(*opts.Skip)
		}
		if opts.Limit != nil {
			mongoOpts.SetLimit(*opts.Limit)
		}
		if opts.MaxTime != nil {
			mongoOpts.SetMaxTime(*opts.MaxTime)
		}
	}

	count, err := c.collection.CountDocuments(ctx, filter, mongoOpts)
	if err != nil {
		return 0, fmt.Errorf("failed to count documents: %w", err)
	}

	return count, nil
}

// EstimatedDocumentCount returns an estimated count of documents in the collection
func (c *Collection) EstimatedDocumentCount(ctx context.Context, opts *database.EstimatedCountOptions) (int64, error) {
	var mongoOpts *options.EstimatedDocumentCountOptions
	if opts != nil {
		mongoOpts = options.EstimatedDocumentCount()
		if opts.MaxTime != nil {
			mongoOpts.SetMaxTime(*opts.MaxTime)
		}
	}

	count, err := c.collection.EstimatedDocumentCount(ctx, mongoOpts)
	if err != nil {
		return 0, fmt.Errorf("failed to get estimated document count: %w", err)
	}

	return count, nil
}

// Aggregate performs aggregation on the collection
func (c *Collection) Aggregate(ctx context.Context, pipeline interface{}, opts *database.AggregateOptions) (database.DocumentCursor, error) {
	var mongoOpts *options.AggregateOptions
	if opts != nil {
		mongoOpts = options.Aggregate()
		if opts.AllowDiskUse != nil {
			mongoOpts.SetAllowDiskUse(*opts.AllowDiskUse)
		}
		if opts.BatchSize != nil {
			mongoOpts.SetBatchSize(*opts.BatchSize)
		}
		if opts.BypassDocumentValidation != nil {
			mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
		}
		if opts.MaxTime != nil {
			mongoOpts.SetMaxTime(*opts.MaxTime)
		}
	}

	cursor, err := c.collection.Aggregate(ctx, pipeline, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to execute aggregation: %w", err)
	}

	return &Cursor{cursor: cursor}, nil
}

// Distinct returns distinct values for a field
func (c *Collection) Distinct(ctx context.Context, fieldName string, filter interface{}, opts *database.DistinctOptions) ([]interface{}, error) {
	var mongoOpts *options.DistinctOptions
	if opts != nil {
		mongoOpts = options.Distinct()
		if opts.MaxTime != nil {
			mongoOpts.SetMaxTime(*opts.MaxTime)
		}
	}

	values, err := c.collection.Distinct(ctx, fieldName, filter, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to get distinct values: %w", err)
	}

	return values, nil
}

// CreateIndex creates an index on the collection
func (c *Collection) CreateIndex(ctx context.Context, model database.IndexModel) error {
	mongoModel := mongo.IndexModel{
		Keys: model.Keys,
	}

	mongoModel.Options = buildIndexOptions(model.Options)

	_, err := c.collection.Indexes().CreateOne(ctx, mongoModel)
	if err != nil {
		return fmt.Errorf("failed to create index: %w", err)
	}

	return nil
}

// CreateIndexes creates multiple indexes on the collection
func (c *Collection) CreateIndexes(ctx context.Context, models []database.IndexModel) error {
	mongoModels := make([]mongo.IndexModel, len(models))
	for i, model := range models {
		mongoModel := mongo.IndexModel{
			Keys: model.Keys,
		}

		mongoModel.Options = buildIndexOptions(model.Options)

		mongoModels[i] = mongoModel
	}

	_, err := c.collection.Indexes().CreateMany(ctx, mongoModels)
	if err != nil {
		return fmt.Errorf("failed to create indexes: %w", err)
	}

	return nil
}

// DropIndex drops an index from the collection
func (c *Collection) DropIndex(ctx context.Context, name string) error {
	_, err := c.collection.Indexes().DropOne(ctx, name)
	if err != nil {
		return fmt.Errorf("failed to drop index %s: %w", name, err)
	}

	return nil
}

// ListIndexes lists all indexes on the collection
func (c *Collection) ListIndexes(ctx context.Context) (database.DocumentCursor, error) {
	cursor, err := c.collection.Indexes().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list indexes: %w", err)
	}

	return &Cursor{cursor: cursor}, nil
}

// BulkWrite performs bulk write operations
func (c *Collection) BulkWrite(ctx context.Context, models []database.WriteModel, opts *database.BulkWriteOptions) (database.DocumentBulkWriteResult, error) {
	// Convert database.WriteModel to mongo.WriteModel
	mongoModels := make([]mongo.WriteModel, len(models))
	for i, model := range models {
		// This would need to be implemented based on the specific WriteModel types
		// For now, we'll assume the WriteModel interface has a GetModel() method
		if mongoModel, ok := model.GetModel().(mongo.WriteModel); ok {
			mongoModels[i] = mongoModel
		} else {
			return nil, fmt.Errorf("invalid write model at index %d", i)
		}
	}

	var mongoOpts *options.BulkWriteOptions
	if opts != nil {
		mongoOpts = options.BulkWrite()
		if opts.BypassDocumentValidation != nil {
			mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
		}
		if opts.Ordered != nil {
			mongoOpts.SetOrdered(*opts.Ordered)
		}
	}

	result, err := c.collection.BulkWrite(ctx, mongoModels, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to execute bulk write: %w", err)
	}

	return &BulkWriteResult{result: result}, nil
}

// Watch creates a change stream for the collection
func (c *Collection) Watch(ctx context.Context, pipeline interface{}, opts *database.ChangeStreamOptions) (database.ChangeStream, error) {
	var mongoOpts *options.ChangeStreamOptions
	if opts != nil {
		mongoOpts = options.ChangeStream()
		if opts.BatchSize != nil {
			mongoOpts.SetBatchSize(*opts.BatchSize)
		}
		// TODO: Fix FullDocument type compatibility
		// if opts.FullDocument != nil {
		//     mongoOpts.SetFullDocument(*opts.FullDocument)
		// }
		if opts.MaxAwaitTime != nil {
			mongoOpts.SetMaxAwaitTime(*opts.MaxAwaitTime)
		}
		if opts.ResumeAfter != nil {
			mongoOpts.SetResumeAfter(opts.ResumeAfter)
		}
		// TODO: Fix StartAtOperationTime type compatibility
		// if opts.StartAtOperationTime != nil {
		//     mongoOpts.SetStartAtOperationTime(opts.StartAtOperationTime)
		// }
		if opts.StartAfter != nil {
			mongoOpts.SetStartAfter(opts.StartAfter)
		}
	}

	stream, err := c.collection.Watch(ctx, pipeline, mongoOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to create change stream: %w", err)
	}

	return &ChangeStreamWrapper{stream: stream}, nil
}

// Helper functions to eliminate code duplication

// buildIndexOptions creates MongoDB index options from database.IndexOptions
func buildIndexOptions(opts *database.IndexOptions) *options.IndexOptions {
	if opts == nil {
		return nil
	}

	mongoOpts := options.Index()
	// Background option is deprecated in MongoDB 4.2+
	// if opts.Background != nil {
	//     mongoOpts.SetBackground(*opts.Background)
	// }
	if opts.ExpireAfterSeconds != nil {
		// Additional validation: ensure we never pass negative values to MongoDB
		if *opts.ExpireAfterSeconds >= 0 {
			mongoOpts.SetExpireAfterSeconds(*opts.ExpireAfterSeconds)
		}
	}
	if opts.Name != nil {
		mongoOpts.SetName(*opts.Name)
	}
	if opts.Sparse != nil {
		mongoOpts.SetSparse(*opts.Sparse)
	}
	if opts.Unique != nil {
		mongoOpts.SetUnique(*opts.Unique)
	}
	if opts.PartialFilterExpression != nil {
		mongoOpts.SetPartialFilterExpression(opts.PartialFilterExpression)
	}
	return mongoOpts
}

// buildFindOptions creates MongoDB find options from database.FindOptions
func buildFindOptions(opts *database.FindOptions) *options.FindOptions {
	if opts == nil {
		return nil
	}

	mongoOpts := options.Find()
	if opts.Sort != nil {
		mongoOpts.SetSort(opts.Sort)
	}
	if opts.Skip != nil {
		mongoOpts.SetSkip(*opts.Skip)
	}
	if opts.Limit != nil {
		mongoOpts.SetLimit(*opts.Limit)
	}
	if opts.Projection != nil {
		mongoOpts.SetProjection(opts.Projection)
	}
	if opts.MaxTime != nil {
		mongoOpts.SetMaxTime(*opts.MaxTime)
	}
	if opts.BatchSize != nil {
		mongoOpts.SetBatchSize(*opts.BatchSize)
	}
	if opts.NoCursorTimeout != nil {
		mongoOpts.SetNoCursorTimeout(*opts.NoCursorTimeout)
	}
	if opts.AllowPartialResults != nil {
		mongoOpts.SetAllowPartialResults(*opts.AllowPartialResults)
	}
	if opts.ShowRecordID != nil {
		mongoOpts.SetShowRecordID(*opts.ShowRecordID)
	}
	return mongoOpts
}

// buildUpdateOptions creates MongoDB update options from database.UpdateOptions
func buildUpdateOptions(opts *database.UpdateOptions) *options.UpdateOptions {
	if opts == nil {
		return nil
	}

	mongoOpts := options.Update()
	// TODO: Fix ArrayFilters type compatibility
	// if len(opts.ArrayFilters) > 0 {
	//     mongoOpts.SetArrayFilters(opts.ArrayFilters)
	// }
	if opts.BypassDocumentValidation != nil {
		mongoOpts.SetBypassDocumentValidation(*opts.BypassDocumentValidation)
	}
	if opts.Upsert != nil {
		mongoOpts.SetUpsert(*opts.Upsert)
	}
	return mongoOpts
}
