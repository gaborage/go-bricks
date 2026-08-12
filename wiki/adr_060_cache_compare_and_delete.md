# ADR-060: `CompareAndDelete` gives the cache interface a safe conditional release

- **Status**: Accepted
- **Date**: 2026-08-12
- **Related**: [ADR-011](adr_011_redis_cache.md) (introduced the cache interface and the
  unconditional-`Delete` lock example this supersedes), [migrations.md](migrations.md) `[C59.3]`

## Context

`cache.Cache` has documented `CompareAndSet` as the distributed-locking primitive since
[ADR-011](adr_011_redis_cache.md), but offered no way to release a lock safely. The only release was
`Delete`, which is unconditional, so the canonical shape was unsound:

```go
acquired, _ := c.CompareAndSet(ctx, lockKey, nil, workerID, 30*time.Second)
// ... work ...
defer c.Delete(ctx, lockKey)   // clears whoever holds the lock NOW, not necessarily us
```

If the work outruns the TTL, the lock expires, a second worker acquires it, and the first worker's
deferred `Delete` clears the **second** worker's lock. Two workers then run concurrently under a
lock that reported success to both. The interface's own godoc admitted this and told callers to live
with it — keep the TTL above worst-case duration, treat the lock as contention reduction rather than
mutual exclusion. That sentence was the honest description of a missing method, not a design.

