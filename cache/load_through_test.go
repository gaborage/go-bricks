package cache_test

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gaborage/go-bricks/cache"
	cachetest "github.com/gaborage/go-bricks/cache/testing"
	"github.com/gaborage/go-bricks/multitenant"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

type ltUser struct {
	ID   int64  `cbor:"1,keyasint"`
	Name string `cbor:"2,keyasint"`
}

const (
	ltKey = "user:1"
	ltTTL = time.Minute
	// Generous by design: Eventually returns as soon as the condition holds, so the bound
	// only decides how much concurrent load the wait tolerates before failing.
	ltWaitFor  = 30 * time.Second
	ltWaitTick = 5 * time.Millisecond
)

var ltAlice = ltUser{ID: 1, Name: "alice"}

// ltCountingLoader returns a loader yielding v and the counter it increments per call.
func ltCountingLoader(v ltUser) (cache.Loader[ltUser], *atomic.Int64) {
	var calls atomic.Int64
	return func(context.Context) (ltUser, error) {
		calls.Add(1)
		return v, nil
	}, &calls
}

func TestLoadThroughMissLoadsAndWritesBack(t *testing.T) {
	mock := cachetest.NewMockCache()
	load, calls := ltCountingLoader(ltAlice)

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	assert.Equal(t, int64(1), calls.Load())

	require.Eventually(t, func() bool { return mock.Has(ltKey) }, ltWaitFor, ltWaitTick, "write-back never landed")
	raw, err := mock.Get(t.Context(), ltKey)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, cache.MustUnmarshal[ltUser](raw))
}

func TestLoadThroughHitSkipsLoader(t *testing.T) {
	mock := cachetest.NewMockCache()
	require.NoError(t, mock.Set(t.Context(), ltKey, cache.MustMarshal(ltAlice), ltTTL))
	load, calls := ltCountingLoader(ltUser{ID: 2, Name: "not-alice"})

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	assert.Zero(t, calls.Load(), "hit must not reach the origin")
}

func TestLoadThroughDegradesToOriginWhenLookupFails(t *testing.T) {
	mock := cachetest.NewMockCache().WithGetFailure(errors.New("boom"))
	load, calls := ltCountingLoader(ltAlice)

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	assert.Equal(t, int64(1), calls.Load())
	require.Eventually(t, func() bool { return mock.Has(ltKey) }, ltWaitFor, ltWaitTick, "write-back must not depend on the lookup")
}

func TestLoadThroughBoundsTheCacheLegAndHandsTheOriginAnUntouchedContext(t *testing.T) {
	mock := cachetest.NewMockCache().WithDelay(10 * time.Second)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	var gotCtx context.Context
	var liveAtOrigin bool
	load := func(ctx context.Context) (ltUser, error) {
		gotCtx = ctx
		liveAtOrigin = ctx.Err() == nil
		return ltAlice, nil
	}

	got, err := cache.LoadThrough(ctx, mock, ltKey, ltTTL, load, cache.WithCacheTimeout(20*time.Millisecond))
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	assert.True(t, gotCtx == ctx, "origin must receive the caller's context, not a derived one")
	assert.True(t, liveAtOrigin, "the slow cache leg must not spend the caller's budget")
}

func TestLoadThroughTreatsUndecodableEntryAsMissAndOverwritesIt(t *testing.T) {
	mock := cachetest.NewMockCache()
	require.NoError(t, mock.Set(t.Context(), ltKey, cache.MustMarshal("not a user"), ltTTL))
	load, calls := ltCountingLoader(ltAlice)

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	assert.Equal(t, int64(1), calls.Load())
	require.Eventually(t, func() bool {
		raw, gerr := mock.Get(t.Context(), ltKey)
		if gerr != nil {
			return false
		}
		u, uerr := cache.Unmarshal[ltUser](raw)
		return uerr == nil && u == ltAlice
	}, ltWaitFor, ltWaitTick, "the fill must overwrite the undecodable entry")
}

