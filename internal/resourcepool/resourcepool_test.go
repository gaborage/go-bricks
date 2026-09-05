package resourcepool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// keyedCreate binds keyedConnector to a single key, producing the func(context.Context) shape
// GetOrCreate expects.
func keyedCreate(key string) func(context.Context) (*fakeResource, error) {
	return func(c context.Context) (*fakeResource, error) {
		return keyedConnector()(c, key)
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

// panickingConnector returns a create function that panics with v after a delay, tallying call
// count. The delay widens the singleflight window so concurrent callers collapse onto one
// panicking create.
func panickingConnector(calls *atomic.Int32, v any, delay time.Duration) func(context.Context) (*fakeResource, error) {
	return func(context.Context) (*fakeResource, error) {
		calls.Add(1)
		time.Sleep(delay)
		panic(v)
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

// leaseResult carries a GetOrCreate outcome across a goroutine boundary.
type leaseResult struct {
	v   *fakeResource
	rel ReleaseFunc
	err error
}

// getOrCreateBounded runs GetOrCreate on its own goroutine and waits a generous bound for the
// outcome, so a regression that makes a collapsed wait uncancelable again surfaces as a clear
// failure instead of hanging the package until the test timeout. The bound is one-directional: the
// callers below hand it an already-dead context, which a correct implementation answers
// immediately, so machine load cannot flake it. unblock lets the in-flight create drain if we do
// give up.
func getOrCreateBounded(ctx context.Context, t *testing.T, p *Pool[*fakeResource], key string,
	create func(context.Context) (*fakeResource, error), unblock func(),
) leaseResult {
	t.Helper()
	out := make(chan leaseResult, 1)
	go func() {
		v, rel, err := p.GetOrCreate(ctx, key, create)
		out <- leaseResult{v, rel, err}
	}()
	select {
	case got := <-out:
		return got
	case <-time.After(2 * time.Second):
		unblock() // let the blocked create finish so the goroutines drain
		t.Fatal("GetOrCreate never returned — the collapsed wait ignored the caller's dead context")
		return leaseResult{}
	}
}

// TestPoolGetOrCreateWaiterHonorsOwnContext pins that a caller collapsed onto someone else's
// in-flight create waits on ITS OWN context: with sf.Do the wait was uncancelable, so a caller
// whose context was already dead still sat through the full dial. The abandoning caller must not
// cancel the create — it completes and installs the resource for everyone else.
func TestPoolGetOrCreateWaiterHonorsOwnContext(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()

	createStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) } // idempotent: safe from any path
	// A driver that honors the context it is handed. The leader's context stays live throughout, so
	// the create must still succeed after the second caller walks away.
	create := func(ctx context.Context) (*fakeResource, error) {
		close(createStarted)
		<-release
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("create ran on a dead context: %w", err)
		}
		return newFakeResource("shared"), nil
	}

	leader := make(chan leaseResult, 1)
	go func() {
		v, rel, err := p.GetOrCreate(context.Background(), keyOne, create)
		leader <- leaseResult{v, rel, err}
	}()
	<-createStarted // the create is in flight, so the next caller is a collapsed waiter

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	abandoned := getOrCreateBounded(dead, t, p, keyOne, create, unblock)
	assert.ErrorIs(t, abandoned.err, context.Canceled,
		"a waiter must fail on its OWN context, not block on the leader's create")
	assert.Nil(t, abandoned.v)
	assert.Nil(t, abandoned.rel)

	unblock()
	got := <-leader
	require.NoError(t, got.err, "the abandoning waiter must not cancel the in-flight create")
	require.NotNil(t, got.rel)
	defer got.rel()
	assert.Equal(t, 1, p.Size(), "the create still installed its entry")
	assert.False(t, tr.wasClosed(got.v.id))
}

// runLeaderCancelPoisonScenario drives the other quadrant of the collapse contract: the LEADER's
// context dies while a healthy waiter is collapsed onto its create. The create runs on a context
// derived from the leader's, so without severing cancellation a context-aware connector aborts the
// shared dial and every waiter inherits an error that was never its own.
func runLeaderCancelPoisonScenario(leaderCtx context.Context, t *testing.T, leaderCancel context.CancelFunc) {
	t.Helper()
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()

	createStarted := make(chan struct{})
	release := make(chan struct{})
	var startedOnce, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) } // idempotent: safe from any path
	// sawDeadCtx flags a canceled context observed inside create. It closes the timing hole where
	// a late waiter misses the collapse and runs a fresh, healthy create: the waiter-side assertion
	// alone would pass, but the leader's poisoned create still flagged its dead context here.
	var sawDeadCtx atomic.Bool
	create := func(ctx context.Context) (*fakeResource, error) {
		startedOnce.Do(func() { close(createStarted) })
		<-release
		if err := ctx.Err(); err != nil {
			sawDeadCtx.Store(true)
			return nil, fmt.Errorf("create ran on a dead context: %w", err)
		}
		return newFakeResource("shared"), nil
	}

	leader := make(chan leaseResult, 1)
	go func() {
		v, rel, err := p.GetOrCreate(leaderCtx, keyOne, create)
		leader <- leaseResult{v, rel, err}
	}()
	<-createStarted // the create is in flight and captured the leader's context

	leaderCancel()
	got := <-leader
	require.ErrorIs(t, got.err, context.Canceled, "the leader gives up on its own budget")

	// The waiter joins the still-blocked create, then a helper lets it drain. If the waiter loses
	// that race the entry (or its absence) still betrays the bug via sawDeadCtx below.
	go func() { time.Sleep(50 * time.Millisecond); unblock() }()
	waiter := getOrCreateBounded(context.Background(), t, p, keyOne, create, unblock)
	require.NoError(t, waiter.err, "a healthy waiter must not inherit the leader's cancellation")
	require.NotNil(t, waiter.rel)
	defer waiter.rel()
	assert.False(t, sawDeadCtx.Load(), "the shared create must never observe the leader's dead context")
	assert.Equal(t, 1, p.Size(), "the create installed its entry despite the leader's cancel")
}

