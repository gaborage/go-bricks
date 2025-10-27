//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTableRef(t *testing.T) {
	t.Run("Table without alias", func(t *testing.T) {
		table := Table("users")
		assert.Equal(t, "users", table.Name())
		assert.Equal(t, "", table.Alias())
		assert.False(t, table.HasAlias())
	})

	t.Run("Table with alias", func(t *testing.T) {
		table := Table("users").As("u")
		assert.Equal(t, "users", table.Name())
		assert.Equal(t, "u", table.Alias())
		assert.True(t, table.HasAlias())
	})

	t.Run("Method chaining", func(t *testing.T) {
		table := Table("customers").As("c")
		assert.Equal(t, "customers", table.Name())
		assert.Equal(t, "c", table.Alias())
		assert.True(t, table.HasAlias())
	})

	t.Run("Oracle reserved word table name", func(t *testing.T) {
		// Should not panic - validation happens at query build time
		table := Table("LEVEL").As("lvl")
		assert.Equal(t, "LEVEL", table.Name())
		assert.Equal(t, "lvl", table.Alias())
	})
}

func TestTableRefValidation(t *testing.T) {
	t.Run("Empty table name panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Table("")
		}, "Table() should panic on empty name")
	})

	t.Run("Empty alias panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Table("users").As("")
		}, "As() should panic on empty alias")
	})

	t.Run("Valid table name does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			Table("users")
		})
	})

	t.Run("Valid alias does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			Table("users").As("u")
		})
	})
}