// ltAssertNotStored loads a nil-shaped value of type T and proves it never reaches the cache.
func ltAssertNotStored[T any](t *testing.T, load cache.Loader[T]) {
	t.Helper()
	mock := cachetest.NewMockCache()

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.True(t, isNilAny(got), "the nil result is still returned")
	require.Never(t, func() bool { return mock.OperationCount("Set") > 0 }, 100*time.Millisecond, ltWaitTick, "a nil result must never be written back")
	cachetest.AssertCacheMiss(t, mock, ltKey)
}

func isNilAny(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Slice) && rv.IsNil()
}

func TestLoadThroughReturnsNilResultWithoutStoringIt(t *testing.T) {
	t.Run("nil_pointer", func(t *testing.T) {
		ltAssertNotStored(t, func(context.Context) (*ltUser, error) { return nil, nil })
	})
	t.Run("nil_slice", func(t *testing.T) {
		ltAssertNotStored(t, func(context.Context) ([]ltUser, error) { return nil, nil })
	})
	t.Run("nil_interface", func(t *testing.T) {
		ltAssertNotStored(t, func(context.Context) (any, error) { return nil, nil })
	})
	t.Run("typed_nil_in_interface", func(t *testing.T) {
		ltAssertNotStored(t, func(context.Context) (any, error) { return (*ltUser)(nil), nil })
	})
}

// ltGate blocks the first loader call until released; every call is counted.
type ltGate struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func newLtGate() *ltGate {
	return &ltGate{started: make(chan struct{}), release: make(chan struct{})}
}

// loader blocks until release or ctx, then returns ltAlice.
func (g *ltGate) loader() cache.Loader[ltUser] {
	return func(ctx context.Context) (ltUser, error) {
		if g.calls.Add(1) == 1 {
			close(g.started)
		}
		select {
		case <-g.release:
			return ltAlice, nil
		case <-ctx.Done():
			return ltUser{}, ctx.Err()
		}
	}
}

type ltResult struct {
	val ltUser
	err error
}

func ltRun(ctx context.Context, c cache.Cache, load cache.Loader[ltUser]) <-chan ltResult {
	out := make(chan ltResult, 1)
	go func() {
		v, err := cache.LoadThrough(ctx, c, ltKey, ltTTL, load)
		out <- ltResult{val: v, err: err}
	}()
	return out
}

func ltWaitGets(t *testing.T, mock *cachetest.MockCache, n int64) {
	t.Helper()
	require.Eventually(t, func() bool { return mock.OperationCount("Get") == n }, ltWaitFor, ltWaitTick)
}

func TestLoadThroughCollapsesConcurrentMissesIntoOneOriginCall(t *testing.T) {
	const followers = 8
	mock := cachetest.NewMockCache()
	gate := newLtGate()
	load := gate.loader()

	leader := ltRun(t.Context(), mock, load)
	<-gate.started

	results := make([]<-chan ltResult, followers)
	for i := range results {
		ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
		t.Cleanup(cancel)
		results[i] = ltRun(ctx, mock, load)
	}
	for _, ch := range results {
		res := <-ch
		assert.ErrorIs(t, res.err, context.DeadlineExceeded, "a follower leaves on its own deadline")
	}
	// A follower that failed to join would have called the loader before blocking.
	assert.Equal(t, int64(1), gate.calls.Load(), "followers must wait on the leader's fill, not start their own")

	close(gate.release)
	res := <-leader
	require.NoError(t, res.err)
	assert.Equal(t, ltAlice, res.val)
}

// ltOnceLoader answers the first call with v/err once release is closed; every later call
// blocks until its own context ends, so a follower that re-loads instead of taking the
// leader's outcome is exposed by its deadline.
func ltOnceLoader(v ltUser, err error) (load cache.Loader[ltUser], calls *atomic.Int64, started, release chan struct{}) {
	calls = new(atomic.Int64)
	started, release = make(chan struct{}), make(chan struct{})
	load = func(ctx context.Context) (ltUser, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return v, err
		}
		<-ctx.Done()
		return ltUser{}, ctx.Err()
	}
	return load, calls, started, release
}

