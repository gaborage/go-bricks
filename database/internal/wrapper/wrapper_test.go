package wrapper

import (
	"context"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

// TestPooledTransactionQuerySkipsSessionProbe pins that NewTransaction (the
// pooled Connection.Begin path) does not probe a nil session: Query reaches
// the driver. Negating sessionDone's nil check would panic on that path.
func TestPooledTransactionQuerySkipsSessionProbe(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT 1").WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectCommit()

	sqlTx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	tx := NewTransaction(sqlTx)

	rows, err := tx.Query(context.Background(), "SELECT 1")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	require.NoError(t, tx.Commit(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet())
}
