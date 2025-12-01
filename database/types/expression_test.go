//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	countClause = "COUNT(*)"
)

func TestExpr(t *testing.T) {
	t.Run("Valid expression without alias", func(t *testing.T) {
		expr, err := Expr(countClause)

		require.NoError(t, err)
		assert.Equal(t, countClause, expr.SQL)
		assert.Equal(t, "", expr.Alias)
	})

	t.Run("Valid expression with alias", func(t *testing.T) {
		expr, err := Expr("SUM(amount)", "total")

		require.NoError(t, err)
		assert.Equal(t, "SUM(amount)", expr.SQL)
		assert.Equal(t, "total", expr.Alias)
	})

	t.Run("Valid expression with complex SQL", func(t *testing.T) {
		expr, err := Expr("COALESCE(email, phone, 'N/A')", "contact")

		require.NoError(t, err)
		assert.Equal(t, "COALESCE(email, phone, 'N/A')", expr.SQL)
		assert.Equal(t, "contact", expr.Alias)
	})

	t.Run("Valid expression with window function", func(t *testing.T) {
		expr, err := Expr("ROW_NUMBER() OVER (PARTITION BY category ORDER BY date)", "row_num")

		require.NoError(t, err)
		assert.Equal(t, "ROW_NUMBER() OVER (PARTITION BY category ORDER BY date)", expr.SQL)
		assert.Equal(t, "row_num", expr.Alias)
	})

	t.Run("Valid expression with calculation", func(t *testing.T) {
		expr, err := Expr("price * quantity", "line_total")

		require.NoError(t, err)
		assert.Equal(t, "price * quantity", expr.SQL)
		assert.Equal(t, "line_total", expr.Alias)
	})

	t.Run("Empty SQL returns error", func(t *testing.T) {
		_, err := Expr("")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyExpressionSQL))
	})

	t.Run("Whitespace-only SQL returns error", func(t *testing.T) {
		_, err := Expr("   ")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptyExpressionSQL))
	})

	t.Run("Multiple aliases return error", func(t *testing.T) {
		_, err := Expr(countClause, "total", "count")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrTooManyAliases))
	})

	t.Run("Alias with semicolon returns error", func(t *testing.T) {
		_, err := Expr(countClause, "total;DROP TABLE users")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDangerousAlias))
	})

	t.Run("Alias with single quote returns error", func(t *testing.T) {
		_, err := Expr(countClause, "total'")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDangerousAlias))
	})

	t.Run("Alias with double quote returns error", func(t *testing.T) {
		_, err := Expr(countClause, "total\"")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDangerousAlias))
	})

	t.Run("Alias with SQL comment returns error", func(t *testing.T) {
		_, err := Expr(countClause, "total--")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDangerousAlias))
	})

	t.Run("Alias with block comment start returns error", func(t *testing.T) {
		_, err := Expr(countClause, "total/*")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDangerousAlias))
	})

	t.Run("Alias with block comment end returns error", func(t *testing.T) {
		_, err := Expr(countClause, "total*/")
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrDangerousAlias))
	})
}

func TestMustExpr(t *testing.T) {
	t.Run("Valid expression returns successfully", func(t *testing.T) {
		expr := MustExpr(countClause, "total")
		assert.Equal(t, countClause, expr.SQL)
		assert.Equal(t, "total", expr.Alias)
	})

	t.Run("Invalid expression panics", func(t *testing.T) {
		assert.Panics(t, func() {
			MustExpr("")
		})
	})
}
