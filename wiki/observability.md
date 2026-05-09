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

**Common Issues:** Spans not appearing (check `observability.enabled`, wait for batch timeout), logs not exported (verify `observability.logs.enabled`, set `logger.pretty: false`), pretty mode conflict (fails fast at startup). See the Troubleshooting section in [CLAUDE.md](../CLAUDE.md#troubleshooting) for details

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