// TestPoolLeaderCancelDoesNotPoisonSharedCreate exercises the deadline-less derivation branch:
// context.WithoutCancel alone carries the create.
func TestPoolLeaderCancelDoesNotPoisonSharedCreate(t *testing.T) {
	leaderCtx, leaderCancel := context.WithCancel(context.Background())
	runLeaderCancelPoisonScenario(leaderCtx, t, leaderCancel)
}

// TestPoolLeaderDeadlineCancelDoesNotPoisonSharedCreate exercises the deadline branch — the path
// every HTTP request takes (server.timeout.middleware puts a deadline on each request context).
// It pins that the carried-over deadline is rebound onto the DERIVED context: rebinding it onto the
// original caller's context re-couples cancellation, a regression the deadline-less variant above
// can never see because it skips this branch entirely.
func TestPoolLeaderDeadlineCancelDoesNotPoisonSharedCreate(t *testing.T) {
	leaderCtx, leaderCancel := context.WithTimeout(context.Background(), time.Hour)
	runLeaderCancelPoisonScenario(leaderCtx, t, leaderCancel)
}

// TestPoolCreateInheritsCallerDeadline pins the budget half of the derived create context: severing
// cancellation must NOT drop the caller's deadline — for a context-aware create (a dynamic tenant
// store, a consumer-supplied cache connector) that carried deadline is the only bound the create
// has.
func TestPoolCreateInheritsCallerDeadline(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()

	deadline := time.Now().Add(time.Hour)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var gotDeadline time.Time
	var hadDeadline bool
	_, rel, err := p.GetOrCreate(ctx, keyOne, func(cctx context.Context) (*fakeResource, error) {
		gotDeadline, hadDeadline = cctx.Deadline()
		return newFakeResource(keyOne), nil
	})
	require.NoError(t, err)
	defer rel()
	require.True(t, hadDeadline, "the create context must carry the caller's deadline")
	assert.True(t, gotDeadline.Equal(deadline), "the caller's budget must arrive unshortened: got %v want %v", gotDeadline, deadline)
}

// TestPoolAbandonedCreateReleasesSeedLease pins the counterpart of the abandon path: a create whose
// caller gave up still installs the entry, and that entry's seed lease — which no caller is left to
// claim — must be handed back. Otherwise refs stays >= 1 forever, eviction can only detach the
// resource, and its close is deferred to a release that never comes: a leaked connection.
func TestPoolAbandonedCreateReleasesSeedLease(t *testing.T) {
	tr := newCloseTracker()
	p := New(1, 0, tr.closer) // capacity 1: the next key evicts the abandoned entry
	defer p.Close()

	createStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) } // idempotent: safe from any path
	// A driver that hands back a live resource even though the caller's context died mid-dial — the
	// realistic shape, since drivers only observe cancellation at their own checkpoints.
	create := func(context.Context) (*fakeResource, error) {
		close(createStarted)
		<-release
		return newFakeResource("abandoned"), nil
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()
	abandoned := getOrCreateBounded(dead, t, p, keyOne, create, unblock)
	require.ErrorIs(t, abandoned.err, context.Canceled)
	require.Nil(t, abandoned.rel)

	<-createStarted
	unblock()
	require.Eventually(t, func() bool { return p.Size() == 1 }, 2*time.Second, 5*time.Millisecond,
		"the abandoned create must still install its entry")

	// Evict it. The resource must actually close — deferred is fine, never is not.
	_, rel2, err := p.GetOrCreate(context.Background(), keyTwo, keyedCreate(keyTwo))
	require.NoError(t, err)
	defer rel2()
	require.Eventually(t, func() bool { return tr.wasClosed("abandoned") }, 2*time.Second, 5*time.Millisecond,
		"an abandoned create's resource must remain closable — its unclaimed seed lease was never handed back")
	assert.Equal(t, 1, tr.count("abandoned"), "exactly one close")
}

