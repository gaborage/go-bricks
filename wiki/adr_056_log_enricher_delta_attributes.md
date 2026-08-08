# ADR-056: The log enricher stamps only the `log.type` delta

- **Status**: Accepted
- **Date**: 2026-08-07
- **Related**: #914 (the finding), [ADR-055](adr_055_reserved_log_attribute_namespaces.md) (the precedence this builds on)

## Context

OTel's `LoggerProvider` holds a **single** resource for every processor attached to it,
but dual-mode logging needs each of its two batch processors to label its records with
its own `log.type` (`"action"` vs `"trace"`). The framework closed that gap with a
per-processor exporter wrapper that copies a set of attributes onto each record on the
way out.

The wrapper was handed the wrong set. `createLogResource` merged the base service
resource with the one attribute that actually differed, and the merged result — not the
delta — reached `newResourceAttributeExporter`. Because the wrapper was constructed from
`res.Attributes()` wholesale, every exported log record carried record-level copies of
**everything the resource held**, all of which the OTLP `ResourceLogs.resource` block
already shipped once per batch. On a default deployment that is six attributes:

- `service.name`, `service.version`, `deployment.environment.name`
- `telemetry.sdk.name`, `telemetry.sdk.language`, `telemetry.sdk.version`

It is more wherever the environment says so: `createResource` merges `resource.Default()`,
whose env detector folds in every key from `OTEL_RESOURCE_ATTRIBUTES`, so a pod under the
Kubernetes OTel operator was duplicating `k8s.pod.name`, `k8s.namespace.name` and the rest
onto every log line too.

The one attribute the wrapper existed for was the one it never added. Records leave
`logger/otel_bridge.go` always carrying `log.type`, and the merged resource always
declared one, so the record-wins collision branch dropped it on every single record —
the six duplicates were the entire observable effect. The cost was paid per log line:
wire payload plus a six-element `AddAttributes` call.

Found by the `/simplify` altitude pass during #873/#918; filed as #914.

## Decision

**Construct the wrapper with the delta, not the merged resource.**

```go
newProcessorAttributeExporter(baseExporter, attribute.String("log.type", logType))
```

`createLogResource` is deleted; the provider's own `sdklog.WithResource(res)` remains the
single place service identity enters the log pipeline. The wrapper is renamed
`processorAttributeExporter` to say what it is for — attributes specific to a *processor*,
which the provider's one shared resource structurally cannot carry.

**Record-wins precedence is unchanged.** A record that already carries `log.type` keeps
its own value; the processor's stamp only lands on records that lack the key. That is not
a leftover — it is load-bearing twice over. Dual-mode routing happens at `OnEmit`, before
batching, keyed on the record's own `log.type` (defaulting to `"trace"`), so a caller-set
value must stay authoritative end to end. And a record emitted directly through the OTel
API by third-party code carries no `log.type` at all; it routes to the trace processor,
whose wrapper is what labels it. That injection is the reason the wrapper survives this
change rather than being deleted outright.

## Consequences

**Positive.** Every exported log record loses one `AddAttributes` call and at least six
attributes — more under `OTEL_RESOURCE_ATTRIBUTES`, which added to the duplicated set.
Service identity now appears in exactly one place on the wire — the resource block, where
it was never spoofable — instead of being duplicated into the record attributes, where
[ADR-055](adr_055_reserved_log_attribute_namespaces.md) had to defend it against caller
shadowing. That defense is strengthened: with no record-level identity duplicate, a
backend that flattens record attributes over resource attributes has nothing left to
flatten *over*.

**Negative.** Behavior change on the OTLP log wire. Backends that index record attributes
separately from resource attributes stop matching record-level filters on log records —
for any resource attribute, not only the framework's six — until those queries are
repointed at the resource attribute of the same name.
Backends that flatten the two levels see the same values as before. No code change can
detect this — the affected artifacts are dashboards, alerts and saved queries.
`[C58.5]` in [migrations.md](migrations.md) carries the detection procedure.

**Neutral.** Traces and metrics are untouched: `createResource` and the trace/metric
providers are unchanged. The raw zerolog stream (stdout/file JSON, console output) never
carried these attributes and is unaffected.

## References

- #914 — the finding
- `observability/processor_attribute_exporter.go` — the wrapper and its precedence
- `observability/logs.go` — `createBatchProcessor`, which now passes the delta
- [ADR-055](adr_055_reserved_log_attribute_namespaces.md) — the record-over-resource precedence relied on here
- [migrations.md](migrations.md) `[C58.5]`
