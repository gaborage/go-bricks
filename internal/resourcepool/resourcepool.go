// Package resourcepool implements the ADR-032 keyed pool of leasable,
// refcounted, LRU-capped, idle-evicted resources. CacheManager is the first
// consumer; the database and messaging managers adopt it in follow-up PRs.
//
// A pool hands each borrower a lease (via GetOrCreate) plus an idempotent
// ReleaseFunc. A resource evicted (LRU, idle, or explicit Remove) while a
// lease is outstanding is detached immediately but its Closer runs only once
// the final lease is released, so an in-use resource is never closed under an
// active caller (the #606 race). The Closer is ALWAYS invoked outside the pool
// lock.
package resourcepool

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrPoolClosed is returned by GetOrCreate after Close has been called.
// Callers can errors.Is(err, ErrPoolClosed) to distinguish "pool is gone" from
// a per-resource creation failure.
var ErrPoolClosed = errors.New("resourcepool: pool closed")

// Closer releases the underlying resource. It is always invoked OUTSIDE the
// pool lock and exactly once per resource. A non-nil return increments the
// pool's error counter on the lifecycle paths that track close failures (final
// lease release, idle cleanup, and Close); the create-time eviction and
// closed-pool cleanup paths discard it.
type Closer[V any] func(v V) error

// ReleaseFunc releases a lease obtained from GetOrCreate. Callers must invoke it
// (typically deferred) when finished with the resource for the current unit of
// work. It is idempotent and does NOT close the shared resource; it signals this
// borrower is done, so a resource evicted while leased is closed only once its
// last lease is released. See ADR-032.
type ReleaseFunc func()

// maxAcquireAttempts bounds the rare retry where a freshly resolved entry is
// evicted before the caller can take a lease (only under extreme pool churn). A
// new entry is inserted at the LRU front with a seed lease, so in practice the
// first attempt always succeeds.
const maxAcquireAttempts = 4

// PoolStats is a point-in-time snapshot of the pool's counters. Consumers adapt
// it into their own stat shape (cache's typed ManagerStats, database/messaging's
// map[string]any).
type PoolStats struct {
	Size         int           // Current number of active entries
	MaxSize      int           // Maximum allowed active entries (0 = unlimited)
	TotalCreated int           // Total entries created since the pool started
	Evictions    int           // Total evictions due to LRU policy
	IdleCleanups int           // Total removals due to idle timeout
	Errors       int           // Total create failures and tracked close failures
	IdleTTL      time.Duration // Idle timeout duration
}

// EntrySnapshot is a point-in-time view of one pooled entry's identity and idle age, for
// managers that surface per-resource detail in Stats (e.g. DbManager's "connections" array).
type EntrySnapshot struct {
	Key      string
	LastUsed time.Time
}

// entry represents a pooled resource in the LRU.
// refs, seedHeld, detached, and closed are guarded by Pool.mu.
type entry[V any] struct {
	value    V
	key      string
	lastUsed time.Time
	element  *list.Element // Position in the LRU list

	// refs counts outstanding leases (current borrowers); an entry with refs > 0 is in use.
	refs int
	// seedHeld is true when one of refs is an unclaimed "seed" lease taken at creation. The
	// seed keeps a brand-new entry alive (refs >= 1) through the window before its first
	// GetOrCreate caller claims it, so a concurrent evict/Remove can only detach (never close)
	// it. The first claimOrAcquire takes the seed; later callers increment refs normally.
	seedHeld bool
	// detached marks an entry removed from the map+LRU whose Closer was deferred because a
	// lease was still outstanding.
	detached bool
	// closed guards against a double Closer call once the deferred close has run.
	closed bool
}

// Pool is a keyed pool of leasable, refcounted, LRU-capped, idle-evicted
// resources. The zero value is not usable; construct with New.
type Pool[V any] struct {
	mu      sync.Mutex
	entries map[string]*entry[V]
	lru     *list.List
	sf      singleflight.Group

	maxSize int
	idleTTL time.Duration
	closer  Closer[V]

	// Statistics (guarded by mu).
	totalCreated int
	evictions    int
	idleCleanups int
	errors       int

	// cleanupErrs retains close failures from the idle-cleanup path (guarded by mu) so Close can
	// surface them through its errors.Join contract. They are only recoverable because StopCleanup
	// joins the loop: by the time Close drains this, the cleanup goroutine has finished recording.
	cleanupErrs []error

	// closed flips to true the moment Close begins. Read on the hot path of
	// GetOrCreate so callers immediately see ErrPoolClosed instead of receiving a
	// handle to a resource that is about to be torn down. Atomic so the hot path
	// does not need to take mu just to consult shutdown state.
	closed atomic.Bool

	// Cleanup-goroutine lifecycle (guarded by cleanupMu, independent of mu).
	cleanupMu   sync.Mutex
	cleanupStop chan struct{} // non-nil while a cleanup loop is running
	cleanupDone chan struct{} // closed by that same loop on exit; paired with cleanupStop
	closeOnce   sync.Once
}