// TestPoolStopCleanupJoins pins that StopCleanup WAITS for the cleanup goroutine: it used to close
// the stop channel and return, so Close could report shutdown complete while cleanupIdle was still
// inside p.closer — shutdown accounting lied and that close's error was invisible to Close.
func TestPoolStopCleanupJoins(t *testing.T) {
	closeStarted := make(chan struct{})
	var startedOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) } // idempotent: safe from any path
	var closes atomic.Int32

	closer := func(*fakeResource) error {
		closes.Add(1)
		startedOnce.Do(func() { close(closeStarted) })
		<-release // hold the cleanup path's close open until the test releases it
		return nil
	}
	p := New(5, time.Millisecond, closer)

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, keyedCreate(keyOne))
	require.NoError(t, err)
	rel() // unleased → the cleanup loop detaches and closes it

	p.StartCleanup(2 * time.Millisecond)
	select {
	case <-closeStarted:
	case <-time.After(2 * time.Second):
		unblock()
		t.Fatal("timed out waiting for idle cleanup to start closing the entry")
	}

	stopped := make(chan struct{})
	go func() {
		p.StopCleanup()
		close(stopped)
	}()

	// Negative bound: StopCleanup must still be blocked while the closer is. Extra machine load can
	// only make this MORE true, so it cannot flake — an early return is the actual defect.
	select {
	case <-stopped:
		unblock()
		t.Fatal("StopCleanup returned while a cleanup-path close was still in flight")
	case <-time.After(50 * time.Millisecond):
	}

	unblock()
	<-stopped // joins now that the closer returned
	require.NoError(t, p.Close())
	assert.Equal(t, int32(1), closes.Load(), "the idle entry was closed by the cleanup path, exactly once")
}

// TestPoolConcurrentStopCleanupBothJoin pins that EVERY StopCleanup caller joins the loop, not just
// the one that closed the stop channel. StopCleanup is public and re-exported by the database and
// messaging managers, so an app stopping cleanup concurrently with shutdown would otherwise make
// Close's own StopCleanup a no-op while a cleanup-path close was still running.
func TestPoolConcurrentStopCleanupBothJoin(t *testing.T) {
	closeStarted := make(chan struct{})
	var startedOnce sync.Once
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) } // idempotent: safe from any path

	closer := func(*fakeResource) error {
		startedOnce.Do(func() { close(closeStarted) })
		<-release
		return nil
	}
	p := New(5, time.Millisecond, closer)

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, keyedCreate(keyOne))
	require.NoError(t, err)
	rel() // unleased → the cleanup loop detaches and closes it

	p.StartCleanup(2 * time.Millisecond)
	select {
	case <-closeStarted:
	case <-time.After(2 * time.Second):
		unblock()
		t.Fatal("timed out waiting for idle cleanup to start closing the entry")
	}

	const stoppers = 2
	stopped := make(chan struct{}, stoppers)
	for i := 0; i < stoppers; i++ {
		go func() {
			p.StopCleanup()
			stopped <- struct{}{}
		}()
	}

	// Negative bound: NEITHER caller may return while the cleanup-path close is still running. Load
	// can only make this more true, so it cannot flake — a caller skipping the join is the defect.
	select {
	case <-stopped:
		unblock()
		t.Fatal("a concurrent StopCleanup returned without joining the cleanup goroutine")
	case <-time.After(50 * time.Millisecond):
	}

	unblock()
	for i := 0; i < stoppers; i++ {
		select {
		case <-stopped:
		case <-time.After(2 * time.Second):
			t.Fatal("StopCleanup did not return after the cleanup-path close completed")
		}
	}
	require.NoError(t, p.Close())
}

