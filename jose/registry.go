package jose

import (
	"reflect"
	"sync"
)

// PolicyRegistry caches scanned + resolved Policies keyed by (reflect.Type, Direction).
// Including direction is essential: the same struct type can legitimately be scanned for
// both DirectionInbound and DirectionOutbound (e.g., a shared envelope used as both a
// request and response on different routes), and the resulting policies have different
// required keys. A type-only key would return the wrong policy on the second scan.
//
// Mirrors the pattern in database/internal/columns/registry.go:24,89 (sync.Map +
// LoadOrStore) which is proven thread-safe and lock-free on the hot path.
//
// The cache stores nil to mean "this type was scanned and has no JOSE policy" — distinct
// from "this type has not been scanned yet" (cache miss). This avoids re-scanning untagged
// types on every request, which would otherwise dominate the request hot path for
// non-JOSE routes.
type PolicyRegistry struct {
	cache sync.Map // map[cacheKey]*policyEntry
}

type cacheKey struct {
	t   reflect.Type
	dir Direction
}

type policyEntry struct {
	policy *Policy
}

func NewPolicyRegistry() *PolicyRegistry {
	return &PolicyRegistry{}
}

// LoadOrScan returns the cached Policy for (t, dir), or scans t with the given direction
// and caches the result. If scan fails, returns the error without caching.
func (r *PolicyRegistry) LoadOrScan(t reflect.Type, dir Direction) (*Policy, error) {
	key := cacheKey{t: t, dir: dir}
	if v, ok := r.cache.Load(key); ok {
		return v.(*policyEntry).policy, nil
	}
	policy, err := ScanType(t, dir)
	if err != nil {
		return nil, err
	}
	actual, _ := r.cache.LoadOrStore(key, &policyEntry{policy: policy})
	return actual.(*policyEntry).policy, nil
}

// Store explicitly caches a (possibly resolved) policy for (t, dir). Used when the
// registration site has already done extra validation (kid resolution) and wants to
// memoize the result.
func (r *PolicyRegistry) Store(t reflect.Type, p *Policy) {
	dir := DirectionInbound
	if p != nil {
		dir = p.Direction
	}
	r.cache.Store(cacheKey{t: t, dir: dir}, &policyEntry{policy: p})
}
