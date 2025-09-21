package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"github.com/gaborage/go-bricks/internal/database"
)

// Cursor wraps mongo.Cursor to implement DocumentCursor interface
type Cursor struct {
	cursor *mongo.Cursor
}

// Ensure Cursor implements DocumentCursor interface
var _ database.DocumentCursor = (*Cursor)(nil)

func (c *Cursor) Next(ctx context.Context) bool {
	return c.cursor.Next(ctx)
}

func (c *Cursor) TryNext(ctx context.Context) bool {
	return c.cursor.TryNext(ctx)
}

func (c *Cursor) Decode(val interface{}) error {
	return c.cursor.Decode(val)
}

func (c *Cursor) All(ctx context.Context, results interface{}) error {
	return c.cursor.All(ctx, results)
}

func (c *Cursor) Close(ctx context.Context) error {
	return c.cursor.Close(ctx)
}

func (c *Cursor) Err() error {
	return c.cursor.Err()
}

func (c *Cursor) ID() int64 {
	return c.cursor.ID()
}

func (c *Cursor) Current() bson.Raw {
	return c.cursor.Current
}

// SingleResult wraps mongo.SingleResult to implement DocumentResult interface
type SingleResult struct {
	result *mongo.SingleResult
}

// Ensure SingleResult implements DocumentResult interface
var _ database.DocumentResult = (*SingleResult)(nil)

func (r *SingleResult) Decode(v interface{}) error {
	return r.result.Decode(v)
}

func (r *SingleResult) Err() error {
	return r.result.Err()
}

// UpdateResult wraps mongo.UpdateResult to implement DocumentUpdateResult interface
type UpdateResult struct {
	result *mongo.UpdateResult
}

// Ensure UpdateResult implements DocumentUpdateResult interface
var _ database.DocumentUpdateResult = (*UpdateResult)(nil)

func (r *UpdateResult) MatchedCount() int64 {
	return r.result.MatchedCount
}

func (r *UpdateResult) ModifiedCount() int64 {
	return r.result.ModifiedCount
}

func (r *UpdateResult) UpsertedCount() int64 {
	return r.result.UpsertedCount
}

func (r *UpdateResult) UpsertedID() interface{} {
	return r.result.UpsertedID
}

// DeleteResult wraps mongo.DeleteResult to implement DocumentDeleteResult interface
type DeleteResult struct {
	result *mongo.DeleteResult
}

// Ensure DeleteResult implements DocumentDeleteResult interface
var _ database.DocumentDeleteResult = (*DeleteResult)(nil)

func (r *DeleteResult) DeletedCount() int64 {
	return r.result.DeletedCount
}

// BulkWriteResult wraps mongo.BulkWriteResult to implement DocumentBulkWriteResult interface
type BulkWriteResult struct {
	result *mongo.BulkWriteResult
}

// Ensure BulkWriteResult implements DocumentBulkWriteResult interface
var _ database.DocumentBulkWriteResult = (*BulkWriteResult)(nil)

func (r *BulkWriteResult) InsertedCount() int64 {
	return r.result.InsertedCount
}

func (r *BulkWriteResult) MatchedCount() int64 {
	return r.result.MatchedCount
}

func (r *BulkWriteResult) ModifiedCount() int64 {
	return r.result.ModifiedCount
}

func (r *BulkWriteResult) DeletedCount() int64 {
	return r.result.DeletedCount
}

func (r *BulkWriteResult) UpsertedCount() int64 {
	return r.result.UpsertedCount
}

func (r *BulkWriteResult) UpsertedIDs() map[int64]interface{} {
	return r.result.UpsertedIDs
}

// ChangeStreamWrapper wraps mongo.ChangeStream to implement ChangeStream interface
type ChangeStreamWrapper struct {
	stream *mongo.ChangeStream
}

// Ensure ChangeStreamWrapper implements ChangeStream interface
var _ database.ChangeStream = (*ChangeStreamWrapper)(nil)

func (c *ChangeStreamWrapper) Next(ctx context.Context) bool {
	return c.stream.Next(ctx)
}

func (c *ChangeStreamWrapper) TryNext(ctx context.Context) bool {
	return c.stream.TryNext(ctx)
}

func (c *ChangeStreamWrapper) Decode(val interface{}) error {
	return c.stream.Decode(val)
}

func (c *ChangeStreamWrapper) Err() error {
	return c.stream.Err()
}

func (c *ChangeStreamWrapper) Close(ctx context.Context) error {
	return c.stream.Close(ctx)
}

func (c *ChangeStreamWrapper) ResumeToken() bson.Raw {
	return c.stream.ResumeToken()
}

// WriteModel implementations for common write operations
type InsertOneModel struct {
	Document interface{}
}

func (m *InsertOneModel) GetModel() interface{} {
	return mongo.NewInsertOneModel().SetDocument(m.Document)
}

type UpdateOneModel struct {
	Filter interface{}
	Update interface{}
	Upsert *bool
}

func (m *UpdateOneModel) GetModel() interface{} {
	model := mongo.NewUpdateOneModel().SetFilter(m.Filter).SetUpdate(m.Update)
	if m.Upsert != nil {
		model.SetUpsert(*m.Upsert)
	}
	return model
}

type UpdateManyModel struct {
	Filter interface{}
	Update interface{}
	Upsert *bool
}

func (m *UpdateManyModel) GetModel() interface{} {
	model := mongo.NewUpdateManyModel().SetFilter(m.Filter).SetUpdate(m.Update)
	if m.Upsert != nil {
		model.SetUpsert(*m.Upsert)
	}
	return model
}

type DeleteOneModel struct {
	Filter interface{}
}

func (m *DeleteOneModel) GetModel() interface{} {
	return mongo.NewDeleteOneModel().SetFilter(m.Filter)
}

type DeleteManyModel struct {
	Filter interface{}
}

func (m *DeleteManyModel) GetModel() interface{} {
	return mongo.NewDeleteManyModel().SetFilter(m.Filter)
}

type ReplaceOneModel struct {
	Filter      interface{}
	Replacement interface{}
	Upsert      *bool
}

func (m *ReplaceOneModel) GetModel() interface{} {
	model := mongo.NewReplaceOneModel().SetFilter(m.Filter).SetReplacement(m.Replacement)
	if m.Upsert != nil {
		model.SetUpsert(*m.Upsert)
	}
	return model
}

// Ensure all WriteModel implementations implement the interface
var _ database.WriteModel = (*InsertOneModel)(nil)
var _ database.WriteModel = (*UpdateOneModel)(nil)
var _ database.WriteModel = (*UpdateManyModel)(nil)
var _ database.WriteModel = (*DeleteOneModel)(nil)
var _ database.WriteModel = (*DeleteManyModel)(nil)
var _ database.WriteModel = (*ReplaceOneModel)(nil)
