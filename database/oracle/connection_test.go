package oracle

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
	mock.ExpectExec("INSERT INTO t").WithArgs("a").WillReturnResult(sqlmock.NewResult(1, 1))
	_, err = c.Exec(ctx, "INSERT INTO t(x) VALUES(?)", "a")
	require.NoError(t, err)

	// Query
	rows := sqlmock.NewRows([]string{"id"}).AddRow(1)
	mock.ExpectQuery("SELECT id FROM t").WillReturnRows(rows)
	rs, err := c.Query(ctx, "SELECT id FROM t")
	require.NoError(t, err)
	assert.True(t, rs.Next())
	_ = rs.Close()

	// QueryRow
	rows = sqlmock.NewRows([]string{"now"}).AddRow(time.Now())
	mock.ExpectQuery("SELECT CURRENT_TIMESTAMP").WillReturnRows(rows)
	_ = c.QueryRow(ctx, "SELECT CURRENT_TIMESTAMP")

	// Prepare + Statement Exec
	mock.ExpectPrepare("UPDATE t SET x").ExpectExec().WithArgs("b", 1).WillReturnResult(sqlmock.NewResult(0, 1))
	st, err := c.Prepare(ctx, "UPDATE t SET x=:1 WHERE id=:2")
	require.NoError(t, err)
	_, err = st.Exec(ctx, "b", 1)
	require.NoError(t, err)
	require.NoError(t, st.Close())

	// Begin + Tx methods
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT 1 FROM dual").WillReturnRows(sqlmock.NewRows([]string{"x"}).AddRow(1))
	mock.ExpectCommit()
	tx, err := c.Begin(ctx)
	require.NoError(t, err)
	rs2, err := tx.Query(ctx, "SELECT 1 FROM dual")
	require.NoError(t, err)
	_ = rs2.Close()
	require.NoError(t, tx.Commit())

	// BeginTx + rollback
	mock.ExpectBegin()
	mock.ExpectRollback()
	tx2, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelDefault})
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())

	// Stats
	m, err := c.Stats()
	require.NoError(t, err)
	assert.Contains(t, m, "max_open_connections")
	assert.Contains(t, m, "open_connections")
	assert.Contains(t, m, "wait_duration")

	// Meta
	assert.Equal(t, "oracle", c.DatabaseType())
	assert.Equal(t, "FLYWAY_SCHEMA_HISTORY", c.GetMigrationTable())

	// CreateMigrationTable should run two Execs
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))
	mock.ExpectExec("BEGIN").WillReturnResult(driver.RowsAffected(0))
	require.NoError(t, c.CreateMigrationTable(ctx))

	// Close
	mock.ExpectClose()
	require.NoError(t, c.Close())

	// Verify expectations
	require.NoError(t, mock.ExpectationsWereMet())
}
