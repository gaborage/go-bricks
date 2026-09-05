package database_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/database"
	dbtesting "github.com/gaborage/go-bricks/database/testing"
	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// Compile-time proof every builder and Raw() satisfy SQLProvider.
var (
	_ database.SQLProvider = dbtypes.SelectQueryBuilder(nil)
	_ database.SQLProvider = dbtypes.InsertQueryBuilder(nil)
	_ database.SQLProvider = dbtypes.UpdateQueryBuilder(nil)
	_ database.SQLProvider = dbtypes.DeleteQueryBuilder(nil)
	// SECURITY: Manual SQL review completed - empty static SQL, compile-time interface assertion only, never executed
	_ database.SQLProvider = database.Raw("", nil)
)

// SECURITY: Manual SQL review completed - all SQL in this file is static test
// literal, no user input is concatenated, values are carried as args.
func rawQ(query string, args ...any) database.SQLProvider { return database.Raw(query, args...) }

// errBuildBoom is the fixed cause failingQuery.ToSQL fails with, so build
// failures can be asserted at the same depth as every other infrastructure
// failure in this file.
var errBuildBoom = errors.New("build boom")

// failingQuery is a SQLProvider whose ToSQL always errors, to exercise StageBuild.
type failingQuery struct{}

func (failingQuery) ToSQL() (query string, args []any, err error) {
	return "", nil, errBuildBoom
}

// errIterateSentinel is returned by errRows after one good row, to exercise
// StageIterate — a failure dbtesting.RowSet cannot inject.
var errIterateSentinel = errors.New("iterate boom")

// errRowsConnector/errRowsConn/errRows model a minimal database/sql/driver
// implementation (mirroring database/testing/rowset.go's rowSetConnector/
// rowSetConn/rowSetRows) whose Next returns one good row then a non-io.EOF
// error, so rows.Err() is non-nil after the loop. This local driver exists
// (rather than relying solely on dbtesting.RowSet, which can only induce an
// iterate failure via an unsupported value type in a later row, yielding just
// a message substring) because it proves the error CHAIN survives —
// errors.Is(err, errIterateSentinel) — a stronger assertion for an
// error-taxonomy feature.
type errRowsConnector struct{}

func (errRowsConnector) Connect(context.Context) (driver.Conn, error) {
	return &errRowsConn{}, nil
}

// Driver satisfies driver.Connector; database/sql only calls it via
// db.Driver(), which the QueryContext path used exclusively below never
// triggers.
func (errRowsConnector) Driver() driver.Driver { return nil }

type errRowsConn struct{}

func (*errRowsConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare not supported for errRowsConn")
}

func (*errRowsConn) Close() error { return nil }

func (*errRowsConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not supported for errRowsConn")
}

func (*errRowsConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &errRows{}, nil
}

type errRows struct {
	idx int
}

func (*errRows) Columns() []string { return []string{"id"} }

func (*errRows) Close() error { return nil }

func (r *errRows) Next(dest []driver.Value) error {
	if r.idx == 0 {
		dest[0] = int64(1)
		r.idx++
		return nil
	}
	return errIterateSentinel
}

// errCloseSentinel is returned by closeErrRows.Close, to exercise StageClose —
// a failure only sql.Row.Scan's post-scan Close call can surface;
// dbtesting.RowSet cannot induce it, and ExecuteQueryMany never reaches it
// (Next()==false already closes rows automatically per the database/sql docs).
var errCloseSentinel = errors.New("close boom")

// closeErrRowsConnector/closeErrRowsConn/closeErrRows model a minimal
// database/sql/driver implementation (same shape as errRowsConnector/
// errRowsConn/errRows above) whose Next yields exactly one good row (io.EOF
// after) and whose Close always fails, so ExecuteQuerySingle's explicit
// post-scan Close call surfaces a real driver error through the chain.
type closeErrRowsConnector struct{}

func (closeErrRowsConnector) Connect(context.Context) (driver.Conn, error) {
	return &closeErrRowsConn{}, nil
}

func (closeErrRowsConnector) Driver() driver.Driver { return nil }

type closeErrRowsConn struct{}

func (*closeErrRowsConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("Prepare not supported for closeErrRowsConn")
}

