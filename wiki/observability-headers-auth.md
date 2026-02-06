# Observability Headers & Authentication

**IMPORTANT:** Headers (API keys, bearer tokens) MUST be configured in YAML files, NOT via environment variables. GoBricks does not support `OBSERVABILITY_*_HEADERS_*` env vars.

## Recommended Approach (Production)

Create environment-specific config files with headers:

```yaml
# config.production.yaml (DO NOT commit - add to .gitignore)
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  # Traces
  traces:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: your-new-relic-license-key-here

  # Metrics (reuses traces endpoint)
  metrics:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: your-new-relic-license-key-here

  # Logs (reuses traces endpoint)
  logs:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: your-new-relic-license-key-here
```

## Security Best Practices

1. **Never commit secrets to git:**
   ```bash
   # .gitignore
   config.production.yaml
   config.staging.yaml
   config.*.local.yaml
   ```

2. **Use separate config files per environment:**
   - `config.yaml` - Base config (committed, no secrets)
   - `config.production.yaml` - Production secrets (NOT committed)
   - `config.development.yaml` - Dev/local settings (committed or git-ignored)

3. **Alternative: Read from secret managers**
   ```go
   // In main.go before app.Run()
   apiKey := os.Getenv("OTEL_API_KEY")  // From AWS Secrets Manager, Vault, etc.

   // Override config programmatically
   cfg.Observability.Traces.Headers = map[string]string{
       "api-key": apiKey,
   }
   ```

## Vendor-Specific Header Examples

```yaml
# New Relic
headers:
  api-key: your-license-key

# Honeycomb
headers:
  x-honeycomb-team: your-team-key

# Datadog
headers:
  dd-api-key: your-api-key

# Grafana Cloud
headers:
  authorization: "Basic base64-encoded-credentials"

# Generic Bearer Token
headers:
  authorization: "Bearer your-token"
```

## Why Not Environment Variables?

Environment variables like `OBSERVABILITY_TRACES_HEADERS_API_KEY` would require complex parsing logic that conflicts with Koanf's nested key handling. The explicit YAML approach:
- ✅ Aligns with "Explicit > Implicit" manifesto principle
- ✅ Matches industry standard (Docker Compose, Kubernetes ConfigMaps)
- ✅ Simpler implementation, fewer edge cases
- ✅ Users control exact header names (no automatic `_` → `-` conversion)
