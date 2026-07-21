package resourcepool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	keyOne   = "key-1"
	keyTwo   = "key-2"
	keyThree = "key-3"
)

// fakeResource is a trivial pooled value with a controllable close outcome.
type fakeResource struct {
	id       string
	closed   atomic.Bool
	closeErr error
}

func newFakeResource(id string) *fakeResource {
	return &fakeResource{id: id}
}

// closeTracker records which resources have been closed and how many times.
type closeTracker struct {
	mu     sync.Mutex
	closed map[string]int
}

func newCloseTracker() *closeTracker {
	return &closeTracker{closed: make(map[string]int)}
}

func (t *closeTracker) closer(r *fakeResource) error {
	r.closed.Store(true)
	t.mu.Lock()
	t.closed[r.id]++
	t.mu.Unlock()
	return r.closeErr
}

func (t *closeTracker) count(id string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed[id]
}

func (t *closeTracker) wasClosed(id string) bool {
	return t.count(id) > 0
}

// countingConnector returns a create function that produces a fresh fakeResource per key and
// tallies creations.
func countingConnector(created *atomic.Int32) func(context.Context) (*fakeResource, error) {
	return func(context.Context) (*fakeResource, error) {
		created.Add(1)
		return newFakeResource(fmt.Sprintf("res-%d", created.Load())), nil
	}
}

func keyedConnector() func(context.Context, string) (*fakeResource, error) {
	return func(_ context.Context, key string) (*fakeResource, error) {
		return newFakeResource(key), nil
	}
}

// slowConnector returns a create function that sleeps before producing a fixed-id resource,
// widening the singleflight window so concurrent followers reliably pile up on one create.
func slowConnector(created *atomic.Int32, id string, delay time.Duration) func(context.Context) (*fakeResource, error) {
	return func(context.Context) (*fakeResource, error) {
		created.Add(1)
		time.Sleep(delay)
		return newFakeResource(id), nil
	}
}

// uniqueConnector returns a create function giving every resource a unique id (via the atomic
// counter's return value, which is race-free unlike a separate Load), so a close tracker can
// detect any double-close under concurrency.
func uniqueConnector(created *atomic.Int32) func(context.Context) (*fakeResource, error) {
	return func(context.Context) (*fakeResource, error) {
		id := created.Add(1)
		return newFakeResource(fmt.Sprintf("res-%d", id)), nil
	}
}

// failingConnector returns a create function that always fails with err after a delay, tallying
// call count. The delay widens the singleflight window so concurrent callers collapse onto one
// failing create.
func failingConnector(calls *atomic.Int32, err error, delay time.Duration) func(context.Context) (*fakeResource, error) {
	return func(context.Context) (*fakeResource, error) {
		calls.Add(1)
		time.Sleep(delay)
		return nil, err
	}
}

// TestPoolGetOrCreateCreatesOnceAndReuses pins lazy creation and reuse of a single entry.
func TestPoolGetOrCreateCreatesOnceAndReuses(t *testing.T) {
	tr := newCloseTracker()
	conn := keyedConnector()
	var creations atomic.Int32
	p := New(5, 0, tr.closer)
	defer p.Close()

	create := func(ctx context.Context) (*fakeResource, error) {
		creations.Add(1)
		return conn(ctx, keyOne)
	}

	v1, rel1, err := p.GetOrCreate(context.Background(), keyOne, create)
	require.NoError(t, err)
	require.NotNil(t, v1)
	require.NotNil(t, rel1)
	assert.Equal(t, int32(1), creations.Load())

	v2, rel2, err := p.GetOrCreate(context.Background(), keyOne, create)
	require.NoError(t, err)
	assert.Same(t, v1, v2, "second GetOrCreate must reuse the cached resource")
	assert.Equal(t, int32(1), creations.Load(), "no new creation on reuse")

	rel1()
	rel2()

	st := p.Stats()
	assert.Equal(t, 1, st.Size)
	assert.Equal(t, 1, st.TotalCreated)
}

// TestPoolGetOrCreateReturnsNonNilRelease verifies releasing a live cached entry does not
// close it.
func TestPoolGetOrCreateReturnsNonNilRelease(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	defer p.Close()

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(ctx context.Context) (*fakeResource, error) {
		return keyedConnector()(ctx, keyOne)
	})
	require.NoError(t, err)
	require.NotNil(t, rel)

	rel()
	assert.False(t, tr.wasClosed(v.id), "releasing a lease on a live cached resource must not close it")
	assert.Equal(t, 1, p.Size())
}