func (*closeErrRowsConn) Close() error { return nil }

func (*closeErrRowsConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not supported for closeErrRowsConn")
}

func (*closeErrRowsConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &closeErrRows{}, nil
}

type closeErrRows struct {
	idx int
}

func (*closeErrRows) Columns() []string { return []string{"id"} }

func (*closeErrRows) Close() error { return errCloseSentinel }

func (r *closeErrRows) Next(dest []driver.Value) error {
	if r.idx == 0 {
		dest[0] = int64(1)
		r.idx++
		return nil
	}
	return io.EOF
}

// stubExecutor implements database.Executor and always returns the *sql.Rows
// it was constructed with from Query, so tests can inject rows backed by a
// custom driver (e.g. errRows) that dbtesting.RowSet cannot produce.
type stubExecutor struct {
	rows *sql.Rows
}

func (s *stubExecutor) Query(context.Context, string, ...any) (*sql.Rows, error) {
	return s.rows, nil
}

func (*stubExecutor) Exec(context.Context, string, ...any) (sql.Result, error) {
	return nil, errors.New("stubExecutor.Exec not implemented")
}

// errRowsAffected is the failure errRowsAffectedResult.RowsAffected
// returns, used to prove ExecuteUpdate/ExecuteUpdateOne wrap a RowsAffected()
// failure at StageRowsAffected — unreachable through dbtesting's TestDB, whose
// fake sql.Result never fails RowsAffected().
var errRowsAffected = errors.New("rows affected boom")

// errRowsAffectedResult is a sql.Result whose RowsAffected always errors.
type errRowsAffectedResult struct{}

func (errRowsAffectedResult) LastInsertId() (int64, error) {
	return 0, errors.New("errRowsAffectedResult.LastInsertId not implemented")
}

func (errRowsAffectedResult) RowsAffected() (int64, error) { return 0, errRowsAffected }

// rowsAffectedErrExecutor is a database.Executor whose Exec always succeeds
// but returns a result that fails RowsAffected.
type rowsAffectedErrExecutor struct{}

func (rowsAffectedErrExecutor) Query(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("rowsAffectedErrExecutor.Query not implemented")
}

func (rowsAffectedErrExecutor) Exec(context.Context, string, ...any) (sql.Result, error) {
	return errRowsAffectedResult{}, nil
}

// assertExecError asserts err wraps a *database.ExecError with the given
// stage and op, and — when cause is non-nil — that the error chain reaches
// cause. Centralizing this keeps every infrastructure-failure test in this
// file at the same assertion depth.
func assertExecError(t *testing.T, err error, stage database.ExecStage, op string, cause error) {
	t.Helper()
	var ee *database.ExecError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, stage, ee.Stage)
	assert.Equal(t, op, ee.Op)
	if cause != nil {
		assert.ErrorIs(t, err, cause)
	}
}

// assertNoRows asserts err is the op-labeled ErrNoRows wrap: both sentinels
// match via errors.Is, IsNotFound reports true, the message names op, and it
// is NOT an *ExecError. Centralizing this keeps the zero-row-outcome
// assertion depth identical across ExecuteQuerySingle and ExecuteUpdateOne.
func assertNoRows(t *testing.T, err error, op string) {
	t.Helper()
	require.ErrorIs(t, err, database.ErrNoRows)
	require.ErrorIs(t, err, sql.ErrNoRows)
	assert.True(t, database.IsNotFound(err))
	assert.Contains(t, err.Error(), op)
	var ee *database.ExecError
	assert.NotErrorAs(t, err, &ee, "no-rows must not be an ExecError")
}

func TestExecuteQuerySingleScansRow(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(dbtesting.NewRowSet("id", "name").AddRow(int64(7), "Alice"))

	var id int64
	var name string
	err := database.ExecuteQuerySingle(t.Context(), db,
		rawQ("SELECT id, name FROM users WHERE id = $1", 7),
		"user_lookup", &id, &name)
	require.NoError(t, err)
	assert.Equal(t, int64(7), id)
	assert.Equal(t, "Alice", name)
}

