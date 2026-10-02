// Package resourcepool implements the ADR-032 keyed pool of leasable,
// refcounted, LRU-capped, idle-evicted resources, backing the cache, database,
// and messaging managers.
//
// A pool hands each borrower a lease (via GetOrCreate) plus an idempotent
// ReleaseFunc. A resource evicted (LRU, idle, explicit Remove, or pool Close)
// while a lease is outstanding is detached immediately but its Closer runs only
// once the final lease is released, so an in-use resource is never closed under
// an active caller (the #606 race) — including across Close: after Close returns,
// a still-borrowed value remains usable until released. The Closer is ALWAYS
// invoked outside the pool lock.
package resourcepool

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// ErrPoolClosed is returned by GetOrCreate after Close has been called.
// Callers can errors.Is(err, ErrPoolClosed) to distinguish "pool is gone" from
// a per-resource creation failure. One deliberate exception: a caller already
// mid-GetOrCreate on a fresh entry Close could not close (another borrower holds
// it, or Remove detached it during its create) may still receive that live
// handle after Close returns; it closes exactly once, at its final release.
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

// maxAcquireAttempts bounds GetOrCreate's acquire loop. Every waiter of a create holds a seed
// lease reserved at install, so only Close can refuse a claim, and the next attempt then returns
// ErrPoolClosed: the loop never reaches this bound.
const maxAcquireAttempts = 4

// PoolStats is a point-in-time snapshot of the pool's counters. Consumers adapt
// it into their own stat shape (cache's typed ManagerStats, database/messaging's
// map[string]any).
type PoolStats struct {
	Size         int           // Current number of active entries
	MaxSize      int           // Maximum allowed active entries (0 = unlimited)
	TotalCreated int           // Total entries created since the pool started
	Evictions    int           // Total evictions due to LRU policy
	Removals     int           // Total explicit Remove calls that detached a cached entry or invalidated an in-flight create
	IdleCleanups int           // Total cleanups due to idle timeout
	Errors       int           // Total create failures (a recovered create panic included) and tracked close failures
	IdleTTL      time.Duration // Idle timeout duration
}

// EntrySnapshot is a point-in-time view of one pooled entry's identity and idle age, for
// managers that surface per-resource detail in Stats (e.g. DbManager's "connections" array).
type EntrySnapshot struct {
	Key      string
	LastUsed time.Time
}

// entry represents a pooled resource in the LRU.
// refs, seeds, detached, and closed are guarded by Pool.mu.
type entry[V any] struct {
	value    V
	key      string
	lastUsed time.Time
	element  *list.Element // Position in the LRU list

	// refs counts outstanding leases (current borrowers); an entry with refs > 0 is in use.
	refs int
	// seeds counts the refs that are unclaimed "seed" leases, one reserved at install for each
	// GetOrCreate caller waiting on the create. The seeds keep a brand-new entry alive through the
	// window before those callers claim, so a concurrent evict/Remove can only detach (never
	// close) it. Each waiter's claimSeed turns one seed into its lease.
	seeds int
	// detached marks an entry removed from the map+LRU whose Closer was deferred because a
	// lease was still outstanding.
	detached bool
	// closed guards against a double Closer call once the deferred close has run.
	closed bool
}

// liveLeases counts leases held by actual borrowers, discounting unclaimed seeds.
// Must be called with Pool.mu held.
func (e *entry[V]) liveLeases() int {
	return e.refs - e.seeds
}