// TestPoolConcurrentGetOrCreateSingleflight pins invariant 1: concurrent first-GetOrCreate for
// one key yields one created resource, N usable leases, no double-create.
func TestPoolConcurrentGetOrCreateSingleflight(t *testing.T) {
	tr := newCloseTracker()
	var creations atomic.Int32
	var inProgress atomic.Bool
	p := New(0, 0, tr.closer)
	defer p.Close()

	create := func(context.Context) (*fakeResource, error) {
		if !inProgress.CompareAndSwap(false, true) {
			t.Error("concurrent creation detected — singleflight failed")
		}
		defer inProgress.Store(false)
		creations.Add(1)
		time.Sleep(30 * time.Millisecond)
		return newFakeResource(keyOne), nil
	}

	const workers = 12
	type res struct {
		v   *fakeResource
		rel ReleaseFunc
	}
	results := make(chan res, workers)
	for i := 0; i < workers; i++ {
		go func() {
			v, rel, err := p.GetOrCreate(context.Background(), keyOne, create)
			if err != nil {
				t.Errorf("GetOrCreate failed: %v", err)
			}
			results <- res{v, rel}
		}()
	}

	var got []res
	for i := 0; i < workers; i++ {
		got = append(got, <-results)
	}
	for i := 1; i < len(got); i++ {
		assert.Same(t, got[0].v, got[i].v, "all concurrent callers must receive the same resource")
	}
	assert.Equal(t, int32(1), creations.Load(), "singleflight must collapse concurrent creates")

	// All N leases are independent: releasing all of them, then evicting, closes exactly once.
	for _, r := range got {
		r.rel()
	}
	assert.Equal(t, int32(1), creations.Load())
}

// TestPoolEvictWhileLeasedDefersClose pins invariant 2 and is the mutation-check target for the
// defer-close-if-leased branch in evictIfNeeded.
func TestPoolEvictWhileLeasedDefersClose(t *testing.T) {
	tr := newCloseTracker()
	p := New(1, 0, tr.closer) // capacity 1: creating key-2 evicts key-1
	defer p.Close()
	ctx := context.Background()

	a, relA, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)

	_, relB, err := p.GetOrCreate(ctx, keyTwo, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyTwo)
	})
	require.NoError(t, err)
	defer relB()

	assert.False(t, tr.wasClosed(a.id), "an evicted-but-leased resource must not be closed while a lease is held (#606)")

	relA()
	assert.True(t, tr.wasClosed(a.id), "an evicted resource must close once its last lease is released")
	assert.Equal(t, 1, tr.count(a.id), "the deferred close must run exactly once")
}

// TestPoolTwoLeasesKeepAliveUntilBothReleased verifies refcounting across two borrowers.
func TestPoolTwoLeasesKeepAliveUntilBothReleased(t *testing.T) {
	tr := newCloseTracker()
	p := New(1, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	a, rel1, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	_, rel2, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)

	_, relB, err := p.GetOrCreate(ctx, keyTwo, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyTwo)
	})
	require.NoError(t, err)
	defer relB()

	rel1()
	assert.False(t, tr.wasClosed(a.id), "resource must stay open while a second lease is outstanding")

	rel2()
	assert.True(t, tr.wasClosed(a.id), "resource must close when the final lease is released")
}

// TestPoolReleaseIsIdempotent pins invariant 3: a double release decrements once.
func TestPoolReleaseIsIdempotent(t *testing.T) {
	tr := newCloseTracker()
	p := New(1, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	a, relA, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	_, relB, err := p.GetOrCreate(ctx, keyTwo, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyTwo)
	})
	require.NoError(t, err)
	defer relB()

	assert.NotPanics(t, func() {
		relA()
		relA() // double release must be a safe no-op
	})
	assert.Equal(t, 1, tr.count(a.id), "double release must not double-close")
}

// TestPoolGetOrCreateAfterCloseReturnsErrPoolClosed pins invariant 4 (the F22 closed guard).
func TestPoolGetOrCreateAfterCloseReturnsErrPoolClosed(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	require.NoError(t, p.Close())

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	assert.ErrorIs(t, err, ErrPoolClosed)
	assert.Nil(t, rel)
}

