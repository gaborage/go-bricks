// Package testing provides utilities for testing database logic in go-bricks applications.
// This package follows the design patterns from observability/testing, providing fluent APIs
// and in-memory fakes that eliminate the complexity of sqlmock and testify mocks.
//
// The primary type is TestDB, which implements database.Querier and database.Interface
// with expectation-based mocking. Use TestDB for unit tests where you want to verify
// SQL queries and execution without needing a real database.
//
// # Resource Management
//
// IMPORTANT: When using Query() or TestTx.Query(), callers MUST call defer rows.Close()
// immediately after obtaining rows to prevent resource leaks. The returned *sql.Rows is
// backed by a temporary *sql.DB that requires explicit cleanup.
//
// Correct pattern:
//
//	rows, err := db.Query(ctx, "SELECT ...")
//	if err != nil {
//	    return err
//	}
//	defer rows.Close()  // REQUIRED - prevents resource leaks
//
// A runtime.SetFinalizer provides a safety net for garbage collection, but this is
// non-deterministic and should NOT be relied upon. Always use defer rows.Close() for
// deterministic resource management.
//
// For integration tests requiring actual database behavior, see the container helpers
// which wrap testcontainers for PostgreSQL and Oracle.
package testing

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// TestDB is an in-memory fake database that implements both database.Querier and database.Interface.
// It provides a fluent API for setting up query expectations and tracking calls for assertions.
//
// TestDB supports two SQL matching modes:
//   - Partial matching (default): Matches if expected SQL is a substring of actual SQL
//   - Strict matching: Requires exact SQL match (enable with StrictSQLMatching())
//
// Usage example:
//
//	db := NewTestDB(dbtypes.PostgreSQL).
//	    ExpectQuery("SELECT * FROM users").
//	        WillReturnRows(NewRowSet("id", "name").AddRow(1, "Alice")).
//	    ExpectExec("INSERT INTO users").
//	        WillReturnRowsAffected(1)
//
//	deps := &app.ModuleDeps{
//	    GetDB: func(ctx context.Context) (database.Interface, error) {
//	        return db, nil
//	    },
//	}
//
// For assertion helpers, see the AssertQueryExecuted, AssertExecExecuted functions.
type TestDB struct {
	vendor              string
	queries             []*QueryExpectation
	execs               []*ExecExpectation
	lastQuery           *QueryExpectation
	lastExec            *ExecExpectation
	queryLog            []QueryCall
	execLog             []ExecCall
	strictMatch         bool
	txExpectations      []*TxExpectation
	startedTransactions []*TxExpectation
	mu                  sync.RWMutex
}

// QueryCall represents a single Query or QueryRow invocation.
type QueryCall struct {
	SQL  string
	Args []any
}

// ExecCall represents a single Exec invocation.
type ExecCall struct {
	SQL  string
	Args []any
}

// QueryExpectation defines what should happen when a query is executed.
type QueryExpectation struct {
	sql  string
	rows *RowSet
	err  error
}

// ExecExpectation defines what should happen when Exec is executed.
type ExecExpectation struct {
	sql          string
	rowsAffected int64
	err          error
}

// TxExpectation tracks expected transaction behavior.
type TxExpectation struct {
	parent    *TestDB
	tx        *TestTx
	shouldErr error
}

// NewTestDB creates a new in-memory fake database for the specified vendor.
// The vendor parameter should be one of: dbtypes.PostgreSQL, dbtypes.Oracle, dbtypes.MongoDB.
//
// The returned TestDB implements both database.Querier (for simple mocking) and
// database.Interface (for full compatibility with framework code).
func NewTestDB(vendor string) *TestDB {
	return &TestDB{
		vendor: vendor,
	}
}

