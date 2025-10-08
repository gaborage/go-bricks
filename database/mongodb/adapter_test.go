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

// ============================================================================
// Helper Function Tests (adapter.go and connection.go)
// ============================================================================

func TestIsInt32InRange(t *testing.T) {
	tests := []struct {
		name     string
		value    float64
		expected bool
	}{
		{"zero is in range", 0, true},
		{"max int32 is in range", float64(2147483647), true},
		{"positive value in range", 1000, true},
		{"negative value out of range", -1, false},
		{"value greater than max int32 out of range", float64(2147483648), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isInt32InRange(tt.value)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSafeConvertToInt32(t *testing.T) {
	tests := []struct {
		name     string
		value    float64
		expected *int32
	}{
		{"valid conversion", 100, int32Ptr(100)},
		{"zero conversion", 0, int32Ptr(0)},
		{"max int32 conversion", 2147483647, int32Ptr(2147483647)},
		{"out of range negative returns nil", -1, nil},
		{"out of range positive returns nil", 2147483648, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := safeConvertToInt32(tt.value)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestParseExpireAfterSeconds(t *testing.T) {
	tests := []struct {
		name     string
		value    any
		expected *int32
	}{
		{"valid int32", int32(300), int32Ptr(300)},
		{"negative int32 returns nil", int32(-1), nil},
		{"valid int64", int64(500), int32Ptr(500)},
		{"int64 out of range returns nil", int64(2147483648), nil},
		{"valid float64", float64(750), int32Ptr(750)},
		{"float64 rounds correctly", float64(750.6), int32Ptr(751)},
		{"float64 negative returns nil", float64(-10), nil},
		{"unsupported type returns nil", "string-value", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseExpireAfterSeconds(tt.value)
			if tt.expected == nil {
				assert.Nil(t, result)
			} else {
				assert.Equal(t, *tt.expected, *result)
			}
		})
	}
}

func TestValidateAndMapFullDocument(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"default value", "default", true},
		{"updateLookup value", "updateLookup", true},
		{"whenAvailable value", "whenAvailable", true},
		{"required value", "required", true},
		{"invalid value", "invalid-option", false},
		{"empty string", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, valid := validateAndMapFullDocument(tt.value)
			assert.Equal(t, tt.valid, valid)
		})
	}
}

func int32Ptr(i int32) *int32 {
	return &i
}