// TestPoolCreateRacingCloseDoesNotResurrect drives Close into the window between the top closed
// check and createEntry taking the lock: the create callback closes the pool before returning.
// createEntry must re-check under the lock, close the just-created resource, and report
// ErrPoolClosed rather than resurrecting the cleared map.
func TestPoolCreateRacingCloseDoesNotResurrect(t *testing.T) {
	tr := newCloseTracker()
	var p *Pool[*fakeResource]
	var once sync.Once
	p = New(5, 0, tr.closer)

	_, _, err := p.GetOrCreate(context.Background(), keyOne, func(context.Context) (*fakeResource, error) {
		once.Do(func() { _ = p.Close() }) // Close lands before createEntry takes the lock
		return newFakeResource(keyOne), nil
	})
	assert.ErrorIs(t, err, ErrPoolClosed, "GetOrCreate must report closed, not resurrect the map")
	assert.True(t, tr.wasClosed(keyOne), "the just-created resource must be closed, not leaked")
	assert.Equal(t, 0, p.Size(), "the map must not be resurrected with a new entry")
}

// TestPoolCreateErrorPropagatesAndCounts verifies a create failure is returned and counted.
func TestPoolCreateErrorPropagatesAndCounts(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	defer p.Close()

	wantErr := errors.New("boom")
	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(context.Context) (*fakeResource, error) {
		return nil, wantErr
	})
	assert.ErrorIs(t, err, wantErr)
	assert.Nil(t, v)
	assert.Nil(t, rel)

	st := p.Stats()
	assert.Equal(t, 0, st.Size)
	assert.Equal(t, 1, st.Errors)
	assert.Equal(t, 0, st.TotalCreated)
}

// TestPoolConcurrentCreateFailureCountsErrorOnce pins that a create failure collapsed across N
// concurrent GetOrCreate callers (singleflight hands the same error to every waiter) increments
// Errors exactly ONCE — in the leader — not once per blocked caller. Every caller still receives
// the shared error.
func TestPoolConcurrentCreateFailureCountsErrorOnce(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()

	wantErr := errors.New("boom")
	var calls atomic.Int32
	create := failingConnector(&calls, wantErr, 20*time.Millisecond)

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			v, rel, err := p.GetOrCreate(context.Background(), keyOne, create)
			assert.ErrorIs(t, err, wantErr)
			assert.Nil(t, v)
			assert.Nil(t, rel)
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), calls.Load(), "singleflight must collapse the failing create to one call")
	assert.Equal(t, 1, p.Stats().Errors, "a single collapsed create failure counts once, not once per waiter")
}

// TestPoolRemoveClosesUnleased verifies Remove hands back an unleased resource for the caller
// to close and detaches it from the pool.
func TestPoolRemoveClosesUnleased(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	v, rel, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	rel() // drop the lease so Remove reports shouldClose

	got, shouldClose := p.Remove(keyOne)
	require.True(t, shouldClose)
	assert.Same(t, v, got)
	assert.Equal(t, 0, p.Size())

	// A subsequent GetOrCreate makes a fresh instance.
	v2, rel2, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	defer rel2()
	assert.NotSame(t, v, v2)
}

// TestPoolRemoveWhileLeasedDefersClose verifies Remove on a leased entry defers the close.
func TestPoolRemoveWhileLeasedDefersClose(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	defer p.Close()

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)

	got, shouldClose := p.Remove(keyOne)
	assert.False(t, shouldClose, "Remove on a leased entry must defer the close")
	assert.Nil(t, got)
	assert.False(t, tr.wasClosed(v.id))

	rel()
	assert.True(t, tr.wasClosed(v.id), "removed resource closes when its last lease is released")
}

// TestPoolRemoveNonexistent verifies removing a missing key is a no-op.
func TestPoolRemoveNonexistent(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	defer p.Close()

	got, shouldClose := p.Remove("missing")
	assert.False(t, shouldClose)
	assert.Nil(t, got)
}

