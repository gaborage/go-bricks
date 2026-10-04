package testing

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtypes "github.com/gaborage/go-bricks/database/types"
)

func TestAssertTxOptions(t *testing.T) {
	serializableReadOnly := &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}
	tests := []struct {
		name     string
		recorded *sql.TxOptions
		want     *sql.TxOptions
		wantFail bool
	}{
		{name: "match_passes", recorded: serializableReadOnly, want: &sql.TxOptions{Isolation: sql.LevelSerializable, ReadOnly: true}},
		{name: "both_nil_passes"},
		{name: "nil_vs_non_nil_fails", want: &sql.TxOptions{}, wantFail: true},
		{name: "non_nil_vs_nil_fails", recorded: &sql.TxOptions{}, wantFail: true},
		{name: "isolation_mismatch_fails", recorded: serializableReadOnly, want: &sql.TxOptions{Isolation: sql.LevelReadCommitted, ReadOnly: true}, wantFail: true},
		{name: "read_only_mismatch_fails", recorded: serializableReadOnly, want: &sql.TxOptions{Isolation: sql.LevelSerializable}, wantFail: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := NewTestDB(dbtypes.PostgreSQL)
			db.ExpectTransaction()
			tx, err := db.BeginTx(t.Context(), tt.recorded)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(t.Context()) }()

			recorder := &testing.T{}
			AssertTxOptions(recorder, tx.(*TestTx), tt.want)

			assert.Equal(t, tt.wantFail, recorder.Failed())
		})
	}
}

func TestAssertAllExpectationsMetPassesWhenEverythingWasMet(t *testing.T) {
	ctx := t.Context()
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT id").WillReturnRows(NewRowSet("id").AddRow(1))
	db.ExpectExec("INSERT INTO users").WillReturnRowsAffected(1)
	db.ExpectTransaction().ExpectExec("UPDATE orders").WillReturnRowsAffected(1)
	sess := db.ExpectSession().ExpectExec("pg_advisory_lock").WillReturnRowsAffected(1)
	sess.ExpectTransaction().ExpectQuery("SELECT total").WillReturnRows(NewRowSet("total").AddRow(2))

	require.NoError(t, queryErr(t, db, "SELECT id FROM users"))
	_, err := db.Exec(ctx, "INSERT INTO users VALUES (1)")
	require.NoError(t, err)
	tx, err := db.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "UPDATE orders SET paid = true")
	require.NoError(t, err)
	s, err := db.Session(ctx)
	require.NoError(t, err)
	_, err = s.Exec(ctx, "SELECT pg_advisory_lock(1)")
	require.NoError(t, err)
	stx, err := s.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = stx.Rollback(ctx) }()
	require.NoError(t, stx.QueryRow(ctx, "SELECT total FROM t").Scan(new(int)))

	assert.Empty(t, unmetExpectations(db))
	recorder := &testing.T{}
	AssertAllExpectationsMet(recorder, db)
	assert.False(t, recorder.Failed())
}

func TestAssertAllExpectationsMetReportsEachUnmetItem(t *testing.T) {
	ctx := t.Context()
	tests := []struct {
		name  string
		setup func(t *testing.T, db *TestDB)
		want  []string
	}{
		{
			name: "unmatched_pool_query",
			setup: func(_ *testing.T, db *TestDB) {
				db.ExpectQuery("SELECT id").WillReturnRows(NewRowSet("id"))
			},
			want: []string{`pool: query "SELECT id" was never matched`},
		},
		{
			name: "unmatched_pool_exec",
			setup: func(_ *testing.T, db *TestDB) {
				db.ExpectExec("DELETE FROM users").WillReturnRowsAffected(1)
			},
			want: []string{`pool: exec "DELETE FROM users" was never matched`},
		},
		{
			name: "shadowed_pattern",
			setup: func(t *testing.T, db *TestDB) {
				db.ExpectExec("UPDATE").WillReturnRowsAffected(1)
				db.ExpectExec("UPDATE users").WillReturnRowsAffected(1)
				_, err := db.Exec(ctx, "UPDATE users SET name = 'x'")
				require.NoError(t, err)
			},
			want: []string{`pool: exec "UPDATE users" was never matched`},
		},
		{
			name: "queued_transaction_never_begun",
			setup: func(_ *testing.T, db *TestDB) {
				db.ExpectTransaction().ExpectExec("INSERT").ExpectQuery("SELECT")
			},
			want: []string{`pool: transaction #1 was never begun`},
		},
		{
			name: "queued_session_never_opened",
			setup: func(_ *testing.T, db *TestDB) {
				sess := db.ExpectSession().ExpectExec("pg_advisory_lock")
				sess.ExpectTransaction().ExpectExec("UPDATE")
			},
			want: []string{`session #1 was never opened`},
		},
		{
			name: "opened_session_with_unused_exec_and_transaction",
			setup: func(t *testing.T, db *TestDB) {
				sess := db.ExpectSession().ExpectExec("pg_advisory_lock").WillReturnRowsAffected(1)
				sess.ExpectTransaction().ExpectExec("UPDATE ledger")
				s, err := db.Session(ctx)
				require.NoError(t, err)
				require.NoError(t, s.Close())

				recorder := &testing.T{}
				AssertSessionClosed(recorder, sess)
				require.False(t, recorder.Failed(), "AssertSessionClosed alone is green")
			},
			want: []string{
				`session #1: exec "pg_advisory_lock" was never matched`,
				`session #1: transaction #1 was never begun`,
			},
		},
		{
			name: "unused_expectation_in_begun_transaction",
			setup: func(t *testing.T, db *TestDB) {
				db.ExpectTransaction().
					ExpectExec("INSERT INTO orders").WillReturnRowsAffected(1).
					ExpectQuery("SELECT total").WillReturnRows(NewRowSet("total"))
				tx, err := db.Begin(ctx)
				require.NoError(t, err)
				defer func() { _ = tx.Rollback(ctx) }()
				_, err = tx.Exec(ctx, "INSERT INTO orders VALUES (1)")
				require.NoError(t, err)
			},
			want: []string{`transaction #1: query "SELECT total" was never matched`},
		},
		{
			name: "unused_expectation_in_begun_session_transaction",
			setup: func(t *testing.T, db *TestDB) {
				db.ExpectSession().ExpectTransaction().ExpectExec("UPDATE ledger")
				s, err := db.Session(ctx)
				require.NoError(t, err)
				_, err = s.Begin(ctx)
				require.NoError(t, err)
			},
			want: []string{`session #1 transaction #1: exec "UPDATE ledger" was never matched`},
		},
		{
			name: "every_scope_in_one_report",
			setup: func(_ *testing.T, db *TestDB) {
				db.ExpectQuery("SELECT a")
				db.ExpectExec("INSERT b")
				db.ExpectTransaction()
				db.ExpectTransaction()
				db.ExpectSession()
			},
			want: []string{
				`pool: query "SELECT a" was never matched`,
				`pool: exec "INSERT b" was never matched`,
				`pool: transaction #1 was never begun`,
				`pool: transaction #2 was never begun`,
				`session #1 was never opened`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := NewTestDB(dbtypes.PostgreSQL)
			tt.setup(t, db)

			assert.Equal(t, tt.want, unmetExpectations(db))
			recorder := &testing.T{}
			AssertAllExpectationsMet(recorder, db)
			assert.True(t, recorder.Failed())
		})
	}
}