// ltFollowersShareTheLeaderOutcome runs a gated leader and N followers with short
// deadlines, then classifies each follower: the leader's outcome means it joined the
// flight, its own deadline means it arrived after the flight closed and loaded alone.
// A canceled leader still loads — the loader is gated on release, not on its context.
func ltFollowersShareTheLeaderOutcome(t *testing.T, leaderCanceled bool, wantVal ltUser, wantErr error) {
	t.Helper()
	const followers = 8
	mock := cachetest.NewMockCache()
	load, calls, started, release := ltOnceLoader(wantVal, wantErr)

	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	defer cancelLeader()
	if leaderCanceled {
		cancelLeader()
	}
	leader := ltRun(leaderCtx, mock, load)
	<-started
	results := make([]<-chan ltResult, followers)
	for i := range results {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		t.Cleanup(cancel)
		results[i] = ltRun(ctx, mock, load)
	}
	ltWaitGets(t, mock, followers+1)
	close(release)

	res := <-leader
	assert.ErrorIs(t, res.err, wantErr)
	assert.Equal(t, wantVal, res.val)

	var joined, late int64
	for _, ch := range results {
		fres := <-ch
		switch {
		case errors.Is(fres.err, context.DeadlineExceeded):
			late++
		default:
			joined++
			assert.ErrorIs(t, fres.err, wantErr)
			assert.Equal(t, wantVal, fres.val)
		}
	}
	// A late follower either arrived after the flight closed and loaded for itself, or
	// joined and then ran out its own deadline before the leader answered — the second
	// kind never reaches the loader, so this is an upper bound, not an equality.
	assert.LessOrEqual(t, calls.Load(), 1+late, "only the leader and the late arrivals may load")
	assert.NotZero(t, joined, "every follower arrived late — the flight never collapsed")
}

func TestLoadThroughServesFollowersFromTheLeaderFill(t *testing.T) {
	ltFollowersShareTheLeaderOutcome(t, false, ltAlice, nil)
}

func TestLoadThroughServesFollowersFromACanceledLeaderThatStillLoaded(t *testing.T) {
	ltFollowersShareTheLeaderOutcome(t, true, ltAlice, nil)
}

func TestLoadThroughFollowersRecoverFromACanceledLeader(t *testing.T) {
	const followers = 8
	mock := cachetest.NewMockCache()
	var calls atomic.Int64
	started := make(chan struct{})
	load := func(ctx context.Context) (ltUser, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return ltUser{}, ctx.Err()
		}
		return ltAlice, nil
	}

	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	defer cancelLeader()
	leader := ltRun(leaderCtx, mock, load)
	<-started
	results := make([]<-chan ltResult, followers)
	for i := range results {
		results[i] = ltRun(t.Context(), mock, load)
	}
	ltWaitGets(t, mock, followers+1)
	cancelLeader()

	res := <-leader
	assert.ErrorIs(t, res.err, context.Canceled)
	for _, ch := range results {
		fres := <-ch
		require.NoError(t, fres.err, "the leader's cancellation must not reach a live follower")
		assert.Equal(t, ltAlice, fres.val)
	}
	n := calls.Load()
	assert.GreaterOrEqual(t, n, int64(2))
	assert.LessOrEqual(t, n, int64(followers+1))
}

func TestLoadThroughNeverCollapsesAcrossCacheInstances(t *testing.T) {
	tenantA, tenantB := cachetest.NewMockCacheWithID("a"), cachetest.NewMockCacheWithID("b")
	gate := newLtGate()
	load := gate.loader()

	leader := ltRun(t.Context(), tenantA, load)
	<-gate.started

	got, err := cache.LoadThrough(t.Context(), tenantB, ltKey, ltTTL, func(context.Context) (ltUser, error) {
		return ltUser{ID: 2, Name: "bob"}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, ltUser{ID: 2, Name: "bob"}, got, "another instance's flight must not serve this one")

	close(gate.release)
	require.NoError(t, (<-leader).err)
}

func TestLoadThroughNeverCollapsesAcrossValueTypes(t *testing.T) {
	mock := cachetest.NewMockCache()
	gate := newLtGate()
	leader := ltRun(t.Context(), mock, gate.loader())
	<-gate.started

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, func(context.Context) (int, error) { return 42, nil })
	require.NoError(t, err)
	assert.Equal(t, 42, got)

	close(gate.release)
	require.NoError(t, (<-leader).err)
}

