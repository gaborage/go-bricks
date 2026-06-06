# ADR-026: Zero-Overhead Request Path When Observability and Logging Are Disabled

**Status:** Accepted
**Date:** 2026-06-06

## Context

Profiling the framework on a default-config read workload (perf iteration-1)
surfaced three sources of per-request allocation that the framework pays even
when the corresponding feature is *disabled*:

1. **OpenTelemetry "no-op" is not free.** With `observability.enabled: false`
   (the default), the framework never registers a real tracer/meter provider —
   but `otel.Tracer()`/`otel.Meter()` return **non-nil no-op** implementations,
   not nil. The existing `if meter == nil { return }` guard in the DB tracking
   layer therefore never fires, so the framework builds span/metric attribute
   slices and calls `Start`/`Record` on every DB query (and the echo-opentelemetry
   HTTP middleware does the same per request), only for the no-op sink to discard
   them. ~20% of total allocations in the profile were this discarded work.
2. **The per-request action log builds ~17 fields unconditionally.** `logActionSummary`
   extracts request metadata and chains ~17 typed setters on every request, even
   at `LOG_LEVEL=warn` where the event is immediately dropped. The `logger.LogEvent`
   interface had no way to ask "would this be emitted?" before building.
3. **Redundant per-request context clones.** A default request performed four
   `context.WithValue` calls (two per counter type) plus two adjacent
   `http.Request.WithContext` clones for trace-context and operation counters.

## Decision

Make "disabled" genuinely allocation-free by gating on **explicit booleans**
rather than relying on the no-op providers, and consolidate the request-context
enrichment:

- **Gate DB tracking** on a process-global `atomic.Bool` in the tracking package,
  set once at app bootstrap from `observability.enabled` and read at the single
  `TrackDBOperation` dispatch (covers all five entry points). When off, no
  span/metric attributes are built.
- **Gate the OTel HTTP middleware** on an explicit `observabilityEnabled` parameter
  to `server.SetupMiddlewares` (passed by the caller from `observability.enabled`,
  like the existing `healthPath`/`readyPath` params). `RequestID` and the request
  enricher stay unconditional so W3C trace propagation always works.
- **Add `Enabled() bool` to `logger.LogEvent`** (delegating to zerolog's nil-safe
  `Event.Enabled()`) and short-circuit `logActionSummary` before the extraction +
  field build when the level is disabled.
- **Consolidate counters** into one `requestCounters` struct (atomic fields) behind
  a single context value via `logger.WithRequestCounters`, and add an additive
  `server.RequestEnrich()` middleware that performs a single `WithContext` clone
  combining trace enrichment and counter seeding. `TraceContext`/`PerformanceStats`
  remain exported and share the trace-enrichment helper.
- **Add `server.gzip.minlength`** (default 1024) so tiny responses skip compression.

### Breaking changes

- `logger.LogEvent` gains `Enabled() bool` — external implementers must add it.
  (Interface evolution, same class as the S8179/S8196 changes in ADR-013.)
- `server.SetupMiddlewares` gains an `observabilityEnabled bool` parameter.
- `server.gzip.minlength` defaults to 1024 (was effectively 0 = compress all).
- Consumers using the `database` package **without** the app bootstrap must call
  `database.SetObservabilityEnabled(true)` to emit DB spans/metrics.

See [migrations.md](migrations.md#request-path-zero-overhead-changes-adr-026).

Rejected alternatives:

- **Keep relying on the no-op providers:** the profile proves it is not free; the
  no-op still accepts fully-built attribute slices.
- **A nil-check instead of an explicit flag (#2):** `otel.Meter()`/`otel.Tracer()`
  return non-nil no-ops, so a nil-check can never gate the work.
- **Thread the obs flag per-connection through `database.Connector`:** connections
  are created lazily inside the manager, so bootstrap never holds the instances;
  a `Connector` signature change would break the public hook, and a per-`Context`
  field would default 26 existing test literals to "off." The process-global flag
  (observability.enabled is itself process-global) with an explicit exported setter
  is the minimal non-breaking seam.
- **Type-assert to `*logger.LogEventAdapter` for `Enabled()`:** a dynamic hack the
  manifesto disfavors; adding the method to the interface is the type-safe choice.

## Consequences

- **Truly zero per-request span/metric/field overhead** when observability and the
  action-log level are off (the common default).
- **Breaking for external implementers/callers** as listed above — mitigated by a
  `migrations.md` row per change and the compiler (a forgotten `Enabled()` fails
  the build).
- **Standalone `database`-package consumers** must opt in via
  `SetObservabilityEnabled` — documented.
- **Out of scope (follow-ups):** the DB per-operation *debug log* still builds its
  `WithFields` map before the level check, and the `messaging`/`httpclient`
  subsystems still build attributes into the no-op provider when disabled. Both are
  the same `Enabled()`/explicit-gate pattern and are tracked for a later iteration.

## Related

- ADR-013 (interface naming) — prior public-interface evolution precedent.
- ADR-025 (pool idle tracks max) — the sibling perf iteration-1 change.