// Pool is a keyed pool of leasable, refcounted, LRU-capped, idle-evicted
// resources. The zero value is not usable; construct with New.
type Pool[V any] struct {
	mu      sync.Mutex
	entries map[string]*entry[V]
	lru     *list.List
	// pending holds each key's in-flight create that new callers may still join (guarded by mu).
	pending map[string]*pendingCreate[V]

	maxSize int
	idleTTL time.Duration
	closer  Closer[V]

	// Statistics (guarded by mu).
	totalCreated int
	evictions    int
	removals     int
	idleCleanups int

	// generation invalidates creates that are ALREADY in flight: Remove bumps the key's value
	// while a create holds a captured one, so that create is delivered to its waiters but never
	// installed. An entry therefore exists only while inFlight[key] > 0 — with no create to
	// invalidate there is nothing to remember, and the last create to finish releases it. That
	// bounds the map by concurrent creates; a per-key ledger would instead grow with every
	// removed tenant or named connection, unbounded by maxSize.
	generation map[string]uint64
	// inFlight counts creates that have captured a generation but not yet finished installing,
	// including ones Remove already invalidated. Remove uses it to count Removals for an
	// in-flight-only invalidation.
	inFlight map[string]int

	// errors counts create failures and tracked close failures. Atomic so incErrors and
	// noteCleanupCloseErr can bump it without taking mu.
	errors atomic.Int64

	// cleanupErrs retains close failures from the idle-cleanup path (guarded by mu) so Close can
	// surface them through its errors.Join contract. They are only recoverable because StopCleanup
	// joins the loop: by the time Close drains this, the cleanup goroutine has finished recording.
	cleanupErrs []error

	// closed flips to true the moment Close begins. GetOrCreate reads it before leasing or
	// joining a create, so callers immediately see ErrPoolClosed instead of receiving a
	// handle to a resource that is about to be torn down. Atomic because Close sets it, and
	// Closed and StartCleanup read it, without mu.
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
		entries:    make(map[string]*entry[V]),
		lru:        list.New(),
		pending:    make(map[string]*pendingCreate[V]),
		generation: make(map[string]uint64),
		inFlight:   make(map[string]int),
		maxSize:    maxSize,
		idleTTL:    idleTTL,
		closer:     closer,
	}
}

// GetOrCreate returns the resource for key plus a ReleaseFunc the caller must
// invoke when finished with it for the current unit of work (typically
// deferred). It creates the resource via create on first use, collapsing
// concurrent creates for the same key into one shared create. Returns
// ErrPoolClosed if Close has been called — except a caller already
// mid-GetOrCreate on a fresh entry Close could not close (see ErrPoolClosed),
// who may still receive that live handle after Close returns; it closes
// exactly once, at its final release. On error the returned ReleaseFunc is nil — check err
// first. A panic inside create is recovered and returned as an error naming
// only the panic value's type (ADR-081), leaving the pool usable.
func (p *Pool[V]) GetOrCreate(ctx context.Context, key string, create func(context.Context) (V, error)) (V, ReleaseFunc, error) {
	var zero V
	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		e, c, err := p.leaseOrJoin(ctx, key, create)
		if err != nil {
			return zero, nil, err
		}
		if c != nil {
			if e, err = p.await(ctx, c); err != nil {
				return zero, nil, err
			}
			if !p.claimSeed(e) {
				// Only Close refuses a reserved seed, so the next attempt returns ErrPoolClosed.
				continue
			}
		}
		return e.value, p.makeRelease(e), nil
	}

	return zero, nil, fmt.Errorf("resourcepool: failed to acquire %q after %d attempts (pool churn)", key, maxAcquireAttempts)
}

// pendingCreate is one in-flight create for a key, shared by every GetOrCreate caller that
// arrives before it finishes. The pool coalesces creates itself, rather than through
// singleflight, because the install must know how many callers wait on it: it reserves one seed
// lease per waiter, so no waiter can find the entry closed before it claims (ADR-032). gen and
// waiters are guarded by Pool.mu; e and err are written under Pool.mu before done closes, and
// read after it.
type pendingCreate[V any] struct {
	done    chan struct{}
	gen     uint64 // the key's generation when the create began; a Remove that moves it detaches the result
	waiters int    // callers still waiting; the install reserves one seed lease for each
	e       *entry[V]
	err     error
}

// leaseOrJoin leases the cached entry for key, or joins the key's in-flight create, starting one
// when none is running. Both happen under ONE lock acquisition: a gap between the cache miss and
// the join would let a create install in between, and this caller would start a second create
// for a cached key. Joining counts the caller as a waiter, which is its seed reservation.
func (p *Pool[V]) leaseOrJoin(ctx context.Context, key string, create func(context.Context) (V, error)) (*entry[V], *pendingCreate[V], error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Checked under the lock, so a caller arriving after Close never joins or starts a create.
	if p.closed.Load() {
		return nil, nil, ErrPoolClosed
	}

	if e, ok := p.entries[key]; ok {
		p.lru.MoveToFront(e.element)
		e.lastUsed = time.Now()
		e.refs++
		return e, nil, nil
	}

	c := p.pending[key]
	if c == nil {
		c = p.beginCreateLocked(key)
		go p.createEntry(ctx, key, c, create)
	}
	c.waiters++
	return nil, c, nil
}