// New creates a pool with the given capacity, idle timeout, and Closer. The
// Closer is required; a nil Closer panics on the first close. maxSize <= 0 means
// unlimited; idleTTL <= 0 disables idle cleanup.
func New[V any](maxSize int, idleTTL time.Duration, closer Closer[V]) *Pool[V] {
	return &Pool[V]{
		entries: make(map[string]*entry[V]),
		lru:     list.New(),
		maxSize: maxSize,
		idleTTL: idleTTL,
		closer:  closer,
	}
}

// GetOrCreate returns the resource for key plus a ReleaseFunc the caller must
// invoke when finished with it for the current unit of work (typically
// deferred). It creates the resource via create on first use, collapsing
// concurrent creates for the same key through singleflight. Returns
// ErrPoolClosed if Close has been called. On error the returned ReleaseFunc is
// nil — check err first.
func (p *Pool[V]) GetOrCreate(ctx context.Context, key string, create func(context.Context) (V, error)) (V, ReleaseFunc, error) {
	var zero V
	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		// Re-check on every iteration (not just once up front): a concurrent Close must not be
		// raced into recreating an entry on a shut-down pool. createEntry also re-checks under
		// the lock to close the window fully.
		if p.closed.Load() {
			return zero, nil, ErrPoolClosed
		}

		// Fast path: getExisting increments the refcount atomically with the lookup, so the
		// entry cannot be evicted-and-closed before the lease is taken.
		if e := p.getExisting(key); e != nil {
			return e.value, p.makeRelease(e), nil
		}

		// Slow path: singleflight collapses concurrent creates for the same key into one. It
		// returns the shared entry (freshly created with a seed lease, or an existing one);
		// every caller then takes its own lease on that pointer via claimOrAcquire — the first
		// claims the seed, the rest increment — so each concurrent borrower is counted.
		// DoChan (not Do) so every collapsed caller waits on ITS OWN context: Do blocks
		// uncancelably, so a caller whose budget was already spent still sat through the whole
		// dial before being handed an error that was not its own.
		ch := p.sf.DoChan(key, func() (any, error) {
			if e := p.peek(key); e != nil {
				return e, nil
			}
			e, cerr := p.createEntry(ctx, key, create)
			if cerr != nil {
				// Count the failure once, HERE in the singleflight leader. Every collapsed caller
				// receives the same error, so incrementing per caller would over-count a single
				// create failure by the number of blocked callers.
				p.incErrors()
				return nil, cerr
			}
			return e, nil
		})

		var res singleflight.Result
		select {
		case res = <-ch:
		case <-ctx.Done():
			// This caller gives up on its own budget; the create is deliberately NOT canceled, so
			// it still installs the resource for future callers. singleflight's result channel is
			// buffered (capacity 1), so the abandoned send never blocks — but a fresh entry's seed
			// lease would go unclaimed, so releaseAbandoned settles it off this goroutine.
			go p.releaseAbandoned(ch)
			return zero, nil, ctx.Err()
		}
		if res.Err != nil {
			return zero, nil, res.Err
		}

		e := res.Val.(*entry[V])
		if p.claimOrAcquire(e) {
			return e.value, p.makeRelease(e), nil
		}
		// The reused entry was closed in the window between lookup and claim (a concurrent
		// evict/Remove of an unleased entry); loop to create a fresh one. The create path
		// always succeeds because a new entry carries a seed lease, so this converges.
	}

	return zero, nil, fmt.Errorf("resourcepool: failed to acquire %q after %d attempts (pool churn)", key, maxAcquireAttempts)
}