// TestPoolLRUEvictionClosesOldest verifies LRU ordering and eviction of an unleased victim.
func TestPoolLRUEvictionClosesOldest(t *testing.T) {
	tr := newCloseTracker()
	p := New(3, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	get := func(key string) {
		_, rel, err := p.GetOrCreate(ctx, key, func(c context.Context) (*fakeResource, error) {
			return keyedConnector()(c, key)
		})
		require.NoError(t, err)
		rel() // release so victims are closable
	}

	get(keyOne)
	get(keyTwo)
	get(keyThree)
	assert.Equal(t, 0, p.Stats().Evictions)

	// Refresh key-1 and key-2 so key-3 is the LRU victim.
	get(keyOne)
	get(keyTwo)

	get("key-4") // evicts key-3
	st := p.Stats()
	assert.Equal(t, 3, st.Size)
	assert.Equal(t, 1, st.Evictions)
	assert.True(t, tr.wasClosed(keyThree), "the LRU victim must be closed")
	assert.False(t, tr.wasClosed(keyOne))
	assert.False(t, tr.wasClosed(keyTwo))
}

// TestPoolUnlimitedMaxSizeNeverEvicts verifies maxSize <= 0 disables eviction.
func TestPoolUnlimitedMaxSizeNeverEvicts(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("key-%d", i)
		_, rel, err := p.GetOrCreate(ctx, key, func(c context.Context) (*fakeResource, error) {
			return keyedConnector()(c, key)
		})
		require.NoError(t, err)
		rel()
	}
	st := p.Stats()
	assert.Equal(t, 20, st.Size)
	assert.Equal(t, 0, st.Evictions)
}

// TestPoolCleanupIdleEvictsUnleased verifies the idle-cleanup loop removes and closes an idle
// unleased entry.
func TestPoolCleanupIdleEvictsUnleased(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 40*time.Millisecond, tr.closer)
	defer p.Close()
	p.StartCleanup(20 * time.Millisecond)
	ctx := context.Background()

	v, rel, err := p.GetOrCreate(ctx, keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	rel() // unleased → idle cleanup may close it

	// The close runs after the IdleCleanups counter bumps and the pool lock is released, so wait
	// on the close itself (the downstream effect), not the counter.
	require.Eventually(t, func() bool { return tr.wasClosed(v.id) }, time.Second, 10*time.Millisecond,
		"idle unleased resource must be closed")
	assert.GreaterOrEqual(t, p.Stats().IdleCleanups, 1)
	assert.Equal(t, 0, p.Size())
}

// TestPoolCleanupIdleHonorsLease verifies an idle but leased entry is detached (counted) yet
// closed only after its lease is released.
func TestPoolCleanupIdleHonorsLease(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 40*time.Millisecond, tr.closer)
	defer p.Close()
	p.StartCleanup(20 * time.Millisecond)

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool { return p.Stats().IdleCleanups >= 1 }, time.Second, 10*time.Millisecond)
	assert.False(t, tr.wasClosed(v.id), "idle cleanup must not close a leased resource")

	rel()
	require.Eventually(t, func() bool { return tr.wasClosed(v.id) }, time.Second, 10*time.Millisecond,
		"idle-cleaned resource closes when its last lease is released")
}

// TestPoolCleanupIdleCloseErrorCounted verifies a failing close during idle cleanup is counted.
func TestPoolCleanupIdleCloseErrorCounted(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 40*time.Millisecond, tr.closer)
	defer p.Close()
	p.StartCleanup(20 * time.Millisecond)

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, func(context.Context) (*fakeResource, error) {
		r := newFakeResource(keyOne)
		r.closeErr = errors.New("close failed")
		return r, nil
	})
	require.NoError(t, err)
	rel()

	// The error counter bumps after the close runs (outside the pool lock), so wait on Errors
	// directly rather than on the IdleCleanups counter.
	require.Eventually(t, func() bool { return p.Stats().Errors == 1 }, time.Second, 10*time.Millisecond,
		"a failing close during idle cleanup must be counted")
}

// TestPoolCleanupIdleNoOpWithoutTTL verifies the defensive guard: cleanupIdle is inert when the
// pool has no idle timeout, even if invoked directly.
func TestPoolCleanupIdleNoOpWithoutTTL(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer) // idleTTL == 0
	defer p.Close()

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	rel()

	p.cleanupIdle() // direct call — must be a no-op with no idle TTL
	assert.Equal(t, 1, p.Size())
	assert.Equal(t, 0, p.Stats().IdleCleanups)
}

