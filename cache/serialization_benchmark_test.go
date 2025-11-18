package cache

import (
	"testing"
	"time"
)

const (
	marshalSetupFailedMsg = "Marshal setup failed: %v"
)

// =============================================================================
// Simple Type Benchmarks
// =============================================================================

func BenchmarkCBORMarshalString(b *testing.B) {
	value := "test string value"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(value)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalString(b *testing.B) {
	value := "test string value"
	data, err := Marshal(value)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[string](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

func BenchmarkCBORMarshalInt(b *testing.B) {
	value := int64(123456789)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(value)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalInt(b *testing.B) {
	value := int64(123456789)
	data, err := Marshal(value)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[int64](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Struct Benchmarks
// =============================================================================

type SimpleUser struct {
	ID    int64
	Name  string
	Email string
}

func BenchmarkCBORMarshalSimpleStruct(b *testing.B) {
	user := SimpleUser{
		ID:    123,
		Name:  "Alice Smith",
		Email: "alice@example.com",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(user)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalSimpleStruct(b *testing.B) {
	user := SimpleUser{
		ID:    123,
		Name:  "Alice Smith",
		Email: "alice@example.com",
	}
	data, err := Marshal(user)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[SimpleUser](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Complex Struct Benchmarks
// =============================================================================

type Address struct {
	Street  string
	City    string
	State   string
	ZipCode string
	Country string
}

type ComplexUser struct {
	ID        int64
	Name      string
	Email     string
	CreatedAt time.Time
	UpdatedAt time.Time
	Address   Address
	Tags      []string
	Metadata  map[string]string
	Active    bool
	Score     float64
}

func BenchmarkCBORMarshalComplexStruct(b *testing.B) {
	user := ComplexUser{
		ID:        12345,
		Name:      "Alice Smith",
		Email:     "alice@example.com",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Address: Address{
			Street:  "123 Main St",
			City:    "Springfield",
			State:   "IL",
			ZipCode: "62701",
			Country: "USA",
		},
		Tags:     []string{"premium", "verified", "active"},
		Metadata: map[string]string{"region": "us-west", "tier": "gold", "source": "web"},
		Active:   true,
		Score:    95.5,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(user)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalComplexStruct(b *testing.B) {
	user := ComplexUser{
		ID:        12345,
		Name:      "Alice Smith",
		Email:     "alice@example.com",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Address: Address{
			Street:  "123 Main St",
			City:    "Springfield",
			State:   "IL",
			ZipCode: "62701",
			Country: "USA",
		},
		Tags:     []string{"premium", "verified", "active"},
		Metadata: map[string]string{"region": "us-west", "tier": "gold", "source": "web"},
		Active:   true,
		Score:    95.5,
	}
	data, err := Marshal(user)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[ComplexUser](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Slice Benchmarks
// =============================================================================

func BenchmarkCBORMarshalSlice(b *testing.B) {
	users := make([]SimpleUser, 100)
	for i := range users {
		users[i] = SimpleUser{
			ID:    int64(i),
			Name:  "User Name",
			Email: "user@example.com",
		}
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(users)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalSlice(b *testing.B) {
	users := make([]SimpleUser, 100)
	for i := range users {
		users[i] = SimpleUser{
			ID:    int64(i),
			Name:  "User Name",
			Email: "user@example.com",
		}
	}
	data, err := Marshal(users)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[[]SimpleUser](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Map Benchmarks
// =============================================================================

func BenchmarkCBORMarshalMap(b *testing.B) {
	data := make(map[string]any)
	for i := 0; i < 50; i++ {
		data[string(rune(i))] = i
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(data)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalMap(b *testing.B) {
	mapData := make(map[string]any)
	for i := 0; i < 50; i++ {
		mapData[string(rune(i))] = i
	}
	encoded, err := Marshal(mapData)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[map[string]any](encoded)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Large Payload Benchmarks
// =============================================================================

type LargeStruct struct {
	Fields [100]string
	Data   [1000]byte
}

func BenchmarkCBORMarshalLargeStruct(b *testing.B) {
	large := LargeStruct{}
	for i := range large.Fields {
		large.Fields[i] = "field value"
	}
	for i := range large.Data {
		large.Data[i] = byte(i % 256)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Marshal(large)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}
	}
}

func BenchmarkCBORUnmarshalLargeStruct(b *testing.B) {
	large := LargeStruct{}
	for i := range large.Fields {
		large.Fields[i] = "field value"
	}
	for i := range large.Data {
		large.Data[i] = byte(i % 256)
	}
	data, err := Marshal(large)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := Unmarshal[LargeStruct](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Round-Trip Benchmarks
// =============================================================================

func BenchmarkCBORRoundTripSimple(b *testing.B) {
	user := SimpleUser{
		ID:    123,
		Name:  "Alice Smith",
		Email: "alice@example.com",
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, err := Marshal(user)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}

		_, err = Unmarshal[SimpleUser](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

func BenchmarkCBORRoundTripComplex(b *testing.B) {
	user := ComplexUser{
		ID:        12345,
		Name:      "Alice Smith",
		Email:     "alice@example.com",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Address: Address{
			Street:  "123 Main St",
			City:    "Springfield",
			State:   "IL",
			ZipCode: "62701",
			Country: "USA",
		},
		Tags:     []string{"premium", "verified", "active"},
		Metadata: map[string]string{"region": "us-west", "tier": "gold"},
		Active:   true,
		Score:    95.5,
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, err := Marshal(user)
		if err != nil {
			b.Fatalf("Marshal failed: %v", err)
		}

		_, err = Unmarshal[ComplexUser](data)
		if err != nil {
			b.Fatalf("Unmarshal failed: %v", err)
		}
	}
}

// =============================================================================
// Parallel Benchmarks
// =============================================================================

func BenchmarkCBORMarshalParallel(b *testing.B) {
	user := SimpleUser{
		ID:    123,
		Name:  "Alice Smith",
		Email: "alice@example.com",
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := Marshal(user)
			if err != nil {
				b.Fatalf("Marshal failed: %v", err)
			}
		}
	})
}

func BenchmarkCBORUnmarshalParallel(b *testing.B) {
	user := SimpleUser{
		ID:    123,
		Name:  "Alice Smith",
		Email: "alice@example.com",
	}
	data, err := Marshal(user)
	if err != nil {
		b.Fatalf(marshalSetupFailedMsg, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := Unmarshal[SimpleUser](data)
			if err != nil {
				b.Fatalf("Unmarshal failed: %v", err)
			}
		}
	})
}
