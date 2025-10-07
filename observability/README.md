# Observability Package

The observability package provides production-ready distributed tracing using OpenTelemetry with support for multiple exporters including OTLP (HTTP/gRPC) and stdout.

## Features

- **Flexible Exporters**: stdout for development, OTLP for production
- **Protocol Support**: Both OTLP/HTTP and OTLP/gRPC protocols
- **Configurable Sampling**: Control trace collection rate (0.0 - 1.0)
- **Custom Headers**: Support for authentication tokens and custom headers
- **Resource Attributes**: Automatic service name, version, and environment tagging
- **Graceful Shutdown**: Proper flushing of pending telemetry data
- **No-op Mode**: Zero overhead when observability is disabled

## Quick Start

### Development with stdout Exporter

```yaml
# config.yaml
observability:
  enabled: true
  service:
    name: "my-service"
    version: "1.0.0"
  environment: "development"
  trace:
    enabled: true
    endpoint: "stdout"
    sample:
      rate: 1.0
```

```go
package main

import (
    "context"
    "github.com/gaborage/go-bricks/config"
    "github.com/gaborage/go-bricks/observability"
)

func main() {
    // Load configuration
    cfg := config.New()
    var obsCfg observability.Config
    if err := cfg.InjectInto(&obsCfg); err != nil {
        panic(err)
    }

    // Initialize observability provider
    provider, err := observability.NewProvider(&obsCfg)
    if err != nil {
        panic(err)
    }
    defer observability.Shutdown(provider, 5)

    // Create tracer
    tracer := provider.TracerProvider().Tracer("my-service")

    // Create span
    ctx, span := tracer.Start(context.Background(), "operation")
    defer span.End()

    // Your business logic here
}
```

## Production Configuration

### OTLP HTTP Exporter

OTLP/HTTP is recommended for production environments with HTTP-based observability backends like Grafana Cloud, Honeycomb, or self-hosted collectors.

```yaml
# config.production.yaml
observability:
  enabled: true
  service:
    name: "my-service"
    version: "1.2.3"
  environment: "production"
  trace:
    enabled: true
    endpoint: "otel-collector.monitoring.svc.cluster.local:4318"
    protocol: "http"
    insecure: false  # Use TLS in production
    headers:
      Authorization: "Bearer ${OTEL_API_KEY}"
    sample:
      rate: 0.1  # Sample 10% of traces
    batch:
      timeout: "5s"
    export:
      timeout: "30s"
    max:
      queue:
        size: 2048
      batch:
        size: 512
```

**Environment variables:**
```bash
export OTEL_API_KEY="your-api-key-here"
```

### OTLP gRPC Exporter

OTLP/gRPC provides better performance and is ideal for high-throughput services.

```yaml
# config.production.yaml
observability:
  enabled: true
  service:
    name: "my-service"
    version: "1.2.3"
  environment: "production"
  trace:
    enabled: true
    endpoint: "otel-collector.monitoring.svc.cluster.local:4317"
    protocol: "grpc"
    insecure: false  # Use TLS in production
    headers:
      x-api-key: "${OTEL_API_KEY}"
    sample:
      rate: 0.25  # Sample 25% of traces
```

### Cloud Provider Examples

#### Grafana Cloud

```yaml
observability:
  trace:
    endpoint: "otlp-gateway-prod-us-central-0.grafana.net:443"
    protocol: "grpc"
    insecure: false
    headers:
      authorization: "Basic ${GRAFANA_CLOUD_TOKEN}"
```

#### Honeycomb

```yaml
observability:
  trace:
    endpoint: "api.honeycomb.io:443"
    protocol: "grpc"
    insecure: false
    headers:
      x-honeycomb-team: "${HONEYCOMB_API_KEY}"
```

#### Jaeger (self-hosted)

```yaml
observability:
  trace:
    endpoint: "jaeger-collector:4318"
    protocol: "http"
    insecure: true  # Or false with TLS
```

## Configuration Reference

### Top-Level Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `enabled` | bool | `false` | Enable/disable observability (no-op when false) |
| `service.name` | string | (required) | Service name for trace identification |
| `service.version` | string | `"unknown"` | Service version for filtering/grouping |
| `environment` | string | `"development"` | Deployment environment (production, staging, etc.) |

