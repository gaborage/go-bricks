package leasescope

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFromContextReturnsFalseWhenNoScope(t *testing.T) {
	_, ok := FromContext(context.Background())
	assert.False(t, ok)
}

func TestInstallMakesScopeRetrievable(t *testing.T) {
	ctx, scope := Install(context.Background())
	require.NotNil(t, scope)

	got, ok := FromContext(ctx)
	assert.True(t, ok)
	assert.Same(t, scope, got)
}

func TestRegisterWithScopeDefersReleaseUntilReleaseAll(t *testing.T) {
	ctx, scope := Install(context.Background())

	var released atomic.Bool
	Register(ctx, func() { released.Store(true) })

	// Not released on registration — the lease is held for the unit of work.
	assert.False(t, released.Load(), "release must not run on Register when a scope is present")

	scope.ReleaseAll()
	assert.True(t, released.Load(), "release must run on ReleaseAll")
}

func TestRegisterWithoutScopeReleasesImmediately(t *testing.T) {
	var released atomic.Bool
	// No scope installed in the context → fallback to immediate release.
	Register(context.Background(), func() { released.Store(true) })

	assert.True(t, released.Load(), "release must run immediately when no scope is present")
}

func TestReleaseAllCallsEachReleaseExactlyOnce(t *testing.T) {
	ctx, scope := Install(context.Background())

	var a, b int
	Register(ctx, func() { a++ })
	Register(ctx, func() { b++ })

	scope.ReleaseAll()
	scope.ReleaseAll() // idempotent — a second boundary call must not re-release.

	assert.Equal(t, 1, a)
	assert.Equal(t, 1, b)
}

func TestReleaseAllOnEmptyScopeIsNoOp(t *testing.T) {
	_, scope := Install(context.Background())
	assert.NotPanics(t, func() { scope.ReleaseAll() })
}

func TestAddAfterReleaseAllReleasesImmediately(t *testing.T) {
	_, scope := Install(context.Background())
	scope.ReleaseAll() // the unit-of-work boundary has already passed

	var released atomic.Bool
	scope.Add(func() { released.Store(true) })
	// A lease registered after the boundary must NOT be silently appended to a slice that
	// nothing will drain (a permanent refcount leak) — it must release immediately.
	assert.True(t, released.Load(), "Add after ReleaseAll must release immediately, not drop the lease")
}

func TestRegisterIntoDrainedScopeReleasesImmediately(t *testing.T) {
	ctx, scope := Install(context.Background())
	scope.ReleaseAll()

	var released atomic.Bool
	// Reachable via a value-inheriting detached context (context.WithoutCancel) borrowing
	// after the originating unit of work's ReleaseAll has fired.
	Register(ctx, func() { released.Store(true) })
	assert.True(t, released.Load(), "Register into a drained scope must release immediately")
}

func TestAddIsConcurrencySafe(t *testing.T) {
	_, scope := Install(context.Background())

	var count atomic.Int64
	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			scope.Add(func() { count.Add(1) })
		}()
	}
	wg.Wait()

	scope.ReleaseAll()
	assert.Equal(t, int64(n), count.Load())
}
