# New Relic OTLP Integration (Optimized)

GoBricks supports all New Relic OTLP optimizations for bandwidth reduction and performance.

`${NEW_RELIC_API_KEY}` below is a placeholder, not interpolated by GoBricks — render it into the runtime file before startup; see [Headers & Authentication](observability_headers_auth.md).

## Complete New Relic Configuration (gRPC - Recommended)

```yaml
# config.production.yaml.tmpl — committed template; rendered to config.production.yaml (gitignored) before startup
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
      api-key: ${NEW_RELIC_API_KEY}

  # Metrics with New Relic optimizations
  metrics:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # Reuse trace endpoint
    protocol: grpc
    compression: gzip
    temporality: delta  # New Relic recommendation (lower memory, better performance)
    histogramaggregation: exponential  # Better precision, ~10x lower memory
    headers:
      api-key: ${NEW_RELIC_API_KEY}

  # Logs with gRPC
  logs:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # Reuse trace endpoint
    protocol: grpc
    compression: gzip
    samplingrate: 0.1  # Export 10% of INFO/DEBUG logs (ERROR/WARN always exported)
    headers:
      api-key: ${NEW_RELIC_API_KEY}
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
      api-key: ${NEW_RELIC_API_KEY}

  metrics:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/metrics  # Signal-specific path required
    protocol: http
    compression: gzip
    temporality: delta
    histogramaggregation: exponential
    headers:
      api-key: ${NEW_RELIC_API_KEY}

  logs:
    enabled: true
    endpoint: https://otlp.nr-data.net:4318/v1/logs  # Signal-specific path required
    protocol: http
    compression: gzip
    headers:
      api-key: ${NEW_RELIC_API_KEY}
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
| -------- | -------- | --------- | -------------------------- |
| `compression` | `gzip`, `none` | `gzip` | **gzip** (~70% bandwidth reduction) |
| `temporality` | `delta`, `cumulative` | `cumulative` | **delta** (lower memory, better performance) |
| `histogramaggregation` | `exponential`, `explicit` | `explicit` | **exponential** (better precision, ~10x lower memory) |
| `protocol` | `http`, `grpc` | `http` | **grpc** (lower latency, better performance) |

## Attribute Limits

New Relic enforces attribute limits on its ingest side, but be aware of:

- **Maximum attributes per span/metric/log:** 255 attributes
- **Maximum attribute value size:** 4095 bytes
- **Truncation behavior:** GoBricks does not validate or truncate attributes before export — oversized or excess attributes may be silently dropped by New Relic, not by GoBricks

## Performance Impact

| Feature | Bandwidth Savings | Memory Savings | Notes |
| --------- | ------------------- | ---------------- | ------- |
| gzip compression | ~70% | N/A | CPU overhead ~1-2ms per batch |
| Delta temporality | N/A | ~50% | Resets counters after each export |
| Exponential histograms | ~30% | ~90% | MaxSize=160, MaxScale=20 (auto-configured) |

## Endpoint Format Rules (CRITICAL)

| Protocol | Endpoint Format | Example | TLS |
| ---------- | ----------------- | --------- | ----- |
| `grpc` | `host:port` (NO scheme) | `otlp.nr-data.net:4317` | Enabled by default (TLS is the default for gRPC unless `insecure: true` is set — independent of port) |
| `grpc` (insecure) | `host:port` + `insecure: true` | `localhost:4317` | Disabled |
| `http` | `https://host:port/path` | `https://otlp.nr-data.net:4318/v1/traces` | Enabled |
| `http` (insecure) | `http://host:port/path` + `insecure: true` | `http://localhost:4318/v1/traces` | Disabled |

> GoBricks strips the URL scheme from `endpoint` before configuring the HTTP exporter and derives TLS solely from the `insecure` field — not from `http://` vs `https://`. Setting `endpoint: http://...` without `insecure: true` still attempts a TLS handshake.

## Common Mistakes

- ❌ `https://otlp.nr-data.net:4317` with `protocol: grpc` → **ERROR: `ErrInvalidEndpointFormat` (fails at startup validation, not a network dial error)**
- ❌ `otlp.nr-data.net:4318` with `protocol: http` → **ERROR: `ErrInvalidEndpointFormat` (fails at startup validation, not a network dial error)**
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