// TestPoolCloseSurfacesCleanupCloseError pins that a close failure on the idle-cleanup path reaches
// Close's errors.Join. The cleanup goroutine has no caller to return an error to, so the failure
// used to exist only as a statistic — invisible to consumers (e.g. DbManager) whose Close contract
// aggregates every failure.
func TestPoolCloseSurfacesCleanupCloseError(t *testing.T) {
	wantErr := errors.New("idle close failed")
	closeStarted := make(chan struct{})
	var startedOnce sync.Once

	closer := func(*fakeResource) error {
		startedOnce.Do(func() { close(closeStarted) })
		return wantErr
	}
	p := New(5, time.Millisecond, closer)

	_, rel, err := p.GetOrCreate(context.Background(), keyOne, keyedCreate(keyOne))
	require.NoError(t, err)
	rel()

	p.StartCleanup(2 * time.Millisecond)
	select {
	case <-closeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for idle cleanup to close the entry")
	}

	// Close joins the cleanup loop first, so the recorded failure is complete before it drains.
	assert.ErrorIs(t, p.Close(), wantErr, "an idle-cleanup close failure must reach Close's errors.Join")
	st := p.Stats()
	assert.GreaterOrEqual(t, st.IdleCleanups, 1, "the failure came from the cleanup path, not Close's own drain")
	assert.Equal(t, 1, st.Errors, "the failure is still counted exactly once")
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

// TestPoolGetOrCreateRecoversCreatePanic pins that a panic inside the create function is
// recovered in the singleflight leader and surfaced as an error rendered by TYPE only (ADR-081):
// the panic value never reaches the error text, and the pool counts the failure once without
// crediting a creation.
func TestPoolGetOrCreateRecoversCreatePanic(t *testing.T) {
	const marker = "marker-abc123"
	tests := []struct {
		name     string
		panicVal any
		wantType string
	}{
		{name: "error_value", panicVal: errors.New(marker), wantType: "*errors.errorString"},
		{name: "bare_string", panicVal: marker, wantType: "string"},
		{name: "struct_value", panicVal: struct{ Secret string }{marker}, wantType: "struct { Secret string }"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := newCloseTracker()
			p := New(0, 0, tr.closer)
			defer p.Close()

			var calls atomic.Int32
			v, rel, err := p.GetOrCreate(context.Background(), keyOne, panickingConnector(&calls, tt.panicVal, 0))

			require.Error(t, err)
			assert.Equal(t, int32(1), calls.Load(), "the panicking create runs exactly once")
			assert.Contains(t, err.Error(), `resourcepool: panic during create for key "key-1"`)
			assert.Contains(t, err.Error(), tt.wantType)
			assert.NotContains(t, err.Error(), marker, "the panic value must never reach the error text")
			assert.Nil(t, v)
			assert.Nil(t, rel)

			st := p.Stats()
			assert.Equal(t, 1, st.Errors)
			assert.Equal(t, 0, st.TotalCreated)
			assert.Equal(t, 0, st.Size)
		})
	}
}

// TestPoolConcurrentCreatePanicCountsErrorOnce pins that a create PANIC collapsed across N
// concurrent callers runs the create once, hands every waiter the same marker-free error, and
// increments Errors exactly once — in the leader — not once per blocked caller.
func TestPoolConcurrentCreatePanicCountsErrorOnce(t *testing.T) {
	const marker = "marker-abc123"
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()

	var calls atomic.Int32
	create := panickingConnector(&calls, errors.New(marker), 20*time.Millisecond)

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	errs := make([]string, workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			v, rel, err := p.GetOrCreate(context.Background(), keyOne, create)
			if assert.Error(t, err) {
				errs[i] = err.Error()
			}
			assert.Nil(t, v)
			assert.Nil(t, rel)
		}()
	}
	wg.Wait()

	for i := range errs {
		assert.Equal(t, errs[0], errs[i], "every collapsed caller must receive the same error")
		assert.NotContains(t, errs[i], marker)
	}
	assert.Contains(t, errs[0], `resourcepool: panic during create for key "key-1"`)
	assert.Equal(t, int32(1), calls.Load(), "singleflight must collapse the panicking create to one call")
	assert.Equal(t, 1, p.Stats().Errors, "a single collapsed create panic counts once, not once per waiter")
	assert.Equal(t, 0, p.Stats().TotalCreated)
}

// TestPoolUsableAfterCreatePanic pins that the pool survives a recovered create panic: the pool
// mutex is not held across the unwind (createEntry calls create before its first Lock), so a
// later create for the same key runs normally instead of deadlocking.
func TestPoolUsableAfterCreatePanic(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()
	ctx := context.Background()

	var calls atomic.Int32
	_, _, err := p.GetOrCreate(ctx, keyOne, panickingConnector(&calls, errors.New("boom"), 0))
	require.Error(t, err)

	v, rel, err := p.GetOrCreate(ctx, keyOne, keyedCreate(keyOne))
	require.NoError(t, err)
	require.NotNil(t, rel)
	defer rel()
	assert.Equal(t, keyOne, v.id)

	st := p.Stats()
	assert.Equal(t, 1, st.TotalCreated)
	assert.Equal(t, 1, st.Errors)
	assert.Equal(t, 1, st.Size)
}