// ltValueCache is a Cache whose dynamic type is not a pointer, so it has no instance identity.
type ltValueCache struct{ *cachetest.MockCache }

func TestLoadThroughSkipsCollapsingWithoutInstanceIdentity(t *testing.T) {
	c := ltValueCache{cachetest.NewMockCache()}
	gate := newLtGate()
	load := gate.loader()

	first := ltRun(t.Context(), c, load)
	<-gate.started
	second := ltRun(t.Context(), c, load)
	require.Eventually(t, func() bool { return gate.calls.Load() == 2 }, ltWaitFor, ltWaitTick, "without identity every caller loads for itself")

	close(gate.release)
	for _, ch := range []<-chan ltResult{first, second} {
		res := <-ch
		require.NoError(t, res.err)
		assert.Equal(t, ltAlice, res.val)
	}
}

func TestLoadThroughReportsLoaderPanicByTypeOnly(t *testing.T) {
	mock := cachetest.NewMockCache()
	const secret = "hunter2-should-never-print"
	load := func(context.Context) (ltUser, error) { panic(secret) }

	_, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "(type: string)")
	assert.NotContains(t, err.Error(), secret)
	require.Never(t, func() bool { return mock.OperationCount("Set") > 0 }, 100*time.Millisecond, ltWaitTick)
}

func TestLoadThroughPropagatesLoaderErrorToEveryWaiter(t *testing.T) {
	ltFollowersShareTheLeaderOutcome(t, false, ltUser{}, errors.New("origin down"))
}

func TestLoadThroughNeverWritesBackALoaderError(t *testing.T) {
	mock := cachetest.NewMockCache()
	load := func(context.Context) (ltUser, error) { return ltAlice, errors.New("origin down") }

	_, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.Error(t, err)
	require.Never(t, func() bool { return mock.OperationCount("Set") > 0 }, 100*time.Millisecond, ltWaitTick)
}

func TestLoadThroughStoresANonNilPointerResult(t *testing.T) {
	mock := cachetest.NewMockCache()
	load := func(context.Context) (*ltUser, error) { u := ltAlice; return &u, nil }

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, ltAlice, *got)
	require.Eventually(t, func() bool { return mock.Has(ltKey) }, ltWaitFor, ltWaitTick)
}

func TestLoadThroughWriteBackSurvivesCallerCancellation(t *testing.T) {
	mock := cachetest.NewMockCache()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	load := func(context.Context) (ltUser, error) { return ltAlice, nil }

	got, err := cache.LoadThrough(ctx, mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	require.Eventually(t, func() bool { return mock.Has(ltKey) }, ltWaitFor, ltWaitTick, "write-back must detach from the caller's cancellation")
}

func TestLoadThroughRejectsInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		ttl  time.Duration
		opts []cache.LoadOption
		want error
	}{
		{name: "negative_ttl", ttl: -time.Second, want: cache.ErrInvalidTTL},
		{name: "zero_cache_timeout", ttl: ltTTL, opts: []cache.LoadOption{cache.WithCacheTimeout(0)}, want: cache.ErrInvalidCacheTimeout},
		{name: "negative_cache_timeout", ttl: ltTTL, opts: []cache.LoadOption{cache.WithCacheTimeout(-time.Millisecond)}, want: cache.ErrInvalidCacheTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := cachetest.NewMockCache()
			load, calls := ltCountingLoader(ltAlice)
			_, err := cache.LoadThrough(t.Context(), mock, ltKey, tt.ttl, load, tt.opts...)
			assert.ErrorIs(t, err, tt.want)
			assert.Zero(t, calls.Load())
			cachetest.AssertOperationCount(t, mock, "Get", 0)
		})
	}
}

