package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

func TestConnection_BasicMethods_WithSQLMock(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()

	c := &Connection{db: db, logger: logger.New("disabled", true)}

	ctx := context.Background()

	// Health
	mock.ExpectPing()
	require.NoError(t, c.Health(ctx))

	// Exec
	mock.ExpectExec("INSERT INTO items").WithArgs("a").WillReturnResult(sqlmock.NewResult(1, 1))
	_, err = c.Exec(ctx, "INSERT INTO items(name) VALUES($1)", "a")
	require.NoError(t, err)

	// Query
	rows := sqlmock.NewRows([]string{"id", "name"}).AddRow(1, "x")
	mock.ExpectQuery("SELECT id, name FROM items").WillReturnRows(rows)
	rs, err := c.Query(ctx, "SELECT id, name FROM items")
	require.NoError(t, err)
	assert.True(t, rs.Next())
	_ = rs.Close()

	// QueryRow
	rows = sqlmock.NewRows([]string{"now"}).AddRow(time.Now())
	mock.ExpectQuery(`SELECT NOW\(\)`).WillReturnRows(rows)
	_ = c.QueryRow(ctx, "SELECT NOW()")

	// Prepare + Statement Exec
	mock.ExpectPrepare("UPDATE items SET name").ExpectExec().WithArgs("b", 1).WillReturnResult(sqlmock.NewResult(0, 1))
	st, err := c.Prepare(ctx, "UPDATE items SET name=$1 WHERE id=$2")
	require.NoError(t, err)
	_, err = st.Exec(ctx, "b", 1)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// Begin + Tx methods
	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM items").WithArgs(1).WillReturnResult(driver.RowsAffected(1))
	mock.ExpectCommit()
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "DELETE FROM items WHERE id=$1", 1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// BeginTx + rollback
	mock.ExpectBegin()
	mock.ExpectRollback()
	opts := &sql.TxOptions{Isolation: sql.LevelDefault}
	tx2, err := c.BeginTx(ctx, opts)
	// ensure tx2 is non-nil and rollback works
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())

	// Stats
	m, err := c.Stats()
	require.NoError(t, err)
	assert.Contains(t, m, "max_open_connections")
	assert.Contains(t, m, "open_connections")
	assert.Contains(t, m, "wait_duration")

	// Close
	mock.ExpectClose()
	require.NoError(t, c.Close())

	// Verify all expectations
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestConnection_Metadata(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectClose()

	c := &Connection{db: db, logger: logger.New("disabled", true)}

	assert.Equal(t, "postgresql", c.DatabaseType())
	assert.Equal(t, "flyway_schema_history", c.GetMigrationTable())
	assert.NoError(t, c.Close())
}
