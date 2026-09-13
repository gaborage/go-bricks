package testing

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

// scopedQuerier is the statement surface the three expectation holders share:
// the pool handle itself, a transaction and a pinned session. The tests below
// drive all three through it so one table can pin the error TEXT each scope
// produces — the strings consumers read in a failing test, and the only thing
// distinguishing a pool miss from a transaction or session miss.
type scopedQuerier interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRow(ctx context.Context, query string, args ...any) dbtypes.Row
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// scopeCase names a holder and how to obtain it from a fresh TestDB.
type scopeCase struct {
	name string
	open func(db *TestDB) scopedQuerier
}

func scopeCases() []scopeCase {
	return []scopeCase{
		{name: "db", open: func(db *TestDB) scopedQuerier { return db }},
		{name: "transaction", open: func(db *TestDB) scopedQuerier { return db.ExpectTransaction() }},
		{name: "session", open: func(db *TestDB) scopedQuerier { return db.ExpectSession() }},
	}
}

func TestExpectationScopeNamesTheUnexpectedQuery(t *testing.T) {
	// The pool path says "unexpected query"; the two per-handle scopes name
	// themselves, so a miss cannot be misread as a pool miss.
	want := map[string]string{
		"db":          "unexpected query: SELECT missing (no matching expectation)",
		"transaction": "unexpected query in transaction: SELECT missing (no matching expectation)",
		"session":     "unexpected query in session: SELECT missing (no matching expectation)",
	}

	for _, tc := range scopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.open(NewTestDB(dbtypes.PostgreSQL))

			rows, err := q.Query(context.Background(), "SELECT missing")
			require.Nil(t, rows)
			require.EqualError(t, err, want[tc.name])

			// QueryRow funnels the same text through the row's deferred error.
			require.EqualError(t, q.QueryRow(context.Background(), "SELECT missing").Scan(new(int)), want[tc.name])
		})
	}
}

func TestExpectationScopeNamesTheUnexpectedExec(t *testing.T) {
	want := map[string]string{
		"db":          "unexpected exec: DELETE FROM missing (no matching expectation)",
		"transaction": "unexpected exec in transaction: DELETE FROM missing (no matching expectation)",
		"session":     "unexpected exec in session: DELETE FROM missing (no matching expectation)",
	}

	for _, tc := range scopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.open(NewTestDB(dbtypes.PostgreSQL))

			res, err := q.Exec(context.Background(), "DELETE FROM missing")
			require.Nil(t, res)
			require.EqualError(t, err, want[tc.name])
		})
	}
}

func TestExpectationScopeNamesTheMissingRowSet(t *testing.T) {
	// A matched query expectation with no WillReturnRows is a test-authoring
	// mistake, and each scope says so in its own words.
	want := map[string]string{
		"db":          `query expectation for "SELECT name FROM users" has no rows configured (use WillReturnRows)`,
		"transaction": `transaction query expectation for "SELECT name FROM users" has no rows configured`,
		"session":     `session query expectation for "SELECT name FROM users" has no rows configured`,
	}
	expect := map[string]func(db *TestDB) scopedQuerier{
		"db": func(db *TestDB) scopedQuerier {
			db.ExpectQuery("SELECT name")
			return db
		},
		"transaction": func(db *TestDB) scopedQuerier { return db.ExpectTransaction().ExpectQuery("SELECT name") },
		"session":     func(db *TestDB) scopedQuerier { return db.ExpectSession().ExpectQuery("SELECT name") },
	}

	for _, tc := range scopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			q := expect[tc.name](NewTestDB(dbtypes.PostgreSQL))

			rows, err := q.Query(context.Background(), "SELECT name FROM users")
			require.Nil(t, rows)
			require.EqualError(t, err, want[tc.name])
		})
	}
}

func TestExpectationSetReturnsTheConfiguredError(t *testing.T) {
	sentinel := errors.New("boom")

	t.Run("transaction_query", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		tx := db.ExpectTransaction().ExpectQuery("SELECT name").WillReturnError(sentinel)

		rows, err := tx.Query(context.Background(), "SELECT name FROM users")
		require.Nil(t, rows)
		require.ErrorIs(t, err, sentinel)
	})

	t.Run("session_exec", func(t *testing.T) {
		db := NewTestDB(dbtypes.PostgreSQL)
		sess := db.ExpectSession().ExpectExec("UPDATE users").WillReturnError(sentinel)

		res, err := sess.Exec(context.Background(), "UPDATE users SET name = $1", "a")
		require.Nil(t, res)
		require.ErrorIs(t, err, sentinel)
	})
}

