# ADR-138: Trace Sampling Honors the Parent's Decision

**Status:** Accepted
**Date:** 2026-10-02

## Context

`initTraceProvider` installed a bare `sdktrace.TraceIDRatioBased(rate)` through
`WithSampler`, unchanged since the provider was written. The default rate is
`1.0`. A ratio sampler ignores the parent: a span whose upstream caller sent a
`traceparent` marked not sampled (`-00`) was still recorded, exported as an
orphan fragment of a trace the rest of the system dropped, and propagated
onward marked sampled. `WithSampler` also overrides `OTEL_TRACES_SAMPLER`, so a
deployment had no environment escape.

The spans this reaches are the HTTP server spans (echo-otel extracts the
inbound `traceparent` through the global propagator) and any consumer code that
calls a propagator's `Extract` itself. AMQP and streams consume spans stay
roots ([ADR-068](adr_068_delivery_pipeline.md)). Dual-mode log sampling
hashes the trace ID and is unaffected.

One input needs special care. When a request context carries no valid span,
the httpclient legacy fallback forwards the inbound `traceparent` if the
request had one, and otherwise writes a synthetic one from
`trace.GenerateTraceParent()` that always ends in `-01` (sampled). With
`observability.enabled` defaulting to `false` and `EnableW3CTrace` to `true`,
that covers every call a tracing-off GoBricks service originates without an
inbound parent, and every background call. A sampler that trusts the remote
sampled flag would let those headers force full recording downstream.

## Decision

**Variant (c): root spans and remote-sampled parents go through the ratio;
every other parent decision is honored.** All four `ParentBased` delegates are written out
(Explicit > Implicit):

```go
ratio := sdktrace.TraceIDRatioBased(rate)
sampler := sdktrace.ParentBased(ratio,
    sdktrace.WithRemoteParentSampled(ratio),
    sdktrace.WithRemoteParentNotSampled(sdktrace.NeverSample()),
    sdktrace.WithLocalParentSampled(sdktrace.AlwaysSample()),
    sdktrace.WithLocalParentNotSampled(sdktrace.NeverSample()),
)
```

| Parent | Decision |
| --- | --- |
| none (root) | ratio |
| remote, sampled | ratio |
| remote, not sampled | drop |
| local, sampled | keep |
| local, not sampled | drop |

- An upstream **not sampled** decision is honored, which ends the orphans.
- An upstream **sampled** flag is re-judged by the same ratio, so neither the
  synthetic `-01` nor a client-chosen flag can force recording.
- `0.0` stays an off switch: every span this provider starts at `0.0` is
  unsampled, so no sampled local parent arises inside the service.

## Alternatives considered

- **(a) Plain `ParentBased(ratio)`.** Honors every upstream decision. Rejected:
  it puts the recording decision on the far side of a trust boundary — any
  caller that can send a `traceparent` decides whether this service records
  and exports. The synthetic `-01` would force 100% recording downstream of
  every tracing-off GoBricks service, a client-chosen sampled flag would force
  recording even at `0.0`, and the off switch would move to
  `trace.enabled: false`.
- **(b) (a), plus stop the httpclient fallback minting a sampled
  `traceparent`.** Rejected: that is its own consumer-visible httpclient change
  (wiki/httpclient.md, "Legacy fallback"), and it still trusts every
  third-party caller's flag.
- **Keep the bare ratio.** Rejected: it is the defect.

## Consequences

- **Breaking at the default `1.0`.** An inbound `traceparent` with
  `sampled=00` now drops this service's spans; before, they were recorded. A
  service behind an upstream sampler (a gateway at 10%, say) now records only
  the requests that sampler kept.
- **Fragment loss below `1.0`.** A trace the upstream sampled is re-judged
  here. With the same algorithm the decision agrees whenever this service's
  rate is at least the upstream's; a higher upstream rate, or a different
  algorithm, can drop this service's fragment of a trace kept elsewhere.
- **Span volume never grows.** Remote-sampled and root spans are judged as
  before, remote-unsampled ones are now dropped, and a local child follows a
  parent that the same ratio already judged. Memory stays bounded by
  `trace.max.queue.size`: the batch processor is built without `WithBlocking`,
  so a full queue drops spans rather than blocking. Vendor ingest cost moves
  with that volume.
- A caller can opt its own requests out of tracing by sending `-00`. Spans are
  not an audit record: action logs still export every request, and ERROR/WARN
  trace logs are never sampled out.
- Detect on the `trace.sample.rate` values each service runs with, and on
  whether it receives `traceparent` headers from a sampled upstream.

## References

- `observability/provider.go` — `initTraceProvider` (the sampler),
  `warnIfZeroSampleRate`
- `observability/config.go` — `SampleConfig.Rate`
- `observability/provider_test.go` — `TestProviderSamplerHonorsParentDecision`,
  `TestProviderSamplerDropsSyntheticTraceParentAtZeroRate`
- `trace/trace.go` — `GenerateTraceParent`; `httpclient/client.go` — the
  legacy `traceparent` fallback
- See [migrations.md](migrations.md) `[C72.5]`.
