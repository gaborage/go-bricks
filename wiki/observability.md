# Observability (Deep Dive)

GoBricks provides production-grade observability built on OpenTelemetry: distributed tracing with W3C traceparent propagation, runtime and custom metrics, structured logging with dual-mode export, and health endpoints. This page captures the framework-level details, the custom metrics API exposed to modules, and pointers to the vendor-specific configuration guides.

## Observability

**Key Features:** W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging with conditional sampling, batching and export timeouts gated on `observability.environment` (500ms/10s for `development`, 5s/60s otherwise — see "Export Timeout Configuration" below)

**Per-Subsystem Instrumented Meters:** The framework ships three automatically-instrumented meters alongside the Go runtime meter. `go-bricks/database` records query durations and pool utilisation. `go-bricks/messaging` records AMQP publish and consume durations. `go-bricks/httpclient` records five outbound HTTP instruments: `http.client.request.duration`, `http.client.active_requests`, `http.client.request.body.size`, `http.client.response.body.size`, and `http.client.retries.total`. When `observability.enabled` is false no metrics are emitted; the `database` meter additionally short-circuits before building any attributes (true zero overhead), while `messaging`/`httpclient` route into the global no-op provider (data discarded, attribute construction not yet skipped — a tracked follow-up). See [httpclient.md#metrics](httpclient.md#metrics) for the full attribute reference.

**Per-Subsystem Instrumented Tracers:** The framework ships three OTel tracers under matching scopes. `go-bricks/database` emits CLIENT-kind spans per query. `go-bricks/messaging` emits PRODUCER/CONSUMER spans per AMQP publish/consume. `go-bricks/httpclient` emits CLIENT-kind spans per outbound HTTP call — one parent "Do" span (the logical request rollup) and one child attempt span per retry attempt — and injects `traceparent` headers via the OTel propagator so downstream services join the trace. When `observability.enabled` is false no spans are emitted; the `database` tracer additionally short-circuits before building any span attributes (true zero overhead), while `messaging`/`httpclient` route into the global no-op provider (spans dropped, attribute construction not yet skipped — a tracked follow-up). See [httpclient.md#tracing](httpclient.md#tracing) for the span tree, attribute reference, and status-mapping rules.

**Group-Scoped 404 Route Labels (echo v5.3.0):** After the echo v5.3.0 upgrade, a 404 for an unmatched sub-path (or a wrong-method request) under a middleware-bearing group — e.g. the scheduler `/_sys` CIDR-gated group or the debug group — now resolves to that group's implicit `/*` catch-all, so its incoming-request span and metrics carry `http.route = "/<group-prefix>/*"` (span name `<METHOD> /<group-prefix>/*` — the span keeps the request method, e.g. `GET /<group-prefix>/*`, or `POST /<group-prefix>/*` for a wrong-method request) instead of the previous empty-route / bare-`GET` bucket. This is a low-cardinality change (one new series per middleware-bearing group prefix); operators with dashboards or alerts keyed on `http.route` for those 404s should expect the new series and re-point any query that matched the old empty/`GET` bucket.

**Go Runtime Metrics:** Auto-exports memory, goroutines, CPU, scheduler latency, GC config when `observability.enabled: true`. Follows [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/runtime/go-metrics/)

**Export Timeout Configuration:** GoBricks gates export timeouts on `observability.environment` and the signal's endpoint, balancing fail-fast feedback against network resilience:

- **`observability.environment: development` (the default) or `endpoint: stdout`:** 10s (quick failure detection for debugging)
- **Any other environment with a network endpoint:** 60s (accommodates network latency, TLS handshake, batch transmission)
- **⚠️ `observability.environment` is not derived from `app.env`.** It is an independent key defaulting to `development`, so a service running `app.env: production` against a remote OTLP collector keeps the 10s default until you set `observability.environment` explicitly.
- **Override via YAML:** `observability.trace.export.timeout: "90s"` (trace only); use `observability.metrics.export.timeout` and `observability.logs.export.timeout` for the other subsystems
- **Why 60s?** Real-world production scenarios involve cross-region latency, TLS negotiation, and 512-span batch transmission to remote OTLP endpoints

**Dual-Mode Logging:** `DualModeLogProcessor` routes logs by `log.type`:

- **Action logs** (`log.type="action"`): Always exported at 100% (request summaries)
- **Trace logs** (`log.type="trace"`): ERROR/WARN always exported, INFO/DEBUG sampled by `samplingrate`
- Configure via `observability.logs.samplingrate` (0.0-1.0, default 0.0 drops INFO/DEBUG; resolution 0.01%, so a rate below 0.00005 exports nothing)
- Sampling is deterministic per trace (all logs in same trace sampled together)
- **All** resource attributes ride the OTLP `ResourceLogs.resource` block once per batch — the identity keys (`service.*`, `deployment.environment.name`, `telemetry.sdk.*`) and anything your deployment injects via `OTEL_RESOURCE_ATTRIBUTES` / `OTEL_SERVICE_NAME` alike. `log.type` is the only record-level attribute the framework injects at export time, and only on records that don't already carry one; log fields your own code sets are untouched ([ADR-056](adr_056_log_enricher_delta_attributes.md))

**Reserved log attribute namespaces:** a record attribute deliberately wins over the attribute a processor would stamp on key collision, so the OTel bridge reserves the resource-identity namespaces at the boundary where zerolog field names become record attributes. A top-level log field whose key starts with `service.` or `telemetry.sdk.`, or equals `deployment.environment.name`, is remapped under the `app.` prefix with its value preserved (`service.name` → `app.service.name`), and the first remap per bridge instance (one bridge per process in practice) emits a one-time WARN record (`reserved.keys` names the offending keys — never their values). `log.type` is intentionally not reserved. `app.*` keys are caller-supplied and unauthenticated — never treat them as service identity. Nested map values are not remapped: they flatten under their parent key and cannot collide with bare resource keys. See [ADR-055](adr_055_reserved_log_attribute_namespaces.md).

**Request Logging:** HTTP requests track severity escalation via `requestLogContext`. Automatic escalation from status codes (4xx→WARN, 5xx→ERROR). Explicit: `c.EscalateSeverity(zerolog.WarnLevel)` (the `HandlerContext` method). Configure `observability.logs.slowrequestthreshold` for slow request detection.

**Testing:** Use `observability/testing` package:

```go
tp := obtest.NewTestTraceProvider()
spans := tp.Exporter.GetSpans()
obtest.AssertSpanName(t, &spans[0], "operation")

mp := obtest.NewTestMeterProvider()
rm := mp.Collect(t)
obtest.AssertMetricExists(t, rm, "my.counter")
```

Span helpers: `AssertSpanName`, `AssertSpanAttribute`, `AssertSpanStatus`, `AssertSpanStatusDescription`, `AssertSpanError`, plus `NewSpanCollector(t, exporter)` for filtering. Metric helpers: `AssertMetricExists`, `AssertMetricValue`, `AssertMetricCount`, `AssertMetricDescription`, `FindMetric`, `GetMetricSumValue`, `GetMetricHistogramCount`. There is no in-memory log exporter today — capture zerolog output via an `io.Writer` sink for action/trace log assertions.

**Debug Mode:** Set `GOBRICKS_DEBUG=true` for `[OBSERVABILITY]` logs (provider init, exporter setup, span lifecycle)

**Common Issues:** Spans not appearing (check `observability.enabled`, wait for batch timeout), logs not exported (verify `observability.logs.enabled`, set `log.pretty: false`), pretty mode conflict (fails fast at startup). See the Troubleshooting section in [wiki/troubleshooting.md](troubleshooting.md#observability-issues) for details

**Log format selection (`log.output.format`):** Defaults to `auto`, which resolves to console (colored) output when stdout is a terminal AND OTLP log export is not active; otherwise structured JSON. Explicit values: `console` / `pretty` (always colored), `json` / `structured` (always JSON). The legacy `log.pretty: true` still works and overrides `log.output.format`. Combining pretty output with `observability.logs.enabled: true` still panics at startup — `auto` is the safe default that keeps local dev colored and production JSON without manual configuration.

## Sensitive Data Filtering

Every log line emitted via the framework logger passes through a `logger.SensitiveDataFilter` that masks values whose **field names** match an allowlist (case-insensitive substring). The filter is applied uniformly — including in the framework's own request/response middleware, AMQP consumer panic recovery, slow-request warnings, scheduler job traces, and any module-level `log.Info()/Error()/...` call. There is no opt-in surface to wrap "after the fact" — the filter is wired into the logger before any subsystem captures a reference to it.

### Default field list

`logger.DefaultFilterConfig()` (in [`logger/filter.go`](../logger/filter.go)) ships these names (all matched case-insensitive substring, so `password` matches `Password`, `db_password`, `oldPasswordHash`, etc.):

| Category | Default names |
| --- | --- |
| Credentials | `password`, `passwd`, `pwd`, `secret`, `key`, `api_key`, `apikey`, `token`, `access_token`, `refresh_token` |
| Auth headers | `auth`, `authorization` |
| Generic | `credential`, `credentials` |
| Connection strings | `broker_url`, `database_url`, `db_url` |
| Card data (PCI) & PII | `cardholder`, `card_number`, `cardnumber`, `primary_account_number`, `cvv`, `cvc`, `track1`, `track2`, `track_data`, `iban`, `otp` |

A URL value on the sensitive path is masked in full, never structure-preserved — query strings and fragments routinely carry the secret itself. The default mask value is `***`.

Bare `pan`, `card`, `pin`, and `track` are deliberately absent: substring matching would mask `span_id`, `discard_reason`, `pinned_at`, and `tracking_id`, so differently-named PAN fields still need a per-service entry via `log.sensitivefields`. `otp` does over-mask camelCase `…otP…` names (e.g. `snapshotPath`) and that trade is intentional — masking a debugging detail costs less than leaking an OTP. Setting `app.Options.LoggerFilterConfig` (or calling `logger.NewWithFilter` directly) with an explicitly empty `SensitiveFields` now logs a WARN at startup (suppressed at `log.level: error` and above) — an empty YAML `log.sensitivefields` list is not this case: it resolves to `nil` and falls back to the defaults, so it neither disables masking nor warns.

### Extending the filter (two seams)

For regulated payloads — PCI-DSS (bare PAN field names), PII (SSN, tax ID) — extend the list at bootstrap. The two seams differ in how they combine with the defaults: YAML merges into them, `LoggerFilterConfig` replaces them wholesale (Seam 2 below). Adopting a seam is opt-in; the default list itself is not — this release widened it, so every app that leaves the defaults in place, YAML extenders included, picks up the new card-data masking with no config change. A non-nil `app.Options.LoggerFilterConfig` is the one configuration that doesn't (see [Migration notes](#migration-notes)).

**Seam 1 — YAML config (recommended for static lists):**

```yaml
log:
  level: info
  sensitivefields:                     # NEW: appended to DefaultFilterConfig
    - pan                               # masks "pan", "PAN", "card_pan"
    - ssn
    - tax_id
```

No Go code changes. The framework reads `cfg.Log.SensitiveFields` during `Builder.CreateLogger`, merges it into `DefaultFilterConfig().SensitiveFields`, and threads the resulting filter through `logger.NewWithFilter(...)` before any module's `Init(deps)` runs. Every default name remains in effect; your custom entries are appended.

**Seam 2 — `app.Options.LoggerFilterConfig` (full replacement, code-level):**

Use this when YAML can't express what you need:

- Custom `MaskValue` (e.g., `[REDACTED]`, `<hidden>`, vendor-specific).
- Opting out of every default field (testing, deterministic-output fixtures).
- Composing the list at startup from a secret manager, feature flag, or remote config.
- Different policies per deployment compiled from one binary.

```go
import (
    "github.com/gaborage/go-bricks/app"
    "github.com/gaborage/go-bricks/logger"
)

// Common pattern: extend defaults + override mask value.
base := logger.DefaultFilterConfig()
base.SensitiveFields = append(base.SensitiveFields,
    "pan", "ssn", "tax_id",
)
base.MaskValue = "[REDACTED]"

fw, _, err := app.NewWithOptions(&app.Options{
    LoggerFilterConfig: base,
})
if err != nil { log.Fatal(err) }
fw.RegisterModule(myModule)
log.Fatal(fw.Run())

// Opt-out variant (no masking at all — use only for test fixtures or
// environments where structured logs are sandboxed):
fw, _, err = app.NewWithOptions(&app.Options{
    LoggerFilterConfig: &logger.FilterConfig{SensitiveFields: nil},
})
```

**Precedence** when both are set: `Options.LoggerFilterConfig` wins. The YAML `log.sensitivefields` value is **ignored entirely** in that case (no silent merge — the consumer is in full control). Mention this in your runbook if your deployment pattern mixes both.

### Matching semantics — what gets masked

- **Field names**, not field values. The filter never scans string contents for PAN-shaped digit sequences or Luhn-valid numbers. Value scanning is a defense-in-depth concern that belongs in application code (see *Defense in depth*, below).
- **Case-insensitive substring**. `pan` matches `pan`, `PAN`, `Pan`, `card_pan`, `primary_account_number`. This is intentional — it survives typos, naming-convention drift, and underscored-vs-camelCase variants in different modules.
- **Recursive into structures**. All log-event methods are covered — `Str`, `Int`, `Int64`, `Uint64`, `Dur`, `Bytes`, and `Interface(...)` — as well as nested `map[string]any`, `map[string]string`, `http.Header` (`map[string][]string`), struct fields (using `json` tags when present), and slice/array elements. Recursion is bounded (`logger.DefaultMaxDepth = 8`) and cycle-safe (visited pointer set). Depth exhaustion fails **closed** — values past the depth limit are replaced with the mask rather than logged verbatim.
- **URLs are masked in full, not partially**. A masked field whose value is an HTTP/AMQP URL (e.g. `database_url`, `broker_url`) is replaced with the default mask value (`***`) in its entirety — host, path, query string, and fragment included, not just the `user:password@host` component. Query strings and fragments routinely carry the secret itself, so partial masking would leave it exposed.

### What this does *not* do

- **No content-pattern scanning.** A PAN embedded in a free-text error message (e.g., `errors.New("card 4111111111111111 failed")` logged via `log.Err(err)`) is *not* caught. Build a `sensitive.Scrub(...)` helper in your service layer if you need this.
- **No per-tenant policies.** The filter is configured once at bootstrap and applied uniformly to every log line, regardless of tenant context. If different tenants have different masking requirements, you need either separate deployments or a custom logger wrapper at the handler layer.
- **No metric/trace masking.** The filter only intercepts log records. OTel span attributes and metric labels go through different code paths. Treat span attributes as "would I publish this on a dashboard?" — never put a PAN in a span attribute.

### Defense in depth (recommended for PCI workloads)

Field-name masking is *one* layer. A complete PCI-DSS 3.3/3.4/3.5 posture combines it with:

1. **Don't log raw payloads.** Use validated DTOs in handlers, mask at the source: `log.Str("pan_last4", req.PAN[len(req.PAN)-4:])` is safer than relying on field-name masking to catch a `log.Interface("payload", req)` call.
2. **Mask in error wrapping.** When wrapping errors that may include sensitive context, redact before `fmt.Errorf`. Helper pattern: `func MaskPAN(s string) string`.
3. **Scrub free-text values.** Outside of structured logging, regex-scan any `log.Msg("...")` argument for digit sequences with Luhn validity. Implement as a `LogFilter` wrapper if your compliance auditor requires evidence.
4. **Audit your default list.** Run `git grep -E '\.(Str|Int|Int64|Uint64|Dur|Bytes|Interface)\("([^"]+)"' --include='*.go'` periodically. Anything that looks PII-shaped should be in the allowlist. All these typed methods pass through the filter when the field name is sensitive.
5. **Test that masking is active.** Add at least one integration test that emits a sensitive value and asserts the captured log line contains `***` (or your configured mask value). See `logger.TestNewWithFilter` for the pattern.

### Migration notes

- **Apps on the defaults pick up the widened list automatically.** This release added the card-data and PII names in the table above (`cardholder`, `card_number`, `cardnumber`, `primary_account_number`, `cvv`, `cvc`, `track1`, `track2`, `track_data`, `iban`, `otp`) to `DefaultFilterConfig()`. Any deployment that leaves `Options.LoggerFilterConfig` unset gets them: with `cfg.Log.SensitiveFields` also absent the logger calls `NewSensitiveDataFilter(DefaultFilterConfig())` directly, and with it set the custom names are merged *into* the widened defaults (`resolveLoggerFilterConfig`, [`app/app_builder.go`](../app/app_builder.go)). A field that logged a value before now logs `***`, so check log-driven alerts, dashboards, and any test asserting on those field values before upgrading. **A non-nil `Options.LoggerFilterConfig` is unaffected** — it replaces the list, so those deployments keep masking exactly what they enumerated; re-derive the list from `logger.DefaultFilterConfig()` to take the new names.
- **Removing the in-module wrapper anti-pattern**: if your codebase previously wrapped `deps.Logger` per-module to apply a filter, you can delete that wrapper after migrating to YAML or `Options.LoggerFilterConfig`. The bootstrap-level filter covers every framework subsystem; the per-module wrapper covered only your code.
- **Upgrading from v0.30.0**: the wiring change is the constructor (`logger.New` → `logger.NewWithFilter` inside `Builder.CreateLogger`). When called with a `nil` filter config, `NewWithFilter` is byte-for-byte equivalent to the legacy `New` — both resolve to `DefaultFilterConfig()`. No flag, no environment variable, no migration step; the observable difference is the widened default list above.

## Correlation Fields and Exemplars

A log line from a consumed message or an HTTP request can carry three different
identifiers, and they hold **different values by design**:

| field | what it is | when it appears |
| --- | --- | --- |
| `correlation_id` | the framework's cross-service id — what travels as `X-Request-ID`, and what both messaging lanes stamp on every outcome line | always |
| `trace_id` | the OpenTelemetry trace id of the span the line was written under | only when a tracer provider is registered |
| `span_id` | the OpenTelemetry span id | only when a tracer provider is registered |

`correlation_id` is not the OTel `trace_id` and is not meant to be. The framework
mints or forwards its own id so correlation survives with tracing switched off;
the OTel ids exist only while a provider is registered. Inbound identifiers are
validated before either is used ([ADR-070](adr_070_inbound_trace_identifier_validation.md)):
a non-conforming one is discarded and replaced, so a value you sent may not be
the value you see.

**Metric exemplars** link a metric data point back to a trace. The messaging
consume metrics record inside the delivery span, so their data points carry an
exemplar naming that span. Note the consume span is a **root** on both lanes:
re-parenting a consume trace onto its producer is deliberately out of scope, so
an exemplar resolves to a per-message trace containing that one delivery. That is
expected, not a gap.

**One configuration is worth naming**, because it fails quietly:
`observability.enabled: true` with `trace.enabled: false` leaves metrics flowing
while every exemplar is silently dropped and `trace_id`/`span_id` vanish from log
lines — leaving `correlation_id` as the only correlation anywhere. Nothing errors
and nothing warns.

## Custom Metrics

GoBricks exposes `MeterProvider` via `ModuleDeps` for creating application-specific metrics. When `observability.enabled: false`, a no-op provider is used with zero overhead.

**Available in ModuleDeps:**

- `deps.MeterProvider` - OpenTelemetry MeterProvider for creating custom instruments

**Helper Functions (observability/metrics.go):**

- `CreateCounter(meter, name, description)` - Monotonically increasing values (requests, errors)
- `CreateHistogram(meter, name, description)` - Distributions (latency, size)
- `CreateUpDownCounter(meter, name, description)` - Values that increase/decrease (connections, queue depth)

**Pattern:**

1. Store `MeterProvider` in module struct
2. Create instruments in `Init()` (one-time, cached)
3. Record values in business logic with attributes

**Quick Example:**

```go
type OrderModule struct {
    meterProvider metric.MeterProvider
    orderCounter  metric.Int64Counter
}

func (m *OrderModule) Init(deps *app.ModuleDeps) error {
    m.meterProvider = deps.MeterProvider
    if m.meterProvider != nil {
        meter := m.meterProvider.Meter("orders")
        m.orderCounter, _ = observability.CreateCounter(meter, "orders.created.total", "Total orders created")
    }
    return nil
}

func (s *OrderService) CreateOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
    // Record metric with attributes
    if s.orderCounter != nil {
        s.orderCounter.Add(ctx, 1,
            metric.WithAttributes(
                attribute.String("order_type", req.Type),
                attribute.String("status", "success"),
            ),
        )
    }
    // ... business logic
}
```

**Metric Types:**

| Type | Use Case | Example |
| ------ | ---------- | --------- |
| `Int64Counter` | Monotonically increasing counts | Requests, errors, events |
| `Float64Histogram` | Value distributions | Latency (seconds), payload size (bytes) |
| `Int64UpDownCounter` | Values that increase/decrease | Active connections, queue depth |
| `Int64ObservableGauge` | Current state via callback | Memory usage, pool size |

**Best Practices:**

- Pre-create instruments in `Init()` for performance (avoid per-request creation)
- Use semantic naming: `<namespace>.<entity>.<measurement>` (e.g., `orders.processing.duration`)
- Add attributes for dimensions: `status`, `tenant_id`, `operation_type`
- Nil-check instruments when recording (safe when observability disabled)
- Test with no-op provider: `noop.NewMeterProvider()` from `go.opentelemetry.io/otel/metric/noop`

**Real-World Example:** See `scheduler/module.go` for production usage with counter, histogram, and panic tracking.

See [llms.txt](../llms.txt) Custom Metrics section for complete code examples including observable gauges and testing patterns.

## Observability Headers & Authentication

The OTLP exporter reads headers from `observability.trace.headers` (and the equivalent `metrics.headers` / `logs.headers`) in YAML — there is no built-in support for separate `OBSERVABILITY_*_HEADERS_*` env vars. **The header structure goes in YAML; the secret value is rendered into the runtime file before startup** — GoBricks has no `${VAR}` interpolation inside YAML values, so an un-rendered placeholder would be sent to the vendor as-is. Hardcoding API keys or bearer tokens directly in committed YAML is forbidden.

```yaml
# config.production.yaml.tmpl — committed template; rendered to config.production.yaml (gitignored) before startup
observability:
  trace:
    headers:
      api-key: ${NEW_RELIC_API_KEY}   # placeholder — rendered before startup, see observability_headers_auth.md

# UNSAFE — never commit:
#   api-key: "nrak-ABC123..."         # hardcoded secret
```

**Supported vendors:** New Relic, Honeycomb, Datadog, Grafana Cloud, generic Bearer tokens.

For complete configuration examples, security best practices, and vendor-specific headers, see [Headers & Authentication](observability_headers_auth.md).

## New Relic OTLP Integration (Optimized)

GoBricks supports all New Relic OTLP optimizations: gzip compression (~70% bandwidth reduction), delta temporality (~50% memory savings), and exponential histograms (~90% memory savings).

**Endpoint Format Rules (CRITICAL):**

| Protocol | Endpoint Format | Example |
| --- | --- | --- |
| `grpc` | `host:port` (NO scheme) | `otlp.nr-data.net:4317` |
| `http` | `https://host:port/path` | `https://otlp.nr-data.net:4318/v1/traces` |

**Common Mistakes:**

- `https://otlp.nr-data.net:4317` with `protocol: grpc` → ERROR
- `otlp.nr-data.net:4317` with `protocol: grpc` → Correct

For complete gRPC/HTTP configs, port 443 alternatives, and performance benchmarks, see [New Relic OTLP](new_relic_otlp.md).

## OpenTelemetry Collector (Recommended for Production)

For high-volume production, use an OTEL Collector as a vendor-agnostic proxy. Benefits: advanced retry/buffering, multi-backend support, data transformation, no vendor lock-in.

**Deployment patterns:** Sidecar (per-pod), DaemonSet (per-node), Gateway (centralized). When using a collector, the GoBricks app points to the collector endpoint with `insecure: true` and no vendor headers.

For deployment patterns, collector configs, and when-to-use guidance, see [OTEL Collector](otel_collector.md).
