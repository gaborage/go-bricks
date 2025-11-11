package testing

import (
	"context"
	"testing"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
)

const (
	testQuery = "SELECT * FROM users"
)

// TestTestDBQueryRow tests the QueryRow method of TestDB.
func TestTestDBQueryRow(t *testing.T) {
	t.Run("returns expected row", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id", "name").AddRow(int64(123), "Alice"))

		row := db.QueryRow(context.Background(), "SELECT * FROM users WHERE id = $1", 123)

		var id int64
		var name string
		err := row.Scan(&id, &name)

		assert.NoError(t, err)
		assert.Equal(t, int64(123), id)
		assert.Equal(t, "Alice", name)
	})

	t.Run("logs query call", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id").AddRow(int64(1)))

		db.QueryRow(context.Background(), testQuery, nil)

		log := db.GetQueryLog()
		assert.Len(t, log, 1)
		assert.Equal(t, testQuery, log[0].SQL)
	})
}

// TestTestDBQuery tests the Query method of TestDB.
func TestTestDBQuery(t *testing.T) {
	t.Run("returns multiple rows", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id", "name").
				AddRow(int64(1), "Alice").
				AddRow(int64(2), "Bob"))

		rows, err := db.Query(context.Background(), testQuery)
		assert.NoError(t, err)
		defer rows.Close()

		var ids []int64
		for rows.Next() {
			var id int64
			var name string
			assert.NoError(t, rows.Scan(&id, &name))
			ids = append(ids, id)
		}

		assert.NoError(t, rows.Err())
		assert.Equal(t, []int64{1, 2}, ids)
	})

	t.Run("handles empty row sets", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id", "name"))

		rows, err := db.Query(context.Background(), "SELECT * FROM users WHERE 1=0")
		assert.NoError(t, err)
		defer rows.Close()

		assert.False(t, rows.Next())
		assert.NoError(t, rows.Err())
	})
}

// TestTestDBExec tests the Exec method of TestDB.
func TestTestDBExec(t *testing.T) {
	t.Run("returns rows affected", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectExec("INSERT").
			WillReturnRowsAffected(5)

		result, err := db.Exec(context.Background(), "INSERT INTO users VALUES ($1, $2)", "Alice", "alice@example.com")

		assert.NoError(t, err)
		affected, _ := result.RowsAffected()
		assert.Equal(t, int64(5), affected)
	})

	t.Run("logs exec call", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectExec("DELETE").
			WillReturnRowsAffected(1)

		db.Exec(context.Background(), "DELETE FROM users WHERE id = $1", 123)

		log := db.GetExecLog()
		assert.Len(t, log, 1)
		assert.Equal(t, "DELETE FROM users WHERE id = $1", log[0].SQL)
	})
}

// TestTestDBDatabaseType tests the DatabaseType method of TestDB.
func TestTestDBDatabaseType(t *testing.T) {
	t.Run("returns configured vendor", func(t *testing.T) {
		db := NewTestDB(dbtypes.Oracle)
		assert.Equal(t, dbtypes.Oracle, db.DatabaseType())
	})
}

// TestTestTxCommitRollback tests the Commit and Rollback methods of TestTx.
func TestTestTxCommitRollback(t *testing.T) {
	t.Run("tracks commit", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction()

		txInstance, err := db.Begin(context.Background())
		assert.NoError(t, err)

		err = txInstance.Commit()
		assert.NoError(t, err)
		assert.True(t, tx.IsCommitted())
		assert.False(t, tx.IsRolledBack())
	})

	t.Run("tracks rollback", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction()

		txInstance, err := db.Begin(context.Background())
		assert.NoError(t, err)

		err = txInstance.Rollback()
		assert.NoError(t, err)
		assert.False(t, tx.IsCommitted())
		assert.True(t, tx.IsRolledBack())
	})
}

// TestTestTxExpectationOrdering tests that expectations within a TestTx
// are matched in the order they were defined.
func TestTestTxExpectationOrdering(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().
		ExpectQuery("FIRST").WillReturnRows(NewRowSet("id").AddRow(int64(1))).
		ExpectQuery("SECOND").WillReturnRows(NewRowSet("id").AddRow(int64(2))).
		ExpectExec("INSERT FIRST").WillReturnRowsAffected(1).
		ExpectExec("INSERT SECOND").WillReturnRowsAffected(5)

	txConn, err := db.Begin(context.Background())
	assert.NoError(t, err)

	row := txConn.QueryRow(context.Background(), "SELECT FIRST")
	var first int64
	assert.NoError(t, row.Scan(&first))
	assert.Equal(t, int64(1), first)

	row = txConn.QueryRow(context.Background(), "SELECT SECOND")
	var second int64
	assert.NoError(t, row.Scan(&second))
	assert.Equal(t, int64(2), second)

	result, err := txConn.Exec(context.Background(), "INSERT FIRST")
	assert.NoError(t, err)
	rowsAffected, err := result.RowsAffected()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), rowsAffected)

	result, err = txConn.Exec(context.Background(), "INSERT SECOND")
	assert.NoError(t, err)
	rowsAffected, err = result.RowsAffected()
	assert.NoError(t, err)
	assert.Equal(t, int64(5), rowsAffected)
}