// releaseAbandoned settles a collapsed create whose caller returned early on its own context. The
// create ran to completion, and a freshly created entry carries an unclaimed seed lease — taking
// and immediately releasing a lease hands that seed back, leaving a normal unleased cached entry
// that eviction, idle cleanup, and Close can close. Without it the seed would pin refs >= 1
// forever and a later eviction would detach the resource with its close deferred to a release that
// never comes, leaking it. Runs on its own goroutine, bounded by the create's own completion.
//
// These goroutines are deliberately NOT joined by Close, unlike the cleanup loop. Joining them
// would make Close wait on an in-flight CREATE, and creation currently carries no bound of its own
// (see createEntry) — a driver dial can outlast the whole shutdown budget, trading a narrow missed
// close for a shutdown that may never return. What remains is narrow: an entry installed here and
// then evicted while its seed was still unclaimed is detached but not closed, so a concurrent Close
// (which can no longer see it) may return while this goroutine's releaseEntry runs the Closer. It
// cannot double-close or resurrect the pool — createEntry re-checks closed under mu, and Close
// marks every entry it drains closed. Bounding creation is the prerequisite for joining these
// safely, so both belong to the deferred create-timeout work.
func (p *Pool[V]) releaseAbandoned(ch <-chan singleflight.Result) {
	res := <-ch
	if res.Err != nil {
		return
	}
	e := res.Val.(*entry[V])
	if p.claimOrAcquire(e) {
		p.releaseEntry(e)
	}
}

// claimOrAcquire takes one lease on e, operating on the shared pointer so it can never "miss"
// via a map lookup. It returns false only when the entry has already been fully closed (a
// reused entry that lost a race), signaling the caller to retry with a fresh entry.
func (p *Pool[V]) claimOrAcquire(e *entry[V]) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.closed {
		return false
	}
	if e.seedHeld {
		e.seedHeld = false // claim the seed: that ref becomes this caller's lease
	} else {
		e.refs++
	}
	return true
}

// getExisting retrieves an existing entry with a lease acquired (refcount incremented) and
// updates LRU position, or nil if not found. The refcount increment happens under the same
// lock as the lookup so the entry cannot be evicted-and-closed before the lease is taken.
func (p *Pool[V]) getExisting(key string) *entry[V] {
	p.mu.Lock()
	defer p.mu.Unlock()

	e, exists := p.entries[key]
	if !exists {
		return nil
	}

	p.lru.MoveToFront(e.element)
	e.lastUsed = time.Now()
	e.refs++

	return e
}

// peek reports whether an entry exists for the key without taking a lease or touching LRU.
func (p *Pool[V]) peek(key string) *entry[V] {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries[key]
}

// makeRelease returns an idempotent ReleaseFunc bound to a single lease on e.
func (p *Pool[V]) makeRelease(e *entry[V]) ReleaseFunc {
	var once sync.Once
	return func() {
		once.Do(func() { p.releaseEntry(e) })
	}
}

// releaseEntry drops one lease. If the entry was detached (evicted/removed/idle-cleaned) while
// leased and this was the final lease, it closes the resource now — outside the lock — and
// counts a close failure.
func (p *Pool[V]) releaseEntry(e *entry[V]) {
	p.mu.Lock()
	e.refs--
	shouldClose := e.detached && e.refs <= 0 && !e.closed
	if shouldClose {
		e.closed = true
	}
	p.mu.Unlock()

	if shouldClose {
		if err := p.closer(e.value); err != nil {
			p.incErrors()
		}
	}
}

// createEntry creates a new resource and adds it to the pool with a single seed lease
// (refs == 1, seedHeld). The seed keeps the entry alive through the window before the caller
// claims it via claimOrAcquire, so a concurrent evict/Remove can only detach it. If Close ran
// between the caller's closed check and here, the just-created resource is closed and
// ErrPoolClosed is returned rather than resurrecting the cleared map.
//
// create still runs on the initiating caller's context. That context is what delivers the
// app.startup.{database,messaging,cache} budgets to the connectors during pre-initialization, so it
// cannot simply be severed; whether a SHARED resource's creation should instead carry its own bound
// (a per-pool CreateTimeout, leaving the startup budgets a separate seam) is deliberately deferred.
func (p *Pool[V]) createEntry(ctx context.Context, key string, create func(context.Context) (V, error)) (*entry[V], error) {
	value, err := create(ctx)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		_ = p.closer(value) // orphaned instance — close is best-effort, not counted
		return nil, ErrPoolClosed
	}

	evicted := p.evictIfNeeded()

	e := &entry[V]{
		value:    value,
		key:      key,
		lastUsed: time.Now(),
		refs:     1,
		seedHeld: true,
	}
	e.element = p.lru.PushFront(e)
	p.entries[key] = e
	p.totalCreated++
	p.mu.Unlock()

	// Close the evicted resource outside the lock (eviction close failures are not counted).
	if evicted != nil {
		_ = p.closer(evicted.value)
	}

	return e, nil
}

