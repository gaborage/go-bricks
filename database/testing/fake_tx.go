package testing

import (
	"context"
	"database/sql"
	"errors"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestTx is an in-memory fake transaction that implements database.Tx.
// It tracks query/exec calls within the transaction and records commit/rollback behavior.
//
// TestTx is created via TestDB.ExpectTransaction() and can be configured with
// expectations for queries and execs that should occur within the transaction.
//
// Usage example:
//
//	db := NewTestDB(dbtypes.PostgreSQL)
//	tx := db.ExpectTransaction().
//	    ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
//	    ExpectExec("INSERT INTO items").WillReturnRowsAffected(3)
//
//	// Test code that uses transactions
//	svc := NewOrderService(deps)
//	err := svc.CreateWithItems(ctx, order, items)
//
//	// Assert transaction was committed
//	AssertCommitted(t, tx)
type TestTx struct {
	expectationSet
	committed  bool
	rolledBack bool
}

// newTestTx builds a transaction fake whose expectations belong to parent.
func newTestTx(parent *TestDB) *TestTx {
	return &TestTx{expectationSet: expectationSet{parent: parent, scope: "transaction"}}
}

// ExpectQuery sets up an expectation for Query or QueryRow calls within the transaction.
// Returns the TestTx for method chaining.
//
// Example:
//
//	tx := db.ExpectTransaction().
//	    ExpectQuery("SELECT * FROM users WHERE id = $1").
//	        WillReturnRows(NewRowSet("id", "name").AddRow(1, "Alice"))
func (tx *TestTx) ExpectQuery(sqlPattern string) *TestTx {
	tx.addQuery(sqlPattern)
	return tx
}

// WillReturnRows configures the last ExpectQuery to return the specified rows.
// This is a convenience method that operates on the most recently added query expectation.
// Returns the TestTx for method chaining.
//
// Example:
//
//	tx.ExpectQuery("SELECT").WillReturnRows(NewRowSet("id").AddRow(1))
func (tx *TestTx) WillReturnRows(rows *RowSet) *TestTx {
	tx.setRows(rows)
	return tx
}

// ExpectExec sets up an expectation for Exec calls within the transaction.
// Returns the TestTx for method chaining.
//
// Example:
//
//	tx := db.ExpectTransaction().
//	    ExpectExec("INSERT INTO users").WillReturnRowsAffected(1)
func (tx *TestTx) ExpectExec(sqlPattern string) *TestTx {
	tx.addExec(sqlPattern)
	return tx
}

// WillReturnRowsAffected configures the last ExpectExec to return the specified rows affected count.
// This is a convenience method that operates on the most recently added exec expectation.
// Returns the TestTx for method chaining.
//
// Example:
//
//	tx.ExpectExec("INSERT").WillReturnRowsAffected(5)
func (tx *TestTx) WillReturnRowsAffected(n int64) *TestTx {
	tx.setRowsAffected(n)
	return tx
}

// WillReturnError configures the most-recently-added expectation (Query or Exec)
// to return the specified error. The target is whichever ExpectQuery or ExpectExec
// was called most recently on this TestTx; calling WillReturnError before any
// expectation has been added is a no-op.
// Returns the TestTx for method chaining.
//
// Example:
//
//	tx.ExpectExec("INSERT INTO orders").WillReturnError(errConstraintViolation)
//	tx.ExpectQuery("SELECT FOR UPDATE").WillReturnError(errLockTimeout)
func (tx *TestTx) WillReturnError(err error) *TestTx {
	tx.setError(err)
	return tx
}

// Query implements dbtypes.Tx.Query.
//
// IMPORTANT: Callers MUST call defer rows.Close() immediately after Query() to prevent
// resource leaks. The returned *sql.Rows is backed by a temporary *sql.DB that requires
// explicit cleanup.
//
// Correct usage:
//
//	rows, err := tx.Query(ctx, "SELECT * FROM users")
//	if err != nil {
//	    return err
//	}
//	defer rows.Close()  // REQUIRED
//
//	for rows.Next() {
//	    // ... scan rows
//	}
func (tx *TestTx) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.runQuery(query, args)
}

// QueryRow implements dbtypes.Tx.QueryRow.
func (tx *TestTx) QueryRow(_ context.Context, query string, args ...any) dbtypes.Row {
	return tx.runQueryRow(query, args)
}

// Exec implements dbtypes.Tx.Exec.
func (tx *TestTx) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	return tx.runExec(query, args)
}

// Prepare implements dbtypes.Tx.Prepare (rarely used in tests).
func (tx *TestTx) Prepare(_ context.Context, _ string) (dbtypes.Statement, error) {
	return nil, errors.New("Prepare() not implemented in TestTx (prepared statements rarely needed in transaction tests)")
}

// Commit implements dbtypes.Tx.Commit.
func (tx *TestTx) Commit(_ context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed {
		return errors.New("transaction already committed")
	}
	if tx.rolledBack {
		return errors.New("transaction already rolled back")
	}

	tx.committed = true
	return nil
}

// Rollback implements dbtypes.Tx.Rollback.
func (tx *TestTx) Rollback(_ context.Context) error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	// Rollback after commit is a no-op (standard database behavior)
	if tx.committed {
		return nil
	}

	tx.rolledBack = true
	return nil
}

// IsCommitted returns true if the transaction was committed.
func (tx *TestTx) IsCommitted() bool {
	tx.mu.RLock()
	defer tx.mu.RUnlock()
	return tx.committed
}

// IsRolledBack returns true if the transaction was rolled back.
func (tx *TestTx) IsRolledBack() bool {
	tx.mu.RLock()
	defer tx.mu.RUnlock()
	return tx.rolledBack
}

// QueryLog returns all Query/QueryRow calls made within this transaction.
func (tx *TestTx) QueryLog() []QueryCall {
	return tx.queryCalls()
}

// ExecLog returns all Exec calls made within this transaction.
func (tx *TestTx) ExecLog() []ExecCall {
	return tx.execCalls()
}