// TestPoolReleaseCloseErrorCounted verifies a failing deferred close (on final release of an
// evicted-while-leased entry) is counted.
func TestPoolReleaseCloseErrorCounted(t *testing.T) {
	tr := newCloseTracker()
	p := New(1, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	_, relA, err := p.GetOrCreate(ctx, keyOne, func(context.Context) (*fakeResource, error) {
		r := newFakeResource(keyOne)
		r.closeErr = errors.New("close failed")
		return r, nil
	})
	require.NoError(t, err)

	_, relB, err := p.GetOrCreate(ctx, keyTwo, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyTwo)
	}) // evicts key-1 (leased) → deferred close
	require.NoError(t, err)
	defer relB()

	relA() // triggers the deferred (failing) close
	assert.Equal(t, 1, p.Stats().Errors, "a failing deferred close must be counted")
}

// TestPoolCloseClosesAllAndReturnsFirstError verifies Close closes every entry, counts close
// failures, and returns the first error.
func TestPoolCloseClosesAllAndReturnsFirstError(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	ctx := context.Background()

	closeErr := errors.New("close failed")
	// key-2 fails to close; the others succeed.
	makeCreate := func(key string, fail bool) func(context.Context) (*fakeResource, error) {
		return func(context.Context) (*fakeResource, error) {
			r := newFakeResource(key)
			if fail {
				r.closeErr = closeErr
			}
			return r, nil
		}
	}
	for _, kv := range []struct {
		key  string
		fail bool
	}{{keyOne, false}, {keyTwo, true}, {keyThree, false}} {
		_, rel, err := p.GetOrCreate(ctx, kv.key, makeCreate(kv.key, kv.fail))
		require.NoError(t, err)
		rel()
	}

	err := p.Close()
	assert.ErrorIs(t, err, closeErr, "Close returns the first close error")
	assert.True(t, tr.wasClosed(keyOne))
	assert.True(t, tr.wasClosed(keyTwo))
	assert.True(t, tr.wasClosed(keyThree))
	assert.Equal(t, 0, p.Size())
	assert.Equal(t, 1, p.Stats().Errors)
}

// TestPoolCloseIsIdempotent verifies Close can be called repeatedly.
func TestPoolCloseIsIdempotent(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)
	rel()

	require.NoError(t, p.Close())
	assert.NoError(t, p.Close(), "Close must be idempotent")
	assert.True(t, p.Closed())
	assert.Equal(t, 1, tr.count(keyOne), "each resource closes exactly once across repeated Close")
}

// TestPoolCloseClosesLeasedEntriesWithoutDoubleClose verifies Close closes even leased entries,
// and a later release of that lease does not double-close.
func TestPoolCloseClosesLeasedEntriesWithoutDoubleClose(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, keyOne)
	})
	require.NoError(t, err)

	require.NoError(t, p.Close())
	assert.True(t, tr.wasClosed(v.id), "Close closes leased entries too")

	rel() // deferred-release must be a safe no-op post-Close
	assert.Equal(t, 1, tr.count(v.id), "release after Close must not double-close")
}

// TestPoolStartStopCleanupIdempotent verifies the cleanup lifecycle plumbing.
func TestPoolStartStopCleanupIdempotent(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 40*time.Millisecond, tr.closer)
	defer p.Close()

	assert.NotPanics(t, func() {
		p.StopCleanup() // stop before any start
		p.StartCleanup(20 * time.Millisecond)
		p.StartCleanup(20 * time.Millisecond) // second start is a no-op
		p.StopCleanup()
		p.StopCleanup() // second stop is a no-op
	})
}

// TestPoolStartCleanupNoOpConditions verifies StartCleanup is inert without an idle TTL, with a
// non-positive interval, or after Close.
func TestPoolStartCleanupNoOpConditions(t *testing.T) {
	tr := newCloseTracker()

	noTTL := New(5, 0, tr.closer)
	defer noTTL.Close()
	noTTL.StartCleanup(20 * time.Millisecond)
	noTTL.cleanupMu.Lock()
	assert.Nil(t, noTTL.cleanupStop, "no idle TTL → no cleanup loop")
	noTTL.cleanupMu.Unlock()

	withTTL := New(5, 40*time.Millisecond, tr.closer)
	defer withTTL.Close()
	withTTL.StartCleanup(0) // non-positive interval
	withTTL.cleanupMu.Lock()
	assert.Nil(t, withTTL.cleanupStop, "non-positive interval → no cleanup loop")
	withTTL.cleanupMu.Unlock()

	closed := New(5, 40*time.Millisecond, tr.closer)
	require.NoError(t, closed.Close())
	closed.StartCleanup(20 * time.Millisecond)
	closed.cleanupMu.Lock()
	assert.Nil(t, closed.cleanupStop, "closed pool → no cleanup loop")
	closed.cleanupMu.Unlock()
}

