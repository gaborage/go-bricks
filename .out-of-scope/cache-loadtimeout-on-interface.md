# `LoadTimeout()` on the `cache.Cache` interface

**Decision:** Deferred (YAGNI) — `LoadTimeout()` stays on the optional
`cache.LoadTimeoutProvider` interface and is not promoted onto `cache.Cache`
until a trigger below fires.

**Reason:** `cache.LoadThrough` bounds its cache legs with the deployment's
`cache.loadtimeout`, read from the resolved cache instance through the optional
`LoadTimeoutProvider` interface. A decorator that embeds `cache.Cache` promotes
only that interface's methods, so a wrapper that does not forward
`LoadTimeout()` silently drops the configured bound back to the 500 ms
fallback. Promoting the method onto `cache.Cache` removes the trap — and
breaks every consumer implementation and mock of the interface,
`cachetest.MockCache` included (apidiff treats an added interface method as
incompatible).

What makes that break speculative today:

- The framework has no production decorator over `cache.Cache`; the only
  wrapper in the tree is the test spy that pins the forwarding requirement.
- The Redis client — the only framework `Cache` — implements the provider, so
  every framework-resolved instance carries the bound.
- `cache.WithCacheTimeout` gives a caller a per-call override, so a wrapped
  cache is never stuck on the fallback.
- The forwarding requirement is documented in the cache wiki with the
  three-line method to add, and `TestLoadThroughWrapperMustForwardLoadTimeout`
  pins that an embedding wrapper does hide the method.

Paying a framework-wide interface break to close a gap no decorator has fallen
into is what breaking batches exist to avoid: when the interface breaks for
another reason, this goes in with it.

**Reopen when either fires:**

1. A consumer decorator over `cache.Cache` drops the configured bound in a real
   deployment — a report, an incident, or a decorator landing in the framework
   itself (instrumentation, tenant scoping, and the like).
2. `cache.Cache` gains or loses a method for another reason — batch the
   promotion into that break (ADR + migration atom + `fix(cache)!:`).

If promoted, the natural shape is `LoadTimeout() time.Duration` on
`cache.Cache` with the provider's "non-positive means not configured"
contract, `LoadTimeoutProvider` deleted, and `resolveCacheTimeout` calling the
method directly.

**Prior requests:**

- [#1326](https://github.com/gaborage/go-bricks/issues/1326) — closed
  2026-09-03 (deferred, this entry)
