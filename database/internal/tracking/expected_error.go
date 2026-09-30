package tracking

import (
	"context"
	"database/sql"
	"errors"
)

// expectedErrorKey is the unexported context-key type under which the
// caller-declared expected-error predicate is stored.
type expectedErrorKey struct{}

// WithExpectedError returns a copy of ctx that declares errors matching expected
// as an anticipated outcome of the database operations executed with it. The
// tracking layer then treats a matching failure as benign: it is logged at DEBUG
// instead of ERROR, the span is not marked Error, and request severity is not
// escalated. The caller still receives the error unchanged.
//
// Scope the declaration to the single statement whose failure is expected (for
// example a SELECT ... FOR UPDATE NOWAIT under IsLockNotAvailable); the same error
// on any other statement is a real failure and must stay loud. A nested
// declaration composes with an outer one: an error is expected when either
// predicate matches. A nil ctx or nil predicate returns ctx unchanged.
func WithExpectedError(ctx context.Context, expected func(error) bool) context.Context {
	if ctx == nil || expected == nil {
		return ctx
	}
	if outer, ok := ctx.Value(expectedErrorKey{}).(func(error) bool); ok {
		inner := expected
		expected = func(err error) bool { return inner(err) || outer(err) }
	}
	return context.WithValue(ctx, expectedErrorKey{}, expected)
}

// isDeclaredExpected reports whether err matches a predicate declared on ctx via
// WithExpectedError.
func isDeclaredExpected(ctx context.Context, err error) bool {
	if ctx == nil || err == nil {
		return false
	}
	expected, ok := ctx.Value(expectedErrorKey{}).(func(error) bool)
	return ok && expected(err)
}

// isBenignError is the single decision every tracking sink (log level, span
// status, severity escalation) consults: sql.ErrNoRows (empty result),
// sql.ErrTxDone (deferred rollback after commit) and caller-declared expected
// errors are not failures.
func isBenignError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, sql.ErrNoRows) || errors.Is(err, sql.ErrTxDone) || isDeclaredExpected(ctx, err)
}