// TestPoolStatsSnapshot verifies the PoolStats fields reflect configuration and counters.
func TestPoolStatsSnapshot(t *testing.T) {
	tr := newCloseTracker()
	p := New(2, 90*time.Second, tr.closer)
	defer p.Close()
	ctx := context.Background()

	st := p.Stats()
	assert.Equal(t, 0, st.Size)
	assert.Equal(t, 2, st.MaxSize)
	assert.Equal(t, 0, st.TotalCreated)
	assert.Equal(t, 0, st.Evictions)
	assert.Equal(t, 0, st.IdleCleanups)
	assert.Equal(t, 0, st.Errors)
	assert.Equal(t, 90*time.Second, st.IdleTTL)

	get := func(key string) {
		_, rel, err := p.GetOrCreate(ctx, key, func(c context.Context) (*fakeResource, error) {
			return keyedConnector()(c, key)
		})
		require.NoError(t, err)
		rel()
	}
	get(keyOne)
	get(keyTwo)
	get(keyThree) // evicts one

	st = p.Stats()
	assert.Equal(t, 2, st.Size)
	assert.Equal(t, 3, st.TotalCreated)
	assert.Equal(t, 1, st.Evictions)
}

// TestPoolClosedAccessor verifies Closed tracks shutdown state.
func TestPoolClosedAccessor(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	assert.False(t, p.Closed())
	require.NoError(t, p.Close())
	assert.True(t, p.Closed())
}

// TestPoolConcurrentGetRacesClose stress-tests the closed guard under concurrent Close +
// GetOrCreate. Each call must either succeed or return ErrPoolClosed — never a different error
// or a use-after-close panic. Run under -race.
func TestPoolConcurrentGetRacesClose(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)

	const n = 64
	var creations atomic.Int32
	results := make(chan error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		go func(id int) {
			<-start
			key := fmt.Sprintf("key-%d", id)
			_, rel, err := p.GetOrCreate(context.Background(), key, countingConnector(&creations))
			if rel != nil {
				rel()
			}
			results <- err
		}(i)
	}
	go func() {
		<-start
		_ = p.Close()
	}()
	close(start)

	for i := 0; i < n; i++ {
		if err := <-results; err != nil && !errors.Is(err, ErrPoolClosed) {
			t.Errorf("GetOrCreate during Close returned unexpected error: %v", err)
		}
	}
}

// TestPoolThreadSafety exercises concurrent Get/release against a shared pool under -race. With
// no eviction (unlimited) and no removal, every GetOrCreate must succeed: first touch of a key
// takes a seed lease (which cannot be closed before it is claimed) and reuse leases the cached
// entry, so there is no acquire churn.
func TestPoolThreadSafety(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	const workers = 16
	const ops = 40
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				key := fmt.Sprintf("key-%d", j%6)
				_, rel, err := p.GetOrCreate(ctx, key, func(c context.Context) (*fakeResource, error) {
					return keyedConnector()(c, key)
				})
				if err != nil {
					t.Errorf("GetOrCreate failed: %v", err)
					continue
				}
				rel()
			}
		}()
	}
	wg.Wait()

	st := p.Stats()
	assert.GreaterOrEqual(t, st.TotalCreated, st.Size)
}

// TestPoolConcurrentGetRacesRemove stress-tests concurrent GetOrCreate + Remove under -race.
// The bounded acquire retry can, under this pathological churn, exhaust its attempts when a
// peeked entry is removed before it can be claimed — a legitimate, documented outcome (mirrors
// cache's maxGetAttempts bound). Any OTHER error, a panic, or a double-close is a failure.
func TestPoolConcurrentGetRacesRemove(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	const workers = 16
	const ops = 40
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				key := fmt.Sprintf("key-%d", j%6)
				_, rel, err := p.GetOrCreate(ctx, key, func(c context.Context) (*fakeResource, error) {
					return keyedConnector()(c, key)
				})
				if err != nil {
					// Only the bounded-retry churn error is tolerated here.
					assert.Contains(t, err.Error(), "pool churn", "unexpected GetOrCreate error")
					continue
				}
				rel()
				if j%8 == 0 {
					if v, shouldClose := p.Remove(key); shouldClose {
						_ = tr.closer(v)
					}
				}
			}
		}()
	}
	wg.Wait()
}

