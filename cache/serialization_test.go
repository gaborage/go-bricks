package cache

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test structures
type SimpleStruct struct {
	ID   int64
	Name string
}

type OptimizedStruct struct {
	ID    int64  `cbor:"1,keyasint"`
	Name  string `cbor:"2,keyasint"`
	Email string `cbor:"3,keyasint"`
}

type ComplexStruct struct {
	ID        int64
	Name      string
	Tags      []string
	Metadata  map[string]any
	CreatedAt time.Time
	IsActive  bool
}

type NestedStruct struct {
	ID       int64
	User     SimpleStruct
	Settings map[string]SimpleStruct
}

func TestMarshalUnmarshal_Basic(t *testing.T) {
	t.Run("SimpleStruct", func(t *testing.T) {
		original := SimpleStruct{ID: 123, Name: "Alice"}

		data, err := Marshal(original)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		result, err := Unmarshal[SimpleStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original, result)
	})

	t.Run("OptimizedStruct", func(t *testing.T) {
		original := OptimizedStruct{
			ID:    456,
			Name:  "Bob",
			Email: "bob@example.com",
		}

		data, err := Marshal(original)
		require.NoError(t, err)
		assert.NotEmpty(t, data)

		result, err := Unmarshal[OptimizedStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original, result)
	})

	t.Run("ComplexStruct", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		original := ComplexStruct{
			ID:   789,
			Name: "Charlie",
			Tags: []string{"admin", "user"},
			Metadata: map[string]any{
				"role":  "admin",
				"level": 5,
			},
			CreatedAt: now,
			IsActive:  true,
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[ComplexStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original.ID, result.ID)
		assert.Equal(t, original.Name, result.Name)
		assert.Equal(t, original.Tags, result.Tags)
		assert.Equal(t, original.IsActive, result.IsActive)
		assert.Equal(t, original.CreatedAt.Unix(), result.CreatedAt.Unix())
	})
}

