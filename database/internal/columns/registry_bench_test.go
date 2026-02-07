package columns

import (
	"testing"

	"github.com/gaborage/go-bricks/database/internal/sqllex"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

const (
	smallStructTest = "Small Struct (5 fields)"
	largeStructTest = "Large Struct (20 fields)"
)

// Benchmark structs
type BenchUser struct {
	ID        int64  `db:"id"`
	Name      string `db:"name"`
	Email     string `db:"email"`
	Status    string `db:"status"`
	CreatedAt string `db:"created_at"`
}

type BenchLargeStruct struct {
	Field01 string `db:"field_01"`
	Field02 string `db:"field_02"`
	Field03 string `db:"field_03"`
	Field04 string `db:"field_04"`
	Field05 string `db:"field_05"`
	Field06 string `db:"field_06"`
	Field07 string `db:"field_07"`
	Field08 string `db:"field_08"`
	Field09 string `db:"field_09"`
	Field10 string `db:"field_10"`
	Field11 string `db:"field_11"`
	Field12 string `db:"field_12"`
	Field13 string `db:"field_13"`
	Field14 string `db:"field_14"`
	Field15 string `db:"field_15"`
	Field16 string `db:"field_16"`
	Field17 string `db:"field_17"`
	Field18 string `db:"field_18"`
	Field19 string `db:"field_19"`
	Field20 string `db:"field_20"`
}

// BenchmarkColumnRegistryFirstUse benchmarks the one-time parsing cost
//
// Performance Target: <5µs per struct type
func BenchmarkColumnRegistryFirstUse(b *testing.B) {
	b.Run(smallStructTest, func(b *testing.B) {
		for b.Loop() {
			registry := &ColumnRegistry{
				vendorCaches: make(map[string]*vendorCache),
			}
			_ = registry.Get(dbtypes.PostgreSQL, &BenchUser{})
		}
	})

	b.Run(largeStructTest, func(b *testing.B) {
		for b.Loop() {
			registry := &ColumnRegistry{
				vendorCaches: make(map[string]*vendorCache),
			}
			_ = registry.Get(dbtypes.PostgreSQL, &BenchLargeStruct{})
		}
	})

	b.Run("Oracle Vendor (with quoting)", func(b *testing.B) {
		for b.Loop() {
			registry := &ColumnRegistry{
				vendorCaches: make(map[string]*vendorCache),
			}
			_ = registry.Get(dbtypes.Oracle, &BenchUser{})
		}
	})
}

// BenchmarkColumnRegistryCachedAccess benchmarks cached metadata retrieval
//
// Performance Target: <100ns per access
func BenchmarkColumnRegistryCachedAccess(b *testing.B) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// Warm the cache
	_ = registry.Get(dbtypes.PostgreSQL, &BenchUser{})

	b.ResetTimer()
	b.Run("Cached Get", func(b *testing.B) {
		for b.Loop() {
			_ = registry.Get(dbtypes.PostgreSQL, &BenchUser{})
		}
	})
}

// BenchmarkColumnMetadataGet benchmarks field name lookup
//
// This measures the map lookup cost for retrieving a column by field name
func BenchmarkColumnMetadataGet(b *testing.B) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}
	metadata := registry.Get(dbtypes.PostgreSQL, &BenchUser{})

	b.ResetTimer()
	b.Run("Get Single Field", func(b *testing.B) {
		for b.Loop() {
			_ = metadata.Col("ID")
		}
	})

	b.Run("Get Multiple Fields", func(b *testing.B) {
		for b.Loop() {
			_ = metadata.Col("ID")
			_ = metadata.Col("Name")
			_ = metadata.Col("Email")
		}
	})
}

// BenchmarkColumnMetadataFields benchmarks bulk field retrieval
func BenchmarkColumnMetadataFields(b *testing.B) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}
	metadata := registry.Get(dbtypes.PostgreSQL, &BenchUser{})

	b.ResetTimer()
	b.Run("Fields (3 columns)", func(b *testing.B) {
		for b.Loop() {
			_ = metadata.Cols("ID", "Name", "Email")
		}
	})

	b.Run("Fields (5 columns)", func(b *testing.B) {
		for b.Loop() {
			_ = metadata.Cols("ID", "Name", "Email", "Status", "CreatedAt")
		}
	})
}