// await waits for c on the caller's OWN context, so a caller whose budget is spent stops waiting
// without canceling the create, which still installs for everyone else. A caller that gives up
// withdraws its reservation under the lock the install reads it under, so an abandoned caller
// never leaves a seed that would pin the entry open; one that loses that race to the install
// takes the result it was reserved, as if its wait had ended first.
func (p *Pool[V]) await(ctx context.Context, c *pendingCreate[V]) (*entry[V], error) {
	select {
	case <-c.done:
		return c.e, c.err
	case <-ctx.Done():
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-c.done:
		return c.e, c.err
	default:
		c.waiters-- // not installed yet: no seed will be reserved for this caller
		return nil, ctx.Err()
	}
}

// claimSeed turns one of e's reserved seed leases into the caller's lease. It returns false only
// when the entry is already closed, which a reserved seed leaves to Close alone: every other close
// path waits for refs, seeds included, to reach zero.
func (p *Pool[V]) claimSeed(e *entry[V]) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e.closed {
		return false
	}
	e.seeds--
	return true
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

// callCreate runs the consumer's create function and converts a panic into an error, so the
// rest of createEntry sees one ordinary failure and hands it to every waiter.
//
// A panic must not escape: create runs on the pool's own goroutine (see createEntry), where no
// recover — including Echo's middleware.Recover — can catch it, so one consumer-supplied
// factory's panic would kill the process instead of failing the callers waiting on that create.
// The guard wraps THIS call and nothing else: a panic anywhere later in createEntry happens after
// the entry is installed with its waiters' seed leases, and converting that one to an error would
// return a failure to waiters who never claim their seeds, leaving an entry pinned at refs >= 1
// that eviction can detach but never close.
//
// The value is rendered by TYPE only, never by value (ADR-081). `completed` is what separates a
// normal return from a panic rather than a non-nil recover(): under GODEBUG=panicnil=1 a
// `panic(nil)` recovers as nil, and reading the recovered value alone would let that panic
// through as a nil error beside a zero resource, which createEntry would then install.
func callCreate[V any](ctx context.Context, key string, create func(context.Context) (V, error)) (v V, err error) {
	completed := false
	defer func() {
		if completed {
			return
		}
		r := recover()
		var zero V
		v, err = zero, fmt.Errorf("resourcepool: panic during create for key %q (type: %T)", key, r)
	}()
	v, err = create(ctx)
	completed = true
	return v, err
}

// createEntry runs c's create and finishes c: it installs the value with one seed lease per
// waiter (see installCreated), or hands every waiter the create's error. It runs on its own
// goroutine, which Close does not join: creation carries no bound of its own, so joining would let
// one slow dial hold up the whole shutdown.
//
// create runs on a context DERIVED from the initiating caller's: values and any deadline carry
// over, but cancellation is severed, so one collapsed caller's early cancel cannot fail the SHARED
// create for every waiter (or for the future callers an installed entry is meant to serve). The
// stock connectors never read this context — each self-bounds its dial — so the carried deadline
// matters to the ctx-aware consumer seams (a dynamic ResourceSource, a custom cache Connector),
// where it is the only bound a create has: a caller without a deadline yields a create nothing
// in-framework can cancel. The derived context is call-scoped — a create must not retain it.
// Whether creation should instead carry its own bound (a per-pool CreateTimeout, leaving the
// startup budgets a separate seam) is deliberately deferred.
func (p *Pool[V]) createEntry(ctx context.Context, key string, c *pendingCreate[V], create func(context.Context) (V, error)) {
	createCtx := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		createCtx, cancel = context.WithDeadline(createCtx, deadline)
		defer cancel()
	}

	// callCreate converts a panic, but runtime.Goexit inside create (a test factory calling
	// t.FailNow, say) unwinds past it. Without this, c would never finish: its waiters would block
	// until their own contexts end, and every later caller for key would join the dead create.
	returned := false
	defer func() {
		if !returned {
			p.failCreate(key, c, fmt.Errorf("resourcepool: create for key %q exited without returning", key))
		}
	}()
	value, err := callCreate(createCtx, key, create)
	returned = true
	if err != nil {
		p.failCreate(key, c, err)
		return
	}

	p.installCreated(key, c, value)
}

