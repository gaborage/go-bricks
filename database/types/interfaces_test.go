//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
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
