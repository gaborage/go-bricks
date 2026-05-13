package types

import (
	"errors"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// NewRowFromSQL / sqlRowAdapter — covers the previously-0% wrapper + the
// nil-safety branches in Scan() and Err(), plus the delegation paths using
// sqlmock to produce a real *sql.Row.
// ---------------------------------------------------------------------------

func TestNewRowFromSQLReturnsNilWhenInputIsNil(t *testing.T) {
	assert.Nil(t, NewRowFromSQL(nil),
		"contract: a nil *sql.Row produces a nil Row so callers can short-circuit")
}

func TestNewRowFromSQLWrapsNonNilRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(42))
	row := db.QueryRowContext(t.Context(), "SELECT 1")

	wrapped := NewRowFromSQL(row)
	require.NotNil(t, wrapped)

	var got int
	require.NoError(t, wrapped.Scan(&got))
	assert.Equal(t, 42, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSqlRowAdapterScanReturnsErrorWhenReceiverIsNil(t *testing.T) {
	var r *sqlRowAdapter // typed nil receiver
	err := r.Scan(new(int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "underlying sql.Row is nil")
}

func TestSqlRowAdapterScanReturnsErrorWhenInnerRowIsNil(t *testing.T) {
	r := &sqlRowAdapter{row: nil}
	err := r.Scan(new(int))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "underlying sql.Row is nil")
}

func TestSqlRowAdapterErrReturnsErrorWhenReceiverIsNil(t *testing.T) {
	var r *sqlRowAdapter
	err := r.Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "underlying sql.Row is nil")
}

func TestSqlRowAdapterErrDelegatesToUnderlyingSQLRow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// Use a query error (not a row error) so sql.Row.Err() reports it on
	// the first Err() call without needing a prior Scan. This exercises
	// the r.row.Err() delegation branch directly.
	mock.ExpectQuery("SELECT err").WillReturnError(errors.New("driver fault"))
	row := db.QueryRowContext(t.Context(), "SELECT err")
	wrapped := NewRowFromSQL(row)
	require.NotNil(t, wrapped)

	assert.Error(t, wrapped.Err(), "deferred query error must surface through Err() delegation")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// ValidateSubquery — covers the previously-uncovered subqueryValidator
// fast-path branches (success and error). Existing tests cover the
// nil/invalid-ToSQL/empty-SQL paths via plain mocks; here we use mocks
// that ALSO implement subqueryValidator to exercise the early-return
// code path before ToSQL is even called.
// ---------------------------------------------------------------------------

// mockValidatingSubquery implements both SelectQueryBuilder and the unexported
// subqueryValidator interface. ValidateForSubquery is wired by the caller.
type mockValidatingSubquery struct {
	mockValidSubquery // reuse the existing valid-mock for the SelectQueryBuilder surface
	validateErr       error
}

func (m *mockValidatingSubquery) ValidateForSubquery() error { return m.validateErr }

func TestValidateSubqueryReturnsEarlyOnValidatorInterfaceSuccess(t *testing.T) {
	subquery := &mockValidatingSubquery{validateErr: nil}
	require.NoError(t, ValidateSubquery(subquery))
}

func TestValidateSubqueryWrapsValidatorInterfaceError(t *testing.T) {
	subquery := &mockValidatingSubquery{validateErr: errors.New("missing FROM clause")}
	err := ValidateSubquery(subquery)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidSubquery),
		"validator error must be wrapped as ErrInvalidSubquery for caller pattern-matching")
	assert.Contains(t, err.Error(), "missing FROM clause")
}

// ---------------------------------------------------------------------------
// TableRef.As — covers the previously-uncovered nil-receiver branch.
// ---------------------------------------------------------------------------

func TestTableRefAsReturnsErrorOnNilReceiver(t *testing.T) {
	var t1 *TableRef // typed-nil receiver
	_, err := t1.As("x")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilTableRef),
		"nil receiver must fail with ErrNilTableRef rather than panic")
}
