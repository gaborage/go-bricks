package outbox

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbtesting "github.com/gaborage/go-bricks/database/testing"
)

func newCleanupWithFakes(store *fakeStore, retention time.Duration) *Cleanup {
	return &Cleanup{store: store, retentionPeriod: retention}
}

func TestCleanupExecuteReturnsErrorWhenDBUnavailable(t *testing.T) {
	c := newCleanupWithFakes(&fakeStore{}, 24*time.Hour)
	ctx := newFakeJobCtx(nil, nil)

	err := c.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database not available")
}

func TestCleanupExecuteWrapsDeleteError(t *testing.T) {
	store := &fakeStore{DeletePublishedErr: errors.New("constraint conflict")}
	c := newCleanupWithFakes(store, 24*time.Hour)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	err := c.Execute(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "delete failed")
	assert.Contains(t, err.Error(), "constraint conflict")
	assert.Equal(t, 1, store.DeletePublishedCalls)
}

func TestCleanupExecuteUsesRetentionCutoff(t *testing.T) {
	store := &fakeStore{DeletePublishedN: 0}
	retention := 7 * 24 * time.Hour
	c := newCleanupWithFakes(store, retention)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	before := time.Now().Add(-retention)
	require.NoError(t, c.Execute(ctx))
	after := time.Now().Add(-retention)

	// The cutoff captured by the store should sit inside the [before, after]
	// window — we can't pin an exact value because time.Now() is the cutoff
	// source, but the bounds prove the retention math is applied.
	assert.True(t, !store.DeletePublishedCutoff.Before(before) && !store.DeletePublishedCutoff.After(after),
		"cutoff %s should fall inside [%s, %s]", store.DeletePublishedCutoff, before, after)
}

func TestCleanupExecuteSucceedsWhenNothingDeleted(t *testing.T) {
	// deleted == 0 must not log an Info line (only logs when > 0) but the
	// Execute function still returns nil. We assert behaviour via the
	// store's call count + error.
	store := &fakeStore{DeletePublishedN: 0}
	c := newCleanupWithFakes(store, 24*time.Hour)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	require.NoError(t, c.Execute(ctx))
	assert.Equal(t, 1, store.DeletePublishedCalls)
}

func TestCleanupExecuteSucceedsWhenRowsDeleted(t *testing.T) {
	store := &fakeStore{DeletePublishedN: 42}
	c := newCleanupWithFakes(store, 24*time.Hour)
	db := dbtesting.NewTestDB("postgresql")
	ctx := newFakeJobCtx(db, nil)

	require.NoError(t, c.Execute(ctx))
	assert.Equal(t, 1, store.DeletePublishedCalls)
}
