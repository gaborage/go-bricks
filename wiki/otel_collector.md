# OpenTelemetry Collector (Recommended for Production)

The OpenTelemetry Collector is a **vendor-agnostic proxy** that sits between your application and observability backends. GoBricks supports direct export to vendors (New Relic, Datadog, Honeycomb), but using a collector provides significant production benefits.

## Why Use a Collector?

| Feature | Direct Export | With OTEL Collector |
|---------|---------------|---------------------|
| **Retry Logic** | Limited (SDK retries 5x) | Advanced retry with exponential backoff |
| **Buffering** | Small in-memory buffers | Large disk-backed queues |
| **Batching** | Fixed batch sizes | Dynamic batching based on backend load |
| **Vendor Lock-In** | Coupled to vendor | Change backends without code changes |
| **Resource Usage** | App memory/CPU for retries | Offloaded to collector |
| **Multi-Backend** | Not supported | Send to multiple backends simultaneously |
| **Data Transformation** | Not supported | Filter, sample, enrich telemetry data |

## Common Deployment Patterns

### 1. Sidecar Pattern (Kubernetes)

Each application pod runs a collector sidecar:

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: my-service
spec:
  template:
    spec:
      containers:
      - name: app
        image: my-service:v1.0.0
        env:
        - name: OBSERVABILITY_TRACE_ENDPOINT
          value: "localhost:4317"  # Collector sidecar
        - name: OBSERVABILITY_TRACE_PROTOCOL
          value: "grpc"
      - name: otel-collector
        image: otel/opentelemetry-collector-contrib:0.96.0
        ports:
        - containerPort: 4317  # gRPC receiver
        - containerPort: 4318  # HTTP receiver
        volumeMounts:
        - name: collector-config
          mountPath: /etc/otelcol
      volumes:
      - name: collector-config
        configMap:
          name: otel-collector-config
```

**Benefits:** Simple network configuration (localhost), isolated per service, automatic scaling with pods

### 2. DaemonSet Pattern (Kubernetes)

One collector per node, shared by all pods on that node:

```yaml
# GoBricks config (points to node-local collector)
observability:
  trace:
    endpoint: ${NODE_IP}:4317  # DaemonSet collector on host network
    protocol: grpc
```

**Benefits:** Lower resource overhead, simplified configuration, node-level batching

### 3. Gateway Pattern (High Volume)

Central collector cluster with load balancing:

```yaml
# GoBricks config (points to load balancer)
observability:
  trace:
    endpoint: otel-gateway.monitoring.svc.cluster.local:4317
    protocol: grpc
    compression: gzip  # Reduce network traffic to gateway
```

**Benefits:** Centralized retry/buffering, horizontal scaling, advanced processing pipelines

## Minimal Collector Configuration (New Relic)

```yaml
# otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318

processors:
  batch:
    timeout: 10s
    send_batch_size: 1024
    send_batch_max_size: 2048

exporters:
  otlp:
    endpoint: otlp.nr-data.net:4317
    compression: gzip
    headers:
      api-key: ${NEW_RELIC_LICENSE_KEY}
    retry_on_failure:
      enabled: true
      initial_interval: 5s
      max_interval: 30s
      max_elapsed_time: 300s

service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]
    logs:
      receivers: [otlp]
      processors: [batch]
      exporters: [otlp]
```

## When to Use Collector

| Scenario | Recommendation |
|----------|----------------|
| **Development/Staging** | Direct export (simpler, faster iteration) |
| **Production (low volume)** | Direct export with compression (simplicity > overhead) |
| **Production (high volume)** | Collector (retry logic, buffering critical) |
| **Multi-region** | Collector per region (reduces cross-region latency) |
| **Multi-vendor** | Collector (send to multiple backends without app changes) |
| **Compliance/Filtering** | Collector (PII scrubbing, data sampling) |

## GoBricks Configuration with Collector

```yaml
# Application points to collector, collector handles vendor specifics
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  trace:
    endpoint: otel-collector:4317  # Collector endpoint
    protocol: grpc
    insecure: true  # Collector often on same network (no TLS needed)
    compression: gzip  # Reduce app->collector network traffic
    # NO headers needed - collector handles vendor auth

  metrics:
    enabled: true
    endpoint: otel-collector:4317
    protocol: grpc
    compression: gzip
    temporality: delta  # Collector can convert if needed
    histogram_aggregation: exponential

  logs:
    enabled: true
    endpoint: otel-collector:4317
    protocol: grpc
    compression: gzip
```

## Resources

- [OpenTelemetry Collector Documentation](https://opentelemetry.io/docs/collector/)
- [Collector Configuration Reference](https://opentelemetry.io/docs/collector/configuration/)
- [Collector Contrib Distributions](https://github.com/open-telemetry/opentelemetry-collector-contrib) (vendor-specific exporters)