**Two independent reporters asked for the same method.**
[#823](https://github.com/gaborage/go-bricks/issues/823) asks from the locking side.
[#966](https://github.com/gaborage/go-bricks/issues/966) item 2 asks from conditional **eviction**: a
read-through fill that must be droppable if an invalidation lands while it is loading.
`CompareAndSet` covers that reporter's write side, but their cleanup path had to `Delete`
unconditionally and so could remove a different writer's freshly-written value. Their recorded
workaround — CAS to a tombstone the next reader fails to decode — trades a clean absent key for a
decode-failure path, which is worse than the gap.

**It could not be emulated.** `casScript` has no `DEL` branch; every path is a `SET`. The natural
emulation `CompareAndSet(ctx, lockKey, workerID, nil, 0)` is — after #830 made the mode selector
explicit — an unambiguous real CAS that matches and writes an empty string, which still **occupies**
the key rather than releasing it, and does so with no expiry.

## Decision

Add one method to `cache.Cache`:

```go
CompareAndDelete(ctx context.Context, key string, expectedValue []byte) (deleted bool, err error)
```

It removes the key only while `expectedValue` is what is stored. Every case mirrors
`CompareAndSet`'s analogous one: a failed comparison is a `false` bool and never an error, a closed
client returns `cache.ErrClosed`, and a Redis failure returns `cache.NewOperationError`.

### A sibling Lua script, not a third mode on `casScript`

This departs from plan 071's recorded guidance (shipped as #830), which said #823 "should reuse the
mode-flag script pattern — same ARGV layout, one more mode value", and that this was the reason the
mode is a string rather than a boolean.

`cadScript` is written as a separate single-purpose script instead. The delete path has no
`new_value` and no TTL, so folding it into `casScript` would add two permanently-ignored ARGV slots
plus a branch a future reader must prove ignores them. A single-purpose script needs **no mode
discriminator at all**, which is the stronger reading of what #830 actually fixed: #830's defect was
the *script* re-deriving a mode from a rendered value's shape, and a script with one behavior cannot
have that class of bug. Issue #823's own proposal also asked for a sibling script.

```lua
local key = KEYS[1]
local expected = ARGV[1]

local current = redis.call('GET', key)
if current == expected then
	return redis.call('DEL', key)
end

return 0
```

It returns `DEL`'s own count rather than a literal `1`, which makes the correctness argument
independent of any claim about mid-script expiry: if the key were somehow gone by the `DEL` it
returns 0, the method returns false, and false is the truth.

### `nil` expected is rejected in Go, before any round trip

`CompareAndSet` gives `nil` a meaning (acquire-if-absent). A delete has no analogous mode, and
"delete unconditionally" already exists as `Delete`. So a nil `expectedValue` returns the new
`cache.ErrNilExpectedValue` sentinel — no existing sentinel fit, and `ErrCASFailed` is explicitly a
comparison result rather than a caller-input rejection.

This cannot be left to prose or to the script: go-redis writes a nil `[]byte` as a zero-length bulk
string, so a nil that slipped past the Go guard would silently compare against `""` and delete a key
holding the empty string — #830's defect in mirror image, inside the method built to close #823. An
empty slice `[]byte{}` remains a genuine comparison against the empty string, exactly as
`CompareAndSet` treats it.

The guard sits **before** the `start := time.Now()` timestamp, matching how `ttl < 0` is handled in
`CompareAndSet`. The trade-off is deliberate and worth naming: a caller bug emits no metric and so
stays invisible in telemetry. That is the existing convention in this file; following it beat
inventing a third placement.

### Rejected alternatives

- **A third mode on `casScript`** — reasons above. The recorded guidance was written before the
  delete path's ARGV shape was examined; two dead slots and a branch that must be proven inert are a
  worse outcome than a second short script.
- **An optional interface discovered by type assertion** (`interface{ CompareAndDelete(...) }`,
  falling back when absent). Rejected because the fallback *is* the hazard: a cache not implementing
  it sends callers back to unconditional `Delete`, now reached silently rather than visibly. An
  interface addition breaks compilation loudly, which is the correct failure for this change.
  The fail-closed variant — type-assert and return an error when the capability is absent — is
  rejected too: `cache.Cache` is the only type consumers ever receive (`deps.Cache(ctx)`,
  `CacheManager.Get`), so an optional interface moves the capability check into every business-code
  release site instead of resolving it once in the framework, and the manifesto's position is that
  breaking GoBricks' own surface is fine when documented, not shimmed.
- **Naming the result `success`.** Rejected in favour of `deleted` — see below.

### `deleted`, not `success`

Returning `DEL`'s count has a cost that must be documented: `false` no longer means only "the value
was not mine". Compare-matched-then-key-vanished also yields false. That is a real divergence from
`CompareAndSet`, where `false` unambiguously means the comparison failed. Hence the name, and hence
**no test asserts "false implies another holder's value is still present."**

### Two caller hazards the contract carries

Both are new, and both make the *fix* dangerous if undocumented.

1. **A `CompareAndDelete`-released lock requires a positive TTL.** `CompareAndSet` legally acquires
   with `ttl == 0` (only `ttl < 0` is rejected; ttl 0 is documented as "stored without expiration").
   Today `defer cache.Delete(...)` frees such a key unconditionally. A token-verified release that
   returns false on any token drift leaves that key held forever, with **no expiry to recover it**.
   Every rewritten example uses a bounded, positive TTL.
2. **`false` and any error are BOTH terminal.** The natural caller reflex — `if err != nil || !ok {
   _ = c.Delete(ctx, lockKey) }` — reinstates the #823 hazard behind an API that reads as safe,
   which is strictly worse than the old state where the danger was visible in the call. `false` is
   also a new observable signal ("I no longer hold it") that invites compensation mid-work, but a
   client-side timeout on a `DEL` that actually landed is indistinguishable from "another holder owns
   it". So: never fall back to `Delete`, never retry the release with it, and treat `false` as
   authorizing "stop and do nothing" — never compensation.

### Not applicable: the sub-millisecond TTL clamp

`CompareAndSet` clamps a positive sub-millisecond TTL to 1ms so it cannot truncate to the script's
"no expiry" reading. This method has no TTL on any path, so the clamp has nothing to apply to. If a
future variant gains one (`CompareAndExpire`?), the clamp belongs with it.

## Consequences

**Positive.** The locking contract the interface has always advertised is now completable: acquire
with `CompareAndSet`, release with `CompareAndDelete`, and a lock that lapsed mid-work is left alone
instead of being stolen back. Conditional eviction (#966 item 2) gets a primitive that does not need
a tombstone. Both the Redis client and `MockCache` implement it, and a new mock↔client parity test
pins them to the same answers — including an expiry case, the class an isolated-only suite cannot
catch.

**Negative — external `cache.Cache` implementers no longer compile.** A method on an exported
interface is apidiff-INCOMPATIBLE. The break usually surfaces in **test doubles** rather than
production code: hand-rolled fakes are the common way to implement this interface, and two of the
four in-repo implementers live in `_test.go` files. `go build ./...` therefore does not see it — the
migration atom prescribes `go vet ./...` for exactly this reason. Migration is
[migrations.md](migrations.md) `[C59.3]`.

**Negative — the two caller hazards above are real and new.** Neither existed before this method
did. A caller who mechanically swaps `Delete` for `CompareAndDelete` on a lock acquired with `ttl 0`
converts a recoverable mistake into a permanent one.

**Neutral — `false` is less informative than `CompareAndSet`'s `false`.** It does not distinguish a
failed comparison from a key that was already gone. Callers wanting that distinction cannot get it
from a single atomic operation, and asking for it usually means the caller is contemplating
compensation, which the contract forbids.

**Neutral — no hit/miss metrics.** The new `tracking.OpCompareAndDelete` (`"cad"`) is deliberately
absent from `isLookupOperation`: a conditional delete is not a lookup. Duration and error-class
metrics are emitted as for every other operation.

## Future work

- **`OperationCounts` is still hand-written.** The omission itself is now caught —
  `TestOperationCountsCoversEveryCacheMethod` reflects over `cache.Cache`'s method set and fails on
  any method missing from the map — so what remains out of scope is deriving the map, and collapsing
  `Stats()`, `OperationCounts`, `OperationCount`, and `ResetCounters` into one counter registry. That
  the hand-maintained vocabulary has already drifted once is visible today: `MockCache.Stats()` has
  no `close_calls` key even though `closeCalls` is both tracked and reset.
- **A `LoadThrough` helper** ([#966](https://github.com/gaborage/go-bricks/issues/966) item 3) would
  be the natural second consumer — that reporter's case is conditional eviction, not locking.
- **A `cache.AcquireLock`-style helper** would retire both hazards this ADR documents rather than
  merely warning about them: if it owns the token, rejects a non-positive TTL, and exposes
  `CompareAndDelete` as its only release path, neither the `ttl == 0` deadlock nor the
  `Delete`-fallback regression is representable.

## References

- [ADR-011](adr_011_redis_cache.md) — the cache interface, and the superseded lock example
- [cache.md](cache.md#key-operations) — the consumer-facing release guidance
- [migrations.md](migrations.md) `[C59.3]` — the compile break and the `ttl == 0` upgrade hazard
- `cache/types.go` (interface + contract), `cache/errors.go` (`ErrNilExpectedValue`)
- `cache/redis/client.go` (`cadScript`, `CompareAndDelete`), `cache/internal/tracking/metrics.go`
  (`OpCompareAndDelete`)
- `cache/testing/mock_cache.go`, `cache/testing/assertions.go` — mock + helper surface
- [#823](https://github.com/gaborage/go-bricks/issues/823) — the locking report
- [#966](https://github.com/gaborage/go-bricks/issues/966) item 2 — the conditional-eviction report
