package testing

import (
	"context"
	"testing"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
	"github.com/stretchr/testify/assert"
)

const (
	testQuery         = "SELECT * FROM users"
	testUnsignedQuery = "SELECT * FROM unsigned_test"
)

// mustParseTime parses a time string in RFC3339 format or fails the test.
func mustParseTime(t *testing.T, timeStr string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		t.Fatalf("failed to parse time %q: %v", timeStr, err)
	}
	return parsed
}

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

		log := db.QueryLog()
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

		log := db.ExecLog()
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

		err = txInstance.Commit(context.Background())
		assert.NoError(t, err)
		assert.True(t, tx.IsCommitted())
		assert.False(t, tx.IsRolledBack())
	})

	t.Run("tracks rollback", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction()

		txInstance, err := db.Begin(context.Background())
		assert.NoError(t, err)

		err = txInstance.Rollback(context.Background())
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
	defer txConn.Rollback(context.Background()) // No-op if committed

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

// TestMultipleTransactionExpectations tests that multiple transaction expectations
// can be queued and consumed in order.
func TestMultipleTransactionExpectations(t *testing.T) {
	t.Run("queues and consumes multiple transactions in order", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Set up three transaction expectations
		tx1 := db.ExpectTransaction().
			ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1)
		tx2 := db.ExpectTransaction().
			ExpectExec("INSERT INTO products").WillReturnRowsAffected(2)
		tx3 := db.ExpectTransaction().
			ExpectExec("INSERT INTO users").WillReturnRowsAffected(3)

		// First transaction
		txConn1, err := db.Begin(context.Background())
		assert.NoError(t, err)
		defer txConn1.Rollback(context.Background()) // No-op if committed
		result1, err := txConn1.Exec(context.Background(), "INSERT INTO orders VALUES (1)")
		assert.NoError(t, err)
		rowsAffected1, _ := result1.RowsAffected()
		assert.Equal(t, int64(1), rowsAffected1)
		err = txConn1.Commit(context.Background())
		assert.NoError(t, err)
		assert.True(t, tx1.IsCommitted())

		// Second transaction
		txConn2, err := db.Begin(context.Background())
		assert.NoError(t, err)
		defer txConn2.Rollback(context.Background()) // No-op if committed
		result2, err := txConn2.Exec(context.Background(), "INSERT INTO products VALUES (1)")
		assert.NoError(t, err)
		rowsAffected2, _ := result2.RowsAffected()
		assert.Equal(t, int64(2), rowsAffected2)
		err = txConn2.Commit(context.Background())
		assert.NoError(t, err)
		assert.True(t, tx2.IsCommitted())

		// Third transaction
		txConn3, err := db.Begin(context.Background())
		assert.NoError(t, err)
		defer txConn3.Rollback(context.Background()) // No-op if committed
		result3, err := txConn3.Exec(context.Background(), "INSERT INTO users VALUES (1)")
		assert.NoError(t, err)
		rowsAffected3, _ := result3.RowsAffected()
		assert.Equal(t, int64(3), rowsAffected3)
		err = txConn3.Commit(context.Background())
		assert.NoError(t, err)
		assert.True(t, tx3.IsCommitted())
	})

	t.Run("errors when Begin called without expectations", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Try to begin without setting up expectations
		_, err := db.Begin(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected Begin() call")
	})

	t.Run("errors when Begin called after all expectations consumed", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Set up one transaction expectation
		db.ExpectTransaction()

		// First Begin succeeds
		tx, err := db.Begin(context.Background())
		assert.NoError(t, err)
		defer tx.Rollback(context.Background()) // No-op: test transaction, cleanup not critical

		// Second Begin fails (no more expectations)
		_, err = db.Begin(context.Background())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unexpected Begin() call")
	})

	t.Run("AssertTransactionCommitted checks last started transaction", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)

		// Set up two transactions
		tx1 := db.ExpectTransaction()
		tx2 := db.ExpectTransaction()

		// Start both transactions
		txConn1, err := db.Begin(context.Background())
		assert.NoError(t, err)
		defer txConn1.Rollback(context.Background()) // No-op if committed
		txConn2, err := db.Begin(context.Background())
		assert.NoError(t, err)
		defer txConn2.Rollback(context.Background()) // No-op if committed

		// Commit both
		txConn1.Commit(context.Background())
		txConn2.Commit(context.Background())

		// AssertTransactionCommitted should check the last one (tx2)
		AssertTransactionCommitted(t, db)
		assert.True(t, tx1.IsCommitted())
		assert.True(t, tx2.IsCommitted())
	})
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

