//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

const (
	countClause = "COUNT(*)"
)

func TestExpr(t *testing.T) {
	t.Run("Valid expression without alias", func(t *testing.T) {
		expr := Expr(countClause)

		assert.Equal(t, countClause, expr.SQL)
		assert.Equal(t, "", expr.Alias)
	})

	t.Run("Valid expression with alias", func(t *testing.T) {
		expr := Expr("SUM(amount)", "total")

		assert.Equal(t, "SUM(amount)", expr.SQL)
		assert.Equal(t, "total", expr.Alias)
	})

	t.Run("Valid expression with complex SQL", func(t *testing.T) {
		expr := Expr("COALESCE(email, phone, 'N/A')", "contact")

		assert.Equal(t, "COALESCE(email, phone, 'N/A')", expr.SQL)
		assert.Equal(t, "contact", expr.Alias)
	})

	t.Run("Valid expression with window function", func(t *testing.T) {
		expr := Expr("ROW_NUMBER() OVER (PARTITION BY category ORDER BY date)", "row_num")

		assert.Equal(t, "ROW_NUMBER() OVER (PARTITION BY category ORDER BY date)", expr.SQL)
		assert.Equal(t, "row_num", expr.Alias)
	})

	t.Run("Valid expression with calculation", func(t *testing.T) {
		expr := Expr("price * quantity", "line_total")

		assert.Equal(t, "price * quantity", expr.SQL)
		assert.Equal(t, "line_total", expr.Alias)
	})

	t.Run("Empty SQL panics", func(t *testing.T) {
		assert.PanicsWithValue(t, "expression SQL cannot be empty", func() {
			Expr("")
		})
	})

	t.Run("Whitespace-only SQL panics", func(t *testing.T) {
		assert.PanicsWithValue(t, "expression SQL cannot be empty", func() {
			Expr("   ")
		})
	})

	t.Run("Multiple aliases panic", func(t *testing.T) {
		assert.PanicsWithValue(t, "Expr accepts maximum 1 alias, got 2", func() {
			Expr(countClause, "total", "count")
		})
	})

	t.Run("Alias with semicolon panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Expr(countClause, "total;DROP TABLE users")
		})
	})

	t.Run("Alias with single quote panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Expr(countClause, "total'")
		})
	})

	t.Run("Alias with double quote panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Expr(countClause, "total\"")
		})
	})

	t.Run("Alias with SQL comment panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Expr(countClause, "total--")
		})
	})

	t.Run("Alias with block comment start panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Expr(countClause, "total/*")
		})
	})

	t.Run("Alias with block comment end panics", func(t *testing.T) {
		assert.Panics(t, func() {
			Expr(countClause, "total*/")
		})
	})
}