func TestLoadThroughRejectsNilCache(t *testing.T) {
	tests := []struct {
		name string
		c    cache.Cache
	}{
		{name: "nil_interface", c: nil},
		{name: "typed_nil", c: (*cachetest.MockCache)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			load, calls := ltCountingLoader(ltAlice)
			_, err := cache.LoadThrough(t.Context(), tt.c, ltKey, ltTTL, load)
			assert.ErrorIs(t, err, cache.ErrNilCache)
			assert.Zero(t, calls.Load(), "a nil cache must fail before the origin is consulted")
		})
	}
}

func TestLoadThroughFailsWhenTheLoadedValueCannotBeEncoded(t *testing.T) {
	mock := cachetest.NewMockCache()
	type unencodable struct{ F func() }
	load := func(context.Context) (unencodable, error) { return unencodable{F: func() {}}, nil }

	_, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cbor marshal failed")
	require.Never(t, func() bool { return mock.OperationCount("Set") > 0 }, 100*time.Millisecond, ltWaitTick)
}

// ltFillCounts sums cache.fill.duration data-point counts by the cache.collapsed attribute.
func ltFillCounts(t *testing.T, rm metricdata.ResourceMetrics) map[string]uint64 {
	t.Helper()
	counts := map[string]uint64{}
	m := obtest.FindMetric(rm, "cache.fill.duration")
	if m == nil {
		return counts
	}
	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "cache.fill.duration must be a float64 histogram")
	for _, dp := range hist.DataPoints {
		role, _ := dp.Attributes.Value(attribute.Key("cache.collapsed"))
		counts[role.AsString()] += dp.Count
	}
	return counts
}

func TestLoadThroughRecordsFillDurationByRole(t *testing.T) {
	const followers = 3
	mp := setupManagerMetricsProvider(t)
	mock := cachetest.NewMockCache()
	gate := newLtGate()
	load := gate.loader()

	leader := ltRun(t.Context(), mock, load)
	<-gate.started
	for range followers {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		t.Cleanup(cancel)
		res := <-ltRun(ctx, mock, load)
		require.ErrorIs(t, res.err, context.DeadlineExceeded)
	}
	assert.Equal(t, map[string]uint64{"follower": followers}, ltFillCounts(t, mp.Collect(t)))

	close(gate.release)
	require.NoError(t, (<-leader).err)
	assert.Equal(t, map[string]uint64{"leader": 1, "follower": followers}, ltFillCounts(t, mp.Collect(t)))

	// The write-back is asynchronous, so wait for the fill to land: a call that raced it
	// would miss and record a second fill, which is what this assertion must not see.
	require.Eventually(t, func() bool { return mock.Has(ltKey) }, ltWaitFor, ltWaitTick)
	_, err := cache.LoadThrough(t.Context(), mock, ltKey, ltTTL, load)
	require.NoError(t, err)
	assert.Equal(t, map[string]uint64{"leader": 1, "follower": followers}, ltFillCounts(t, mp.Collect(t)), "a hit records no fill")
}

func TestLoadThroughAcceptsZeroTTLAsNoExpiration(t *testing.T) {
	mock := cachetest.NewMockCache()
	load, _ := ltCountingLoader(ltAlice)

	got, err := cache.LoadThrough(t.Context(), mock, ltKey, 0, load)
	require.NoError(t, err)
	assert.Equal(t, ltAlice, got)
	require.Eventually(t, func() bool { return mock.Has(ltKey) }, ltWaitFor, ltWaitTick)
}

func TestLoadThroughStampsTheTenantOnTheFillMetric(t *testing.T) {
	mp := setupManagerMetricsProvider(t)
	mock := cachetest.NewMockCache()
	load, _ := ltCountingLoader(ltAlice)

	_, err := cache.LoadThrough(multitenant.SetTenant(t.Context(), "acme"), mock, ltKey, ltTTL, load)
	require.NoError(t, err)

	m := obtest.FindMetric(mp.Collect(t), "cache.fill.duration")
	require.NotNil(t, m)
	hist, ok := m.Data.(metricdata.Histogram[float64])
	require.True(t, ok)
	require.Len(t, hist.DataPoints, 1)
	ns, found := hist.DataPoints[0].Attributes.Value(attribute.Key("db.namespace"))
	require.True(t, found, "tenant on the context must reach db.namespace")
	assert.Equal(t, "acme", ns.AsString())
}