// childModeEnv selects a child-process body inside a re-executed test binary. Two properties of
// the panic guard cannot be observed in-process: an UNRECOVERED panic on singleflight's own
// goroutine takes the process down (that is the whole reason the guard exists), and
// GODEBUG=panicnil=1 is read at startup, so it cannot be set from inside a running test.
const childModeEnv = "GOBRICKS_RESOURCEPOOL_CHILD"

// runPoolChild re-executes this test binary with mode selected and the given extra environment,
// returning its combined output and exit error.
func runPoolChild(t *testing.T, testName, mode string, env ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run="+testName, "-test.v")
	cmd.Env = append(append(os.Environ(), childModeEnv+"="+mode), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestPoolCloserPanicIsNotConvertedToAnAcquisitionError pins the guard's SCOPE. The recover wraps
// the consumer's create and nothing else, so a panic in the Closer — which runs after the entry
// is installed with its seed lease — still unwinds. Converting that one would hand the caller an
// acquisition error for an entry that WAS installed, leaving its seed lease unclaimed: refs stays
// >= 1 forever, so eviction can detach the resource but never close it, and repeated closer panics
// leak live resources past the pool's own limit.
func TestPoolCloserPanicIsNotConvertedToAnAcquisitionError(t *testing.T) {
	const marker = "closer-boom"
	if os.Getenv(childModeEnv) == "closer-panic" {
		p := New(1, 0, func(*fakeResource) error { panic(marker) })
		ctx := context.Background()

		_, rel, err := p.GetOrCreate(ctx, keyOne, keyedCreate(keyOne))
		if err != nil {
			t.Errorf("first create failed: %v", err)
			return
		}
		rel() // unleased, so the next create evicts and closes it

		// maxSize is 1, so this create evicts keyOne and calls the panicking Closer AFTER
		// installing keyTwo's entry.
		_, _, _ = p.GetOrCreate(ctx, keyTwo, keyedCreate(keyTwo))
		t.Errorf("child survived the closer panic")
		return
	}

	out, err := runPoolChild(t, "TestPoolCloserPanicIsNotConvertedToAnAcquisitionError", "closer-panic")

	require.Error(t, err, "the closer panic must still take the process down, not become an error:\n%s", out)
	assert.Contains(t, out, "panic: "+marker, "the runtime's own panic, not a converted one")
	assert.NotContains(t, out, "resourcepool: panic during create",
		"the guard must not reach past the create into the Closer")
}

// TestPoolCreatePanicNilIsStillAnError covers panic(nil) under GODEBUG=panicnil=1, where recover()
// returns nil: the guard tracks normal completion rather than a non-nil recovered value, so the
// create still fails with the documented error instead of returning a nil error beside a zero
// resource for createEntry to install.
func TestPoolCreatePanicNilIsStillAnError(t *testing.T) {
	if os.Getenv(childModeEnv) == "panic-nil" {
		tr := newCloseTracker()
		p := New(0, 0, tr.closer)
		defer p.Close()

		var calls atomic.Int32
		v, rel, err := p.GetOrCreate(context.Background(), keyOne, panickingConnector(&calls, nil, 0))

		require.Error(t, err)
		assert.Contains(t, err.Error(), `resourcepool: panic during create for key "key-1"`)
		assert.Nil(t, v)
		assert.Nil(t, rel)
		st := p.Stats()
		assert.Equal(t, 1, st.Errors)
		assert.Equal(t, 0, st.Size)
		assert.Equal(t, 0, st.TotalCreated)
		return
	}

	out, err := runPoolChild(t, "TestPoolCreatePanicNilIsStillAnError", "panic-nil", "GODEBUG=panicnil=1")

	require.NoError(t, err, "child failed under GODEBUG=panicnil=1:\n%s", out)
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
func TestPoolCloseClosesAllAndJoinsErrors(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	ctx := context.Background()

	errTwo := errors.New("close key-2 failed")
	errThree := errors.New("close key-3 failed")
	// key-2 and key-3 both fail to close with distinct errors; key-1 succeeds.
	makeCreate := func(key string, closeErr error) func(context.Context) (*fakeResource, error) {
		return func(context.Context) (*fakeResource, error) {
			r := newFakeResource(key)
			r.closeErr = closeErr
			return r, nil
		}
	}
	for _, kv := range []struct {
		key      string
		closeErr error
	}{{keyOne, nil}, {keyTwo, errTwo}, {keyThree, errThree}} {
		_, rel, err := p.GetOrCreate(ctx, kv.key, makeCreate(kv.key, kv.closeErr))
		require.NoError(t, err)
		rel()
	}

	err := p.Close()
	// Close joins EVERY close failure via errors.Join — errors.Is matches each individual one,
	// so a consumer aggregating them (DbManager) surfaces all, not just the first.
	assert.ErrorIs(t, err, errTwo, "Close surfaces the key-2 close error")
	assert.ErrorIs(t, err, errThree, "Close surfaces the key-3 close error")
	assert.True(t, tr.wasClosed(keyOne))
	assert.True(t, tr.wasClosed(keyTwo))
	assert.True(t, tr.wasClosed(keyThree))
	assert.Equal(t, 0, p.Size())
	assert.Equal(t, 2, p.Stats().Errors, "each close failure counts once")
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

// TestPoolCloseDefersBorrowedEntryToRelease pins the #606 invariant on the Close path: a
// borrowed entry is detached but NOT closed by Close; its final release closes it exactly
// once. Was TestPoolCloseClosesLeasedEntriesWithoutDoubleClose, which asserted the opposite
// (see plan 115 and the ADR-032 amendment).
func TestPoolCloseDefersBorrowedEntryToRelease(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, keyedCreate(keyOne))
	require.NoError(t, err)

	require.NoError(t, p.Close())
	assert.False(t, tr.wasClosed(v.id), "Close must not close a borrowed entry (#606)")
	assert.Equal(t, 0, p.Size(), "the entry is still detached from the pool")
	assert.Equal(t, 0, p.Stats().Errors)

	rel()
	assert.Equal(t, 1, tr.count(v.id), "the final release closes it exactly once")
}

// TestPoolCloseClosesUnborrowedImmediately verifies an unborrowed entry still closes during
// Close, and its failure is returned.
func TestPoolCloseClosesUnborrowedImmediately(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	errBoom := errors.New("close failed")

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(context.Context) (*fakeResource, error) {
		r := newFakeResource(keyOne)
		r.closeErr = errBoom
		return r, nil
	})
	require.NoError(t, err)
	rel() // no borrower left

	assert.ErrorIs(t, p.Close(), errBoom)
	assert.Equal(t, 1, tr.count(v.id))
	assert.Equal(t, 1, p.Stats().Errors)
}

// TestPoolCloseThenReleaseCountsBorrowedCloseError verifies a borrowed entry's deferred close
// failure cannot reach Close's return; it lands in Stats instead.
func TestPoolCloseThenReleaseCountsBorrowedCloseError(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	errBoom := errors.New("close failed")

	v, rel, err := p.GetOrCreate(context.Background(), keyOne, func(context.Context) (*fakeResource, error) {
		r := newFakeResource(keyOne)
		r.closeErr = errBoom
		return r, nil
	})
	require.NoError(t, err)

	assert.NoError(t, p.Close(), "a deferred close cannot contribute to Close's returned error")
	assert.Equal(t, 0, p.Stats().Errors)

	rel()
	assert.Equal(t, 1, tr.count(v.id))
	assert.Equal(t, 1, p.Stats().Errors, "the deferred failure is counted at release time instead")
}

// TestPoolCloseRacingFinalReleaseClosesExactlyOnce verifies Close racing the final release
// closes exactly once in EITHER interleaving.
func TestPoolCloseRacingFinalReleaseClosesExactlyOnce(t *testing.T) {
	for i := 0; i < 100; i++ {
		tr := newCloseTracker()
		p := New(5, 0, tr.closer)

		v, rel, err := p.GetOrCreate(context.Background(), keyOne, keyedCreate(keyOne))
		require.NoError(t, err)

		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _ = p.Close() }()
		go func() { defer wg.Done(); <-start; rel() }()
		close(start)
		wg.Wait()

		require.Equal(t, 1, tr.count(v.id), "exactly one close regardless of interleaving")
	}
}

// TestPoolCloseClosesUnclaimedSeedEntry verifies an UNCLAIMED SEED is not a borrower: Close
// closes such an entry, so a late claimer is refused and GetOrCreate reports ErrPoolClosed
// instead of handing out a live resource.
func TestPoolCloseClosesUnclaimedSeedEntry(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)

	// createEntry installs the entry with refs==1, seedHeld — exactly the state a GetOrCreate
	// caller is in between createEntry and claimOrAcquire.
	e, err := p.createEntry(context.Background(), keyOne, keyedCreate(keyOne))
	require.NoError(t, err)

	require.NoError(t, p.Close())
	assert.Equal(t, 1, tr.count(keyOne), "a seed-only entry has no borrower — Close closes it")
	assert.False(t, p.claimOrAcquire(e), "the late claim must be refused, so GetOrCreate reports ErrPoolClosed")
}

