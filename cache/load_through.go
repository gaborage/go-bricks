package cache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/cache/internal/tracking"
)

// fallbackCacheLegTimeout bounds a cache leg only when the deployment cannot say: the
// caller passed no WithCacheTimeout AND the Cache does not implement LoadTimeoutProvider.
// Every framework cache carries `cache.loadtimeout` (default 500ms) and therefore never
// reaches this; it exists so a hand-written Cache cannot produce an unbounded leg.
// Deliberately unexported: the configurable bound is the supported surface.
const fallbackCacheLegTimeout = 500 * time.Millisecond

// LoadTimeoutProvider is implemented by a Cache that knows its deployment's
// `cache.loadtimeout`. The framework's Redis client implements it, so the bound resolves
// per cache instance and a per-tenant override is honoured without any global state.
type LoadTimeoutProvider interface {
	// LoadTimeout returns the cache-leg bound; a non-positive value means "not configured".
	LoadTimeout() time.Duration
}

var (
	// ErrInvalidCacheTimeout is returned by LoadThrough when WithCacheTimeout was given a
	// non-positive duration.
	ErrInvalidCacheTimeout = errors.New("cache: load-through cache timeout must be positive")

	// ErrNilCache is returned by LoadThrough when the Cache is nil, including a typed nil
	// behind the interface.
	ErrNilCache = errors.New("cache: load-through requires a non-nil cache")
)

// Loader produces the value for a key from the origin when the cache cannot serve it. It
// receives the LoadThrough caller's context untouched.
type Loader[T any] func(ctx context.Context) (T, error)

// LoadOption tunes a LoadThrough call.
type LoadOption func(*loadConfig)

type loadConfig struct {
	cacheTimeout    time.Duration
	cacheTimeoutSet bool
}

// WithCacheTimeout bounds each cache leg of a LoadThrough call, overriding the deployment's
// `cache.loadtimeout` for this call. Keep it well under the request budget: a slow cache
// spends at most this much of the caller's deadline before the origin is consulted.
func WithCacheTimeout(d time.Duration) LoadOption {
	return func(cfg *loadConfig) { cfg.cacheTimeout, cfg.cacheTimeoutSet = d, true }
}

// resolveCacheTimeout picks the cache-leg bound: an explicit WithCacheTimeout wins, then
// the cache instance's configured `cache.loadtimeout`, then the fallback.
func resolveCacheTimeout(c Cache, opt time.Duration) time.Duration {
	if opt > 0 {
		return opt
	}
	if p, ok := c.(LoadTimeoutProvider); ok {
		if d := p.LoadTimeout(); d > 0 {
			return d
		}
	}
	return fallbackCacheLegTimeout
}

// LoadThrough serves key from c, loading it from the origin on a miss and writing the
// loaded value back under ttl. It is the read-through path: the cache legs run under a
// timeout derived from ctx and every cache-side failure degrades to the origin, the loader
// receives ctx untouched, the write-back is detached from the caller's cancellation and
// never stores a nil result, and concurrent misses for one cache instance, key and T
// collapse into a single loader call whose live followers refill rather than inherit the
// leader's cancellation. A loader panic becomes an error naming only the panic value's
// type (ADR-081); a loaded value that cannot be CBOR-encoded fails the call with the
// Marshal error. A nil c returns ErrNilCache, ttl < 0 returns ErrInvalidTTL and a
// non-positive WithCacheTimeout returns ErrInvalidCacheTimeout, all before any cache or
// origin call.
//
// The bound comes from the deployment's `cache.loadtimeout` (500ms by default), carried on
// the resolved cache instance so per-tenant values are honoured; WithCacheTimeout overrides
// it per call.
//
// See wiki/cache.md#load-through-reads for the full contract and the cache.fill.duration
// metric each fill records.
func LoadThrough[T any](ctx context.Context, c Cache, key string, ttl time.Duration, load Loader[T], opts ...LoadOption) (T, error) {
	var zero T
	var cfg loadConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.cacheTimeoutSet && cfg.cacheTimeout <= 0 {
		return zero, ErrInvalidCacheTimeout
	}
	if ttl < 0 {
		return zero, ErrInvalidTTL
	}
	if isNilValue(c) {
		return zero, ErrNilCache
	}
	timeout := resolveCacheTimeout(c, cfg.cacheTimeout)

	if v, ok := lookup[T](ctx, c, key, timeout); ok {
		return v, nil
	}

	start := time.Now()
	v, role, err := fill(ctx, c, key, ttl, load, timeout)
	tracking.RecordCacheFill(ctx, role, time.Since(start), err)
	return v, err
}

// lookup runs the bounded cache leg and reports whether it produced a usable value.
func lookup[T any](ctx context.Context, c Cache, key string, timeout time.Duration) (T, bool) {
	var zero T
	cacheCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := c.Get(cacheCtx, key)
	if err != nil {
		return zero, false
	}
	v, err := Unmarshal[T](data)
	if err != nil {
		return zero, false
	}
	return v, true
}