// TestAssertions tests the assertion helper functions.
func TestAssertions(t *testing.T) {
	t.Run("AssertQueryExecuted passes when query executed", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id").AddRow(int64(1)))

		db.QueryRow(context.Background(), testQuery)

		// Should not fail
		AssertQueryExecuted(t, db, "SELECT")
	})

	t.Run("AssertExecExecuted passes when exec executed", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectExec("INSERT").
			WillReturnRowsAffected(1)

		db.Exec(context.Background(), "INSERT INTO users VALUES (1, 'Alice')")

		// Should not fail
		AssertExecExecuted(t, db, "INSERT")
	})
}

// TestRowSet tests the RowSet struct and its methods.
func TestRowSet(t *testing.T) {
	t.Run("AddRow builds rows", func(t *testing.T) {
		rs := NewRowSet("id", "name", "email").
			AddRow(int64(1), "Alice", "alice@example.com").
			AddRow(int64(2), "Bob", "bob@example.com")

		assert.Equal(t, 2, rs.RowCount())
		assert.Equal(t, []string{"id", "name", "email"}, rs.Columns())
	})

	t.Run("AddRows with generator", func(t *testing.T) {
		rs := NewRowSet("id", "name").
			AddRows(3, func(i int) []any {
				return []any{int64(i + 1), "User" + string(rune('A'+i))}
			})

		assert.Equal(t, 3, rs.RowCount())
	})

	t.Run("handles pointer values", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Test with mix of nil pointers and non-nil pointers
		name := "Alice"
		var nilName *string
		age := int64(30)

		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("name", "age", "email").
				AddRow(&name, &age, nilName))

		row := db.QueryRow(context.Background(), "SELECT * FROM users")

		var scannedName string
		var scannedAge int64
		var scannedEmail *string

		err := row.Scan(&scannedName, &scannedAge, &scannedEmail)
		assert.NoError(t, err)
		assert.Equal(t, "Alice", scannedName)
		assert.Equal(t, int64(30), scannedAge)
		assert.Nil(t, scannedEmail)
	})
}

// TestDeterministicExpectationMatching verifies that expectations are matched
// in insertion order (first-match wins) when multiple patterns could match.
func TestDeterministicExpectationMatching(t *testing.T) {
	t.Run("first query expectation wins with overlapping patterns", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Add overlapping expectations - both match testQuery
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id").AddRow(int64(1)))
		db.ExpectQuery("SELECT * FROM").
			WillReturnRows(NewRowSet("id").AddRow(int64(2)))

		// First expectation should match
		row := db.QueryRow(context.Background(), testQuery)
		var id int64
		err := row.Scan(&id)
		assert.NoError(t, err)
		assert.Equal(t, int64(1), id, "first expectation should match")
	})

	t.Run("first exec expectation wins with overlapping patterns", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Add overlapping expectations - both match "INSERT INTO users"
		db.ExpectExec("INSERT").
			WillReturnRowsAffected(10)
		db.ExpectExec("INSERT INTO").
			WillReturnRowsAffected(20)

		// First expectation should match
		result, err := db.Exec(context.Background(), "INSERT INTO users VALUES (1, 'Alice')")
		assert.NoError(t, err)
		affected, _ := result.RowsAffected()
		assert.Equal(t, int64(10), affected, "first expectation should match")
	})

	t.Run("allows duplicate patterns with deterministic order", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Add duplicate patterns - first one should always match
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id").AddRow(int64(100)))
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("id").AddRow(int64(200)))

		// First expectation should always match
		row := db.QueryRow(context.Background(), testQuery)
		var id int64
		err := row.Scan(&id)
		assert.NoError(t, err)
		assert.Equal(t, int64(100), id, "first duplicate expectation should match")

		// Subsequent queries also match the first expectation
		row = db.QueryRow(context.Background(), "SELECT * FROM orders")
		err = row.Scan(&id)
		assert.NoError(t, err)
		assert.Equal(t, int64(100), id, "first expectation matches subsequent queries")
	})
}