// TestPoolCloseDefersBorrowerHoldingAlongsideSeed verifies the quadrant the seed discount must
// NOT swallow: a real borrower alongside an unclaimed seed still defers the close to the last
// release.
func TestPoolCloseDefersBorrowerHoldingAlongsideSeed(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)

	e, err := p.createEntry(context.Background(), keyOne, keyedCreate(keyOne))
	require.NoError(t, err)
	require.NotNil(t, p.getExisting(keyOne), "a second caller borrows it: refs==2, seed still unclaimed")

	require.NoError(t, p.Close())
	assert.Equal(t, 0, tr.count(keyOne), "one live borrower is enough to defer the close")

	require.True(t, p.claimOrAcquire(e), "the pending caller still claims the seed")
	p.releaseEntry(e)
	assert.Equal(t, 0, tr.count(keyOne), "one release is not the last one")
	p.releaseEntry(e)
	assert.Equal(t, 1, tr.count(keyOne), "the final release closes it exactly once")
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

// TestPoolStartCleanupSecondCallKeepsOneLoop pins that a second StartCleanup spawns no second
// goroutine. A second loop would have to overwrite cleanupStop/cleanupDone (StartCleanup assigns
// both), orphaning the first loop with no channel left to stop it — so channel identity across
// the two calls is the observable proof that exactly one loop exists.
func TestPoolStartCleanupSecondCallKeepsOneLoop(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 40*time.Millisecond, tr.closer)
	defer p.Close()

	p.StartCleanup(20 * time.Millisecond)
	p.cleanupMu.Lock()
	firstStop, firstDone := p.cleanupStop, p.cleanupDone
	p.cleanupMu.Unlock()
	require.NotNil(t, firstStop, "the first StartCleanup must start a loop")

	p.StartCleanup(5 * time.Millisecond)
	p.cleanupMu.Lock()
	secondStop, secondDone := p.cleanupStop, p.cleanupDone
	p.cleanupMu.Unlock()

	assert.Equal(t, firstStop, secondStop, "a second StartCleanup must not replace the running loop's stop channel")
	assert.Equal(t, firstDone, secondDone, "a second StartCleanup must not replace the running loop's done channel")

	p.StopCleanup()
	select {
	case <-firstDone:
	default:
		t.Fatal("one StopCleanup must have joined the single running loop")
	}
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