func TestExecuteQuerySingleNoRows(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(dbtesting.NewRowSet("id"))

	var id int64
	err := database.ExecuteQuerySingle(t.Context(), db,
		rawQ("SELECT id FROM users WHERE id = $1", 7),
		"user_lookup", &id)
	require.Error(t, err)
	assertNoRows(t, err, "user_lookup")
}

func TestExecuteQuerySingleExecError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	sentinel := errors.New("boom")
	db.ExpectQuery("SELECT").WillReturnError(sentinel)

	var id int64
	err := database.ExecuteQuerySingle(t.Context(), db,
		rawQ("SELECT id FROM users"), "user_lookup", &id)
	require.Error(t, err)
	assertExecError(t, err, database.StageExec, "user_lookup", sentinel)
}

func TestExecuteQuerySingleBuildError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

	var id int64
	err := database.ExecuteQuerySingle(t.Context(), db, failingQuery{}, "user_lookup", &id)
	require.Error(t, err)
	assertExecError(t, err, database.StageBuild, "user_lookup", errBuildBoom)
	assert.Empty(t, db.QueryLog(), "ToSQL failure must short-circuit before Query is called")
}

func TestExecuteQuerySingleScanError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(dbtesting.NewRowSet("id").AddRow("not-an-int"))

	var id int64
	err := database.ExecuteQuerySingle(t.Context(), db,
		rawQ("SELECT id FROM users"), "user_lookup", &id)
	require.Error(t, err)
	assertExecError(t, err, database.StageScan, "user_lookup", nil)
}

func TestExecuteQuerySingleCloseError(t *testing.T) {
	sqlDB := sql.OpenDB(closeErrRowsConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	rows, err := sqlDB.QueryContext(t.Context(), "irrelevant")
	require.NoError(t, err)

	exec := &stubExecutor{rows: rows}
	var id int64
	err = database.ExecuteQuerySingle(t.Context(), exec,
		rawQ("SELECT id FROM users"), "user_lookup", &id)
	require.Error(t, err)
	assertExecError(t, err, database.StageClose, "user_lookup", errCloseSentinel)
}

func TestExecuteQueryManyCollectsRows(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(
		dbtesting.NewRowSet("id").AddRow(int64(1)).AddRow(int64(2)).AddRow(int64(3)),
	)

	var ids []int64
	err := database.ExecuteQueryMany(t.Context(), db,
		rawQ("SELECT id FROM users"), "list_users",
		func(rows *sql.Rows) error {
			var id int64
			if scanErr := rows.Scan(&id); scanErr != nil {
				return scanErr
			}
			ids = append(ids, id)
			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, ids)
}

func TestExecuteQueryManyZeroRowsIsNotError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(dbtesting.NewRowSet("id"))

	called := false
	err := database.ExecuteQueryMany(t.Context(), db,
		rawQ("SELECT id FROM users"), "list_users",
		func(*sql.Rows) error {
			called = true
			return nil
		})
	require.NoError(t, err)
	assert.False(t, called)
}

func TestExecuteQueryManyNilScanCallback(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))

	err := database.ExecuteQueryMany(t.Context(), db, rawQ("SELECT id FROM users"), "list_users", nil)
	require.Error(t, err)
	assertExecError(t, err, database.StageBuild, "list_users", nil)
	assert.Empty(t, db.QueryLog(), "a nil scan callback must short-circuit before the query ever dispatches")
}

func TestExecuteQueryManyCallbackError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT").WillReturnRows(
		dbtesting.NewRowSet("id").AddRow(int64(1)).AddRow(int64(2)),
	)
	sentinel := errors.New("callback boom")

	count := 0
	err := database.ExecuteQueryMany(t.Context(), db,
		rawQ("SELECT id FROM users"), "list_users",
		func(rows *sql.Rows) error {
			count++
			if count == 2 {
				return sentinel
			}
			var id int64
			return rows.Scan(&id)
		})
	require.Error(t, err)
	assertExecError(t, err, database.StageScan, "list_users", sentinel)
}