func TestExpectationSetGuardPreemptsMatching(t *testing.T) {
	// The session is the only holder with a guard: once closed it refuses every
	// statement with sql.ErrConnDone, BEFORE a matching expectation is consulted,
	// and the call is not logged.
	db := NewTestDB(dbtypes.PostgreSQL)
	sess := db.ExpectSession().
		ExpectQuery("SELECT name").WillReturnRows(NewRowSet("name").AddRow("Alice")).
		ExpectExec("UPDATE users").WillReturnRowsAffected(1)
	require.NoError(t, sess.Close())

	rows, err := sess.Query(context.Background(), "SELECT name FROM users")
	require.Nil(t, rows)
	require.ErrorIs(t, err, sql.ErrConnDone)

	res, execErr := sess.Exec(context.Background(), "UPDATE users SET name = $1", "a")
	require.Nil(t, res)
	require.ErrorIs(t, execErr, sql.ErrConnDone)

	require.ErrorIs(t, sess.QueryRow(context.Background(), "SELECT name FROM users").Scan(new(string)), sql.ErrConnDone)

	assert.Empty(t, sess.QueryLog())
	assert.Empty(t, sess.ExecLog())
}

func TestExpectationSetWithoutGuardResolvesNormally(t *testing.T) {
	// A transaction sets no guard, so the nil-guard arm must fall through to
	// matching rather than refusing the statement.
	db := NewTestDB(dbtypes.PostgreSQL)
	tx := db.ExpectTransaction().
		ExpectQuery("SELECT name").WillReturnRows(NewRowSet("name").AddRow("Alice"))

	rows, err := tx.Query(context.Background(), "SELECT name FROM users")
	require.NoError(t, err)
	defer func() { assert.NoError(t, rows.Close()) }()

	require.True(t, rows.Next())
	var name string
	require.NoError(t, rows.Scan(&name))
	assert.Equal(t, "Alice", name)
	require.NoError(t, rows.Err())
}

func TestExpectationSetLogsEveryResolvedCall(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	sess := db.ExpectSession().
		ExpectQuery("SELECT name").WillReturnRows(NewRowSet("name").AddRow("Alice")).
		ExpectExec("UPDATE users").WillReturnRowsAffected(1)

	querySession(t, sess, "SELECT name FROM users", 7)
	_, err := sess.Exec(context.Background(), "UPDATE users SET name = $1", "a")
	require.NoError(t, err)
	// An unmatched statement is logged too: the log is the call record, not the
	// match record.
	_, err = sess.Exec(context.Background(), "DELETE FROM users")
	require.Error(t, err)

	queries := sess.QueryLog()
	require.Len(t, queries, 1)
	assert.Equal(t, "SELECT name FROM users", queries[0].SQL)
	assert.Equal(t, []any{7}, queries[0].Args)

	execs := sess.ExecLog()
	require.Len(t, execs, 2)
	assert.Equal(t, "UPDATE users SET name = $1", execs[0].SQL)
	assert.Equal(t, "DELETE FROM users", execs[1].SQL)
}

func TestExpectationSetQueryRowReportsNoRows(t *testing.T) {
	db := NewTestDB(dbtypes.PostgreSQL)
	sess := db.ExpectSession().
		ExpectQuery("SELECT name").WillReturnRows(NewRowSet("name"))

	require.ErrorIs(t, sess.QueryRow(context.Background(), "SELECT name FROM users").Scan(new(string)), sql.ErrNoRows)
}

func TestExpectationSetErrorSettersIgnoreAnEmptyQueue(t *testing.T) {
	// WillReturn* before any Expect* has nothing to configure and must not panic.
	db := NewTestDB(dbtypes.PostgreSQL)
	sess := db.ExpectSession().
		WillReturnError(errors.New("nothing to attach to")).
		WillReturnRows(NewRowSet("name").AddRow("Alice")).
		WillReturnRowsAffected(3)

	rows, err := sess.Query(context.Background(), "SELECT name FROM users")
	require.Nil(t, rows)
	require.EqualError(t, err, "unexpected query in session: SELECT name FROM users (no matching expectation)")
}