// failCreate ends c with err for every waiter. The failure is counted once, here, not once per
// waiter.
func (p *Pool[V]) failCreate(key string, c *pendingCreate[V], err error) {
	p.incErrors()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.endCreateLocked(key, c)
	c.err = err
	close(c.done)
}

// beginCreateLocked registers a new in-flight create for key, capturing the generation installCreated
// compares. Must be called with mu held, and paired with endCreateLocked even when create fails, or
// Remove would keep counting an in-flight invalidation that already finished.
func (p *Pool[V]) beginCreateLocked(key string) *pendingCreate[V] {
	c := &pendingCreate[V]{done: make(chan struct{}), gen: p.generation[key]}
	p.pending[key] = c
	p.inFlight[key]++
	return c
}

// endCreateLocked stops new callers joining c and decrements inFlight[key], deleting the entry at
// zero and releasing the key's generation with it: the last create to finish is the last one that
// could compare against it, so keeping it would only grow the map. Must be called with mu held,
// and AFTER the caller has read the generation it compares (see installCreated).
func (p *Pool[V]) endCreateLocked(key string, c *pendingCreate[V]) {
	if p.pending[key] == c {
		delete(p.pending, key) // Remove may already have dropped c and let a newer create start
	}
	n := p.inFlight[key] - 1
	if n <= 0 {
		delete(p.inFlight, key)
		delete(p.generation, key)
		return
	}
	p.inFlight[key] = n
}

// installCreated places a successfully created value into the pool, or marks it detached-at-birth
// when Remove moved the key's generation during create. Either way the entry carries one seed lease
// per waiter still on c (refs == seeds == waiters), so evict, Remove and idle cleanup can only
// detach it, never close it, before every waiter has claimed. The in-flight count is dropped under
// the same lock as the generation check so Remove cannot observe a torn "still in flight / already
// installed" state. A closed pool still closes the orphaned instance and fails every waiter with
// ErrPoolClosed.
//
// That is what makes credential rotation safe: a dial that started under the old config is
// delivered to its waiters but never cached, and closes at their final release.
func (p *Pool[V]) installCreated(key string, c *pendingCreate[V], value V) {
	p.mu.Lock()
	// Read the generation BEFORE endCreateLocked: this create may be the last one in flight, and
	// ending it releases the key's entry. Reading after would see the fresh zero value and make a
	// create Remove invalidated under generation 0 look valid again.
	detached := p.generation[key] != c.gen
	p.endCreateLocked(key, c)
	if p.closed.Load() {
		c.err = ErrPoolClosed
		close(c.done)
		p.mu.Unlock()
		_ = p.closer(value) // orphaned instance — close is best-effort, not counted
		return
	}

	e := &entry[V]{
		value:    value,
		key:      key,
		lastUsed: time.Now(),
		refs:     c.waiters,
		seeds:    c.waiters,
		detached: detached,
	}
	p.totalCreated++
	c.e = e
	var evicted *entry[V]
	// Detached with every waiter gone: no release will ever come, so it closes now.
	closeNow := detached && c.waiters == 0
	if closeNow {
		e.closed = true
	} else if !detached {
		evicted = p.evictIfNeeded()
		e.element = p.lru.PushFront(e)
		p.entries[key] = e
	}
	close(c.done)
	p.mu.Unlock()

	if closeNow {
		if err := p.closer(value); err != nil {
			p.incErrors()
		}
	}
	// Close the evicted resource outside the lock (eviction close failures are not counted).
	if evicted != nil {
		_ = p.closer(evicted.value)
	}
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

	// createEntry is the list's only writer and always pushes *entry[V], so ok is
	// discarded rather than guarded with a branch no test can reach.
	e, _ := oldest.Value.(*entry[V])

	p.removeEntryLocked(e.key)
	p.evictions++

	if e.refs > 0 {
		return nil // still leased — defer the close to the final lease release
	}
	e.closed = true
	return e
}