func TestExecuteQueryManyIterateError(t *testing.T) {
	sqlDB := sql.OpenDB(errRowsConnector{})
	t.Cleanup(func() { _ = sqlDB.Close() })
	rows, err := sqlDB.QueryContext(t.Context(), "irrelevant")
	require.NoError(t, err)

	exec := &stubExecutor{rows: rows}
	err = database.ExecuteQueryMany(t.Context(), exec,
		rawQ("SELECT id FROM users"), "list_users",
		func(rows *sql.Rows) error {
			var id int64
			return rows.Scan(&id)
		})
	require.Error(t, err)
	assertExecError(t, err, database.StageIterate, "list_users", errIterateSentinel)
}

func TestExecuteUpdateReturnsAffected(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("UPDATE").WillReturnRowsAffected(3)

	n, err := database.ExecuteUpdate(t.Context(), db,
		rawQ("UPDATE users SET active = true"), "activate_users")
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)
}

func TestExecuteUpdateZeroRowsReturnsCount(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("UPDATE").WillReturnRowsAffected(0)

	n, err := database.ExecuteUpdate(t.Context(), db,
		rawQ("UPDATE sessions SET expired = true WHERE expires_at < now()"),
		"expire_sessions")
	require.NoError(t, err, "zero rows affected is a legitimate outcome, not an error")
	assert.Equal(t, int64(0), n)
}

func TestExecuteUpdateExecError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	sentinel := errors.New("boom")
	db.ExpectExec("UPDATE").WillReturnError(sentinel)

	n, err := database.ExecuteUpdate(t.Context(), db,
		rawQ("UPDATE users SET active = true"), "activate_users")
	require.Error(t, err)
	assert.Equal(t, int64(0), n)
	assertExecError(t, err, database.StageExec, "activate_users", sentinel)
}

func TestExecuteUpdateRowsAffectedError(t *testing.T) {
	exec := rowsAffectedErrExecutor{}

	n, err := database.ExecuteUpdate(t.Context(), exec,
		rawQ("UPDATE users SET active = true"), "activate_users")
	require.Error(t, err)
	assert.Equal(t, int64(0), n)
	assertExecError(t, err, database.StageRowsAffected, "activate_users", errRowsAffected)
}

func TestExecuteUpdateOneSucceeds(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("UPDATE").WillReturnRowsAffected(1)

	err := database.ExecuteUpdateOne(t.Context(), db,
		rawQ("UPDATE users SET active = true WHERE id = $1", 1), "activate_user")
	require.NoError(t, err)
}

func TestExecuteUpdateOneZeroRowsIsErrNoRows(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("UPDATE").WillReturnRowsAffected(0)

	err := database.ExecuteUpdateOne(t.Context(), db,
		rawQ("UPDATE users SET active = true WHERE id = $1", 999),
		"activate_user")
	require.Error(t, err)
	assertNoRows(t, err, "activate_user")
}

func TestExecuteUpdateOneRejectsMultipleRows(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("UPDATE").WillReturnRowsAffected(3)

	err := database.ExecuteUpdateOne(t.Context(), db,
		rawQ("UPDATE users SET active = true WHERE tier = $1", "gold"),
		"activate_tier")
	require.Error(t, err)
	assertExecError(t, err, database.StageRowsAffected, "activate_tier", nil)
	assert.Contains(t, err.Error(), "3", "message must name the offending count")
}

func TestExecuteUpdateOneExecError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	sentinel := errors.New("boom")
	db.ExpectExec("UPDATE").WillReturnError(sentinel)

	err := database.ExecuteUpdateOne(t.Context(), db,
		rawQ("UPDATE users SET active = true WHERE id = $1", 1), "activate_user")
	require.Error(t, err)
	assertExecError(t, err, database.StageExec, "activate_user", sentinel)
}

func TestExecuteUpdateOneRowsAffectedError(t *testing.T) {
	exec := rowsAffectedErrExecutor{}

	err := database.ExecuteUpdateOne(t.Context(), exec,
		rawQ("UPDATE users SET active = true WHERE id = $1", 1), "activate_user")
	require.Error(t, err)
	assertExecError(t, err, database.StageRowsAffected, "activate_user", errRowsAffected)
}

func TestExecuteInsertSucceeds(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	// 0 rows affected on a successful INSERT is what proves ExecuteInsert does
	// NOT unify with the zero-rows-is-an-error behavior — that is exactly the
	// distinction ExecuteUpdateOne exists to draw.
	db.ExpectExec("INSERT").WillReturnRowsAffected(0)

	err := database.ExecuteInsert(t.Context(), db,
		rawQ("INSERT INTO users (name) VALUES ($1)", "Alice"),
		"create_user")
	require.NoError(t, err)
}

