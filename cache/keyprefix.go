package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/internal/cachekey"
)

// ErrInvalidKeyPrefix is returned by WithKeyPrefix when the prefix is not a usable
// namespace: whitespace, a glob metacharacter, a Redis Cluster hash tag, or a
// trailing separator. Callers can match it with errors.Is; the wrapped cause spells
// out which rule was broken.
var ErrInvalidKeyPrefix = errors.New("cache: invalid key prefix")

// WithKeyPrefix returns a view of c whose every key travels as <prefix>:<key>, so two
// services (or two tenants) sharing one Redis endpoint cannot read or overwrite each
// other's entries. It is connector-agnostic: the framework wraps whichever cache
// instance the connector produced, including a custom app.Options.CacheConnector.
//
// An empty prefix is the documented opt-out and returns c itself, unwrapped. A nil c
// returns ErrNilCache and an unusable prefix returns an error wrapping
// ErrInvalidKeyPrefix, both before any wrapper exists.
//
// Every error from c passes through untouched, so errors.Is(err, ErrNotFound) still
// holds through the view.
func WithKeyPrefix(c Cache, prefix string) (Cache, error) {
	if isNilValue(c) {
		return nil, ErrNilCache
	}
	if err := cachekey.Validate(prefix); err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrInvalidKeyPrefix, prefix, err)
	}
	if prefix == "" {
		return c, nil
	}
	return &prefixedCache{inner: c, prefix: prefix}, nil
}

// prefixedCache namespaces every key of the cache it wraps.
//
// inner is a NAMED field rather than an embedded one on purpose: embedding would
// promote any Cache method this type forgets to override, so a key-taking method added
// to Cache tomorrow would silently escape the namespace. Named, it fails to compile
// here instead.
type prefixedCache struct {
	inner  Cache
	prefix string
}

var (
	_ Cache               = (*prefixedCache)(nil)
	_ LoadTimeoutProvider = (*prefixedCache)(nil)
)

// key renders the wire key for a caller-supplied key.
func (p *prefixedCache) key(key string) string {
	return p.prefix + cachekey.Sep + key
}

// Get retrieves the value stored under the namespaced key.
func (p *prefixedCache) Get(ctx context.Context, key string) ([]byte, error) {
	return p.inner.Get(ctx, p.key(key))
}

// Set stores value under the namespaced key.
func (p *prefixedCache) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return p.inner.Set(ctx, p.key(key), value, ttl)
}

// Delete removes the namespaced key.
func (p *prefixedCache) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, p.key(key))
}

// GetOrSet runs the atomic get-or-set against the namespaced key.
func (p *prefixedCache) GetOrSet(ctx context.Context, key string, value []byte, ttl time.Duration) (storedValue []byte, wasSet bool, err error) {
	return p.inner.GetOrSet(ctx, p.key(key), value, ttl)
}

// CompareAndSet runs the atomic compare-and-set against the namespaced key.
func (p *prefixedCache) CompareAndSet(ctx context.Context, key string, expectedValue, newValue []byte, ttl time.Duration) (success bool, err error) {
	return p.inner.CompareAndSet(ctx, p.key(key), expectedValue, newValue, ttl)
}

// CompareAndDelete runs the atomic compare-and-delete against the namespaced key.
func (p *prefixedCache) CompareAndDelete(ctx context.Context, key string, expectedValue []byte) (deleted bool, err error) {
	return p.inner.CompareAndDelete(ctx, p.key(key), expectedValue)
}

// Health checks the wrapped cache; it takes no key, so nothing is namespaced.
func (p *prefixedCache) Health(ctx context.Context) error {
	return p.inner.Health(ctx)
}

// Stats returns the wrapped cache's statistics unchanged.
func (p *prefixedCache) Stats() (map[string]any, error) {
	return p.inner.Stats()
}

// Close closes the wrapped cache. The view owns no resources of its own.
func (p *prefixedCache) Close() error {
	return p.inner.Close()
}

// LoadTimeout forwards the wrapped cache's configured load-through bound, or 0 when it
// has none. Without this forward every LoadThrough through a prefixed cache would drop
// to the hand-written-cache fallback instead of the deployment's `cache.loadtimeout`.
func (p *prefixedCache) LoadTimeout() time.Duration {
	if provider, ok := p.inner.(LoadTimeoutProvider); ok {
		return provider.LoadTimeout()
	}
	return 0
}
