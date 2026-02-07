# New Relic OTLP Integration (Optimized)

GoBricks supports all New Relic OTLP optimizations for bandwidth reduction and performance.

## Complete New Relic Configuration (gRPC - Recommended)

```yaml
# config.production.yaml
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  # Traces with gRPC (recommended by New Relic)
  trace:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # gRPC port (NO https:// prefix)
    protocol: grpc
    insecure: false  # TLS required for New Relic
    compression: gzip  # ~70% bandwidth reduction
    headers:
      api-key: your-new-relic-license-key-here

  # Metrics with New Relic optimizations
  metrics:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # Reuse trace endpoint
    protocol: grpc
    compression: gzip
    temporality: delta  # New Relic recommendation (lower memory, better performance)
    histogram_aggregation: exponential  # Better precision, ~10x lower memory
    headers:
      api-key: your-new-relic-license-key-here

  # Logs with gRPC
  logs:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # Reuse trace endpoint
    protocol: grpc
    compression: gzip
    sampling_rate: 0.1  # Export 10% of INFO/DEBUG logs (ERROR/WARN always exported)
    headers:
      api-key: your-new-relic-license-key-here
```

## New Relic HTTP Alternative (Port 4318)

```yaml
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  trace:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/traces  # HTTP requires https:// + path
    protocol: http
    compression: gzip
    headers:
      api-key: your-license-key

  metrics:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/metrics  # Signal-specific path required
    protocol: http
    compression: gzip
    temporality: delta
    histogram_aggregation: exponential
    headers:
      api-key: your-license-key

  logs:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/logs  # Signal-specific path required
    protocol: http
    compression: gzip
    headers:
      api-key: your-license-key
```

## New Relic Port 443 Alternative

New Relic supports both gRPC and HTTP on port 443 (default HTTPS port). This simplifies firewall rules:

```yaml
# gRPC on port 443
trace:
  endpoint: otlp.nr-data.net:443  # Explicit port 443 for gRPC
  protocol: grpc

# HTTP on port 443 (port implicit when using https://)
trace:
  endpoint: https://otlp.nr-data.net/v1/traces  # Port 443 implicit
  protocol: http
```

## Configuration Options Explained

| Option | Values | Default | New Relic Recommendation |
|--------|--------|---------|--------------------------|
| `compression` | `gzip`, `none` | `gzip` | **gzip** (~70% bandwidth reduction) |
| `temporality` | `delta`, `cumulative` | `cumulative` | **delta** (lower memory, better performance) |
| `histogram_aggregation` | `exponential`, `explicit` | `explicit` | **exponential** (better precision, ~10x lower memory) |
| `protocol` | `http`, `grpc` | `http` | **grpc** (lower latency, better performance) |

## Attribute Limits

GoBricks automatically handles New Relic's attribute limits, but be aware of:
- **Maximum attributes per span/metric/log:** 255 attributes
- **Maximum attribute value size:** 4095 bytes
- **Truncation behavior:** Attributes exceeding limits are dropped with warning logs

## Performance Impact

| Feature | Bandwidth Savings | Memory Savings | Notes |
|---------|-------------------|----------------|-------|
| gzip compression | ~70% | N/A | CPU overhead ~1-2ms per batch |
| Delta temporality | N/A | ~50% | Resets counters after each export |
| Exponential histograms | ~30% | ~90% | MaxSize=160, MaxScale=20 (auto-configured) |

## Endpoint Format Rules (CRITICAL)

| Protocol | Endpoint Format | Example | TLS |
|----------|-----------------|---------|-----|
| `grpc` | `host:port` (NO scheme) | `otlp.nr-data.net:4317` | Auto-enabled for 4317 |
| `grpc` (insecure) | `host:port` + `insecure: true` | `localhost:4317` | Disabled |
| `http` | `https://host:port/path` | `https://otlp.nr-data.net:4318/v1/traces` | Enabled |
| `http` (insecure) | `http://host:port/path` | `http://localhost:4318/v1/traces` | Disabled |

## Common Mistakes

- ❌ `https://otlp.nr-data.net:4317` with `protocol: grpc` → **ERROR: "too many colons in address"**
- ❌ `otlp.nr-data.net:4318` with `protocol: http` → **ERROR: missing scheme**
- ✅ `otlp.nr-data.net:4317` with `protocol: grpc` → **Correct**
- ✅ `https://otlp.nr-data.net:4318/v1/traces` with `protocol: http` → **Correct**

## Insecure gRPC Example (localhost)

```yaml
trace:
  endpoint: localhost:4317  # No https://
  protocol: grpc
  insecure: true  # Disable TLS for local testing
```

**Validation:** GoBricks validates endpoint format at startup (fail-fast). Invalid combinations return `ErrInvalidEndpointFormat`.