func TestExecuteInsertExecError(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	sentinel := errors.New("boom")
	db.ExpectExec("INSERT").WillReturnError(sentinel)

	err := database.ExecuteInsert(t.Context(), db,
		rawQ("INSERT INTO users (name) VALUES ($1)", "Alice"),
		"create_user")
	require.Error(t, err)
	assertExecError(t, err, database.StageExec, "create_user", sentinel)
}

func TestExecuteHelpersRunInsideTransaction(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().ExpectQuery("SELECT").WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))

	err := database.WithTx(t.Context(), db, func(ctx context.Context, tx dbtypes.Tx) error {
		var id int64
		return database.ExecuteQuerySingle(ctx, tx,
			rawQ("SELECT id FROM users WHERE id = $1", 1),
			"user_lookup", &id)
	})
	require.NoError(t, err)
}

func TestRawToSQLPassthrough(t *testing.T) {
	// SECURITY: Manual SQL review completed - static literal, no user input; the value side is carried as an arg
	query, args, err := database.Raw("SELECT 1", 2).ToSQL()
	require.NoError(t, err)
	assert.Equal(t, "SELECT 1", query)
	assert.Equal(t, []any{2}, args)
}

func TestBuildersSatisfySQLProvider(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT id FROM users").WillReturnRows(dbtesting.NewRowSet("id").AddRow(int64(1)))

	qb := database.NewQueryBuilder(dbtypes.PostgreSQL).Select("id").From("users")

	var ids []int64
	err := database.ExecuteQueryMany(t.Context(), db, qb, "list_users",
		func(rows *sql.Rows) error {
			var id int64
			if scanErr := rows.Scan(&id); scanErr != nil {
				return scanErr
			}
			ids = append(ids, id)
			return nil
		})
	require.NoError(t, err)
	assert.Equal(t, []int64{1}, ids)
}

// TestUpdateBuilderNotRejectedAsFragment is the regression fence for the
// fragment guard in buildQuery: an UpdateQueryBuilder result carries a WHERE
// clause internally but its own ToSQL() renders a complete UPDATE statement,
// so it must reach exec.Exec rather than being rejected at StageBuild the way
// a bare Filter/JoinFilter is.
func TestUpdateBuilderNotRejectedAsFragment(t *testing.T) {
	db := dbtesting.NewTestDB(dbtypes.PostgreSQL)
	db.ExpectExec("UPDATE users").WillReturnRowsAffected(1)

	qb := database.NewQueryBuilder(dbtypes.PostgreSQL)
	q := qb.Update("users").Set("active", true).Where(qb.Filter().Eq("id", 1))

	err := database.ExecuteUpdateOne(t.Context(), db, q, "activate_user")
	require.NoError(t, err)
	require.Len(t, db.ExecLog(), 1, "the UPDATE must actually reach Exec, not be rejected at StageBuild")
}

func TestExecuteRejectsWhereFragment(t *testing.T) {
	qb := database.NewQueryBuilder(dbtypes.PostgreSQL)

	t.Run("filter", func(t *testing.T) {
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

		var id int64
		err := database.ExecuteQuerySingle(t.Context(), db, qb.Filter().Eq("id", 5), "user_lookup", &id)
		require.Error(t, err)
		assertExecError(t, err, database.StageBuild, "user_lookup", nil)
		assert.Contains(t, err.Error(), "fragment")
		assert.Empty(t, db.QueryLog(), "a rejected fragment must never reach Query")
	})

	t.Run("join_filter", func(t *testing.T) {
		db := dbtesting.NewTestDB(dbtypes.PostgreSQL)

		var id int64
		err := database.ExecuteQuerySingle(t.Context(), db, qb.JoinFilter().EqColumn("a.id", "b.id"), "user_lookup", &id)
		require.Error(t, err)
		assertExecError(t, err, database.StageBuild, "user_lookup", nil)
		assert.Contains(t, err.Error(), "fragment")
		assert.Empty(t, db.QueryLog(), "a rejected fragment must never reach Query")
	})
}