// evictIfNeeded removes the least recently used entry if at capacity. Must be called with mu
// held. It detaches the entry and returns it for the caller to close OUTSIDE the lock — but
// ONLY when the entry has no outstanding leases. If the LRU victim is still leased, its close
// is deferred to the final lease release (the #606 race). Returns nil when nothing should be
// closed now.
func (p *Pool[V]) evictIfNeeded() *entry[V] {
	if p.maxSize <= 0 || len(p.entries) < p.maxSize {
		return nil
	}

	oldest := p.lru.Back()
	if oldest == nil {
		return nil
	}

	e := oldest.Value.(*entry[V])
	p.removeEntryLocked(e.key)
	p.evictions++

	if e.refs > 0 {
		return nil // still leased — defer the close to the final lease release
	}
	e.closed = true
	return e
}

// Remove detaches the entry for key from the pool. If it exists and is unleased, it marks the
// entry closed and returns (value, true) for the caller to close OUTSIDE the pool. If it is
// still leased, the close is deferred to the final lease release and Remove returns
// (zero, false). A missing key returns (zero, false).
func (p *Pool[V]) Remove(key string) (v V, shouldClose bool) {
	var zero V
	p.mu.Lock()
	e := p.removeEntryLocked(key)
	shouldClose = e != nil && e.refs <= 0 && !e.closed
	if shouldClose {
		e.closed = true
	}
	p.mu.Unlock()

	if !shouldClose {
		return zero, false
	}
	return e.value, true
}

// removeEntryLocked removes bookkeeping for an entry (must be called with mu held). Returns
// the removed entry or nil if not found. The caller is responsible for closing the returned
// entry's resource. A removed entry is marked detached so a concurrent final lease release
// runs its deferred close.
func (p *Pool[V]) removeEntryLocked(key string) *entry[V] {
	e, exists := p.entries[key]
	if !exists {
		return nil
	}

	p.lru.Remove(e.element)
	delete(p.entries, key)
	e.detached = true

	return e
}

// StartCleanup starts the idle-cleanup loop at the given interval. It is a no-op when the
// pool has no idle timeout, the interval is non-positive, the pool is closed, or a loop is
// already running (idempotent).
func (p *Pool[V]) StartCleanup(interval time.Duration) {
	if p.idleTTL <= 0 || interval <= 0 || p.closed.Load() {
		return
	}

	p.cleanupMu.Lock()
	defer p.cleanupMu.Unlock()
	// Re-check closed under cleanupMu. A concurrent Close may have flipped closed and run its
	// (no-op, because cleanupStop was still nil) StopCleanup between the lock-free guard above
	// and our acquiring cleanupMu. Without this re-check we would start a cleanupLoop that Close
	// will never stop — a goroutine leaked forever on a closed pool.
	if p.closed.Load() {
		return
	}
	if p.cleanupStop != nil {
		return // already running
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	p.cleanupStop = stop
	p.cleanupDone = done
	go p.cleanupLoop(interval, stop, done)
}

// StopCleanup stops a running idle-cleanup loop and JOINS it: once it returns, no cleanupIdle
// close can still be in flight. Close relies on that — otherwise it could report shutdown complete
// while p.closer was still running on the cleanup goroutine, hiding that close's error from
// Close's errors.Join contract. It is idempotent: calling it when no loop is running is a no-op.
func (p *Pool[V]) StopCleanup() {
	p.cleanupMu.Lock()
	// Capture done BEFORE clearing cleanupStop, and leave it in place: StopCleanup is public and
	// re-exported by the database and messaging managers, so a second concurrent caller must join
	// the same loop rather than see a nil cleanupStop and return while a close is still in flight.
	// StartCleanup overwrites cleanupDone when it starts the next loop, so a caller can only ever
	// wait on the loop that was running when it took this lock.
	done := p.cleanupDone
	if p.cleanupStop != nil {
		close(p.cleanupStop)
		p.cleanupStop = nil
	}
	p.cleanupMu.Unlock()

	if done == nil {
		return // no loop has ever run
	}

	// Join OUTSIDE cleanupMu. cleanupLoop never takes cleanupMu, so waiting under it would not
	// deadlock today, but a future closer that touches the cleanup lifecycle would deadlock.
	<-done
}

// cleanupLoop periodically removes idle entries until stopped, closing done on exit so StopCleanup
// can join it.
func (p *Pool[V]) cleanupLoop(interval time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.cleanupIdle()
		case <-stop:
			return
		}
	}
}

