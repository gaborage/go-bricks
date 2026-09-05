# ADR-026: Zero-Overhead Request Path When Observability and Logging Are Disabled

**Status:** Accepted
**Date:** 2026-06-06

> **Amended (2026-09-05, #1179):** The allocation guard for the default
> middleware chain lives in `server/route_registrar_test.go`. It was not added by
> this ADR — #627 (`b0ef71d1`, three weeks later) introduced it with a measured
> baseline of 53 allocs/op. `defaultMiddlewareChainBaselineAllocs` is 62 today and
> the ceiling it asserts against is 69; this records where the nine between 53 and
> 62 came from, six of which are post-toolchain. The decision below is unchanged.
>
> - **Toolchain, 53 → 56** (#1177). Not this project's code: the anchor commit
>   `b0ef71d1` reads 53 on go1.26.x and 56 on go1.27.1.
> - **`d93dc743` (#682), 56 → 61 — the test harness, not the chain.**
>   `testLogEvent.Str` began recording values into a `map[string]string` so tests
>   could assert on `SensitiveDataFilter` masking, and `buildLogEvent` makes ten
>   unconditional `Str` calls per request, so every measured request now builds
>   and fills that map. The commit's only non-test file is `server/server.go`,
>   and every path it changes hangs off the error handler — `classifyError`'s
>   `status >= 500` branch and the panic branch — which a 200 OK never enters.
>   Proven by patching the map out at the head and re-measuring: 62 → 57.
> - **`d715a7c5` (#1128), 61 → 62 — justified feature cost.**
>   [ADR-070](adr_070_inbound_trace_identifier_validation.md) made the HTTP door
>   shadow both inherited W3C keys unconditionally, so a request carrying no
>   `traceparent` now pays for context values the old present-header-only path
>   skipped. The step measures +1 net. The two shadow lines cost 2 at today's
>   head (removing both by overlay: 62 → 60), so the same commit gave one back
>   elsewhere on this path; that half was not decomposed further.
>
> Net of the nine: three toolchain, five test-harness bookkeeping, one a
> deliberate ingress cost. Giving the guard a values-free logger double and
> re-pinning to 57 is tracked in #1439.
>
> To reproduce: `go test -count=1 -run TestDefaultMiddlewareChainAllocsStable -v
> ./server/` and read the value the test logs — `make test-alloc` runs the guard
> but only asserts the ceiling. Three runs per data point, taking the value all
> three agree on, since `AllocsPerRun` averages. The 57 comes from deleting the
> two lines in `testLogEvent.Str` that build and fill `e.values` and re-running.
> Every number here was produced on go1.27.1; `go.mod` pins go 1.27.0 and the
> guard comment records the baseline as measured on 1.27.0, a patch-level
> difference that does not move these counts.

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
- **Make the `X-Response-Time` header opt-in** via `server.responsetime.enabled`
  (default false). The always-on `Timing` middleware set the header on every
  response, and each `Header().Set` allocates a `[]string` in
  `net/textproto.MIMEHeader.Set` — ~2.4% of total allocations on the default read
  workload (perf iteration-2). The middleware is registered only when enabled, and
  `server.CORS` advertises the header in `Access-Control-Expose-Headers` only when
  it is actually emitted. `X-Request-ID` and `traceparent` stay unconditional.

### Breaking changes

- `logger.LogEvent` gains `Enabled() bool` — external implementers must add it.
  (Interface evolution, same class as the S8179/S8196 changes in ADR-013.)
- `server.SetupMiddlewares` gains an `observabilityEnabled bool` parameter.
- `server.gzip.minlength` defaults to 1024 (was effectively 0 = compress all).
- `X-Response-Time` is no longer emitted by default — opt in with
  `server.responsetime.enabled: true`. Direct callers of `server.CORS(...)` gain a
  leading `exposeResponseTime bool` parameter.
- Consumers using the `database` package **without** the app bootstrap must call
  `database.SetObservabilityEnabled(true)` to emit DB spans/metrics.

See [migrations.md](migrations.md#e41--v0401--v0410--perf-iteration-2-zero-overhead-request-path-adr-026--pool-idle-tracks-max-adr-025) (E41 › C41.1–C41.5).

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
- **Follow-ups completed:** the DB per-operation *debug log* was gated in #562 and
  the `messaging`/`httpclient` subsystems were gated in #954, both using the same
  `Enabled()`-short-circuit pattern.

## Related

- ADR-013 (interface naming) — prior public-interface evolution precedent.
- ADR-025 (pool idle tracks max) — the sibling perf iteration-1 change.
- [ADR-070](adr_070_inbound_trace_identifier_validation.md) (inbound trace identifier
  validation) — its unconditional W3C key clear at the HTTP door is the one allocation
  this path has gained since, per the amendment above.
