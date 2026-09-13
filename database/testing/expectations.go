package testing

import (
	"database/sql"
	"fmt"
	"sync"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// expectationSet is the query/exec expectation machinery TestTx and TestSession
// share: each holder carries its OWN expectations, matches a statement against
// them in declaration order using the parent TestDB's matching strategy, and
// logs every call. scope names the holder in error text ("transaction",
// "session"), so the two produce the same messages they always did.
type expectationSet struct {
	parent      *TestDB
	scope       string
	guard       func() error
	queries     []*QueryExpectation
	execs       []*ExecExpectation
	lastQuery   *QueryExpectation
	lastExec    *ExecExpectation
	lastWasExec bool
	queryLog    []QueryCall
	execLog     []ExecCall
	mu          sync.RWMutex
}

// addQuery records a new query expectation and makes it the WillReturn* target.
func (e *expectationSet) addQuery(sqlPattern string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	exp := &QueryExpectation{sql: sqlPattern}
	e.queries = append(e.queries, exp)
	e.lastQuery = exp
	e.lastWasExec = false
}

// addExec records a new exec expectation and makes it the WillReturn* target.
func (e *expectationSet) addExec(sqlPattern string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	exp := &ExecExpectation{sql: sqlPattern}
	e.execs = append(e.execs, exp)
	e.lastExec = exp
	e.lastWasExec = true
}

// setRows configures the most recent query expectation to return rows.
func (e *expectationSet) setRows(rows *RowSet) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastQuery != nil {
		e.lastQuery.rows = rows
	}
}

// setRowsAffected configures the most recent exec expectation's rows-affected count.
func (e *expectationSet) setRowsAffected(n int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastExec != nil {
		e.lastExec.rowsAffected = n
	}
}

// setError configures the most-recently-added expectation (query or exec) to
// return err. Calling it before any expectation has been added is a no-op.
func (e *expectationSet) setError(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.lastWasExec {
		if e.lastExec != nil {
			e.lastExec.err = err
		}
		return
	}
	if e.lastQuery != nil {
		e.lastQuery.err = err
	}
}

// queryCalls returns a copy of the logged Query/QueryRow calls.
func (e *expectationSet) queryCalls() []QueryCall {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]QueryCall{}, e.queryLog...)
}

// execCalls returns a copy of the logged Exec calls.
func (e *expectationSet) execCalls() []ExecCall {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return append([]ExecCall{}, e.execLog...)
}

// resolveQuery logs the call and returns the matching query expectation, or the
// error an unmatched statement, an unconfigured expectation or the holder's own
// guard produces.
func (e *expectationSet) resolveQuery(query string, args []any) (*QueryExpectation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.guarded(); err != nil {
		return nil, err
	}
	e.queryLog = append(e.queryLog, QueryCall{SQL: query, Args: args})

	for _, exp := range e.queries {
		if !e.parent.matchSQL(exp.sql, query) {
			continue
		}
		if exp.err != nil {
			return nil, exp.err
		}
		if exp.rows == nil {
			return nil, fmt.Errorf("%s query expectation for %q has no rows configured", e.scope, query)
		}
		return exp, nil
	}
	return nil, fmt.Errorf("unexpected query in %s: %s (no matching expectation)", e.scope, query)
}

// resolveExec logs the call and returns the matching exec expectation, or the
// error an unmatched statement or the holder's own guard produces.
func (e *expectationSet) resolveExec(query string, args []any) (*ExecExpectation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.guarded(); err != nil {
		return nil, err
	}
	e.execLog = append(e.execLog, ExecCall{SQL: query, Args: args})

	for _, exp := range e.execs {
		if !e.parent.matchSQL(exp.sql, query) {
			continue
		}
		if exp.err != nil {
			return nil, exp.err
		}
		return exp, nil
	}
	return nil, fmt.Errorf("unexpected exec in %s: %s (no matching expectation)", e.scope, query)
}

// guarded consults the holder's own precondition, if it set one. The guard runs
// with e.mu already held and MUST NOT take it again.
func (e *expectationSet) guarded() error {
	if e.guard == nil {
		return nil
	}
	return e.guard()
}

// runQuery resolves the statement and hands back the expectation's rows.
func (e *expectationSet) runQuery(query string, args []any) (*sql.Rows, error) {
	exp, err := e.resolveQuery(query, args)
	if err != nil {
		return nil, err
	}
	return exp.rows.toSQLRows()
}

// runQueryRow resolves the statement and hands back its first row, normalized.
func (e *expectationSet) runQueryRow(query string, args []any) dbtypes.Row {
	exp, err := e.resolveQuery(query, args)
	if err != nil {
		return &testRow{err: err}
	}
	if len(exp.rows.rows) == 0 {
		return &testRow{err: sql.ErrNoRows}
	}
	normalized, normErr := exp.rows.normalizeRow(0)
	if normErr != nil {
		return &testRow{err: fmt.Errorf("failed to normalize row: %w", normErr)}
	}
	return &testRow{values: normalized}
}

// runExec resolves the statement and hands back the expectation's result.
func (e *expectationSet) runExec(query string, args []any) (sql.Result, error) {
	exp, err := e.resolveExec(query, args)
	if err != nil {
		return nil, err
	}
	return &testResult{rowsAffected: exp.rowsAffected}, nil
}