// TestPoolStartCleanupAfterCloseIsNoOp pins the contract behind the StartCleanup/Close leak fix:
// once the pool is closed, StartCleanup must not launch a cleanup goroutine (Close would never
// stop it). Both the lock-free top guard and the under-lock re-check enforce this; removing both
// fails this test. idleTTL and interval are positive so `closed` is the only thing that can
// prevent the loop from starting.
func TestPoolStartCleanupAfterCloseIsNoOp(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 50*time.Millisecond, tr.closer)
	require.NoError(t, p.Close())

	p.StartCleanup(10 * time.Millisecond)

	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()
	assert.Nil(t, p.cleanupStop, "StartCleanup on a closed pool must not start a cleanup loop")
}

// TestPoolConcurrentLeasesAllCountedBeforeClose pins that EVERY concurrent follower's lease is
// counted (claimOrAcquire's non-seed refs++), not just the seed claim. N callers race on one key
// (singleflight -> one create, N leases); Remove then detaches the entry while all N are held, so
// the deferred close must wait for the FINAL release. If the follower refs++ were dropped, refs
// would sit at 1 and the first release would close the resource while N-1 leases still hold it.
func TestPoolConcurrentLeasesAllCountedBeforeClose(t *testing.T) {
	tr := newCloseTracker()
	var creations atomic.Int32
	p := New(0, 0, tr.closer)
	defer p.Close()

	create := slowConnector(&creations, "shared", 20*time.Millisecond) // widen the singleflight window

	const workers = 8
	rels := make(chan ReleaseFunc, workers)
	var v0 *fakeResource
	var v0mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			v, rel, err := p.GetOrCreate(context.Background(), keyOne, create)
			if err != nil {
				t.Errorf("GetOrCreate failed: %v", err)
				return
			}
			v0mu.Lock()
			v0 = v
			v0mu.Unlock()
			rels <- rel
		}()
	}
	wg.Wait()
	close(rels)
	require.Equal(t, int32(1), creations.Load(), "singleflight must collapse to one create")

	// Detach the entry while all N leases are outstanding: Remove must defer the close.
	if _, shouldClose := p.Remove(keyOne); shouldClose {
		t.Fatal("Remove must defer close while leases are held")
	}

	got := make([]ReleaseFunc, 0, workers)
	for r := range rels {
		got = append(got, r)
	}
	require.Len(t, got, workers)

	// Release all but the last; the resource must stay open the entire time — which only holds if
	// every follower lease was counted, not just the seed.
	for i := 0; i < len(got)-1; i++ {
		got[i]()
		assert.Falsef(t, tr.wasClosed(v0.id),
			"resource closed after %d of %d releases — a follower lease was uncounted", i+1, workers)
	}
	got[len(got)-1]()
	assert.True(t, tr.wasClosed(v0.id), "final release must close the detached resource")
	assert.Equal(t, 1, tr.count(v0.id), "the deferred close must run exactly once")
}

// TestPoolConcurrentEvictWhileLeasedRace drives eviction and lease-release on the same entries
// from different goroutines with maxSize>0, exercising the #606 deferred-close path under -race
// (the single-threaded TestPoolEvictWhileLeasedDefersClose cannot surface a data race between a
// concurrent evict marking detached/closed and a concurrent releaseEntry reading them). Unique
// per-resource ids let the tracker catch any double-close.
func TestPoolConcurrentEvictWhileLeasedRace(t *testing.T) {
	tr := newCloseTracker()
	var creations atomic.Int32
	p := New(2, 0, tr.closer) // small capacity forces constant eviction across 8 keys
	ctx := context.Background()

	create := uniqueConnector(&creations)

	const workers = 16
	const ops = 60
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				key := fmt.Sprintf("key-%d", (w+j)%8)
				_, rel, err := p.GetOrCreate(ctx, key, create)
				if err != nil {
					assert.Contains(t, err.Error(), "pool churn", "unexpected GetOrCreate error")
					continue
				}
				time.Sleep(time.Millisecond) // hold the lease across other goroutines' evictions
				rel()
			}
		}(w)
	}
	wg.Wait()

	// No resource may have been closed more than once during the concurrent eviction phase.
	tr.mu.Lock()
	for id, n := range tr.closed {
		assert.LessOrEqualf(t, n, 1, "resource %s closed %d times (double-close under eviction race)", id, n)
	}
	tr.mu.Unlock()

	require.NoError(t, p.Close())
}