// TestConvertAssignUnsignedTypes tests the support for unsigned integer types.
func TestConvertAssignUnsignedTypes(t *testing.T) {
	t.Run("scans unsigned integer types", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet(
				"u8", "u16", "u32", "u64", "u",
			).AddRow(
				uint64(255), uint64(65535), uint64(4294967295), uint64(9223372036854775807), uint64(12345),
			))

		row := db.QueryRow(context.Background(), testUnsignedQuery)

		var u8 uint8
		var u16 uint16
		var u32 uint32
		var u64 uint64
		var u uint
		err := row.Scan(&u8, &u16, &u32, &u64, &u)

		assert.NoError(t, err)
		assert.Equal(t, uint8(255), u8)
		assert.Equal(t, uint16(65535), u16)
		assert.Equal(t, uint32(4294967295), u32)
		assert.Equal(t, uint64(9223372036854775807), u64) // max int64 value
		assert.Equal(t, uint(12345), u)
	})

	t.Run("scans unsigned integer pointer types", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet(
				"u8", "u16", "u32", "u64", "u",
			).AddRow(
				uint64(100), uint64(1000), uint64(100000), uint64(1000000), uint64(999),
			))

		row := db.QueryRow(context.Background(), testUnsignedQuery)

		var u8 *uint8
		var u16 *uint16
		var u32 *uint32
		var u64 *uint64
		var u *uint
		err := row.Scan(&u8, &u16, &u32, &u64, &u)

		assert.NoError(t, err)
		assert.NotNil(t, u8)
		assert.Equal(t, uint8(100), *u8)
		assert.NotNil(t, u16)
		assert.Equal(t, uint16(1000), *u16)
		assert.NotNil(t, u32)
		assert.Equal(t, uint32(100000), *u32)
		assert.NotNil(t, u64)
		assert.Equal(t, uint64(1000000), *u64)
		assert.NotNil(t, u)
		assert.Equal(t, uint(999), *u)
	})

	t.Run("handles nil unsigned integer values", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet(
				"u8", "u16", "u32", "u64", "u",
			).AddRow(
				nil, nil, nil, nil, nil,
			))

		row := db.QueryRow(context.Background(), testUnsignedQuery)

		var u8 *uint8
		var u16 *uint16
		var u32 *uint32
		var u64 *uint64
		var u *uint
		err := row.Scan(&u8, &u16, &u32, &u64, &u)

		assert.NoError(t, err)
		assert.Nil(t, u8)
		assert.Nil(t, u16)
		assert.Nil(t, u32)
		assert.Nil(t, u64)
		assert.Nil(t, u)
	})

	t.Run("converts signed to unsigned integers", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("u64").AddRow(int64(42)))

		row := db.QueryRow(context.Background(), "SELECT * FROM test")

		var u64 uint64
		err := row.Scan(&u64)

		assert.NoError(t, err)
		assert.Equal(t, uint64(42), u64)
	})

	t.Run("rejects negative values for unsigned integers", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("u64").AddRow(int64(-1)))

		row := db.QueryRow(context.Background(), "SELECT * FROM test")

		var u64 uint64
		err := row.Scan(&u64)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cannot convert negative value")
	})
}

// TestConvertAssignTimePointer tests the support for **time.Time.
func TestConvertAssignTimePointer(t *testing.T) {
	t.Run("scans time.Time pointer pointer", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		testTime := mustParseTime(t, "2025-01-15T10:30:00Z")
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("created_at").AddRow(testTime))

		row := db.QueryRow(context.Background(), "SELECT created_at FROM events")

		var createdAt *time.Time
		err := row.Scan(&createdAt)

		assert.NoError(t, err)
		assert.NotNil(t, createdAt)
		assert.Equal(t, testTime, *createdAt)
	})

	t.Run("handles nil time.Time pointer pointer", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		db.ExpectQuery("SELECT").
			WillReturnRows(NewRowSet("deleted_at").AddRow(nil))

		row := db.QueryRow(context.Background(), "SELECT deleted_at FROM events")

		var deletedAt *time.Time
		err := row.Scan(&deletedAt)

		assert.NoError(t, err)
		assert.Nil(t, deletedAt)
	})
}
