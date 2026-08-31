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

// inListPredicate and nullPredicate have no direct tests here: every branch
// (empty/non-empty list, negate true/false, column-validation error, operand-
// resolution error, Oracle reserved-word quoting) is already exercised through
// both families' In/NotIn/Null/NotNull in filter_test.go and join_filter_test.go,
// plus the shared column-validation and cyclic-pointer suites
// (TestFilterColumnsValidateIdentifiers, TestOperandResolutionRefusesACyclicPointer).
// A byte-for-byte duplicate at this layer would not add coverage.
