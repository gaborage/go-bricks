package mongodb

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/gaborage/go-bricks/internal/database"
	"github.com/stretchr/testify/assert"
)

// Test constants to avoid duplication
const (
	testNilInput     = "nil input"
	testEmptyOptions = "empty options"
)

func TestBuildIndexOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.IndexOptions
		expected func(*options.IndexOptionsBuilder) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.IndexOptions{},
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with name",
			input: &database.IndexOptions{
				Name: database.StringPtr("test_index"),
			},
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with unique",
			input: &database.IndexOptions{
				Unique: database.BoolPtr(true),
			},
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with sparse",
			input: &database.IndexOptions{
				Sparse: database.BoolPtr(true),
			},
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with expire after seconds",
			input: &database.IndexOptions{
				ExpireAfterSeconds: database.Int32Ptr(3600),
			},
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with partial filter expression",
			input: &database.IndexOptions{
				PartialFilterExpression: bson.M{"age": bson.M{"$gt": 18}},
			},
			expected: func(opts *options.IndexOptionsBuilder) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildIndexOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}

func TestBuildFindOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.FindOptions
		expected func(*options.FindOptionsBuilder) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.FindOptionsBuilder) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.FindOptions{},
			expected: func(opts *options.FindOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with skip and limit",
			input: &database.FindOptions{
				Skip:  database.Int64Ptr(10),
				Limit: database.Int64Ptr(5),
			},
			expected: func(opts *options.FindOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with batch size",
			input: &database.FindOptions{
				BatchSize: database.Int32Ptr(100),
			},
			expected: func(opts *options.FindOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with no cursor timeout",
			input: &database.FindOptions{
				NoCursorTimeout: database.BoolPtr(true),
			},
			expected: func(opts *options.FindOptionsBuilder) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFindOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}

func TestBuildUpdateOneOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.UpdateOptions
		expected func(*options.UpdateOneOptionsBuilder) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.UpdateOneOptionsBuilder) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.UpdateOptions{},
			expected: func(opts *options.UpdateOneOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with upsert",
			input: &database.UpdateOptions{
				Upsert: database.BoolPtr(true),
			},
			expected: func(opts *options.UpdateOneOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with bypass document validation",
			input: &database.UpdateOptions{
				BypassDocumentValidation: database.BoolPtr(true),
			},
			expected: func(opts *options.UpdateOneOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with array filters",
			input: &database.UpdateOptions{
				ArrayFilters: []any{bson.M{"elem.score": bson.M{"$gte": 80}}},
			},
			expected: func(opts *options.UpdateOneOptionsBuilder) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildUpdateOneOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}

func TestBuildUpdateManyOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    *database.UpdateOptions
		expected func(*options.UpdateManyOptionsBuilder) bool
	}{
		{
			name:  testNilInput,
			input: nil,
			expected: func(opts *options.UpdateManyOptionsBuilder) bool {
				return opts == nil
			},
		},
		{
			name:  testEmptyOptions,
			input: &database.UpdateOptions{},
			expected: func(opts *options.UpdateManyOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with upsert",
			input: &database.UpdateOptions{
				Upsert: database.BoolPtr(true),
			},
			expected: func(opts *options.UpdateManyOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with bypass document validation",
			input: &database.UpdateOptions{
				BypassDocumentValidation: database.BoolPtr(true),
			},
			expected: func(opts *options.UpdateManyOptionsBuilder) bool {
				return opts != nil
			},
		},
		{
			name: "with array filters",
			input: &database.UpdateOptions{
				ArrayFilters: []any{bson.M{"elem.score": bson.M{"$gte": 80}}},
			},
			expected: func(opts *options.UpdateManyOptionsBuilder) bool {
				return opts != nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildUpdateManyOptions(tt.input)
			assert.True(t, tt.expected(result))
		})
	}
}
