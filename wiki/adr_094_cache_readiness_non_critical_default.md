# ADR-094: Cache Readiness Is Non-Critical by Default; `critical: true` Opts In

- **Status**: Accepted
- **Date**: 2026-09-01
- **Related**: [ADR-046](adr_046_cache_readiness_strict_default.md) (the strict
  default this reverses), [ADR-048](adr_048_ready_sanitize_by_default.md) (the
  sanitized `503` body, unchanged), [ADR-077](adr_077_delivered_empty_bool_config.md)
  (a delivered-empty `cache.critical` still fails resolution),
  [ADR-054](adr_054_cache_construction_fails_startup.md) (construction failure
  still aborts startup — that is a different question from reachability)
- **Issue**: #1296 (split from #966, item 5); amended by #1316

> **Amendment — 2026-09-02 (#1316).** Decision §4 is reversed: `CacheConfig.Critical`
> is a plain `bool`, not a `*bool`. Under this ADR's own §1 an absent key and an
> explicit `false` produce the same answer from `IsCacheCritical`, so the pointer's
> nil arm encoded nothing — the type now follows the two states that exist. The
> reason §4 gave for keeping it, that narrowing would break hand-built
> `config.CacheConfig{Critical: new(true)}` literals, is not one the framework
> recognises (CLAUDE.md, Backward Compatibility: obsolete shapes are removed and
> the break documented, not shimmed). What stands: no koanf default is registered
> for the key, `cache.critical` stays on the derivation-denied list, and the
> accessor keeps its nil-receiver guard. `cache.critical` is no longer an example
> of CONTEXT.md's Tri-state setting. Compile-break for code that sets the field;
> `[C62.2]` covers it on the same hop. Decision §1's "when `Cache.Critical` is nil"
> and the "Register `false` as a koanf default" alternative below keep their
> pre-#1316 tri-state wording as the historical record; the current contract reads
> an unset field and an explicit `false` identically.

## Context

ADR-046 (v0.56.0) made an absent `cache.critical` mean critical: a cache-enabled
service answered `/ready` with `503` while Redis was unreachable, on the argument
that an opt-in fix for a silent-failure bug protects only the operators who
already suspected the problem. The escape hatch was kept as `critical: false`,
and made loud — an unsuppressible startup WARN on every boot.

Four weeks of operating that posture put the cost on the common case rather than
the rare one. Most services put the cache in front of an origin they can still
serve from — a read-through cache over a database that absorbs the miss, a
memoized lookup, a rate limiter that fails open by design. For those, the strict
default converted one Redis blip into a fleet-wide `/ready` `503`: every replica
left the Service endpoints at the same instant, which is the correlated-eviction
risk ADR-046 accepted and asked `readinessProbe.failureThreshold` to absorb. A
local or CI boot without a Redis never reported Ready at all.

And the safe configuration was the noisy one. The deployment that made the right
call paid a permanent WARN line for it, and the WARN's own text told the operator
to "remove `cache.critical` to restore the strict default" — the framework
nagging a decision it had no standing to second-guess. The population that
genuinely needs the `503` — a rate limiter that must fail closed, a session
store, an idempotency ledger — is the smaller one, and it is the one that reads
the readiness docs.

## Decision

1. **Non-critical by default.** `Config.IsCacheCritical` answers `false` when
   `Cache.Critical` is nil and on a nil receiver. A cache-enabled deployment that
   says nothing has an informational cache probe: the outage is reported in the
   `/ready` `200` body (`cache: "unhealthy"`, `cache_stats.errors` climbing), the
   status code does not move, and no `Readiness check failed` ERROR line is
   logged for it.
2. **`critical: true` is the only way into readiness gating.** When set, nothing
   changes from v0.61.0: the `503`, its sanitized body (ADR-048) and the ERROR
   line all stand.
3. **An explicit `false` is a decision, not a smell.** The startup WARN and
   `Builder.warnIfCacheCriticalityOptOut` are deleted. `false` is the shipped
   default spelled out, for config review.
4. **The tri-state stays.** *(Historical — reversed by the 2026-09-02 amendment
   above; the field is a plain `bool` since #1316.)* `CacheConfig.Critical` remains
   a `*bool` with no registered koanf default. ADR-046's reasoning for that shape still holds — a
   registered default would populate the pointer and collapse absent and explicit
   into one state, which `CONTEXT.md`'s tri-state definition and the
   derivation-denied list in `config/config.go` both rely on — and narrowing the
   type to `bool` would break every hand-built `config.CacheConfig{Critical:
   new(true)}` for nothing.
5. **Nothing is derived.** Criticality does not follow from a declared fallback,
   a registered loader, or any other part of the config.

## Alternatives considered

- **Derive criticality from declared fallbacks** — a cache with a registered
  origin is non-critical, one without is critical. Rejected for implicitness:
  the framework cannot tell whether a consumer's `GetOrSet` loader is an origin or
  a placeholder, and "Explicit > Implicit" is the first principle in the
  manifesto. A wrong guess here is a silent fleet eviction.
- **Keep the strict default, drop only the WARN.** Removes the nag but leaves the
  fleet-wide `503` as the shipped behavior for the common shape.
- **Remove the key.** The services that need the `503` are real; the greppable
  one-line opt stays, it merely flips direction.
- **Register `false` as a koanf default.** Collapses the tri-state — see §4.

## Consequences

- **Behavior change, silent.** A cache-enabled deployment relying on the
  v0.56.0–v0.61.0 default to leave rotation during a Redis outage stays in
  rotation after the bump. It must set `critical: true`, which is a no-op on
  v0.61.0 and can ship ahead — `[C62.2]` in [migrations.md](migrations.md).
- **Alerts on the deleted WARN line** (`cache.critical is explicitly false`) stop
  firing.
- **ADR-046's disclosure argument is moot** — the strict default was what made the
  Redis-host leak default-on; ADR-048's sanitization stays as defense in depth for
  the opted-in case.
- **Three seams flip their pins**: the config accessor, the probe description in
  `app/`, and the `/ready` handler. Each keeps a `critical: true` regression guard
  so the opted-in path cannot silently lose its `503`.
- **Documentation**: [cache.md#readiness](cache.md#readiness), `llms.txt`,
  `README.md` and `config.example.yaml` now describe an opt-in; ADR-046 carries
  an amendment pointing here.
