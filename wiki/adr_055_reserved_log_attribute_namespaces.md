# ADR-055: Reserve resource-identity namespaces in the OTel log bridge

- **Status**: Accepted
- **Date**: 2026-08-07
- **Related**: #915 (the finding), #873 (the `/security-audit` pass that surfaced it)

## Context

`logger/otel_bridge.go` copies every zerolog field key verbatim into a log record
attribute, skipping only `time`, `level`, `message` and `msg`. Nothing reserved the
OTel semantic-convention identity namespace, so a log call could set a record
attribute that shadows the service's own identity:

```go
logger.Info().Str("service.name", "payments-core").Msg("hello")
```

The spoof survives enrichment by design: `resourceAttributeExporter.enrichWithResource`
gives record attributes precedence over resource attributes on a key collision. That
precedence is load-bearing — it is what keeps a caller-set `log.type` from being
clobbered, which is how dual-mode routing works — so the spoofed value was
deliberately protected from correction. The authoritative resource-level
`service.name` in the OTLP `ResourceLogs` envelope was never spoofable; the exposure
is backends that flatten record attributes over resource attributes in search and
dashboards, where the record-level duplicate wins.

As of [ADR-056](adr_056_log_enricher_delta_attributes.md) the enricher (now
`processorAttributeExporter`) stamps only the `log.type` delta, so identity attributes no
longer reach records as duplicates at all. Two separate things follow, and only the first
narrows: the **exporter's** record-over-resource precedence still works exactly as described
above, but `log.type` is now the only attribute left for it to decide. The **bridge's**
reserved-namespace remap in `logger/otel_bridge.go` — everything this ADR decides — is
untouched: `service.*`, `telemetry.sdk.*` and `deployment.environment.name` are still
reserved, still remapped under `app.`, still warned about once per bridge.

The fix belongs at the bridge — the boundary where caller-supplied field names become
attributes — not at the exporter, whose precedence must not change. The open question
was what to do with a colliding field: drop it, prefix it, and/or warn.

## Decision

**Remap colliding top-level keys under the `app.` prefix, and emit a one-time WARN.**

The reserved set mirrors what the provider's resource actually carries:

- `service.*` (prefix)
- `telemetry.sdk.*` (prefix)
- `deployment.environment.name` (exact)

`log.type` is intentionally not reserved — middleware sets it to `"action"` and
dual-mode routing depends on it.

A four-lens review (operator, framework philosophy, implementation risk, security)
settled the policy 3–1 for prefixing over dropping and 3–1 for warning over silence:

- **Prefix over drop.** The rename is self-evidencing on every affected record —
  `app.service.name` sits next to the attributes an operator is already reading —
  while a drop destroys the caller's data and, in the spoof case, the forensic
  evidence of the attempt. The value survives for the one legitimate collision shape
  (fields about a *downstream* service, e.g. `service.latency`). The envelope-meta
  precedent (`timestamp`/`traceId` dropped with WARN in `server/handler.go`) does not
  transfer: those values are framework-provided either way, so nothing is lost there.
- **WARN over silence.** A silent rename breaks saved queries keyed on the original
  name with no signal — the manifesto's "no degradation without signal". The WARN is
  emitted **directly via the bridge's `log.Logger`**, never through zerolog: the
  bridge sits inside zerolog's writer chain, so logging through it would recurse. It
  therefore bypasses the `SensitiveDataFilter` and carries key names only, never
  field values, with the joined key list length-bounded.
- **Global `sync.Once`, not a per-key map.** Field names are caller-influenced, so a
  per-key dedup map is an unbounded allocation under `service.<random>` churn. The
  per-record prefix carries the per-key evidence; the WARN only needs to exist once
  per bridge instance (one bridge per process in practice).

Scope is deliberately top-level keys only: nested map values flatten under their
parent key (`ctx.service.name`) and cannot collide with bare resource keys.

## Consequences

**Positive.** Record-level identity spoofing is neutralized for every backend,
including those that flatten record attributes over resource attributes. The
no-collision hot path stays allocation-free (two `strings.HasPrefix` plus one
equality per key).

**Negative.** Behavioral break for any consumer legitimately logging top-level
fields inside the reserved namespaces — their dashboards and queries see the `app.`-
prefixed key instead. `[C58.4]` in [migrations.md](migrations.md) records the rename.
`app.*` keys remain caller-supplied and unauthenticated; they must never be treated
as service identity.

**Neutral.** The exporter's record-over-resource precedence is untouched. The
resource-level identity was correct before and after.

## References

- #915 — the finding
- `logger/otel_bridge.go` — guard, remap, and WARN emission
- `observability/processor_attribute_exporter.go` — the precedence that stays (this file was named `resource_exporter.go` when this ADR was written; renamed by ADR-056)
- [migrations.md](migrations.md) `[C58.4]`
