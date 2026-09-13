package wrapper

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database/types"
)

const sessionVendor = "postgresql"

func openMockSession(t *testing.T) (*sql.DB, sqlmock.Sqlmock, types.Session) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	sess, err := (&Connection{DB: db}).OpenSession(context.Background(), sessionVendor)
	require.NoError(t, err)
	return db, mock, sess
}

func TestSessionPinsOneConnectionUntilClose(t *testing.T) {
	db, mock, sess := openMockSession(t)
	ctx := context.Background()

	mock.ExpectExec("SET search_path").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("SELECT 2").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(2))

	_, err := sess.Exec(ctx, "SET search_path TO app")
	require.NoError(t, err)
	rows, err := sess.Query(ctx, "SELECT 1")
	require.NoError(t, err)
	require.NoError(t, rows.Close())
	var n int
	require.NoError(t, sess.QueryRow(ctx, "SELECT 2").Scan(&n))
	assert.Equal(t, 2, n)
	assert.Equal(t, 1, db.Stats().InUse)
	assert.Equal(t, sessionVendor, sess.DatabaseType())

	require.NoError(t, sess.Close())
	assert.Equal(t, 0, db.Stats().InUse)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSessionUseAfterCloseReturnsErrConnDone(t *testing.T) {
	_, _, sess := openMockSession(t)
	ctx := context.Background()
	require.NoError(t, sess.Close())

	_, err := sess.Exec(ctx, "SELECT 1")
	require.ErrorIs(t, err, sql.ErrConnDone)
	_, err = sess.Query(ctx, "SELECT 1")
	require.ErrorIs(t, err, sql.ErrConnDone)
	require.ErrorIs(t, sess.QueryRow(ctx, "SELECT 1").Scan(new(int)), sql.ErrConnDone)
	_, err = sess.Begin(ctx)
	require.ErrorIs(t, err, sql.ErrConnDone)
}

func TestSessionBadConnSurfacesErrConnDone(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, types.Session, sqlmock.Sqlmock) error
	}{
		{name: "exec", call: func(ctx context.Context, s types.Session, m sqlmock.Sqlmock) error {
			m.ExpectExec("SELECT 1").WillReturnError(driver.ErrBadConn)
			_, err := s.Exec(ctx, "SELECT 1")
			return err
		}},
		{name: "query", call: func(ctx context.Context, s types.Session, m sqlmock.Sqlmock) error {
			m.ExpectQuery("SELECT 1").WillReturnError(driver.ErrBadConn)
			_, err := s.Query(ctx, "SELECT 1")
			return err
		}},
		{name: "query_row", call: func(ctx context.Context, s types.Session, m sqlmock.Sqlmock) error {
			m.ExpectQuery("SELECT 1").WillReturnError(driver.ErrBadConn)
			return s.QueryRow(ctx, "SELECT 1").Err()
		}},
		{name: "begin_tx", call: func(ctx context.Context, s types.Session, m sqlmock.Sqlmock) error {
			m.ExpectBegin().WillReturnError(driver.ErrBadConn)
			_, err := s.BeginTx(ctx, nil)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, mock, sess := openMockSession(t)
			ctx := context.Background()

			require.ErrorIs(t, tt.call(ctx, sess, mock), sql.ErrConnDone)
			_, err := sess.Exec(ctx, "SELECT 1")
			require.ErrorIs(t, err, sql.ErrConnDone)
		})
	}
}

func TestSessionBeginTxCommitsOnPinnedConnection(t *testing.T) {
	db, mock, sess := openMockSession(t)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO t").WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	tx, err := sess.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err)
	assert.IsType(t, &Transaction{}, tx)
	_, err = tx.Exec(ctx, "INSERT INTO t VALUES (1)")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
	assert.Equal(t, 1, db.Stats().InUse)

	mock.ExpectBegin()
	mock.ExpectRollback()
	tx, err = sess.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestOpenSessionPropagatesAcquireError also pins the interface VALUE on
// failure: a plain `!=` catches a typed nil, which reflection-based
// assert.Nil/require.Nil would pass.
func TestOpenSessionPropagatesAcquireError(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	mock.ExpectClose()
	require.NoError(t, db.Close())

	sess, err := (&Connection{DB: db}).OpenSession(context.Background(), sessionVendor)
	require.Error(t, err)
	if sess != nil {
		t.Fatalf("typed-nil session returned: %T", sess)
	}
}

// TestSessionRowsIterationErrorIsNotTranslated pins the documented EXCEPTION to
// the sql.ErrConnDone promise: Query hands back a raw *sql.Rows, so a failure
// that only shows up mid-stream surfaces through rows.Next/rows.Err exactly as
// the driver reported it — wrapConnErr never sees it. types.Session's doc states
// this exception; this test is what makes the doc falsifiable.
func TestSessionRowsIterationErrorIsNotTranslated(t *testing.T) {
	_, mock, sess := openMockSession(t)
	ctx := context.Background()

	mock.ExpectQuery("SELECT n").WillReturnRows(
		sqlmock.NewRows([]string{"n"}).AddRow(1).RowError(0, driver.ErrBadConn))

	rows, err := sess.Query(ctx, "SELECT n FROM t")
	require.NoError(t, err, "the failure is deferred to iteration, so Query itself succeeds")
	defer func() { _ = rows.Close() }()

	assert.False(t, rows.Next())
	iterErr := rows.Err()
	require.Error(t, iterErr)
	assert.ErrorIs(t, iterErr, driver.ErrBadConn, "the driver error reaches the caller raw")
	assert.NotErrorIs(t, iterErr, sql.ErrConnDone,
		"documented exception: rows-iteration errors are NOT translated to sql.ErrConnDone")
}

// TestSessionCloseIsNotIdempotent pins the documented Close behaviour: a second
// Close returns sql.ErrConnDone rather than succeeding silently.
func TestSessionCloseIsNotIdempotent(t *testing.T) {
	_, _, sess := openMockSession(t)

	require.NoError(t, sess.Close())
	require.ErrorIs(t, sess.Close(), sql.ErrConnDone,
		"Close is not idempotent: the second call reports the connection is already done")
}