func TestAssertAllExpectationsMetCountsErrorsAsMet(t *testing.T) {
	ctx := t.Context()
	boom := errors.New("boom")
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT id").WillReturnError(boom)
	db.ExpectQuery("SELECT name")
	db.ExpectExec("DELETE").WillReturnError(boom)
	db.ExpectTransaction().
		ExpectExec("INSERT").WillReturnError(boom).
		ExpectQuery("SELECT total").WillReturnError(boom).
		ExpectQuery("SELECT bare")

	require.ErrorIs(t, queryErr(t, db, "SELECT id FROM users"), boom)
	require.Error(t, db.QueryRow(ctx, "SELECT name FROM users").Scan(new(string)))
	_, err := db.Exec(ctx, "DELETE FROM users")
	require.ErrorIs(t, err, boom)
	tx, err := db.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "INSERT INTO t VALUES (1)")
	require.ErrorIs(t, err, boom)
	require.ErrorIs(t, queryErr(t, tx, "SELECT total FROM t"), boom)
	require.Error(t, queryErr(t, tx, "SELECT bare FROM t"))

	assert.Empty(t, unmetExpectations(db))
	recorder := &testing.T{}
	AssertAllExpectationsMet(recorder, db)
	assert.False(t, recorder.Failed())
}

func TestAssertAllExpectationsMetSkipsFailedBegin(t *testing.T) {
	ctx := t.Context()
	sentinel := errors.New("begin refused")
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectTransaction().WillFailBegin(sentinel).ExpectExec("INSERT pool")
	db.ExpectSession().ExpectTransaction().WillFailBegin(sentinel).ExpectExec("INSERT session")

	require.ErrorIs(t, beginErr(ctx, db.Begin), sentinel)
	s, err := db.Session(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, beginErr(ctx, func(ctx context.Context) (dbtypes.Tx, error) { return s.BeginTx(ctx, nil) }), sentinel)

	assert.Empty(t, unmetExpectations(db))
	recorder := &testing.T{}
	AssertAllExpectationsMet(recorder, db)
	assert.False(t, recorder.Failed())
}

func TestAssertAllExpectationsMetUnderConcurrentCalls(t *testing.T) {
	ctx := t.Context()
	db := NewTestDB(dbtypes.PostgreSQL)
	db.ExpectQuery("SELECT id").WillReturnRows(NewRowSet("id").AddRow(1))
	db.ExpectExec("INSERT INTO users").WillReturnRowsAffected(1)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := queryErr(t, db, "SELECT id FROM users"); err != nil {
				t.Errorf("query: %v", err)
			}
			if _, err := db.Exec(ctx, "INSERT INTO users VALUES (1)"); err != nil {
				t.Errorf("exec: %v", err)
			}
		})
	}
	wg.Wait()

	recorder := &testing.T{}
	AssertAllExpectationsMet(recorder, db)
	assert.False(t, recorder.Failed())
}

// queryErr runs query on q, closes any rows it returns, and reports only the error.
func queryErr(t *testing.T, q interface {
	Query(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}, query string,
) error {
	t.Helper()
	rows, err := q.Query(t.Context(), query)
	if rows != nil {
		defer rows.Close()
	}
	return err
}

// beginErr begins a transaction expected to fail, rolls back any it did get, and
// reports only the error.
func beginErr(ctx context.Context, begin func(context.Context) (dbtypes.Tx, error)) error {
	tx, err := begin(ctx)
	if tx != nil {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	return err
}