func TestMarshalUnmarshal_EdgeCases(t *testing.T) {
	t.Run("EmptyStruct", func(t *testing.T) {
		original := SimpleStruct{}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[SimpleStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original, result)
	})

	t.Run("EmptySlice", func(t *testing.T) {
		original := ComplexStruct{
			ID:   1,
			Tags: []string{},
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[ComplexStruct](data)
		require.NoError(t, err)
		assert.NotNil(t, result.Tags)
		assert.Empty(t, result.Tags)
	})

	t.Run("NilSlice", func(t *testing.T) {
		original := ComplexStruct{
			ID:   1,
			Tags: nil,
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[ComplexStruct](data)
		require.NoError(t, err)
		// CBOR may represent nil slice as empty slice
		assert.Empty(t, result.Tags)
	})

	t.Run("EmptyMap", func(t *testing.T) {
		original := ComplexStruct{
			ID:       1,
			Metadata: map[string]any{},
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[ComplexStruct](data)
		require.NoError(t, err)
		assert.NotNil(t, result.Metadata)
		assert.Empty(t, result.Metadata)
	})

	t.Run("NilMap", func(t *testing.T) {
		original := ComplexStruct{
			ID:       1,
			Metadata: nil,
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[ComplexStruct](data)
		require.NoError(t, err)
		// CBOR may represent nil map as empty map
		assert.Empty(t, result.Metadata)
	})

	t.Run("UnicodeStrings", func(t *testing.T) {
		original := SimpleStruct{
			ID:   1,
			Name: "こんにちは 🌍 مرحبا",
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[SimpleStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original.Name, result.Name)
	})

	t.Run("LargeString", func(t *testing.T) {
		largeText := strings.Repeat("A", 10000)
		original := SimpleStruct{
			ID:   1,
			Name: largeText,
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[SimpleStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original.Name, result.Name)
	})
}

func TestMarshalUnmarshal_NestedStructures(t *testing.T) {
	t.Run("NestedStruct", func(t *testing.T) {
		original := NestedStruct{
			ID:   1,
			User: SimpleStruct{ID: 100, Name: "Alice"},
			Settings: map[string]SimpleStruct{
				"default": {ID: 200, Name: "Setting1"},
				"custom":  {ID: 300, Name: "Setting2"},
			},
		}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[NestedStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original.ID, result.ID)
		assert.Equal(t, original.User, result.User)
		assert.Equal(t, original.Settings, result.Settings)
	})
}

func TestMarshalUnmarshal_PointerTypes(t *testing.T) {
	t.Run("PointerStruct", func(t *testing.T) {
		original := &SimpleStruct{ID: 123, Name: "Alice"}

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[*SimpleStruct](data)
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, original.ID, result.ID)
		assert.Equal(t, original.Name, result.Name)
	})

	t.Run("NilPointer", func(t *testing.T) {
		var original *SimpleStruct

		data, err := Marshal(original)
		require.NoError(t, err)

		result, err := Unmarshal[*SimpleStruct](data)
		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestMarshalUnmarshal_PrimitiveTypes(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"int", int(42)},
		{"int64", int64(9223372036854775807)},
		{"string", "hello world"},
		{"bool", true},
		{"float64", 3.14159},
		{"slice", []int{1, 2, 3, 4, 5}},
		{"map", map[string]int{"one": 1, "two": 2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := Marshal(tt.value)
			require.NoError(t, err)

			// Type assertion required for primitive types
			switch v := tt.value.(type) {
			case int:
				result, err := Unmarshal[int](data)
				require.NoError(t, err)
				assert.Equal(t, v, result)
			case int64:
				result, err := Unmarshal[int64](data)
				require.NoError(t, err)
				assert.Equal(t, v, result)
			case string:
				result, err := Unmarshal[string](data)
				require.NoError(t, err)
				assert.Equal(t, v, result)
			case bool:
				result, err := Unmarshal[bool](data)
				require.NoError(t, err)
				assert.Equal(t, v, result)
			case float64:
				result, err := Unmarshal[float64](data)
				require.NoError(t, err)
				assert.InDelta(t, v, result, 0.0001)
			case []int:
				result, err := Unmarshal[[]int](data)
				require.NoError(t, err)
				assert.Equal(t, v, result)
			case map[string]int:
				result, err := Unmarshal[map[string]int](data)
				require.NoError(t, err)
				assert.Equal(t, v, result)
			}
		})
	}
}

func TestSecurityLimits(t *testing.T) {
	t.Run("LargeArray", func(t *testing.T) {
		// Create array within limits (should succeed)
		largeArray := make([]int, 5000)
		for i := range largeArray {
			largeArray[i] = i
		}

		data, err := Marshal(largeArray)
		require.NoError(t, err)

		result, err := Unmarshal[[]int](data)
		require.NoError(t, err)
		assert.Len(t, result, 5000)
	})

	t.Run("LargeMap", func(t *testing.T) {
		// Create map within limits (should succeed)
		largeMap := make(map[string]int, 5000)
		for i := 0; i < 5000; i++ {
			largeMap[string(rune('A'+i%26))+string(rune(i))] = i
		}

		data, err := Marshal(largeMap)
		require.NoError(t, err)

		result, err := Unmarshal[map[string]int](data)
		require.NoError(t, err)
		assert.Len(t, result, 5000)
	})
}

func TestMustMarshal(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		original := SimpleStruct{ID: 123, Name: "Alice"}
		data := MustMarshal(original)
		assert.NotEmpty(t, data)

		result, err := Unmarshal[SimpleStruct](data)
		require.NoError(t, err)
		assert.Equal(t, original, result)
	})
}

func TestMustUnmarshal(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		original := SimpleStruct{ID: 123, Name: "Alice"}
		data, err := Marshal(original)
		require.NoError(t, err)

		result := MustUnmarshal[SimpleStruct](data)
		assert.Equal(t, original, result)
	})

	t.Run("Panic", func(t *testing.T) {
		invalidData := []byte{0xFF, 0xFF, 0xFF}

		assert.Panics(t, func() {
			_ = MustUnmarshal[SimpleStruct](invalidData)
		})
	})
}

func TestInvalidData(t *testing.T) {
	t.Run("CorruptedData", func(t *testing.T) {
		invalidData := []byte{0xFF, 0xFF, 0xFF}

		_, err := Unmarshal[SimpleStruct](invalidData)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cbor unmarshal failed")
	})

	t.Run("EmptyData", func(t *testing.T) {
		emptyData := []byte{}

		_, err := Unmarshal[SimpleStruct](emptyData)
		assert.Error(t, err)
	})
}

// Benchmarks comparing CBOR vs JSON vs gob

func BenchmarkMarshal_CBOR_Simple(b *testing.B) {
	data := SimpleStruct{ID: 123, Name: "Alice"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Marshal(data)
	}
}

func BenchmarkMarshal_JSON_Simple(b *testing.B) {
	data := SimpleStruct{ID: 123, Name: "Alice"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(data)
	}
}

func BenchmarkMarshal_Gob_Simple(b *testing.B) {
	data := SimpleStruct{ID: 123, Name: "Alice"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		enc := gob.NewEncoder(&buf)
		_ = enc.Encode(data)
	}
}

func BenchmarkUnmarshal_CBOR_Simple(b *testing.B) {
	data := SimpleStruct{ID: 123, Name: "Alice"}
	encoded, _ := Marshal(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Unmarshal[SimpleStruct](encoded)
	}
}

func BenchmarkUnmarshal_JSON_Simple(b *testing.B) {
	data := SimpleStruct{ID: 123, Name: "Alice"}
	encoded, _ := json.Marshal(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result SimpleStruct
		_ = json.Unmarshal(encoded, &result)
	}
}

func BenchmarkUnmarshal_Gob_Simple(b *testing.B) {
	data := SimpleStruct{ID: 123, Name: "Alice"}
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	_ = enc.Encode(data)
	encoded := buf.Bytes()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result SimpleStruct
		dec := gob.NewDecoder(bytes.NewReader(encoded))
		_ = dec.Decode(&result)
	}
}

func BenchmarkMarshal_CBOR_Complex(b *testing.B) {
	data := ComplexStruct{
		ID:   789,
		Name: "Charlie",
		Tags: []string{"admin", "user", "moderator"},
		Metadata: map[string]any{
			"role":  "admin",
			"level": 5,
			"score": 98.5,
		},
		CreatedAt: time.Now(),
		IsActive:  true,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Marshal(data)
	}
}

func BenchmarkMarshal_JSON_Complex(b *testing.B) {
	data := ComplexStruct{
		ID:   789,
		Name: "Charlie",
		Tags: []string{"admin", "user", "moderator"},
		Metadata: map[string]any{
			"role":  "admin",
			"level": 5,
			"score": 98.5,
		},
		CreatedAt: time.Now(),
		IsActive:  true,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(data)
	}
}

func BenchmarkUnmarshal_CBOR_Complex(b *testing.B) {
	data := ComplexStruct{
		ID:   789,
		Name: "Charlie",
		Tags: []string{"admin", "user", "moderator"},
		Metadata: map[string]any{
			"role":  "admin",
			"level": 5,
			"score": 98.5,
		},
		CreatedAt: time.Now(),
		IsActive:  true,
	}
	encoded, _ := Marshal(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Unmarshal[ComplexStruct](encoded)
	}
}

func BenchmarkUnmarshal_JSON_Complex(b *testing.B) {
	data := ComplexStruct{
		ID:   789,
		Name: "Charlie",
		Tags: []string{"admin", "user", "moderator"},
		Metadata: map[string]any{
			"role":  "admin",
			"level": 5,
			"score": 98.5,
		},
		CreatedAt: time.Now(),
		IsActive:  true,
	}
	encoded, _ := json.Marshal(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result ComplexStruct
		_ = json.Unmarshal(encoded, &result)
	}
}

func BenchmarkMarshal_CBOR_Optimized(b *testing.B) {
	data := OptimizedStruct{
		ID:    456,
		Name:  "Bob",
		Email: "bob@example.com",
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Marshal(data)
	}
}

func BenchmarkUnmarshal_CBOR_Optimized(b *testing.B) {
	data := OptimizedStruct{
		ID:    456,
		Name:  "Bob",
		Email: "bob@example.com",
	}
	encoded, _ := Marshal(data)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = Unmarshal[OptimizedStruct](encoded)
	}
}
