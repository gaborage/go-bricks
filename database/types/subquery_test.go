//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockValidSubquery is a mock SelectQueryBuilder that produces valid SQL
type mockValidSubquery struct{}

func (m *mockValidSubquery) Select(_ ...any) SelectQueryBuilder            { return m }
func (m *mockValidSubquery) From(_ ...any) SelectQueryBuilder              { return m }
func (m *mockValidSubquery) Where(_ Filter) SelectQueryBuilder             { return m }
func (m *mockValidSubquery) JoinOn(_ any, _ JoinFilter) SelectQueryBuilder { return m }
func (m *mockValidSubquery) LeftJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockValidSubquery) RightJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockValidSubquery) InnerJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockValidSubquery) CrossJoinOn(_ any) SelectQueryBuilder      { return m }
func (m *mockValidSubquery) GroupBy(_ ...any) SelectQueryBuilder       { return m }
func (m *mockValidSubquery) Having(_ any, _ ...any) SelectQueryBuilder { return m }
func (m *mockValidSubquery) OrderBy(_ ...any) SelectQueryBuilder       { return m }
func (m *mockValidSubquery) Limit(_ uint64) SelectQueryBuilder         { return m }
func (m *mockValidSubquery) Offset(_ uint64) SelectQueryBuilder        { return m }
func (m *mockValidSubquery) Paginate(_, _ uint64) SelectQueryBuilder   { return m }
func (m *mockValidSubquery) ToSQL() (sql string, args []any, err error) {
	return "SELECT id FROM test_table WHERE status = :1", []any{"active"}, nil
}

// mockInvalidSubquery is a mock SelectQueryBuilder that produces an error
type mockInvalidSubquery struct{}

func (m *mockInvalidSubquery) Select(_ ...any) SelectQueryBuilder            { return m }
func (m *mockInvalidSubquery) From(_ ...any) SelectQueryBuilder              { return m }
func (m *mockInvalidSubquery) Where(_ Filter) SelectQueryBuilder             { return m }
func (m *mockInvalidSubquery) JoinOn(_ any, _ JoinFilter) SelectQueryBuilder { return m }
func (m *mockInvalidSubquery) LeftJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockInvalidSubquery) RightJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockInvalidSubquery) InnerJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockInvalidSubquery) CrossJoinOn(_ any) SelectQueryBuilder      { return m }
func (m *mockInvalidSubquery) GroupBy(_ ...any) SelectQueryBuilder       { return m }
func (m *mockInvalidSubquery) Having(_ any, _ ...any) SelectQueryBuilder { return m }
func (m *mockInvalidSubquery) OrderBy(_ ...any) SelectQueryBuilder       { return m }
func (m *mockInvalidSubquery) Limit(_ uint64) SelectQueryBuilder         { return m }
func (m *mockInvalidSubquery) Offset(_ uint64) SelectQueryBuilder        { return m }
func (m *mockInvalidSubquery) Paginate(_, _ uint64) SelectQueryBuilder   { return m }
func (m *mockInvalidSubquery) ToSQL() (sql string, args []any, err error) {
	return "", nil, assert.AnError
}

// mockEmptySubquery is a mock SelectQueryBuilder that produces empty SQL
type mockEmptySubquery struct{}

func (m *mockEmptySubquery) Select(_ ...any) SelectQueryBuilder            { return m }
func (m *mockEmptySubquery) From(_ ...any) SelectQueryBuilder              { return m }
func (m *mockEmptySubquery) Where(_ Filter) SelectQueryBuilder             { return m }
func (m *mockEmptySubquery) JoinOn(_ any, _ JoinFilter) SelectQueryBuilder { return m }
func (m *mockEmptySubquery) LeftJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockEmptySubquery) RightJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockEmptySubquery) InnerJoinOn(_ any, _ JoinFilter) SelectQueryBuilder {
	return m
}
func (m *mockEmptySubquery) CrossJoinOn(_ any) SelectQueryBuilder      { return m }
func (m *mockEmptySubquery) GroupBy(_ ...any) SelectQueryBuilder       { return m }
func (m *mockEmptySubquery) Having(_ any, _ ...any) SelectQueryBuilder { return m }
func (m *mockEmptySubquery) OrderBy(_ ...any) SelectQueryBuilder       { return m }
func (m *mockEmptySubquery) Limit(_ uint64) SelectQueryBuilder         { return m }
func (m *mockEmptySubquery) Offset(_ uint64) SelectQueryBuilder        { return m }
func (m *mockEmptySubquery) Paginate(_, _ uint64) SelectQueryBuilder   { return m }
func (m *mockEmptySubquery) ToSQL() (sql string, args []any, err error) {
	return "", []any{}, nil
}

func TestValidateSubquery(t *testing.T) {
	t.Run("Valid subquery returns no error", func(t *testing.T) {
		subquery := &mockValidSubquery{}
		err := ValidateSubquery(subquery)
		require.NoError(t, err)
	})

	t.Run("Nil subquery returns error", func(t *testing.T) {
		err := ValidateSubquery(nil)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrNilSubquery))
	})

	t.Run("Invalid subquery returns error", func(t *testing.T) {
		subquery := &mockInvalidSubquery{}
		err := ValidateSubquery(subquery)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidSubquery))
	})

	t.Run("Empty SQL subquery returns error", func(t *testing.T) {
		subquery := &mockEmptySubquery{}
		err := ValidateSubquery(subquery)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrEmptySubquerySQL))
	})
}

func TestMustValidateSubquery(t *testing.T) {
	t.Run("Valid subquery does not panic", func(t *testing.T) {
		subquery := &mockValidSubquery{}
		assert.NotPanics(t, func() {
			MustValidateSubquery(subquery)
		})
	})

	t.Run("Invalid subquery panics", func(t *testing.T) {
		assert.Panics(t, func() {
			MustValidateSubquery(nil)
		})
	})
}
