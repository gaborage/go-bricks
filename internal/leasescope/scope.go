// Package leasescope carries a unit-of-work "lease scope" through context.Context.
//
// Per-tenant resource managers (database, cache, messaging) hand out a long-lived,
// shared handle plus a release callback. The release must run when the borrowing unit
// of work (an HTTP request, an AMQP message, a scheduler job) finishes — not per call,
// because a handle may be borrowed across a transaction that spans many statements.
//
// A Scope collects the release callbacks acquired during one unit of work; the framework
// installs a Scope at the unit's boundary and calls ReleaseAll when it completes. Callers
// that have no Scope in their context (framework-internal probes, ad-hoc background work)
// fall back to releasing immediately via Register — identical to the pre-lease behavior,
// so those paths never leak and never change.
package leasescope

import (
	"context"
	"sync"
)

// scopeKey is the unexported context key under which a *Scope is stored, so no other
// package can collide with or overwrite the active scope.
type scopeKey struct{}

// Scope accumulates the release callbacks acquired during a single unit of work.
// The zero value is not usable; obtain a Scope via Install. A Scope is safe for
// concurrent use: handlers may acquire leases from multiple goroutines.
type Scope struct {
	mu       sync.Mutex
	releases []func() // nil until the first Add — a unit that borrows nothing allocates nothing
	drained  bool     // set by ReleaseAll; a later Add releases immediately (boundary passed)
}

// Add records a release callback to be invoked by ReleaseAll. Safe for concurrent use.
// If the scope has already been drained by ReleaseAll (the unit-of-work boundary has passed,
// e.g. a borrow on a value-inheriting detached context), the release is invoked immediately
// rather than appended to a slice that would never be drained — so a lease is never lost.
func (s *Scope) Add(release func()) {
	if release == nil {
		return
	}
	s.mu.Lock()
	if s.drained {
		s.mu.Unlock()
		release()
		return
	}
	s.releases = append(s.releases, release)
	s.mu.Unlock()
}

// ReleaseAll invokes every recorded release callback exactly once and marks the scope drained.
// It is idempotent: a second call (or a call on an empty scope) is a no-op. Each callback is
// itself expected to be idempotent, but ReleaseAll guarantees one invocation per lease.
func (s *Scope) ReleaseAll() {
	s.mu.Lock()
	releases := s.releases
	s.releases = nil
	s.drained = true
	s.mu.Unlock()

	for _, release := range releases {
		release()
	}
}

// Install returns a context carrying a fresh Scope plus the Scope itself, so the boundary
// can defer scope.ReleaseAll(). Child contexts derived from the returned context (e.g. a
// per-tenant context.WithValue) inherit the same Scope.
func Install(ctx context.Context) (context.Context, *Scope) {
	scope := &Scope{}
	return context.WithValue(ctx, scopeKey{}, scope), scope
}

// FromContext returns the Scope installed in ctx, if any.
func FromContext(ctx context.Context) (*Scope, bool) {
	scope, ok := ctx.Value(scopeKey{}).(*Scope)
	return scope, ok
}

// Register adds release to the Scope in ctx so it runs when the unit of work completes.
// When no Scope is present, it calls release immediately — the documented fallback for
// unscoped contexts (non-leaking, but unprotected against mid-use eviction).
func Register(ctx context.Context, release func()) {
	if release == nil {
		return
	}
	if scope, ok := FromContext(ctx); ok {
		scope.Add(release)
		return
	}
	release()
}
