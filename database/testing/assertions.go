package testing

import (
	"database/sql"
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
	log := db.QueryLog()
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
	log := db.QueryLog()
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
	log := db.QueryLog()
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
	log := db.ExecLog()
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
	log := db.ExecLog()
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
	log := db.ExecLog()
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
		var details strings.Builder
		fmt.Fprintf(&details, "unexpected transaction(s) started: %d total\n", len(startedTxs))
		for i, txExp := range startedTxs {
			status := "pending"
			if txExp.tx != nil {
				if txExp.tx.IsCommitted() {
					status = "committed"
				} else if txExp.tx.IsRolledBack() {
					status = "rolled back"
				}
			}
			fmt.Fprintf(&details, "  %d. Transaction [status: %s]\n", i+1, status)
		}
		t.Errorf("%s", details.String())
	}
}

// AssertSessionClosed asserts that the session was closed, releasing its pinned
// connection back to the pool.
func AssertSessionClosed(t *testing.T, sess *TestSession) {
	t.Helper()
	if !sess.IsClosed() {
		t.Errorf("expected session to be closed, but it was not\nQueries: %d, Execs: %d",
			len(sess.QueryLog()), len(sess.ExecLog()))
	}
}

// formatQueryLog formats the query log for error messages.
func formatQueryLog(log []QueryCall) string {
	if len(log) == 0 {
		return "  (no queries executed)"
	}

	var sb strings.Builder
	for i, call := range log {
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, call.SQL)
		if len(call.Args) > 0 {
			fmt.Fprintf(&sb, "     Args: %v\n", call.Args)
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
		fmt.Fprintf(&sb, "  %d. %s\n", i+1, call.SQL)
		if len(call.Args) > 0 {
			fmt.Fprintf(&sb, "     Args: %v\n", call.Args)
		}
	}
	return sb.String()
}

// AssertTxOptions fails when the options tx was begun with differ from want: a nil vs
// non-nil mismatch, or a different Isolation or ReadOnly.
//
// Example:
//
//	AssertTxOptions(t, tx, &sql.TxOptions{ReadOnly: true})
func AssertTxOptions(t *testing.T, tx *TestTx, want *sql.TxOptions) {
	t.Helper()
	got := tx.Options()
	if (got == nil) != (want == nil) {
		t.Errorf("transaction options: got %+v, want %+v", got, want)
		return
	}
	if got == nil {
		return
	}
	if got.Isolation != want.Isolation || got.ReadOnly != want.ReadOnly {
		t.Errorf("transaction options: got %+v, want %+v", *got, *want)
	}
}

// AssertAllExpectationsMet fails, in one report, on every expectation the test
// declared but never used: a query or exec expectation no statement resolved to
// (on the pool, in a begun transaction, or in an opened session), a transaction
// still queued on the pool or on an opened session, and a session never opened.
// Resolving to an expectation meets it even when it returned its configured
// error. Matching is first-match-wins, so a pattern shadowed by an earlier,
// broader one is reported as unmet. A transaction whose Begin failed through
// WillFailBegin counts as consumed. It is opt-in: call it at the end of a test.
//
// Example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	// ... set expectations, execute test code ...
//	AssertAllExpectationsMet(t, db)
func AssertAllExpectationsMet(t *testing.T, db *TestDB) {
	t.Helper()
	unmet := unmetExpectations(db)
	if len(unmet) > 0 {
		t.Errorf("%d unmet expectation(s):\n  %s", len(unmet), strings.Join(unmet, "\n  "))
	}
}

// unmetExpectations builds AssertAllExpectationsMet's report, one line per item.
func unmetExpectations(db *TestDB) []string {
	db.mu.RLock()
	lines := unmetIn("pool", db.queries, db.execs)
	for _, txExp := range db.txExpectations {
		lines = append(lines, fmt.Sprintf("pool: transaction #%d was never begun", txExp.seq))
	}
	started := append([]*TxExpectation{}, db.startedTransactions...)
	opened := append([]*TestSession{}, db.openedSessions...)
	queued := append([]*TestSession{}, db.sessionExpectations...)
	db.mu.RUnlock()

	for _, txExp := range started {
		lines = append(lines, txExp.tx.unmet(txExp.scope)...)
	}
	for _, sess := range opened {
		lines = append(lines, sess.unmetItems()...)
	}
	for _, sess := range queued {
		lines = append(lines, sess.label+" was never opened")
	}
	return lines
}
