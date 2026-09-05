//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
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
		assert.Empty(t, expr.Alias)
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
		assert.ErrorIs(t, err, ErrEmptyExpressionSQL)
	})

	t.Run("Whitespace-only SQL returns error", func(t *testing.T) {
		_, err := Expr("   ")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrEmptyExpressionSQL)
	})

	t.Run("Multiple aliases return error", func(t *testing.T) {
		_, err := Expr(countClause, "total", "count")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTooManyAliases)
	})

	// The alias is judged against the shared bare-identifier grammar, not a
	// substring denylist: the denylist accepted everything it did not enumerate,
	// so a space, a parenthesis or a newline passed straight into the AS clause
	// (#1164). The quoted form is rejected too — the framework never emits a
	// quoted alias, so accepting one would widen the grammar for caller text alone.
	t.Run("alias grammar", func(t *testing.T) {
		accepted := []string{"total", "Total_1", "_x", "a$b", "c#d"}
		for _, alias := range accepted {
			t.Run("accepts_"+alias, func(t *testing.T) {
				expr, err := Expr(countClause, alias)
				require.NoError(t, err)
				assert.Equal(t, alias, expr.Alias)
			})
		}

		rejected := map[string]string{
			"space":             "my alias",
			"call":              "f(x)",
			"semicolon":         "total;DROP TABLE users",
			"single_quote":      "total'",
			"double_quote":      "total\"",
			"line_comment":      "total--",
			"block_open":        "total/*",
			"block_close":       "total*/",
			"newline":           "a\nb",
			"quoted_identifier": "\"quoted\"",
			"backtick":          "`bt`",
			"subquery":          "a, (SELECT password FROM users) b",
			"leading_digit":     "1total",
		}
		for name, alias := range rejected {
			t.Run("rejects_"+name, func(t *testing.T) {
				_, err := Expr(countClause, alias)
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrInvalidAlias)
			})
		}
	})

	// An empty alias is "no alias", not a grammar violation.
	t.Run("empty alias is accepted", func(t *testing.T) {
		expr, err := Expr(countClause)
		require.NoError(t, err)
		assert.Empty(t, expr.Alias)
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