// BenchmarkColumnMetadataAll benchmarks All() method
func BenchmarkColumnMetadataAll(b *testing.B) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	b.Run(smallStructTest, func(b *testing.B) {
		metadata := registry.Get(dbtypes.PostgreSQL, &BenchUser{})
		b.ResetTimer()
		for b.Loop() {
			_ = metadata.All()
		}
	})

	b.Run(largeStructTest, func(b *testing.B) {
		metadata := registry.Get(dbtypes.PostgreSQL, &BenchLargeStruct{})
		b.ResetTimer()
		for b.Loop() {
			_ = metadata.All()
		}
	})
}

// BenchmarkRegisterColumns benchmarks the global registration function
func BenchmarkRegisterColumns(b *testing.B) {
	// Clear global registry before benchmark
	ClearGlobalRegistry()

	// Warm the cache with first access
	_ = RegisterColumns(dbtypes.PostgreSQL, &BenchUser{})

	b.ResetTimer()
	b.Run("Global RegisterColumns (cached)", func(b *testing.B) {
		for b.Loop() {
			_ = RegisterColumns(dbtypes.PostgreSQL, &BenchUser{})
		}
	})
}

// BenchmarkVendorComparison compares performance across different vendors
func BenchmarkVendorComparison(b *testing.B) {
	type ReservedWordStruct struct {
		Number string `db:"number"` // Oracle reserved word
		Level  int    `db:"level"`  // Oracle reserved word
		Size   string `db:"size"`   // Oracle reserved word
	}

	b.Run("Oracle (with reserved word quoting)", func(b *testing.B) {
		registry := &ColumnRegistry{
			vendorCaches: make(map[string]*vendorCache),
		}
		_ = registry.Get(dbtypes.Oracle, &ReservedWordStruct{})
		b.ResetTimer()
		for b.Loop() {
			_ = registry.Get(dbtypes.Oracle, &ReservedWordStruct{})
		}
	})

	b.Run("PostgreSQL (no quoting)", func(b *testing.B) {
		registry := &ColumnRegistry{
			vendorCaches: make(map[string]*vendorCache),
		}
		_ = registry.Get(dbtypes.PostgreSQL, &ReservedWordStruct{})
		b.ResetTimer()
		for b.Loop() {
			_ = registry.Get(dbtypes.PostgreSQL, &ReservedWordStruct{})
		}
	})
}

// BenchmarkConcurrentAccess benchmarks concurrent cached access
func BenchmarkConcurrentAccess(b *testing.B) {
	registry := &ColumnRegistry{
		vendorCaches: make(map[string]*vendorCache),
	}

	// Warm the cache
	_ = registry.Get(dbtypes.PostgreSQL, &BenchUser{})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = registry.Get(dbtypes.PostgreSQL, &BenchUser{})
		}
	})
}

// BenchmarkParseStructDirect benchmarks the parser function directly
func BenchmarkParseStructDirect(b *testing.B) {
	b.Run(smallStructTest, func(b *testing.B) {
		for b.Loop() {
			_, _ = parseStruct(dbtypes.PostgreSQL, &BenchUser{})
		}
	})

	b.Run(largeStructTest, func(b *testing.B) {
		for b.Loop() {
			_, _ = parseStruct(dbtypes.PostgreSQL, &BenchLargeStruct{})
		}
	})
}

// BenchmarkOracleQuoting benchmarks Oracle identifier quoting logic
func BenchmarkOracleQuoting(b *testing.B) {
	b.Run("Simple column (no quoting)", func(b *testing.B) {
		for b.Loop() {
			_ = oracleQuoteIdentifier("user_id")
		}
	})

	b.Run("Reserved word (quoting required)", func(b *testing.B) {
		for b.Loop() {
			_ = oracleQuoteIdentifier("number")
		}
	})

	b.Run("Already quoted (no-op)", func(b *testing.B) {
		for b.Loop() {
			_ = oracleQuoteIdentifier(`"NUMBER"`)
		}
	})
}

// BenchmarkIsOracleReservedWord benchmarks reserved word lookup from sqllex package
func BenchmarkIsOracleReservedWord(b *testing.B) {
	b.Run("Reserved word", func(b *testing.B) {
		for b.Loop() {
			_ = sqllex.IsOracleReservedWord("number")
		}
	})

	b.Run("Not reserved", func(b *testing.B) {
		for b.Loop() {
			_ = sqllex.IsOracleReservedWord("user_id")
		}
	})
}
