package cache

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// CBOR encoding/decoding options configured for security and determinism.
// These options prevent potential DoS attacks and ensure consistent encoding.
var (
	// encMode defines encoding options for serialization.
	// - Sort: SortCanonical ensures deterministic field ordering (same input → same bytes)
	// - Time: TimeRFC3339 uses standard timestamp format for cross-language compatibility
	encMode cbor.EncMode

	// decMode defines decoding options for deserialization.
	// - MaxArrayElements: 10000 prevents DoS from malicious large arrays
	// - MaxMapPairs: 10000 prevents DoS from malicious large maps
	// - MaxNestedLevels: 16 prevents stack overflow from deeply nested structures
	decMode cbor.DecMode
)

//nolint:gochecknoinits // Required for CBOR mode configuration at package load time
func init() {
	var err error

	// Configure encoding mode
	encOpts := cbor.EncOptions{
		Sort: cbor.SortCanonical, // Deterministic encoding
		Time: cbor.TimeRFC3339,   // Standard timestamp format
	}
	encMode, err = encOpts.EncMode()
	if err != nil {
		panic(fmt.Sprintf("failed to create CBOR encoding mode: %v", err))
	}

	// Configure decoding mode with security limits
	decOpts := cbor.DecOptions{
		MaxArrayElements: 10000, // Prevent DoS from huge arrays
		MaxMapPairs:      10000, // Prevent DoS from huge maps
		MaxNestedLevels:  16,    // Prevent stack overflow
	}
	decMode, err = decOpts.DecMode()
	if err != nil {
		panic(fmt.Sprintf("failed to create CBOR decoding mode: %v", err))
	}
}

// Marshal serializes a value to CBOR bytes.
// Returns an error if serialization fails.
//
// The generic type parameter T allows type-safe serialization:
//
//	type User struct {
//	    ID    int64  `cbor:"1,keyasint"`  // Optimized: integer keys
//	    Name  string `cbor:"2,keyasint"`
//	    Email string `cbor:"3,keyasint"`
//	}
//
//	user := User{ID: 123, Name: "Alice", Email: "alice@example.com"}
//	data, err := cache.Marshal(user)
//
// Struct tags are optional but recommended for optimization:
//   - `cbor:"1,keyasint"` - Use integer keys (smaller encoding)
//   - `cbor:"-"` - Skip field
//   - `cbor:"field_name,omitempty"` - Omit zero values
//
// See: https://github.com/fxamacker/cbor#struct-tags
func Marshal[T any](v T) ([]byte, error) {
	data, err := encMode.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("cbor marshal failed: %w", err)
	}
	return data, nil
}

// Unmarshal deserializes CBOR bytes into a value.
// Returns the deserialized value and an error if deserialization fails.
//
// The generic type parameter T specifies the expected type:
//
//	data := []byte{...} // CBOR-encoded User
//	user, err := cache.Unmarshal[User](data)
//	if err != nil {
//	    return err
//	}
//	fmt.Println(user.Name) // "Alice"
//
// Security features:
//   - MaxArrayElements: 10000 (prevents DoS)
//   - MaxMapPairs: 10000 (prevents DoS)
//   - MaxNestedLevels: 16 (prevents stack overflow)
//
// For pointer types, use:
//
//	user, err := cache.Unmarshal[*User](data)
func Unmarshal[T any](data []byte) (T, error) {
	var v T
	if err := decMode.Unmarshal(data, &v); err != nil {
		return v, fmt.Errorf("cbor unmarshal failed: %w", err)
	}
	return v, nil
}

// MustMarshal is like Marshal but panics on error.
// Use this only when you're certain the value can be serialized
// (e.g., in tests or with pre-validated data).
//
// Example:
//
//	data := cache.MustMarshal(User{ID: 123, Name: "Alice"})
//	cache.Set(ctx, "user:123", data, 5*time.Minute)
func MustMarshal[T any](v T) []byte {
	data, err := Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("MustMarshal failed: %v", err))
	}
	return data
}

// MustUnmarshal is like Unmarshal but panics on error.
// Use this only when you're certain the data is valid CBOR
// (e.g., in tests or with data you just marshaled).
//
// Example:
//
//	data, _ := cache.Get(ctx, "user:123")
//	user := cache.MustUnmarshal[User](data) // Panics if data is invalid
func MustUnmarshal[T any](data []byte) T {
	v, err := Unmarshal[T](data)
	if err != nil {
		panic(fmt.Sprintf("MustUnmarshal failed: %v", err))
	}
	return v
}
