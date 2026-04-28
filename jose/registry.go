package jose

import (
	"reflect"
	"sync"
)

// PolicyRegistry caches scanned + resolved Policies keyed by reflect.Type. Entries are
// populated once at route registration time and read concurrently per request.
//
// Mirrors the pattern in database/internal/columns/registry.go:24,89 (sync.Map +
// LoadOrStore) which is proven thread-safe and lock-free on the hot path.
//
// The cache stores nil to mean "this type was scanned and has no JOSE policy" — distinct
// from "this type has not been scanned yet" (cache miss). This avoids re-scanning untagged
// types on every request, which would otherwise dominate the request hot path for
// non-JOSE routes.
type PolicyRegistry struct {
	cache sync.Map // map[reflect.Type]*policyEntry
}

type policyEntry struct {
	policy *Policy
}

func NewPolicyRegistry() *PolicyRegistry {
	return &PolicyRegistry{}
}

// LoadOrScan returns the cached Policy for t (which may be nil if t has no jose tag), or
// scans t with the given direction and caches the result. If scan fails, returns the
// error without caching (so a subsequent fix can retry).
func (r *PolicyRegistry) LoadOrScan(t reflect.Type, dir Direction) (*Policy, error) {
	if v, ok := r.cache.Load(t); ok {
		return v.(*policyEntry).policy, nil
	}
	policy, err := ScanType(t, dir)
	if err != nil {
		return nil, err
	}
	actual, _ := r.cache.LoadOrStore(t, &policyEntry{policy: policy})
	return actual.(*policyEntry).policy, nil
}

// Store explicitly caches a (possibly resolved) policy for t. Used when the registration
// site has already done extra validation (kid resolution) and wants to memoize the result.
func (r *PolicyRegistry) Store(t reflect.Type, p *Policy) {
	r.cache.Store(t, &policyEntry{policy: p})
}
