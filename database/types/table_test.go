//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableRef(t *testing.T) {
	t.Run("Table without alias", func(t *testing.T) {
		table, err := Table("users")
		require.NoError(t, err)
		assert.Equal(t, "users", table.Name())
		assert.Equal(t, "", table.Alias())
		assert.False(t, table.HasAlias())
	})

	t.Run("Table with alias", func(t *testing.T) {
		table, err := Table("users")
		require.NoError(t, err)
		aliased, err := table.As("u")
		require.NoError(t, err)
		assert.Equal(t, "users", aliased.Name())
		assert.Equal(t, "u", aliased.Alias())
		assert.True(t, aliased.HasAlias())
	})

	t.Run("Method chaining", func(t *testing.T) {
		table, err := Table("customers")
		require.NoError(t, err)
		aliased, err := table.As("c")
		require.NoError(t, err)
		assert.Equal(t, "customers", aliased.Name())
		assert.Equal(t, "c", aliased.Alias())
		assert.True(t, aliased.HasAlias())
	})

	t.Run("Oracle reserved word table name", func(t *testing.T) {
		// Should not error - validation happens at query build time
		table, err := Table("LEVEL")
		require.NoError(t, err)
		aliased, err := table.As("lvl")
		require.NoError(t, err)
		assert.Equal(t, "LEVEL", aliased.Name())
		assert.Equal(t, "lvl", aliased.Alias())
	})
}

func TestTableRefValidation(t *testing.T) {
	t.Run("Empty table name returns error", func(t *testing.T) {
		_, err := Table("")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyTableName))
	})

	t.Run("Empty alias returns error", func(t *testing.T) {
		table, err := Table("users")
		require.NoError(t, err)
		_, err = table.As("")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyTableAlias))
	})

	t.Run("Valid table name succeeds", func(t *testing.T) {
		_, err := Table("users")
		require.NoError(t, err)
	})

	t.Run("Valid alias succeeds", func(t *testing.T) {
		table, err := Table("users")
		require.NoError(t, err)
		_, err = table.As("u")
		require.NoError(t, err)
	})
}

func TestMustTable(t *testing.T) {
	t.Run("Valid table returns successfully", func(t *testing.T) {
		table := MustTable("users")
		assert.Equal(t, "users", table.Name())
	})

	t.Run("Invalid table panics", func(t *testing.T) {
		assert.Panics(t, func() {
			MustTable("")
		})
	})
}

func TestMustAs(t *testing.T) {
	t.Run("Valid alias returns successfully", func(t *testing.T) {
		table := MustTable("users")
		aliased := table.MustAs("u")
		assert.Equal(t, "u", aliased.Alias())
	})

	t.Run("Invalid alias panics", func(t *testing.T) {
		table := MustTable("users")
		assert.Panics(t, func() {
			table.MustAs("")
		})
	})
}
