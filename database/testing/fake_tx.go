package testing

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

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
	parent     *TestDB
	queries    []*QueryExpectation
	execs      []*ExecExpectation
	lastQuery  *QueryExpectation
	lastExec   *ExecExpectation
	queryLog   []QueryCall
	execLog    []ExecCall
	committed  bool
	rolledBack bool
	mu         sync.RWMutex
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
	tx.mu.Lock()
	defer tx.mu.Unlock()
	exp := &QueryExpectation{sql: sqlPattern}
	tx.queries = append(tx.queries, exp)
	tx.lastQuery = exp
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
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.lastQuery != nil {
		tx.lastQuery.rows = rows
	}

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
	tx.mu.Lock()
	defer tx.mu.Unlock()
	exp := &ExecExpectation{sql: sqlPattern}
	tx.execs = append(tx.execs, exp)
	tx.lastExec = exp
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
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.lastExec != nil {
		tx.lastExec.rowsAffected = n
	}

	return tx
}

// Query implements dbtypes.Tx.Query.
//
//nolint:dupl // Intentional duplication with TestDB.Query - different contexts require separate implementations
func (tx *TestTx) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	tx.mu.Lock()
	tx.queryLog = append(tx.queryLog, QueryCall{SQL: query, Args: args})
	tx.mu.Unlock()

	exp := tx.findQueryExpectation(query)
	if exp == nil {
		return nil, fmt.Errorf("unexpected query in transaction: %s (no matching expectation)", query)
	}

	if exp.err != nil {
		return nil, exp.err
	}

	if exp.rows == nil {
		return nil, fmt.Errorf("transaction query expectation for %q has no rows configured", query)
	}

	return exp.rows.toSQLRows()
}

// QueryRow implements dbtypes.Tx.QueryRow.
func (tx *TestTx) QueryRow(_ context.Context, query string, args ...any) dbtypes.Row {
	tx.mu.Lock()
	tx.queryLog = append(tx.queryLog, QueryCall{SQL: query, Args: args})
	tx.mu.Unlock()

	exp := tx.findQueryExpectation(query)
	if exp == nil {
		return &testRow{err: fmt.Errorf("unexpected query in transaction: %s (no matching expectation)", query)}
	}

	if exp.err != nil {
		return &testRow{err: exp.err}
	}

	if exp.rows == nil {
		return &testRow{err: fmt.Errorf("transaction query expectation for %q has no rows configured", query)}
	}

	// Return first row for QueryRow
	if len(exp.rows.rows) == 0 {
		return &testRow{err: sql.ErrNoRows}
	}

	// Normalize pointer values before returning
	normalized, err := exp.rows.normalizeRow(0)
	if err != nil {
		return &testRow{err: fmt.Errorf("failed to normalize row: %w", err)}
	}

	return &testRow{values: normalized}
}

// Exec implements dbtypes.Tx.Exec.
func (tx *TestTx) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	tx.mu.Lock()
	tx.execLog = append(tx.execLog, ExecCall{SQL: query, Args: args})
	tx.mu.Unlock()

	exp := tx.findExecExpectation(query)
	if exp == nil {
		return nil, fmt.Errorf("unexpected exec in transaction: %s (no matching expectation)", query)
	}

	if exp.err != nil {
		return nil, exp.err
	}

	return &testResult{rowsAffected: exp.rowsAffected}, nil
}

// Prepare implements dbtypes.Tx.Prepare (rarely used in tests).
func (tx *TestTx) Prepare(_ context.Context, _ string) (dbtypes.Statement, error) {
	return nil, fmt.Errorf("Prepare() not implemented in TestTx (prepared statements rarely needed in transaction tests)")
}

// Commit implements dbtypes.Tx.Commit.
func (tx *TestTx) Commit() error {
	tx.mu.Lock()
	defer tx.mu.Unlock()

	if tx.committed {
		return fmt.Errorf("transaction already committed")
	}
	if tx.rolledBack {
		return fmt.Errorf("transaction already rolled back")
	}

	tx.committed = true
	return nil
}

// Rollback implements dbtypes.Tx.Rollback.
func (tx *TestTx) Rollback() error {
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

// GetQueryLog returns all Query/QueryRow calls made within this transaction.
func (tx *TestTx) GetQueryLog() []QueryCall {
	tx.mu.RLock()
	defer tx.mu.RUnlock()
	return append([]QueryCall{}, tx.queryLog...)
}

// GetExecLog returns all Exec calls made within this transaction.
func (tx *TestTx) GetExecLog() []ExecCall {
	tx.mu.RLock()
	defer tx.mu.RUnlock()
	return append([]ExecCall{}, tx.execLog...)
}

// findQueryExpectation searches for a matching query expectation in the transaction.
// Uses the parent TestDB's matching strategy (strict or partial).
func (tx *TestTx) findQueryExpectation(actualSQL string) *QueryExpectation {
	tx.mu.RLock()
	defer tx.mu.RUnlock()

	for _, exp := range tx.queries {
		if tx.parent.matchSQL(exp.sql, actualSQL) {
			return exp
		}
	}
	return nil
}

// findExecExpectation searches for a matching exec expectation in the transaction.
// Uses the parent TestDB's matching strategy (strict or partial).
func (tx *TestTx) findExecExpectation(actualSQL string) *ExecExpectation {
	tx.mu.RLock()
	defer tx.mu.RUnlock()

	for _, exp := range tx.execs {
		if tx.parent.matchSQL(exp.sql, actualSQL) {
			return exp
		}
	}
	return nil
}