// StrictSQLMatching enables exact SQL matching instead of partial substring matching.
// Returns the TestDB for method chaining.
//
// Default behavior (partial matching):
//
//	db.ExpectQuery("SELECT")  // Matches "SELECT * FROM users WHERE id = $1"
//
// With strict matching:
//
//	db.StrictSQLMatching().ExpectQuery("SELECT * FROM users WHERE id = $1")  // Exact match only
func (db *TestDB) StrictSQLMatching() *TestDB {
	db.mu.Lock()
	defer db.mu.Unlock()
	db.strictMatch = true
	return db
}

// ExpectQuery sets up an expectation for Query or QueryRow calls.
// The sqlPattern parameter is matched against actual queries (partial match by default, exact with StrictSQLMatching).
//
// Returns a QueryExpectation builder for configuring the response.
//
// Example:
//
//	db.ExpectQuery("SELECT * FROM users WHERE id = $1").
//	    WillReturnRows(NewRowSet("id", "name").AddRow(1, "Alice"))
func (db *TestDB) ExpectQuery(sqlPattern string) *QueryExpectation {
	exp := &QueryExpectation{sql: sqlPattern}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.queries = append(db.queries, exp)
	db.lastQuery = exp
	return exp
}

// ExpectExec sets up an expectation for Exec calls.
// The sqlPattern parameter is matched against actual executions (partial match by default, exact with StrictSQLMatching).
//
// Returns an ExecExpectation builder for configuring the response.
//
// Example:
//
//	db.ExpectExec("INSERT INTO users").
//	    WillReturnRowsAffected(1)
func (db *TestDB) ExpectExec(sqlPattern string) *ExecExpectation {
	exp := &ExecExpectation{sql: sqlPattern}
	db.mu.Lock()
	defer db.mu.Unlock()
	db.execs = append(db.execs, exp)
	db.lastExec = exp
	return exp
}

// ExpectTransaction sets up an expectation for Begin() calls.
// Returns a TestTx that can be configured with query/exec expectations.
//
// Example:
//
//	tx := db.ExpectTransaction().
//	    ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
//	    ExpectExec("INSERT INTO items").WillReturnRowsAffected(3)
func (db *TestDB) ExpectTransaction() *TestTx {
	tx := &TestTx{parent: db}
	db.mu.Lock()
	defer db.mu.Unlock()
	txExp := &TxExpectation{
		parent: db,
		tx:     tx,
	}
	db.txExpectations = append(db.txExpectations, txExp)
	return tx
}

// QueryLog returns all Query/QueryRow calls made to this TestDB.
// Useful for custom assertions beyond the provided helpers.
func (db *TestDB) QueryLog() []QueryCall {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return append([]QueryCall{}, db.queryLog...)
}

// ExecLog returns all Exec calls made to this TestDB.
// Useful for custom assertions beyond the provided helpers.
func (db *TestDB) ExecLog() []ExecCall {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return append([]ExecCall{}, db.execLog...)
}

// matchSQL returns true if the actual SQL matches the expected SQL pattern.
// Uses partial matching by default, exact matching if StrictSQLMatching() was called.
func (db *TestDB) matchSQL(expected, actual string) bool {
	if db.strictMatch {
		return strings.TrimSpace(expected) == strings.TrimSpace(actual)
	}
	return strings.Contains(actual, expected)
}

// findQueryExpectation searches for a matching query expectation.
// Returns the first matching expectation in insertion order (first-match wins).
// Returns nil if no match found.
func (db *TestDB) findQueryExpectation(actualSQL string) *QueryExpectation {
	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, exp := range db.queries {
		if db.matchSQL(exp.sql, actualSQL) {
			return exp
		}
	}
	return nil
}

// findExecExpectation searches for a matching exec expectation.
// Returns the first matching expectation in insertion order (first-match wins).
// Returns nil if no match found.
func (db *TestDB) findExecExpectation(actualSQL string) *ExecExpectation {
	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, exp := range db.execs {
		if db.matchSQL(exp.sql, actualSQL) {
			return exp
		}
	}
	return nil
}

