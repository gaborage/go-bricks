# Observability (Deep Dive)

GoBricks provides production-grade observability built on OpenTelemetry: distributed tracing with W3C traceparent propagation, runtime and custom metrics, structured logging with dual-mode export, and health endpoints. This page captures the framework-level details, the custom metrics API exposed to modules, and pointers to the vendor-specific configuration guides.

## Observability

**Key Features:** W3C traceparent propagation, OpenTelemetry metrics (database/HTTP/AMQP/Go runtime), health endpoints (`/health`, `/ready`), dual-mode logging with conditional sampling, environment-aware batching (500ms dev, 5s prod), environment-aware export timeouts (10s dev, 60s prod)

**Go Runtime Metrics:** Auto-exports memory, goroutines, CPU, scheduler latency, GC config when `observability.enabled: true`. Follows [OpenTelemetry semantic conventions](https://opentelemetry.io/docs/specs/semconv/runtime/go-metrics/)

**Export Timeout Configuration:** GoBricks uses environment-aware export timeouts to balance fail-fast feedback (development) with network resilience (production):
- **Development/stdout:** 10s (quick failure detection for debugging)
- **Production:** 60s (accommodates network latency, TLS handshake, batch transmission)
- **Override via YAML:** `observability.trace.export.timeout: "90s"` (applies to traces/metrics/logs)
- **Why 60s?** Real-world production scenarios involve cross-region latency, TLS negotiation, and 512-span batch transmission to remote OTLP endpoints

**Dual-Mode Logging:** `DualModeLogProcessor` routes logs by `log.type`:
- **Action logs** (`log.type="action"`): Always exported at 100% (request summaries)
- **Trace logs** (`log.type="trace"`): ERROR/WARN always exported, INFO/DEBUG sampled by `sampling_rate`
- Configure via `observability.logs.sampling_rate` (0.0-1.0, default 0.0 drops INFO/DEBUG)
- Sampling is deterministic per trace (all logs in same trace sampled together)

**Request Logging:** HTTP requests track severity escalation via `requestLogContext`. Automatic escalation from status codes (4xx→WARN, 5xx→ERROR). Explicit: `server.EscalateSeverity(c, zerolog.WarnLevel)`. Configure `observability.logs.slow_request_threshold` for slow request detection.

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

**Common Issues:** Spans not appearing (check `observability.enabled`, wait for batch timeout), logs not exported (verify `observability.logs.enabled`, set `log.pretty: false`), pretty mode conflict (fails fast at startup). See the Troubleshooting section in [CLAUDE.md](../CLAUDE.md#troubleshooting) for details

**Log format selection (`log.output.format`):** Defaults to `auto`, which resolves to console (colored) output when stdout is a terminal AND OTLP log export is not active; otherwise structured JSON. Explicit values: `console` / `pretty` (always colored), `json` / `structured` (always JSON). The legacy `log.pretty: true` still works and overrides `log.output.format`. Combining pretty output with `observability.logs.enabled: true` still panics at startup — `auto` is the safe default that keeps local dev colored and production JSON without manual configuration.

## Sensitive Data Filtering

Every log line emitted via the framework logger passes through a `logger.SensitiveDataFilter` that masks values whose **field names** match an allowlist (case-insensitive substring). The filter is applied uniformly — including in the framework's own request/response middleware, AMQP consumer panic recovery, slow-request warnings, scheduler job traces, and any module-level `log.Info()/Error()/...` call. There is no opt-in surface to wrap "after the fact" — the filter is wired into the logger before any subsystem captures a reference to it.

### Default field list

`logger.DefaultFilterConfig()` (in [`logger/filter.go`](../logger/filter.go)) ships these names (all matched case-insensitive substring, so `password` matches `Password`, `db_password`, `oldPasswordHash`, etc.):

| Category | Default names |
|---|---|
| Credentials | `password`, `passwd`, `pwd`, `secret`, `key`, `api_key`, `apikey`, `token`, `access_token`, `refresh_token` |
| Auth headers | `auth`, `authorization` |
| Generic | `credential`, `credentials` |
| Connection strings | `broker_url`, `database_url`, `db_url` |

URLs in masked fields keep host/path but strip the password: `https://user:pwd@host/path?token=x` → `https://user:***@host/path?token=x`. The default mask value is `***`.

### Extending the filter (two seams)

For regulated payloads — PCI-DSS (PAN, CVV2), PII (SSN, account numbers), one-time passwords — extend the list at bootstrap. Both seams are additive-only changes; existing apps see no behavior difference until they opt in.

**Seam 1 — YAML config (recommended for static lists):**

```yaml
log:
  level: info
  sensitive_fields:                     # NEW: appended to DefaultFilterConfig
    - pan                               # masks "pan", "PAN", "card_pan"
    - primary_account_number            # masks the long-form variant too
    - cvv
    - cvv2
    - cvc2
    - otp
    - one_time_password
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
    "pan", "primary_account_number", "cvv", "cvv2", "otp",
)
base.MaskValue = "[REDACTED]"

fw, err := app.NewWithOptions(&app.Options{
    LoggerFilterConfig: base,
})
if err != nil { log.Fatal(err) }
fw.RegisterModules(myModule)
log.Fatal(fw.Run())

// Opt-out variant (no masking at all — use only for test fixtures or
// environments where structured logs are sandboxed):
fw, err = app.NewWithOptions(&app.Options{
    LoggerFilterConfig: &logger.FilterConfig{SensitiveFields: nil},
})
```

**Precedence** when both are set: `Options.LoggerFilterConfig` wins. The YAML `log.sensitive_fields` value is **ignored entirely** in that case (no silent merge — the consumer is in full control). Mention this in your runbook if your deployment pattern mixes both.

### Matching semantics — what gets masked

- **Field names**, not field values. The filter never scans string contents for PAN-shaped digit sequences or Luhn-valid numbers. Value scanning is a defense-in-depth concern that belongs in application code (see *Defense in depth*, below).
- **Case-insensitive substring**. `pan` matches `pan`, `PAN`, `Pan`, `card_pan`, `primary_account_number`. This is intentional — it survives typos, naming-convention drift, and underscored-vs-camelCase variants in different modules.
- **Recursive into structures**. Direct `Str/Int/Interface(...)` fields, nested `map[string]any`, struct fields (using `json` tags when present), and slice/array elements are all walked. Recursion is bounded (`logger.DefaultMaxDepth = 8`) and cycle-safe (visited pointer set).
- **URLs as a special case**. If a masked field's value is an HTTP/AMQP URL with `user:password@host`, only the password component is replaced — the rest of the URL stays readable. This keeps `database_url` and `broker_url` actionable in error logs.

### What this does *not* do

- **No content-pattern scanning.** A PAN embedded in a free-text error message (e.g., `errors.New("card 4111111111111111 failed")` logged via `log.Err(err)`) is *not* caught. Build a `sensitive.Scrub(...)` helper in your service layer if you need this.
- **No per-tenant policies.** The filter is configured once at bootstrap and applied uniformly to every log line, regardless of tenant context. If different tenants have different masking requirements, you need either separate deployments or a custom logger wrapper at the handler layer.
- **No metric/trace masking.** The filter only intercepts log records. OTel span attributes and metric labels go through different code paths. Treat span attributes as "would I publish this on a dashboard?" — never put a PAN in a span attribute.

### Defense in depth (recommended for PCI workloads)

Field-name masking is *one* layer. A complete PCI-DSS 3.3/3.4/3.5 posture combines it with:

1. **Don't log raw payloads.** Use validated DTOs in handlers, mask at the source: `log.Str("pan_last4", req.PAN[len(req.PAN)-4:])` is safer than relying on field-name masking to catch a `log.Interface("payload", req)` call.
2. **Mask in error wrapping.** When wrapping errors that may include sensitive context, redact before `fmt.Errorf`. Helper pattern: `func MaskPAN(s string) string`.
3. **Scrub free-text values.** Outside of structured logging, regex-scan any `log.Msg("...")` argument for digit sequences with Luhn validity. Implement as a `LogFilter` wrapper if your compliance auditor requires evidence.
4. **Audit your default list.** Run `git grep -E 'Str|Int64|Interface\("([^"]+)"' --include='*.go'` periodically. Anything that looks PII-shaped should be in the allowlist.
5. **Test that masking is active.** Add at least one integration test that emits a sensitive value and asserts the captured log line contains `***` (or your configured mask value). See `logger.TestNewWithFilter` for the pattern.

### Migration notes

- **Existing apps see no change** until they set either seam. The framework's logger continues to call `NewSensitiveDataFilter(DefaultFilterConfig())` internally when both `Options.LoggerFilterConfig` and `cfg.Log.SensitiveFields` are absent.
- **Removing the in-module wrapper anti-pattern**: if your codebase previously wrapped `deps.Logger` per-module to apply a filter, you can delete that wrapper after migrating to YAML or `Options.LoggerFilterConfig`. The bootstrap-level filter covers every framework subsystem; the per-module wrapper covered only your code.
- **Upgrading from v0.30.0**: the only behavioral change is the constructor (`logger.New` → `logger.NewWithFilter` inside `Builder.CreateLogger`). When called with a `nil` filter config, `NewWithFilter` is byte-for-byte equivalent to the legacy `New`. No flag, no environment variable, no migration step.

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
|------|----------|---------|
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

The OTLP exporter reads headers from `observability.trace.headers` (and the equivalent `metrics.headers` / `logs.headers`) in YAML — there is no built-in support for separate `OBSERVABILITY_*_HEADERS_*` env vars. **The header structure goes in YAML; the secret values come from environment variables via Koanf substitution** (the same pattern used elsewhere in `config.example.yaml`). Hardcoding API keys or bearer tokens directly in committed YAML is forbidden.

```yaml
# config.production.yaml — SAFE: header structure in YAML, value from env
observability:
  trace:
    headers:
      api-key: ${NEW_RELIC_API_KEY}   # resolved at startup from environment

# UNSAFE — never commit:
#   api-key: "nrak-ABC123..."         # hardcoded secret
```

**Supported vendors:** New Relic, Honeycomb, Datadog, Grafana Cloud, generic Bearer tokens.

For complete configuration examples, security best practices, and vendor-specific headers, see [Headers & Authentication](observability-headers-auth.md).

## New Relic OTLP Integration (Optimized)

GoBricks supports all New Relic OTLP optimizations: gzip compression (~70% bandwidth reduction), delta temporality (~50% memory savings), and exponential histograms (~90% memory savings).

**Endpoint Format Rules (CRITICAL):**

| Protocol | Endpoint Format | Example |
|----------|-----------------|---------|
| `grpc` | `host:port` (NO scheme) | `otlp.nr-data.net:4317` |
| `http` | `https://host:port/path` | `https://otlp.nr-data.net:4318/v1/traces` |

**Common Mistakes:**
- `https://otlp.nr-data.net:4317` with `protocol: grpc` → ERROR
- `otlp.nr-data.net:4317` with `protocol: grpc` → Correct

For complete gRPC/HTTP configs, port 443 alternatives, and performance benchmarks, see [New Relic OTLP](new-relic-otlp.md).

## OpenTelemetry Collector (Recommended for Production)

For high-volume production, use an OTEL Collector as a vendor-agnostic proxy. Benefits: advanced retry/buffering, multi-backend support, data transformation, no vendor lock-in.

**Deployment patterns:** Sidecar (per-pod), DaemonSet (per-node), Gateway (centralized). When using a collector, the GoBricks app points to the collector endpoint with `insecure: true` and no vendor headers.

For deployment patterns, collector configs, and when-to-use guidance, see [OTEL Collector](otel-collector.md).
