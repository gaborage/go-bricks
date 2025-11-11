package testing

import (
	"fmt"
	"strings"
	"testing"
)

// AssertQueryExecuted asserts that a query matching the SQL pattern was executed on the TestDB.
// Uses partial matching by default (can be changed with db.StrictSQLMatching()).
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute test code ...
//	AssertQueryExecuted(t, db, "SELECT * FROM users")
func AssertQueryExecuted(t *testing.T, db *TestDB, sqlPattern string) {
	t.Helper()
	log := db.GetQueryLog()
	for _, call := range log {
		if db.matchSQL(sqlPattern, call.SQL) {
			return // Found matching query
		}
	}

	// No match found - fail with helpful message
	t.Errorf("expected query not executed: %q\nActual queries:\n%s",
		sqlPattern, formatQueryLog(log))
}

// AssertQueryNotExecuted asserts that no query matching the SQL pattern was executed on the TestDB.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute test code ...
//	AssertQueryNotExecuted(t, db, "DELETE FROM users")
func AssertQueryNotExecuted(t *testing.T, db *TestDB, sqlPattern string) {
	t.Helper()
	log := db.GetQueryLog()
	for _, call := range log {
		if db.matchSQL(sqlPattern, call.SQL) {
			t.Errorf("unexpected query executed: %q\nQuery SQL: %s",
				sqlPattern, call.SQL)
			return
		}
	}
}

// AssertQueryCount asserts that exactly N queries matching the SQL pattern were executed.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute test code that should call SELECT twice ...
//	AssertQueryCount(t, db, "SELECT", 2)
func AssertQueryCount(t *testing.T, db *TestDB, sqlPattern string, expected int) {
	t.Helper()
	log := db.GetQueryLog()
	count := 0
	for _, call := range log {
		if db.matchSQL(sqlPattern, call.SQL) {
			count++
		}
	}

	if count != expected {
		t.Errorf("expected %d queries matching %q, got %d\nActual queries:\n%s",
			expected, sqlPattern, count, formatQueryLog(log))
	}
}

// AssertExecExecuted asserts that an exec matching the SQL pattern was executed on the TestDB.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute test code ...
//	AssertExecExecuted(t, db, "INSERT INTO users")
func AssertExecExecuted(t *testing.T, db *TestDB, sqlPattern string) {
	t.Helper()
	log := db.GetExecLog()
	for _, call := range log {
		if db.matchSQL(sqlPattern, call.SQL) {
			return // Found matching exec
		}
	}

	// No match found - fail with helpful message
	t.Errorf("expected exec not executed: %q\nActual execs:\n%s",
		sqlPattern, formatExecLog(log))
}

// AssertExecNotExecuted asserts that no exec matching the SQL pattern was executed on the TestDB.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute test code ...
//	AssertExecNotExecuted(t, db, "DELETE FROM users")
func AssertExecNotExecuted(t *testing.T, db *TestDB, sqlPattern string) {
	t.Helper()
	log := db.GetExecLog()
	for _, call := range log {
		if db.matchSQL(sqlPattern, call.SQL) {
			t.Errorf("unexpected exec executed: %q\nExec SQL: %s",
				sqlPattern, call.SQL)
			return
		}
	}
}

// AssertExecCount asserts that exactly N execs matching the SQL pattern were executed.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute batch insert that should affect 5 rows ...
//	AssertExecCount(t, db, "INSERT", 5)
func AssertExecCount(t *testing.T, db *TestDB, sqlPattern string, expected int) {
	t.Helper()
	log := db.GetExecLog()
	count := 0
	for _, call := range log {
		if db.matchSQL(sqlPattern, call.SQL) {
			count++
		}
	}

	if count != expected {
		t.Errorf("expected %d execs matching %q, got %d\nActual execs:\n%s",
			expected, sqlPattern, count, formatExecLog(log))
	}
}

