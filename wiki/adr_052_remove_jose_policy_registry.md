# ADR-052: Delete `jose.PolicyRegistry` rather than wire it up

- **Status**: Accepted
- **Date**: 2026-08-07
- **Related**: [ADR-045](adr_045_no_producer_side_manager_interfaces.md) (the same disposition applied to a dead `cache.Manager` interface)

## Context

`jose.PolicyRegistry` was a `sync.Map`-backed cache of scanned-and-resolved JOSE policies
keyed by `(reflect.Type, Direction)`, mirroring the shape of
`database/internal/columns/registry.go`. It was fully tested and had four exported symbols —
`PolicyRegistry`, `NewPolicyRegistry`, `LoadOrScan`, `Store` — and no production caller. Its
only references outside its own file were in its own test.

Its doc comment justified the design with a specific performance claim:

> The cache stores nil to mean "this type was scanned and has no JOSE policy" — distinct
> from "this type has not been scanned yet" (cache miss). This avoids re-scanning untagged
> types on every request, which would otherwise dominate the request hot path for non-JOSE
> routes.

That hot path does not exist. `jose:` tag scanning happens in `server.scanRouteJOSE`, which
`HandlerRegistry.RegisterHandler` calls **once per route at startup** (`server/handler.go`).
It scans, validates bidirectional symmetry, resolves every kid against the `KeyResolver`, and
writes the resolved policies onto the route descriptor as `d.InboundJOSE` / `d.OutboundJOSE`.
The per-request path reads those fields. Nothing re-scans, so there was never a re-scan for
the negative cache to prevent.

The same false claim had propagated: `jose/policy.go` described a `Policy` as "cached in the
registry", which no identifier grep for the deleted symbols would have found.

## Decision

**Delete all four exported symbols.**

The issue offered three dispositions and the measurement decides between them:

**Wire it into `scanRouteJOSE`.** Rejected. This is what the type was built for, but with
scanning already startup-only it would memoize an operation that runs a handful of times per
process. There is no win to measure.

**Keep it as a documented extension point** for consumers implementing their own registration
path. Rejected. It preserves four exported symbols whose only stated justification is the
claim above, and the framework's own registration path demonstrates that a consumer needs
`ScanType` + `ResolvePolicy` and a policy field on their route record — not this type.

**Deprecate in place and delete at the next breaking hop.** Rejected as exactly the
compatibility shim the manifesto forbids: *"remove obsolete paths instead of adding
compatibility layers, fallbacks, or in-code migration shims."*

## Consequences

**Positive.** Four exported symbols leave the public surface, and with them a doc comment
asserting a performance property the framework does not have — the more expensive of the two,
because it would have misled the next person to reason about JOSE request cost.

**Negative.** This is apidiff-INCOMPATIBLE. A consumer with a hand-rolled registration path
that called `NewPolicyRegistry` must scan with `jose.ScanType` + `jose.ResolvePolicy` and
memoize the resolved `*jose.Policy` itself — keyed on the type **and** the direction, since
one struct used as both request and response resolves to two different policies. `[C58.1]` in
[migrations.md](migrations.md) carries the before/after.

**Neutral.** No security control moves. `PolicyRegistry` was never on the enforcement path:
the algorithm allowlist (`Policy.validateAlgorithms`), the bidirectional-symmetry check, and
fail-fast kid resolution all run inside `scanRouteJOSE`, independent of it.

## References

- #817 — the report, from the `doc-drift` audit at `bb43eb1`
- `jose/registry.go` (deleted) · `jose/scanner.go` (`ScanType`) · `jose/resolver.go` (`ResolvePolicy`)
- `server/jose.go` (`scanRouteJOSE`) · `server/descriptor.go` (`InboundJOSE`, `OutboundJOSE`)
- [migrations.md](migrations.md) `[C58.1]`