// TestPoolSnapshotReportsLiveEntries pins the observability-only Snapshot accessor: one
// EntrySnapshot per live entry (key + non-zero LastUsed), shrinking as entries are removed or
// the pool is closed. It takes no lease and does not touch LRU.
func TestPoolSnapshotReportsLiveEntries(t *testing.T) {
	tr := newCloseTracker()
	p := New(5, 0, tr.closer)
	ctx := context.Background()

	// Empty pool → empty snapshot.
	assert.Empty(t, p.Snapshot())

	_, relA, err := p.GetOrCreate(ctx, keyOne, keyedCreate(keyOne))
	require.NoError(t, err)
	_, relB, err := p.GetOrCreate(ctx, keyTwo, keyedCreate(keyTwo))
	require.NoError(t, err)

	snap := p.Snapshot()
	require.Len(t, snap, 2, "one snapshot entry per live key")
	byKey := make(map[string]EntrySnapshot, len(snap))
	for _, e := range snap {
		byKey[e.Key] = e
		assert.False(t, e.LastUsed.IsZero(), "LastUsed must be populated")
	}
	require.Contains(t, byKey, keyOne)
	require.Contains(t, byKey, keyTwo)

	// Remove one (release its lease first so Remove closes it) → snapshot shrinks.
	relA()
	if v, shouldClose := p.Remove(keyOne); shouldClose {
		_ = tr.closer(v)
	}
	snap = p.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, keyTwo, snap[0].Key)

	// Close → snapshot empties.
	relB()
	require.NoError(t, p.Close())
	assert.Empty(t, p.Snapshot(), "closed pool reports no live entries")
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

