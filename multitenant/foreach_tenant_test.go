package multitenant

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/leasescope"
)

// leaseLog records lease releases so a test can observe when each tenant's scope drained.
type leaseLog struct{ released []string }

func (l *leaseLog) borrow(ctx context.Context, name string) {
	leasescope.Register(ctx, func() { l.released = append(l.released, name) })
}

func TestForEachTenantVisitsTenantsInOrderEachInItsOwnScope(t *testing.T) {
	outer, enclosing := leasescope.Install(context.Background())
	leases := &leaseLog{}
	var visited []string

	err := ForEachTenant(outer, []string{"a", "b", "c"}, func(ctx context.Context, tenantID string) error {
		got, ok := GetTenant(ctx)
		require.True(t, ok)
		assert.Equal(t, tenantID, got)
		assert.Equal(t, visited, leases.released, "every earlier tenant's lease drained before this one starts")
		visited = append(visited, tenantID)
		leases.borrow(ctx, tenantID)
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, visited)
	assert.Equal(t, []string{"a", "b", "c"}, leases.released)
	enclosing.ReleaseAll()
	assert.Len(t, leases.released, 3, "nothing landed in the enclosing scope")
}

func TestForEachTenantNoTenantsCallsNothing(t *testing.T) {
	for name, tenants := range map[string][]string{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			err := ForEachTenant(context.Background(), tenants, func(context.Context, string) error {
				t.Errorf("fn called for an empty tenant list")
				return nil
			})
			require.NoError(t, err)
		})
	}
}

func TestForEachTenantEmptyTenantRunsOnceWithoutATenant(t *testing.T) {
	calls := 0
	err := ForEachTenant(context.Background(), []string{""}, func(ctx context.Context, tenantID string) error {
		calls++
		assert.Empty(t, tenantID)
		_, ok := GetTenant(ctx)
		assert.False(t, ok, "SetTenant with \"\" is a no-op")
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestForEachTenantJoinsErrorsAndKeepsGoing(t *testing.T) {
	errA, errC := errors.New("tenant a failed"), errors.New("tenant c failed")
	var visited []string

	err := ForEachTenant(context.Background(), []string{"a", "b", "c"}, func(_ context.Context, tenantID string) error {
		visited = append(visited, tenantID)
		switch tenantID {
		case "a":
			return errA
		case "c":
			return errC
		}
		return nil
	})

	require.ErrorIs(t, err, errA)
	require.ErrorIs(t, err, errC)
	assert.Equal(t, []string{"a", "b", "c"}, visited)
	assert.Equal(t, errA.Error()+"\n"+errC.Error(), err.Error(), "fn owns its error text: no wrapping")
}

func TestForEachTenantStopsWhenTheContextIsDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var visited []string

	err := ForEachTenant(ctx, []string{"a", "b", "c"}, func(_ context.Context, tenantID string) error {
		visited = append(visited, tenantID)
		if tenantID == "b" {
			cancel()
		}
		return nil
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, []string{"a", "b"}, visited, "no tenant starts after ctx is done")
}

func TestForEachTenantReleasesTheLeaseBeforeAPanicPropagates(t *testing.T) {
	outer, enclosing := leasescope.Install(context.Background())
	defer enclosing.ReleaseAll()
	leases := &leaseLog{}
	assert.PanicsWithValue(t, "boom", func() {
		_ = ForEachTenant(outer, []string{"a", "b"}, func(ctx context.Context, tenantID string) error {
			leases.borrow(ctx, tenantID)
			panic("boom")
		})
	})
	assert.Equal(t, []string{"a"}, leases.released, "the panicking tenant's scope drained; no later tenant ran")
}