// Remove detaches the entry for key from the pool and invalidates any create still in flight
// for that key. If a cached entry exists and is unleased, it marks the entry closed and returns
// (value, true) for the caller to close OUTSIDE the pool. If it is still leased, the close is
// deferred to the final lease release and Remove returns (zero, false). A missing key with no
// in-flight create returns (zero, false). A create that captured the key's generation before this
// call still delivers its value to every waiter of that create, but the entry is marked detached
// and never cached, so the next GetOrCreate runs create again. Every Remove that detaches a cached
// entry or invalidates an in-flight create counts toward PoolStats.Removals; a no-op Remove does
// not.
func (p *Pool[V]) Remove(key string) (v V, shouldClose bool) {
	var zero V
	p.mu.Lock()
	e := p.removeEntryLocked(key)
	inFlight := p.inFlight[key] > 0
	if e == nil && !inFlight {
		p.mu.Unlock()
		return zero, false
	}
	if inFlight {
		// Only a create that already captured a generation can be invalidated by bumping it. With
		// none in flight, detaching the cached entry above IS the whole invalidation, and a stored
		// generation would never be read again — it would just occupy the map forever. Dropping
		// the pending create stops a GetOrCreate that starts after this Remove from joining it and
		// receiving a handle built from pre-removal config; the next caller starts a fresh create.
		p.generation[key]++
		delete(p.pending, key)
	}
	p.removals++
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

// RecordCloseError counts a close failure toward PoolStats.Errors for a value the caller closed
// itself after Remove handed it back, so that close is counted as a pool-run close would be.
func (p *Pool[V]) RecordCloseError() {
	p.incErrors()
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
// a statistic. Must be called without mu held: the counter is atomic, but the cleanupErrs append
// still takes mu.
func (p *Pool[V]) noteCleanupCloseErr(err error) {
	p.errors.Add(1)

	p.mu.Lock()
	p.cleanupErrs = append(p.cleanupErrs, err)
	p.mu.Unlock()
}

// Close shuts down all pooled resources, stops the cleanup loop, and makes subsequent
// GetOrCreate calls return ErrPoolClosed. It is idempotent and returns every close error it
// triggers itself, joined via errors.Join (nil if none) — so consumers whose Close contract
// aggregates all failures (e.g. DbManager) can surface them all, while errors.Is still matches
// any individual error. Every close failure is also counted. Failures from the idle-cleanup path
// are included: stopping the cleanup loop joins it, so the errors it recorded are complete by the
// time Close drains them. An entry still borrowed when Close runs is left open — its final release
// closes it (#606) — so its close error, if any, is NOT in this return value; it surfaces later in
// Stats().Errors instead.
func (p *Pool[V]) Close() error {
	var closeErrs []error

	p.closeOnce.Do(func() {
		// Flip closed BEFORE any teardown so concurrent GetOrCreate callers immediately see
		// ErrPoolClosed rather than racing against half-torn-down state.
		p.closed.Store(true)
		p.StopCleanup() // joins the loop: no cleanup-path p.closer is in flight past this point

		// Collect all entries under the lock. An entry with no live borrower is marked closed
		// here so a concurrent lease release cannot double-close it; a still-borrowed entry is
		// left detached-but-open on purpose — its final releaseEntry closes it exactly once.
		// StopCleanup joined the cleanup loop above, so every idle-cleanup close failure is
		// already recorded — drain them into the same set Close joins.
		p.mu.Lock()
		closeErrs = append(closeErrs, p.cleanupErrs...)
		p.cleanupErrs = nil
		var toClose []*entry[V]
		for key := range p.entries {
			e := p.removeEntryLocked(key)
			if e == nil {
				continue
			}
			if e.liveLeases() > 0 {
				continue // still borrowed — the final releaseEntry closes it (#606)
			}
			e.closed = true
			toClose = append(toClose, e)
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
		Removals:     p.removals,
		IdleCleanups: p.idleCleanups,
		Errors:       int(p.errors.Load()),
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

// incErrors bumps the error counter. Safe to call with or without mu held: the
// counter is atomic and takes no lock of its own.
func (p *Pool[V]) incErrors() {
	p.errors.Add(1)
}