// Query implements database.Querier.Query.
//
// IMPORTANT: Callers MUST call defer rows.Close() immediately after Query() to prevent
// resource leaks. The returned *sql.Rows is backed by a temporary *sql.DB that requires
// explicit cleanup.
//
// Correct usage:
//
//	rows, err := db.Query(ctx, "SELECT * FROM users")
//	if err != nil {
//	    return err
//	}
//	defer rows.Close()  // REQUIRED
//
//	for rows.Next() {
//	    // ... scan rows
//	}
//
//nolint:dupl // Intentional duplication with TestTx.Query - different contexts require separate implementations
func (db *TestDB) Query(_ context.Context, query string, args ...any) (*sql.Rows, error) {
	db.mu.Lock()
	db.queryLog = append(db.queryLog, QueryCall{SQL: query, Args: args})
	db.mu.Unlock()

	exp := db.findQueryExpectation(query)
	if exp == nil {
		return nil, fmt.Errorf("unexpected query: %s (no matching expectation)", query)
	}

	if exp.err != nil {
		return nil, exp.err
	}

	if exp.rows == nil {
		return nil, fmt.Errorf("query expectation for %q has no rows configured (use WillReturnRows)", query)
	}

	return exp.rows.toSQLRows()
}

// QueryRow implements database.Querier.QueryRow.
func (db *TestDB) QueryRow(_ context.Context, query string, args ...any) dbtypes.Row {
	db.mu.Lock()
	db.queryLog = append(db.queryLog, QueryCall{SQL: query, Args: args})
	db.mu.Unlock()

	exp := db.findQueryExpectation(query)
	if exp == nil {
		return &testRow{err: fmt.Errorf("unexpected query: %s (no matching expectation)", query)}
	}

	if exp.err != nil {
		return &testRow{err: exp.err}
	}

	if exp.rows == nil {
		return &testRow{err: fmt.Errorf("query expectation for %q has no rows configured (use WillReturnRows)", query)}
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

// Exec implements database.Querier.Exec.
func (db *TestDB) Exec(_ context.Context, query string, args ...any) (sql.Result, error) {
	db.mu.Lock()
	db.execLog = append(db.execLog, ExecCall{SQL: query, Args: args})
	db.mu.Unlock()

	exp := db.findExecExpectation(query)
	if exp == nil {
		return nil, fmt.Errorf("unexpected exec: %s (no matching expectation)", query)
	}

	if exp.err != nil {
		return nil, exp.err
	}

	return &testResult{rowsAffected: exp.rowsAffected}, nil
}

// DatabaseType implements database.Querier.DatabaseType.
func (db *TestDB) DatabaseType() string {
	return db.vendor
}

// Begin implements database.Transactor.Begin.
func (db *TestDB) Begin(_ context.Context) (dbtypes.Tx, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	// Check if there are any expectations queued
	if len(db.txExpectations) == 0 {
		return nil, fmt.Errorf("unexpected Begin() call (use ExpectTransaction)")
	}

	// Pop the first expectation from the queue
	txExp := db.txExpectations[0]
	db.txExpectations = db.txExpectations[1:]

	// Track this transaction as started
	db.startedTransactions = append(db.startedTransactions, txExp)

	if txExp.shouldErr != nil {
		return nil, txExp.shouldErr
	}

	return txExp.tx, nil
}

// BeginTx implements database.Transactor.BeginTx.
func (db *TestDB) BeginTx(ctx context.Context, _ *sql.TxOptions) (dbtypes.Tx, error) {
	// For test purposes, delegate to Begin (ignore opts)
	return db.Begin(ctx)
}

// Prepare implements database.Interface.Prepare (rarely used in tests).
func (db *TestDB) Prepare(_ context.Context, _ string) (dbtypes.Statement, error) {
	return nil, fmt.Errorf("Prepare() not implemented in TestDB (prepared statements rarely needed in tests)")
}

// Health implements database.Interface.Health.
func (db *TestDB) Health(_ context.Context) error {
	return nil // Always healthy in tests
}

// Stats implements database.Interface.Stats.
func (db *TestDB) Stats() (map[string]any, error) {
	return map[string]any{
		"vendor":      db.vendor,
		"query_count": len(db.queryLog),
		"exec_count":  len(db.execLog),
	}, nil
}

// Close implements database.Interface.Close.
func (db *TestDB) Close() error {
	return nil // No-op in tests
}

// MigrationTable implements database.Interface.MigrationTable.
func (db *TestDB) MigrationTable() string {
	return "flyway_schema_history" // Default value
}

// CreateMigrationTable implements database.Interface.CreateMigrationTable.
func (db *TestDB) CreateMigrationTable(_ context.Context) error {
	return nil // No-op in tests
}

// WillReturnRows configures the QueryExpectation to return the specified rows.
// Returns the expectation for method chaining.
func (qe *QueryExpectation) WillReturnRows(rows *RowSet) *QueryExpectation {
	qe.rows = rows
	return qe
}

// WillReturnError configures the QueryExpectation to return an error.
// Returns the expectation for method chaining.
func (qe *QueryExpectation) WillReturnError(err error) *QueryExpectation {
	qe.err = err
	return qe
}

// WillReturnRowsAffected configures the ExecExpectation to return the specified rows affected count.
// Returns the expectation for method chaining.
func (ee *ExecExpectation) WillReturnRowsAffected(n int64) *ExecExpectation {
	ee.rowsAffected = n
	return ee
}

// WillReturnError configures the ExecExpectation to return an error.
// Returns the expectation for method chaining.
func (ee *ExecExpectation) WillReturnError(err error) *ExecExpectation {
	ee.err = err
	return ee
}

// testRow implements dbtypes.Row for QueryRow results.
type testRow struct {
	values  []any
	err     error
	scanned bool
}

func (r *testRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if r.scanned {
		return fmt.Errorf("row already scanned")
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan expects %d values, got %d", len(dest), len(r.values))
	}

	// Use sql.ConvertAssign for database/sql compatibility
	for i, v := range r.values {
		if err := convertAssign(dest[i], v); err != nil {
			return fmt.Errorf("column %d: %w", i, err)
		}
	}

	r.scanned = true
	return nil
}

func (r *testRow) Err() error {
	return r.err
}

// convertAssign mimics sql.ConvertAssign for database/sql compatibility.
// It handles type conversions from source values to destination pointers,
// supporting sql.Scanner interface and common Go types.
//
//nolint:gocyclo // Type conversion requires exhaustive case coverage for database/sql compatibility
func convertAssign(dest, src any) error {
	if src == nil {
		return setDestNil(dest)
	}

	if scanner, ok := dest.(sql.Scanner); ok {
		return scanner.Scan(src)
	}

	switch d := dest.(type) {
	case *string:
		return assignString(d, src)
	case **string:
		return assignStringPtr(d, src)
	case *[]byte:
		return assignBytes(d, src)
	case **[]byte:
		return assignBytesPtr(d, src)
	case *int:
		return assignInt(d, src)
	case **int:
		return assignIntPtr(d, src)
	case *int64:
		return assignInt64(d, src)
	case **int64:
		return assignInt64Ptr(d, src)
	case *int32:
		return assignInt32(d, src)
	case **int32:
		return assignInt32Ptr(d, src)
	case *bool:
		return assignBool(d, src)
	case **bool:
		return assignBoolPtr(d, src)
	case *float64:
		return assignFloat64(d, src)
	case **float64:
		return assignFloat64Ptr(d, src)
	case *time.Time:
		t, ok := src.(time.Time)
		if !ok {
			return fmt.Errorf("unsupported conversion from %T to *time.Time", src)
		}
		*d = t
		return nil
	case **time.Time:
		return assignTimePtr(d, src)
	case *uint:
		return assignUint(d, src)
	case **uint:
		return assignUintPtr(d, src)
	case *uint64:
		return assignUint64(d, src)
	case **uint64:
		return assignUint64Ptr(d, src)
	case *uint32:
		return assignUint32(d, src)
	case **uint32:
		return assignUint32Ptr(d, src)
	case *uint16:
		return assignUint16(d, src)
	case **uint16:
		return assignUint16Ptr(d, src)
	case *uint8:
		return assignUint8(d, src)
	case **uint8:
		return assignUint8Ptr(d, src)
	case *any:
		*d = src
		return nil
	default:
		return fmt.Errorf("unsupported scan destination type %T", dest)
	}
}

func assignString(dest *string, src any) error {
	switch s := src.(type) {
	case string:
		*dest = s
	case []byte:
		*dest = string(s)
	default:
		return fmt.Errorf("unsupported conversion from %T to *string", src)
	}
	return nil
}

func assignBytes(dest *[]byte, src any) error {
	switch s := src.(type) {
	case []byte:
		*dest = s
	case string:
		*dest = []byte(s)
	default:
		return fmt.Errorf("unsupported conversion from %T to *[]byte", src)
	}
	return nil
}

func assignInt(dest *int, src any) error {
	switch s := src.(type) {
	case int64:
		*dest = int(s)
	case int:
		*dest = s
	default:
		return fmt.Errorf("unsupported conversion from %T to *int", src)
	}
	return nil
}

func assignInt64(dest *int64, src any) error {
	switch s := src.(type) {
	case int64:
		*dest = s
	case int:
		*dest = int64(s)
	default:
		return fmt.Errorf("unsupported conversion from %T to *int64", src)
	}
	return nil
}

func assignInt32(dest *int32, src any) error {
	switch s := src.(type) {
	case int64:
		if s < math.MinInt32 || s > math.MaxInt32 {
			return fmt.Errorf("value %d overflows int32", s)
		}
		*dest = int32(s)
	case int:
		if int64(s) < math.MinInt32 || int64(s) > math.MaxInt32 {
			return fmt.Errorf("value %d overflows int32", s)
		}
		*dest = int32(s) //nolint:gosec // G115 - overflow checked above
	default:
		return fmt.Errorf("unsupported conversion from %T to *int32", src)
	}
	return nil
}

func assignBool(dest *bool, src any) error {
	b, ok := src.(bool)
	if !ok {
		return fmt.Errorf("unsupported conversion from %T to *bool", src)
	}
	*dest = b
	return nil
}

func assignFloat64(dest *float64, src any) error {
	switch s := src.(type) {
	case float64:
		*dest = s
	case float32:
		*dest = float64(s)
	default:
		return fmt.Errorf("unsupported conversion from %T to *float64", src)
	}
	return nil
}

// Pointer-to-pointer assignment functions for nullable columns
func assignStringPtr(dest **string, src any) error {
	var temp string
	if err := assignString(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignBytesPtr(dest **[]byte, src any) error {
	var temp []byte
	if err := assignBytes(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignIntPtr(dest **int, src any) error {
	var temp int
	if err := assignInt(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignInt64Ptr(dest **int64, src any) error {
	var temp int64
	if err := assignInt64(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignInt32Ptr(dest **int32, src any) error {
	var temp int32
	if err := assignInt32(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignBoolPtr(dest **bool, src any) error {
	var temp bool
	if err := assignBool(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignFloat64Ptr(dest **float64, src any) error {
	var temp float64
	if err := assignFloat64(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignTimePtr(dest **time.Time, src any) error {
	t, ok := src.(time.Time)
	if !ok {
		return fmt.Errorf("unsupported conversion from %T to **time.Time", src)
	}
	*dest = &t
	return nil
}

// Unsigned integer assignment functions
func assignUint(dest *uint, src any) error {
	switch s := src.(type) {
	case uint64:
		*dest = uint(s)
	case uint:
		*dest = s
	case int64:
		if s < 0 {
			return fmt.Errorf("cannot convert negative value %d to uint", s)
		}
		*dest = uint(s)
	case int:
		if s < 0 {
			return fmt.Errorf("cannot convert negative value %d to uint", s)
		}
		*dest = uint(s)
	default:
		return fmt.Errorf("unsupported conversion from %T to *uint", src)
	}
	return nil
}

func assignUint64(dest *uint64, src any) error {
	switch s := src.(type) {
	case uint64:
		*dest = s
	case uint:
		*dest = uint64(s)
	case int64:
		if s < 0 {
			return fmt.Errorf("cannot convert negative value %d to uint64", s)
		}
		*dest = uint64(s)
	case int:
		if s < 0 {
			return fmt.Errorf("cannot convert negative value %d to uint64", s)
		}
		*dest = uint64(s)
	default:
		return fmt.Errorf("unsupported conversion from %T to *uint64", src)
	}
	return nil
}

//nolint:dupl // Intentionally similar to assignUint16/assignUint8 - type-safe helpers for test utilities
func assignUint32(dest *uint32, src any) error {
	switch s := src.(type) {
	case uint64:
		if s > math.MaxUint32 {
			return fmt.Errorf("value %d overflows uint32", s)
		}
		*dest = uint32(s)
	case uint:
		if uint64(s) > math.MaxUint32 {
			return fmt.Errorf("value %d overflows uint32", s)
		}
		*dest = uint32(s) //nolint:gosec // G115 - overflow checked above
	case int64:
		if s < 0 || s > math.MaxUint32 {
			return fmt.Errorf("value %d overflows uint32", s)
		}
		*dest = uint32(s)
	case int:
		if s < 0 || int64(s) > math.MaxUint32 {
			return fmt.Errorf("value %d overflows uint32", s)
		}
		*dest = uint32(s) //nolint:gosec // G115 - overflow checked above
	default:
		return fmt.Errorf("unsupported conversion from %T to *uint32", src)
	}
	return nil
}

//nolint:dupl // Intentionally similar to assignUint32/assignUint8 - type-safe helpers for test utilities
func assignUint16(dest *uint16, src any) error {
	switch s := src.(type) {
	case uint64:
		if s > math.MaxUint16 {
			return fmt.Errorf("value %d overflows uint16", s)
		}
		*dest = uint16(s)
	case uint:
		if uint64(s) > math.MaxUint16 {
			return fmt.Errorf("value %d overflows uint16", s)
		}
		*dest = uint16(s) //nolint:gosec // G115 - overflow checked above
	case int64:
		if s < 0 || s > math.MaxUint16 {
			return fmt.Errorf("value %d overflows uint16", s)
		}
		*dest = uint16(s)
	case int:
		if s < 0 || int64(s) > math.MaxUint16 {
			return fmt.Errorf("value %d overflows uint16", s)
		}
		*dest = uint16(s) //nolint:gosec // G115 - overflow checked above
	default:
		return fmt.Errorf("unsupported conversion from %T to *uint16", src)
	}
	return nil
}

//nolint:dupl // Intentionally similar to assignUint32/assignUint16 - type-safe helpers for test utilities
func assignUint8(dest *uint8, src any) error {
	switch s := src.(type) {
	case uint64:
		if s > math.MaxUint8 {
			return fmt.Errorf("value %d overflows uint8", s)
		}
		*dest = uint8(s)
	case uint:
		if uint64(s) > math.MaxUint8 {
			return fmt.Errorf("value %d overflows uint8", s)
		}
		*dest = uint8(s) //nolint:gosec // G115 - overflow checked above
	case int64:
		if s < 0 || s > math.MaxUint8 {
			return fmt.Errorf("value %d overflows uint8", s)
		}
		*dest = uint8(s)
	case int:
		if s < 0 || int64(s) > math.MaxUint8 {
			return fmt.Errorf("value %d overflows uint8", s)
		}
		*dest = uint8(s) //nolint:gosec // G115 - overflow checked above
	default:
		return fmt.Errorf("unsupported conversion from %T to *uint8", src)
	}
	return nil
}

// Pointer-to-pointer assignment functions for unsigned integers
func assignUintPtr(dest **uint, src any) error {
	var temp uint
	if err := assignUint(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignUint64Ptr(dest **uint64, src any) error {
	var temp uint64
	if err := assignUint64(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignUint32Ptr(dest **uint32, src any) error {
	var temp uint32
	if err := assignUint32(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignUint16Ptr(dest **uint16, src any) error {
	var temp uint16
	if err := assignUint16(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

func assignUint8Ptr(dest **uint8, src any) error {
	var temp uint8
	if err := assignUint8(&temp, src); err != nil {
		return err
	}
	*dest = &temp
	return nil
}

// setDestNil handles NULL source values for scan destinations.
// Matches database/sql behavior: scalar pointers (*int, *string, etc.) reject NULL with an error,
// while pointer-to-pointer types (**int, **string) accept NULL by setting to nil.
//
// This ensures test fakes accurately simulate production database/sql behavior, preventing tests
// from passing when production code would fail on NULL values.
//
//nolint:gocyclo // Nil handling requires exhaustive case coverage for all supported types
func setDestNil(dest any) error {
	switch d := dest.(type) {
	// Pointer-to-pointer types: set to nil (nullable columns)
	case **string:
		*d = nil
	case **int:
		*d = nil
	case **int64:
		*d = nil
	case **int32:
		*d = nil
	case **bool:
		*d = nil
	case **float64:
		*d = nil
	case **time.Time:
		*d = nil
	case **uint:
		*d = nil
	case **uint64:
		*d = nil
	case **uint32:
		*d = nil
	case **uint16:
		*d = nil
	case **uint8:
		*d = nil

	// Special cases: []byte and any interface can accept nil
	case *[]byte:
		*d = nil
	case **[]byte:
		*d = nil
	case *any:
		*d = nil

	// Scalar pointer types (*int, *string, *bool, etc.) intentionally omitted
	// They fall through to default case and return an error, matching database/sql behavior
	default:
		// For sql.Scanner types, let them handle nil
		if scanner, ok := dest.(sql.Scanner); ok {
			return scanner.Scan(nil)
		}

		// Match database/sql error format for scalar types
		switch dest.(type) {
		case *string:
			return fmt.Errorf("sql: converting NULL to string is unsupported")
		case *int:
			return fmt.Errorf("sql: converting NULL to int is unsupported")
		case *int64:
			return fmt.Errorf("sql: converting NULL to int64 is unsupported")
		case *int32:
			return fmt.Errorf("sql: converting NULL to int32 is unsupported")
		case *bool:
			return fmt.Errorf("sql: converting NULL to bool is unsupported")
		case *float64:
			return fmt.Errorf("sql: converting NULL to float64 is unsupported")
		case *time.Time:
			return fmt.Errorf("sql: converting NULL to time.Time is unsupported")
		case *uint:
			return fmt.Errorf("sql: converting NULL to uint is unsupported")
		case *uint64:
			return fmt.Errorf("sql: converting NULL to uint64 is unsupported")
		case *uint32:
			return fmt.Errorf("sql: converting NULL to uint32 is unsupported")
		case *uint16:
			return fmt.Errorf("sql: converting NULL to uint16 is unsupported")
		case *uint8:
			return fmt.Errorf("sql: converting NULL to uint8 is unsupported")
		default:
			return fmt.Errorf("unsupported scan destination type %T", dest)
		}
	}
	return nil
}

// testResult implements sql.Result for Exec results.
type testResult struct {
	rowsAffected int64
	lastInsertID int64
}

func (r *testResult) LastInsertId() (int64, error) {
	return r.lastInsertID, nil
}

func (r *testResult) RowsAffected() (int64, error) {
	return r.rowsAffected, nil
}