// AssertCommitted asserts that the transaction was committed.
//
// Example:
//
//	tx := db.ExpectTransaction()
//	// ... execute test code ...
//	AssertCommitted(t, tx)
func AssertCommitted(t *testing.T, tx *TestTx) {
	t.Helper()
	if !tx.IsCommitted() {
		t.Errorf("expected transaction to be committed, but it was not\nRolled back: %v",
			tx.IsRolledBack())
	}
}

// AssertRolledBack asserts that the transaction was rolled back.
//
// Example:
//
//	tx := db.ExpectTransaction()
//	// ... execute test code that should fail and rollback ...
//	AssertRolledBack(t, tx)
func AssertRolledBack(t *testing.T, tx *TestTx) {
	t.Helper()
	if !tx.IsRolledBack() {
		t.Errorf("expected transaction to be rolled back, but it was not\nCommitted: %v",
			tx.IsCommitted())
	}
}

// AssertTransactionCommitted asserts that the TestDB's transaction was committed.
// This is a convenience wrapper around AssertCommitted that extracts the transaction from TestDB.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	db.ExpectTransaction()
//	// ... execute test code ...
//	AssertTransactionCommitted(t, db)
func AssertTransactionCommitted(t *testing.T, db *TestDB) {
	t.Helper()
	db.mu.RLock()
	startedTxs := db.startedTransactions
	db.mu.RUnlock()

	if len(startedTxs) == 0 {
		t.Error("no transaction was started (use db.ExpectTransaction() and Begin())")
		return
	}

	// Check the last started transaction
	txExp := startedTxs[len(startedTxs)-1]
	if txExp.tx == nil {
		t.Error("transaction expectation has no tx (internal error)")
		return
	}

	AssertCommitted(t, txExp.tx)
}

// AssertTransactionRolledBack asserts that the TestDB's transaction was rolled back.
// This is a convenience wrapper around AssertRolledBack that extracts the transaction from TestDB.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	db.ExpectTransaction()
//	// ... execute test code that should fail ...
//	AssertTransactionRolledBack(t, db)
func AssertTransactionRolledBack(t *testing.T, db *TestDB) {
	t.Helper()
	db.mu.RLock()
	startedTxs := db.startedTransactions
	db.mu.RUnlock()

	if len(startedTxs) == 0 {
		t.Error("no transaction was started (use db.ExpectTransaction() and Begin())")
		return
	}

	// Check the last started transaction
	txExp := startedTxs[len(startedTxs)-1]
	if txExp.tx == nil {
		t.Error("transaction expectation has no tx (internal error)")
		return
	}

	AssertRolledBack(t, txExp.tx)
}

// AssertNoTransaction asserts that no transaction was started on the TestDB.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... execute test code that should NOT use transactions ...
//	AssertNoTransaction(t, db)
func AssertNoTransaction(t *testing.T, db *TestDB) {
	t.Helper()
	db.mu.RLock()
	startedTxs := db.startedTransactions
	db.mu.RUnlock()

	if len(startedTxs) > 0 {
		for _, txExp := range startedTxs {
			if txExp.tx != nil && (txExp.tx.IsCommitted() || txExp.tx.IsRolledBack()) {
				t.Error("unexpected transaction was started and completed")
				return
			}
		}
	}
}

// formatQueryLog formats the query log for error messages.
func formatQueryLog(log []QueryCall) string {
	if len(log) == 0 {
		return "  (no queries executed)"
	}

	var sb strings.Builder
	for i, call := range log {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, call.SQL))
		if len(call.Args) > 0 {
			sb.WriteString(fmt.Sprintf("     Args: %v\n", call.Args))
		}
	}
	return sb.String()
}

// formatExecLog formats the exec log for error messages.
func formatExecLog(log []ExecCall) string {
	if len(log) == 0 {
		return "  (no execs executed)"
	}

	var sb strings.Builder
	for i, call := range log {
		sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, call.SQL))
		if len(call.Args) > 0 {
			sb.WriteString(fmt.Sprintf("     Args: %v\n", call.Args))
		}
	}
	return sb.String()
}
