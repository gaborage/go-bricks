# ADR-141: Delta Temporality Keeps UpDownCounters and Gauges Cumulative

**Status:** Accepted
**Date:** 2026-10-03

## Context

`observability.metrics.temporality: delta` installed a hand-rolled selector on
both OTLP metric exporters (`otlpmetricgrpc` and `otlpmetrichttp`) that returned
`metricdata.DeltaTemporality` for every instrument kind. Every non-monotonic sum
was exported as `is_monotonic=false` with delta temporality:

- An observable level exported its change since the previous collection, so a
  constant such as `db.client.connection.max` read `0` after the first export.
- A sync UpDownCounter exported only the interval's net change, and its series
  dropped out when idle.
- New Relic, the backend the option is documented for, lists delta non-monotonic
  sums as "Not supported since data is not meaningful".

The affected series include the Go runtime levels (`go.goroutine.count`,
`go.memory.used`, `go.memory.gc.goal`, `go.processor.limit`, `go.config.gogc`,
`go.memory.limit`), `cache.manager.active_caches`, the per-pool
`db.client.connection.*` ObservableUpDownCounters, `http.client.active_requests`
and every consumer UpDownCounter. `temporality: cumulative` and the `stdout`
endpoint were unaffected: neither installs a selector.

## Decision

**`delta` means the OpenTelemetry spec's delta temporality preference.** The
delta path passes `sdkmetric.DeltaTemporalitySelector` to both OTLP exporters,
the same mapping the exporters apply for
`OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta`:

| Instrument kind | Temporality |
| --- | --- |
| Counter, ObservableCounter, Histogram | Delta |
| UpDownCounter, ObservableUpDownCounter, Gauge, ObservableGauge | Cumulative |

- The hand-rolled `deltaTemporalitySelector` is deleted.
- The cumulative path keeps passing no selector, so
  `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE` still applies there. Under
  `delta` the explicit selector wins over that variable.
- The `temporality` key, its values and the `cumulative` default are unchanged;
  only the meaning of `delta` becomes per-kind. The `stdout` exporter still
  ignores the key.
- The database, cache and httpclient tracking instruments stay UpDownCounters,
  as OTel semconv defines them.

## Alternatives considered

- **Return Cumulative for the two UpDownCounter kinds only** (the issue's first
  proposal). Rejected: it leaves both gauge kinds on Delta, which neither the
  spec preference nor New Relic's OTLP guidance does.
- **An explicit `CumulativeTemporalitySelector` on the cumulative path**, for
  symmetry. Rejected: it would silently disable the environment preference, the
  only working route to per-kind delta without this key.
- **An opt-out or legacy all-delta value.** Rejected under the Backward
  Compatibility doctrine: the old output was not meaningful, so the break is
  documented, not shimmed.

## Consequences

- **Breaking under `temporality: delta`.** Non-monotonic sums now carry
  absolute levels, which New Relic maps to gauges. Datadog and Dynatrace stored
  the old delta UpDownCounters as counts and now store gauges.
- Gauge values are unchanged, but their start time stays at provider start.
- **Retention.** A sync UpDownCounter or sync Gauge series now lives for the
  whole process, as under `cumulative`. An instrument with unbounded attribute
  sets fills the cardinality limit (SDK default 2000) and then reports into the
  overflow series until restart. Delta's per-cycle reset still applies to
  counters and histograms.
- The workaround for older versions is `temporality: cumulative` (the default)
  with `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE=delta`.

## References

- `observability/metrics.go` — `createOTLPHTTPMetricExporter`,
  `createOTLPGRPCMetricExporter`
- `observability/config.go` — `TemporalityDelta`, `MetricsConfig.Temporality`
- `observability/metrics_test.go` — `TestCreateMetricExporterTemporalityPerKind`,
  `TestInitMeterProviderWithDeltaTemporality`
- OpenTelemetry metrics SDK exporter spec, `OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE`
- gaborage/go-bricks#1874
- See [migrations.md](migrations.md) `[C72.12]`.
