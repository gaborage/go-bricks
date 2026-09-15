//revive:disable-next-line:var-naming // Package name "types" avoids circular imports.
package types

import (
	"context"
	"database/sql"
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

	require.Error(t, wrapped.Err(), "deferred query error must surface through Err() delegation")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// Interface.Session — compile-time proof that the session door is on the
// interface, so reaching it needs no type assertion.
// ---------------------------------------------------------------------------

// interfaceStub implements Interface: the assertion below it is what turns a
// method added to the interface into a compile error here.
type interfaceStub struct{}

func (s *interfaceStub) Query(context.Context, string, ...any) (*sql.Rows, error) { return nil, nil }
func (s *interfaceStub) QueryRow(context.Context, string, ...any) Row             { return nil }
func (s *interfaceStub) Exec(context.Context, string, ...any) (sql.Result, error) { return nil, nil }

func (s *interfaceStub) DatabaseType() string { return PostgreSQL }

func (s *interfaceStub) Begin(context.Context) (Tx, error) { return nil, nil }

func (s *interfaceStub) BeginTx(context.Context, *sql.TxOptions) (Tx, error) { return nil, nil }

func (s *interfaceStub) Prepare(context.Context, string) (Statement, error) { return nil, nil }
func (s *interfaceStub) Health(context.Context) error                       { return nil }
func (s *interfaceStub) Stats() (map[string]any, error)                     { return nil, nil }
func (s *interfaceStub) Close() error                                       { return nil }
func (s *interfaceStub) MigrationTable() string                             { return "flyway_schema_history" }
func (s *interfaceStub) CreateMigrationTable(context.Context) error         { return nil }

func (s *interfaceStub) Session(context.Context) (Session, error) { return nil, nil }

var _ Interface = (*interfaceStub)(nil)

// sessionStub is this package's own Session double: types cannot import
// database/testing, its tester, so the assertion below needs a local stub.
type sessionStub struct{}

func (s *sessionStub) Query(context.Context, string, ...any) (*sql.Rows, error) { return nil, nil }
func (s *sessionStub) QueryRow(context.Context, string, ...any) Row             { return nil }
func (s *sessionStub) Exec(context.Context, string, ...any) (sql.Result, error) { return nil, nil }
func (s *sessionStub) DatabaseType() string                                     { return PostgreSQL }
func (s *sessionStub) Begin(context.Context) (Tx, error)                        { return nil, nil }
func (s *sessionStub) BeginTx(context.Context, *sql.TxOptions) (Tx, error)      { return nil, nil }
func (s *sessionStub) Close() error                                             { return nil }

var _ Session = (*sessionStub)(nil)