// cleanupIdle removes entries idle beyond the TTL. A leased idle entry is detached (and
// counted as a cleanup) but its close is deferred to the final lease release. Close failures
// are counted.
func (p *Pool[V]) cleanupIdle() {
	if p.idleTTL <= 0 {
		return
	}

	p.mu.Lock()
	now := time.Now()
	var toClose []*entry[V]

	for key, e := range p.entries {
		if now.Sub(e.lastUsed) > p.idleTTL {
			if removed := p.removeEntryLocked(key); removed != nil {
				p.idleCleanups++
				if removed.refs > 0 {
					continue // still leased — defer close to the final lease release
				}
				removed.closed = true
				toClose = append(toClose, removed)
			}
		}
	}
	p.mu.Unlock()

	for _, e := range toClose {
		if err := p.closer(e.value); err != nil {
			p.noteCleanupCloseErr(err)
		}
	}
}

// noteCleanupCloseErr counts an idle-cleanup close failure AND retains it for Close. The cleanup
// goroutine has no caller to return an error to, so without this the failure only ever showed up as
// a statistic. Must be called without mu held.
func (p *Pool[V]) noteCleanupCloseErr(err error) {
	p.mu.Lock()
	p.errors++
	p.cleanupErrs = append(p.cleanupErrs, err)
	p.mu.Unlock()
}

// Close shuts down all pooled resources, stops the cleanup loop, and makes subsequent
// GetOrCreate calls return ErrPoolClosed. It is idempotent and returns EVERY close error joined
// via errors.Join (nil if none) — so consumers whose Close contract aggregates all failures
// (e.g. DbManager) can surface them all, while errors.Is still matches any individual error.
// Every close failure is also counted. Failures from the idle-cleanup path are included: stopping
// the cleanup loop joins it, so the errors it recorded are complete by the time Close drains them.
func (p *Pool[V]) Close() error {
	var closeErrs []error

	p.closeOnce.Do(func() {
		// Flip closed BEFORE any teardown so concurrent GetOrCreate callers immediately see
		// ErrPoolClosed rather than racing against half-torn-down state.
		p.closed.Store(true)
		p.StopCleanup() // joins the loop: no cleanup-path p.closer is in flight past this point

		// Collect all entries under the lock, marking each closed so a concurrent lease release
		// cannot also close it (avoids a double Closer call). StopCleanup joined the cleanup loop
		// above, so every idle-cleanup close failure is already recorded — drain them into the same
		// set Close joins.
		p.mu.Lock()
		closeErrs = append(closeErrs, p.cleanupErrs...)
		p.cleanupErrs = nil
		var toClose []*entry[V]
		for key := range p.entries {
			if e := p.removeEntryLocked(key); e != nil {
				e.closed = true
				toClose = append(toClose, e)
			}
		}
		p.mu.Unlock()

		for _, e := range toClose {
			if err := p.closer(e.value); err != nil {
				p.incErrors()
				closeErrs = append(closeErrs, err)
			}
		}
	})

	return errors.Join(closeErrs...)
}

// Closed reports whether Close has been called.
func (p *Pool[V]) Closed() bool {
	return p.closed.Load()
}

// Size returns the current number of active entries.
func (p *Pool[V]) Size() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// Stats returns a point-in-time snapshot of the pool's counters.
func (p *Pool[V]) Stats() PoolStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PoolStats{
		Size:         len(p.entries),
		MaxSize:      p.maxSize,
		TotalCreated: p.totalCreated,
		Evictions:    p.evictions,
		IdleCleanups: p.idleCleanups,
		Errors:       p.errors,
		IdleTTL:      p.idleTTL,
	}
}

// Snapshot returns a point-in-time snapshot of every live entry (key + last-used), in
// unspecified order. Observability/Stats surfaces only — it takes no lease and does not touch LRU.
func (p *Pool[V]) Snapshot() []EntrySnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]EntrySnapshot, 0, len(p.entries))
	for key, e := range p.entries {
		out = append(out, EntrySnapshot{Key: key, LastUsed: e.lastUsed})
	}
	return out
}

// incErrors bumps the error counter. Must be called without mu held.
func (p *Pool[V]) incErrors() {
	p.mu.Lock()
	p.errors++
	p.mu.Unlock()
}