// flight is one in-progress origin load, shared by every concurrent LoadThrough call for
// the same cache instance, key and value type. It is not x/sync/singleflight because the
// leader must know it leads before waiting: it runs the loader on its own goroutine with
// the caller's context and waits for it unconditionally, while followers wait on theirs.
type flight[T any] struct {
	done chan struct{}
	val  T
	err  error
	// leaderCtxDone records that the leader's own context was done when the load
	// returned, so a live follower can tell the leader's cancellation from an origin
	// failure it should share.
	leaderCtxDone bool
}

// flightID scopes collapsing to one cache instance — the same key on two tenants' caches
// must never share a load — and to one value type. A zero cache identity means the Cache
// has no pointer identity to scope by, so its callers load for themselves.
type flightID struct {
	cache uintptr
	typ   reflect.Type
	key   string
}

var (
	flightsMu sync.Mutex
	// flights holds only in-progress loads; the leader removes its entry before waking
	// followers, so a retry always starts or joins a fresh flight.
	flights = map[flightID]any{}
)

func flightKey[T any](c Cache, key string) flightID {
	id := flightID{typ: reflect.TypeFor[T](), key: key}
	if rv := reflect.ValueOf(c); rv.Kind() == reflect.Pointer {
		id.cache = rv.Pointer()
	}
	return id
}

// joinOrStart returns the flight for id and whether this caller leads it, or nil when id
// carries no cache identity.
func joinOrStart[T any](id flightID) (f *flight[T], leader bool) {
	if id.cache == 0 {
		return nil, false
	}
	flightsMu.Lock()
	defer flightsMu.Unlock()
	if existing, ok := flights[id]; ok {
		f, _ = existing.(*flight[T])
		return f, false
	}
	f = &flight[T]{done: make(chan struct{})}
	flights[id] = f
	return f, true
}

// settle publishes the leader's outcome and wakes the followers.
func (f *flight[T]) settle(id flightID, v T, err error, leaderCtxDone bool) {
	f.val, f.err, f.leaderCtxDone = v, err, leaderCtxDone
	flightsMu.Lock()
	delete(flights, id)
	flightsMu.Unlock()
	close(f.done)
}

// fill loads key from the origin, collapsing with concurrent callers when the cache has an
// identity, and reports the role this caller played.
func fill[T any](ctx context.Context, c Cache, key string, ttl time.Duration, load Loader[T], timeout time.Duration) (v T, role string, err error) {
	id := flightKey[T](c, key)
	for {
		f, leader := joinOrStart[T](id)
		if leader || f == nil {
			v, err = lead(ctx, c, key, ttl, load, timeout)
			if f != nil {
				f.settle(id, v, err, ctx.Err() != nil)
			}
			return v, tracking.FillLeader, err
		}
		select {
		case <-f.done:
			if f.err != nil && f.leaderCtxDone && ctx.Err() == nil {
				continue
			}
			return f.val, tracking.FillFollower, f.err
		case <-ctx.Done():
			return v, tracking.FillFollower, ctx.Err()
		}
	}
}

// lead runs the loader for this caller and schedules the write-back of a storable result.
// The value is encoded here, before it is returned: the caller owns v from then on, so the
// detached goroutine must not read it.
func lead[T any](ctx context.Context, c Cache, key string, ttl time.Duration, load Loader[T], timeout time.Duration) (T, error) {
	v, err := runLoader(ctx, key, load)
	if err != nil || isNilValue(v) {
		return v, err
	}
	data, err := Marshal(v)
	if err != nil {
		var zero T
		return zero, err
	}
	go writeBack(ctx, c, key, data, ttl, timeout)
	return v, nil
}

// runLoader converts a loader panic into an error that names only the panic value's type
// (ADR-081). completed, not the recovered value, separates a normal return from a panic:
// under GODEBUG=panicnil=1 a panic(nil) recovers as nil.
func runLoader[T any](ctx context.Context, key string, load Loader[T]) (v T, err error) {
	completed := false
	defer func() {
		if completed {
			return
		}
		r := recover()
		var zero T
		v, err = zero, fmt.Errorf("cache: load-through loader panicked for key %q (type: %T)", key, r)
	}()
	v, err = load(ctx)
	completed = true
	return v, err
}

// writeBack stores data on a context detached from the caller's cancellation and bounded
// by timeout. A Set failure is dropped: the cache client's own operation metrics carry it.
func writeBack(ctx context.Context, c Cache, key string, data []byte, ttl, timeout time.Duration) {
	wbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	_ = c.Set(wbCtx, key, data, ttl)
}

// isNilKind reports whether k is a kind whose zero value is nil.
func isNilKind(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return true
	default:
		return false
	}
}

// isNilValue reports whether v is a nil pointer, map, slice, channel or func, or an
// interface holding nothing or one of those nils. Value kinds never reach reflect.ValueOf,
// which would box v.
func isNilValue[T any](v T) bool {
	if kind := reflect.TypeFor[T]().Kind(); kind != reflect.Interface && !isNilKind(kind) {
		return false
	}
	rv := reflect.ValueOf(v)
	return !rv.IsValid() || (isNilKind(rv.Kind()) && rv.IsNil())
}
