package database

import (
	"context"
	"database/sql"
)

// WithTx runs fn inside a database transaction. It commits when fn returns nil,
// rolls back and returns fn's original error when fn returns an error, and rolls
// back then re-panics if fn panics. After a successful commit the deferred
// rollback is skipped (a committed flag guards it), so there is no post-commit
// rollback noise on the happy path.
//
// fn must use the provided tx for all database work; using the outer db handle
// inside fn escapes the transaction.
func WithTx(ctx context.Context, db Interface, fn func(ctx context.Context, tx Tx) error) error {
	return WithTxOptions(ctx, db, nil, fn)
}

// WithTxOptions behaves like WithTx but begins the transaction with the given
// options (isolation level, read-only mode) via BeginTx. A nil opts is
// equivalent to WithTx.
func WithTxOptions(ctx context.Context, db Interface, opts *sql.TxOptions, fn func(ctx context.Context, tx Tx) error) error {
	var (
		tx  Tx
		err error
	)
	if opts != nil {
		tx, err = db.BeginTx(ctx, opts)
	} else {
		tx, err = db.Begin(ctx)
	}
	if err != nil {
		return err
	}

	committed := false
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
		// Roll back unless the transaction already committed. The rollback error
		// is intentionally ignored: fn's (or Commit's) error is the one that
		// matters, and the tracking layer still logs a genuine rollback failure.
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	if ferr := fn(ctx, tx); ferr != nil {
		return ferr
	}
	if cerr := tx.Commit(ctx); cerr != nil {
		return cerr
	}
	committed = true
	return nil
}