### Trace Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `trace.enabled` | bool | `true` | Enable/disable tracing |
| `trace.endpoint` | string | `"stdout"` | Endpoint for trace export (stdout, http, grpc) |
| `trace.protocol` | string | `"http"` | OTLP protocol: "http" or "grpc" |
| `trace.insecure` | bool | `true` | Use insecure connection (no TLS) |
| `trace.headers` | map[string]string | - | Custom headers for authentication |
| `trace.sample.rate` | float64 | `1.0` | Sampling rate (0.0 = none, 1.0 = all) |
| `trace.batch.timeout` | duration | `5s` | Time to wait before sending batch |
| `trace.export.timeout` | duration | `30s` | Maximum time for export operation |
| `trace.max.queue.size` | int | `2048` | Maximum buffered spans |
| `trace.max.batch.size` | int | `512` | Maximum spans per batch |

## Advanced Usage

### Custom Provider Configuration

```go
import "github.com/gaborage/go-bricks/observability"

// Create custom configuration
cfg := &observability.Config{
    Enabled:     true,
    ServiceName: "my-service",
    ServiceVersion: "1.0.0",
    Environment: "production",
    Trace: observability.TraceConfig{
        Enabled:  true,
        Endpoint: "localhost:4318",
        Protocol: "http",
        Insecure: true,
        Headers: map[string]string{
            "Authorization": "Bearer token",
        },
        SampleRate:    0.1,
        BatchTimeout:  5 * time.Second,
        ExportTimeout: 30 * time.Second,
        MaxQueueSize:  2048,
        MaxBatchSize:  512,
    },
}

// Initialize provider
provider, err := observability.NewProvider(cfg)
if err != nil {
    panic(err)
}
defer observability.Shutdown(provider, 5)
```

### Graceful Shutdown

```go
import "github.com/gaborage/go-bricks/observability"

// Shutdown with custom timeout (in seconds)
observability.Shutdown(provider, 10)

// Or panic on shutdown failure
observability.MustShutdown(provider, 10)

// Manual shutdown with context
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := provider.Shutdown(ctx); err != nil {
    log.Printf("Failed to shutdown observability: %v", err)
}
```

### Force Flush

```go
// Flush pending telemetry before critical operations
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
if err := provider.ForceFlush(ctx); err != nil {
    log.Printf("Failed to flush telemetry: %v", err)
}
```

## Performance Tuning

### High-Throughput Services

For services with high request rates, reduce overhead:

```yaml
observability:
  trace:
    sample:
      rate: 0.01  # Sample 1% of traces
    batch:
      timeout: "10s"  # Larger batches
    max:
      batch:
        size: 2048  # More spans per batch
```

### Low-Latency Services

For latency-sensitive services, reduce export latency:

```yaml
observability:
  trace:
    batch:
      timeout: "1s"  # Faster export
    max:
      batch:
        size: 128  # Smaller batches
```

## Troubleshooting

### No Traces Appearing

1. **Check observability is enabled**: `observability.enabled: true`
2. **Verify service name**: `observability.service.name` is set
3. **Check sampling rate**: `trace.sample.rate > 0.0`
4. **Verify endpoint**: OTLP collector is reachable
5. **Check logs**: Look for initialization or export errors

### Connection Errors

```
Error: failed to initialize trace provider: failed to create trace exporter
```

**Solutions:**
- Verify endpoint is correct (hostname:port)
- Check protocol matches endpoint (HTTP=4318, gRPC=4317)
- Ensure firewall allows outbound connections
- For TLS, verify certificates or set `insecure: true` for testing

### Memory Issues

If observability consumes too much memory:

```yaml
observability:
  trace:
    max:
      queue:
        size: 512  # Reduce buffer size
    sample:
      rate: 0.05  # Lower sampling rate
```

## Best Practices

1. **Use stdout in development** - Easy debugging without external dependencies
2. **Use OTLP in production** - Industry standard with broad backend support
3. **Sample in production** - Reduce costs while maintaining visibility
4. **Set proper service.name** - Makes filtering and grouping easier
5. **Use environment tags** - Differentiate prod/staging/dev traces
6. **Implement graceful shutdown** - Ensure all traces are exported
7. **Monitor export errors** - Set up alerts for failed exports
8. **Secure credentials** - Use environment variables for API keys

## Integration with GoBricks

The observability package integrates seamlessly with other GoBricks components:

- **HTTP Server**: Automatic trace propagation via W3C traceparent headers
- **Database**: Database query spans (when using instrumented drivers)
- **Messaging**: AMQP message tracing (when configured)
- **Configuration**: Full support for config injection via `InjectInto()`

See the [go-bricks-demo-project](https://github.com/gaborage/go-bricks-demo-project) for complete examples.