// hammerGetRemove runs ops rounds of GetOrCreate + release, periodically Removing the key, for the
// concurrent churn stress test. Extracted into a helper so the test body stays under the
// cognitive-complexity gate.
func hammerGetRemove(t *testing.T, p *Pool[*fakeResource], tr *closeTracker, ops int) {
	t.Helper()
	ctx := context.Background()
	for j := 0; j < ops; j++ {
		key := fmt.Sprintf("key-%d", j%6)
		_, rel, err := p.GetOrCreate(ctx, key, keyedCreate(key))
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
}

// TestPoolConcurrentGetRacesRemove stress-tests concurrent GetOrCreate + Remove under -race.
// The bounded acquire retry can, under this pathological churn, exhaust its attempts when a
// peeked entry is removed before it can be claimed — a legitimate, documented outcome (mirrors
// cache's maxGetAttempts bound). Any OTHER error, a panic, or a double-close is a failure.
func TestPoolConcurrentGetRacesRemove(t *testing.T) {
	tr := newCloseTracker()
	p := New(0, 0, tr.closer)
	defer p.Close()

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			hammerGetRemove(t, p, tr, 40)
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

// TestPoolSlowCloseDoesNotBlockConcurrentGet pins the audit's M3 property: a slow Closer on an
// evicted entry runs OUTSIDE the pool lock, so it cannot head-of-line-block a concurrent GetOrCreate
// on a DIFFERENT key. This latency guarantee moved from the managers into the pool with the ADR-032
// extraction; a regression putting close back under the lock would pass -race but fail this timing pin.
func TestPoolSlowCloseDoesNotBlockConcurrentGet(t *testing.T) {
	const slowClose = 200 * time.Millisecond
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) } // idempotent: safe from any path
	closeStarted := make(chan struct{})
	closer := func(r *fakeResource) error {
		if r.id == "slow" {
			close(closeStarted)
			<-release // hold the close open until the test releases it
		}
		return nil
	}
	p := New(1, 0, closer) // capacity 1: creating a new key evicts the previous
	ctx := context.Background()

	// Seed the slow-close victim and release its lease so it is evictable.
	_, rel1, err := p.GetOrCreate(ctx, keyOne, func(context.Context) (*fakeResource, error) {
		return newFakeResource("slow"), nil
	})
	require.NoError(t, err)
	rel1()

	// Evicting the victim (a Get on another key) runs its slow close in the background.
	evictDone := make(chan struct{})
	go func() {
		_, rel2, gErr := p.GetOrCreate(ctx, keyTwo, func(context.Context) (*fakeResource, error) {
			return newFakeResource("k2"), nil
		})
		assert.NoError(t, gErr)
		rel2()
		close(evictDone)
	}()

	// Wait (bounded) for the victim's slow close to begin — it holds NO pool lock.
	select {
	case <-closeStarted:
	case <-time.After(2 * time.Second):
		unblock()
		t.Fatal("timed out waiting for the evicted victim's slow close to start")
	}

	// Run the third-key Get in a goroutine so a REGRESSION (close under the lock) surfaces as a
	// bounded-time FAILURE rather than hanging until the package test timeout.
	got := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		_, rel3, gErr := p.GetOrCreate(ctx, keyThree, func(context.Context) (*fakeResource, error) {
			return newFakeResource("k3"), nil
		})
		if gErr == nil {
			rel3()
		}
		got <- time.Since(start)
	}()

	select {
	case elapsed := <-got:
		assert.Less(t, elapsed, slowClose/2,
			"a slow close on an evicted entry must not block Get on another key (close runs outside the lock)")
	case <-time.After(slowClose / 2):
		unblock() // let the slow close finish so the goroutines drain
		t.Fatal("Get on a third key was blocked by the evicted victim's slow close (M3 regression: close under the lock)")
	}

	unblock()
	<-evictDone
	require.NoError(t, p.Close())
}

// TestPoolErrorCounterCountsEveryConcurrentFailure pins the atomic error counter: eight
// goroutines drive the incErrors path concurrently through failing creates on distinct keys —
// distinct so singleflight never collapses them — and the counter must end at exactly the number
// of injected failures, with no lost update. It asserts the DELTA from a baseline snapshot rather
// than an absolute count, so it stays honest if the pool ever counts something else on the way in.
//
// The -race detector is the other half of the assertion, but note what it does and does not
// prove: the previous mu-guarded int was already safe here and passes this test too. What it
// catches is the counter's synchronization being REMOVED later — an unguarded plain int fails
// immediately under this write-write contention.
func TestPoolErrorCounterCountsEveryConcurrentFailure(t *testing.T) {
	const (
		creators       = 8
		failsPerWriter = 25
	)

	wantErr := errors.New("create failed")
	// The closer must fail: no entry can be installed when every create errors, so the
	// require.NoError on Close below is what proves none slipped through.
	closeErr := errors.New("close failed")
	p := New(0, 0, func(any) error { return closeErr })

	baseline := p.Stats().Errors

	var wg sync.WaitGroup
	for w := range creators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range failsPerWriter {
				key := fmt.Sprintf("race-key-%d-%d", w, i)
				_, _, err := p.GetOrCreate(t.Context(), key, func(context.Context) (any, error) {
					return nil, wantErr
				})
				assert.ErrorIs(t, err, wantErr)
			}
		}()
	}
	wg.Wait()

	got := p.Stats().Errors - baseline
	assert.Equal(t, creators*failsPerWriter, got,
		"every injected create failure must be counted exactly once, with no lost update")

	require.NoError(t, p.Close(), "no entry was ever installed, so Close has nothing to fail on")
}
