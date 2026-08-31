package builder

import (
	"testing"

	"github.com/Masterminds/squirrel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// ========== combineLogical ==========

// otherFilter is a dbtypes.Filter implementation that is NOT the package's own
// concrete Filter type, exercising combineLogical's fallback branch: a
// caller-supplied Sqlizer used as-is rather than unwrapped.
type otherFilter struct {
	sql string
}

func (o otherFilter) ToSql() (sql string, args []any, err error) { return o.sql, nil, nil }
func (o otherFilter) ToSQL() (sql string, args []any, err error) { return o.ToSql() }

var _ dbtypes.Filter = otherFilter{}
var _ dbtypes.JoinFilter = otherFilter{}

func TestCombineLogicalSkipsNilFilters(t *testing.T) {
	filters := []dbtypes.Filter{
		nil,
		Filter{sqlizer: squirrel.Expr("a = ?", 1)},
		nil,
		Filter{sqlizer: squirrel.Expr("b = ?", 2)},
	}

	combined := combineLogical[dbtypes.Filter, squirrel.And](filters, classifyFilter, wrapFilter)

	sql, args, err := combined.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(a = ? AND b = ?)", sql)
	assert.Equal(t, []any{1, 2}, args)
}

func TestCombineLogicalAllNilProducesEmptyContainer(t *testing.T) {
	t.Run("and_renders_squirrels_own_empty_constant", func(t *testing.T) {
		combined := combineLogical[dbtypes.Filter, squirrel.And]([]dbtypes.Filter{nil, nil}, classifyFilter, wrapFilter)
		sql, args, err := combined.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "(1=1)", sql)
		assert.Empty(t, args)
	})

	t.Run("or_renders_squirrels_own_empty_constant", func(t *testing.T) {
		combined := combineLogical[dbtypes.Filter, squirrel.Or]([]dbtypes.Filter{nil, nil}, classifyFilter, wrapFilter)
		sql, args, err := combined.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "(1=0)", sql)
		assert.Empty(t, args)
	})
}

func TestCombineLogicalUsesOrContainer(t *testing.T) {
	filters := []dbtypes.JoinFilter{
		JoinFilter{sqlizer: squirrel.Expr("a.id = b.id")},
		JoinFilter{sqlizer: squirrel.Expr("a.id = c.id")},
	}

	combined := combineLogical[dbtypes.JoinFilter, squirrel.Or](filters, classifyJoinFilter, wrapJoinFilter)

	sql, _, err := combined.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "(a.id = b.id OR a.id = c.id)", sql)
}

func TestClassifyFilterFallsBackToNonConcreteImplementation(t *testing.T) {
	sqlizer, include := classifyFilter(otherFilter{sql: "x = 1"})
	assert.True(t, include)
	sql, _, err := sqlizer.ToSql()
	require.NoError(t, err)
	assert.Equal(t, "x = 1", sql)
}

func TestClassifyJoinFilterFallsBackToNonConcreteImplementation(t *testing.T) {
	sqlizer, include := classifyJoinFilter(otherFilter{sql: "a.id = b.id"})
	assert.True(t, include)
	sql, _, err := sqlizer.ToSql()
	require.NoError(t, err)
	assert.Equal(t, "a.id = b.id", sql)
}

func TestClassifyFilterSkipsNil(t *testing.T) {
	sqlizer, include := classifyFilter(nil)
	assert.False(t, include)
	assert.Nil(t, sqlizer)
}

func TestClassifyJoinFilterSkipsNil(t *testing.T) {
	sqlizer, include := classifyJoinFilter(nil)
	assert.False(t, include)
	assert.Nil(t, sqlizer)
}

// ========== inListPredicate ==========

func eqComparison(quotedColumn string, normalized any) squirrel.Sqlizer {
	return squirrel.Eq{quotedColumn: normalized}
}

func notEqComparison(quotedColumn string, normalized any) squirrel.Sqlizer {
	return squirrel.NotEq{quotedColumn: normalized}
}

func TestInListPredicateEmptyListRendersTheCallersConstant(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	t.Run("in_renders_always_false", func(t *testing.T) {
		predicate := inListPredicate(qb, "status", nil, "IN", "(1=0)", eqComparison, wrapFilter)
		sql, args, err := predicate.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "(1=0)", sql)
		assert.Empty(t, args)
	})

	t.Run("not_in_renders_always_true", func(t *testing.T) {
		predicate := inListPredicate(qb, "status", nil, "NOT IN", "(1=1)", notEqComparison, wrapFilter)
		sql, args, err := predicate.ToSQL()
		require.NoError(t, err)
		assert.Equal(t, "(1=1)", sql)
		assert.Empty(t, args)
	})
}

func TestInListPredicateNonEmptyListUsesTheCallersComparison(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	predicate := inListPredicate(qb, "status", []string{"active", "pending"}, "IN", "(1=0)", eqComparison, wrapFilter)
	sql, args, err := predicate.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "status IN (?,?)", sql)
	assert.Equal(t, []any{"active", "pending"}, args)
}

func TestInListPredicateSurfacesColumnValidationErrors(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	predicate := inListPredicate(qb, `id) OR 1=1 --`, []string{"x"}, "IN", "(1=0)", eqComparison, wrapJoinFilter)
	_, _, err := predicate.ToSQL()
	require.Error(t, err)
	require.ErrorContains(t, err, "identifier")
}

func TestInListPredicateSurfacesOperandResolutionErrors(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	// A cyclic pointer chain is the one shape resolveListOperands cannot resolve
	// via a scalar (non-slice) operand, so it is the shortest path to the
	// resolution-error branch rather than the column-validation one.
	var v any
	v = &v

	predicate := inListPredicate(qb, "status", v, "IN", "(1=0)", eqComparison, wrapFilter)
	_, _, err := predicate.ToSQL()
	require.Error(t, err)
}

// ========== nullPredicate ==========

func TestNullPredicateRendersIsNullByteForByte(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	predicate := nullPredicate(qb, "deleted_at",
		func(quotedColumn string) squirrel.Sqlizer { return squirrel.Eq{quotedColumn: nil} },
		wrapFilter)

	sql, args, err := predicate.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "deleted_at IS NULL", sql)
	assert.Empty(t, args)
}

func TestNullPredicateRendersIsNotNullByteForByte(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	predicate := nullPredicate(qb, "email",
		func(quotedColumn string) squirrel.Sqlizer { return squirrel.NotEq{quotedColumn: nil} },
		wrapJoinFilter)

	sql, args, err := predicate.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "email IS NOT NULL", sql)
	assert.Empty(t, args)
}

func TestNullPredicateSurfacesColumnValidationErrors(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.PostgreSQL)

	predicate := nullPredicate(qb, `id) OR 1=1 --`,
		func(quotedColumn string) squirrel.Sqlizer { return squirrel.Eq{quotedColumn: nil} },
		wrapFilter)

	_, _, err := predicate.ToSQL()
	require.Error(t, err)
	require.ErrorContains(t, err, "identifier")
}

func TestNullPredicateQuotesOracleReservedWords(t *testing.T) {
	qb := NewQueryBuilder(dbtypes.Oracle)

	predicate := nullPredicate(qb, "level",
		func(quotedColumn string) squirrel.Sqlizer { return squirrel.Eq{quotedColumn: nil} },
		wrapJoinFilter)

	sql, _, err := predicate.ToSQL()
	require.NoError(t, err)
	assert.Equal(t, `"level" IS NULL`, sql)
}
