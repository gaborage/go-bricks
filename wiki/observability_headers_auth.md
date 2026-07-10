# Observability Headers & Authentication

**IMPORTANT:** OTLP headers (API keys, bearer tokens) are declared in YAML under `observability.{trace,metrics,logs}.headers`. GoBricks does **not** auto-derive header names from `OBSERVABILITY_*_HEADERS_*` env vars — the framework needs to know the exact header keys, which can't be reconstructed from env var paths.

**The 12-factor pattern still applies:** declare the header *structure* in YAML; keep the *secret value* out of committed YAML. Hardcoded secrets in committed YAML are forbidden.

## Recommended Approach (Production)

Reference env vars from YAML using `${VAR_NAME}` placeholders — **but render them before GoBricks reads the file; GoBricks itself does not.** `config.Load()` (backed by Koanf) loads the YAML file verbatim via `file.Provider` and layers env vars onto the config tree only by matching variable *names* onto config key paths — it never scans an already-loaded string value for a `${...}` token. Left un-rendered, `api-key: ${NEW_RELIC_API_KEY}` is read back as the literal string `${NEW_RELIC_API_KEY}`, not the secret. Resolve the placeholder with an external render step — `envsubst`, a Vault Agent template, Helm/Kustomize, or an init container — that writes the real value into the file *before* the process starts.

```yaml
# config.production.yaml — checked into git
observability:
  enabled: true
  service:
    name: my-service
    version: v1.0.0

  trace:
    enabled: true
    endpoint: otlp.nr-data.net:4317  # No https:// for gRPC
    protocol: grpc
    headers:
      api-key: ${NEW_RELIC_API_KEY}  # SAFE: rendered from env before GoBricks reads this file

  metrics:
    enabled: true
    endpoint: otlp.nr-data.net:4317
    protocol: grpc
    headers:
      api-key: ${NEW_RELIC_API_KEY}

  logs:
    enabled: true
    endpoint: otlp.nr-data.net:4317
    protocol: grpc
    headers:
      api-key: ${NEW_RELIC_API_KEY}
```

The actual secret lives in the deployment environment (Kubernetes Secret, AWS Secrets Manager → env, HashiCorp Vault → env, `.env` file in dev) — not in source control.

## Security Best Practices

1. **Never hardcode secrets in YAML.** Always use `${VAR}` references for credentials, even in non-production configs.
   ```yaml
   # SAFE
   api-key: ${NEW_RELIC_API_KEY}

   # FORBIDDEN — real value committed to git
   api-key: nrak-ABC123XYZ
   ```

2. **Separate config files per environment** (no secrets in any of them):
   - `config.yaml` — Base config (committed)
   - `config.production.yaml` — Production overrides (committed; references `${VAR}`)
   - `config.development.yaml` — Dev settings (committed; references `${VAR}` or default literals for local-only test keys)

3. **Programmatic secret injection** (when a sidecar or secret manager provides the value at process start — e.g., rotating tokens): render the YAML file itself before calling `config.Load()` / `app.Run()`. GoBricks has no `${VAR}` interpolation step and no override hook for `observability.*` values, so the placeholder must already be a literal by the time the file is read.

   ```go
   // In main.go, before app.Run()
   apiKey, err := vault.GetSecret(ctx, "otel/api-key")
   if err != nil { return err }
   // strconv.Quote yields a valid YAML double-quoted scalar, so secrets containing
   // quotes, ':', '#', or newlines cannot corrupt the rendered document.
   rendered := strings.ReplaceAll(string(configTemplate), "${NEW_RELIC_API_KEY}", strconv.Quote(apiKey))
   if err := os.WriteFile("config.production.yaml", []byte(rendered), 0o600); err != nil { return err }
   // GoBricks reads the rendered file when config.Load() runs inside app.Run() —
   // no ${VAR} placeholder left to resolve.
   ```

   Note: `config.Config` has no `Observability` struct field, and there is no programmatic override for `observability.*` values — the rendered secret must already be present in the file GoBricks loads.

## Vendor-Specific Header Examples

All examples assume the secret is set in the deployment environment.

```yaml
# New Relic
headers:
  api-key: ${NEW_RELIC_API_KEY}

# Honeycomb
headers:
  x-honeycomb-team: ${HONEYCOMB_API_KEY}

# Datadog
headers:
  dd-api-key: ${DATADOG_API_KEY}

# Grafana Cloud
headers:
  authorization: ${GRAFANA_CLOUD_BASIC_AUTH}    # "Basic <base64>" pre-encoded in env

# Generic Bearer Token
headers:
  authorization: ${OTEL_BEARER_AUTHORIZATION}   # "Bearer <token>" pre-formatted in env
```

## Why Not Auto-Derive Header Names from Env Vars?

A flag like `OBSERVABILITY_TRACE_HEADERS_API_KEY=...` would force the framework to invent header names from env var paths (`API_KEY` → `api-key`? `Api-Key`? `X-API-Key`?). That conflicts with Koanf's nested key handling and forces a lossy convention. Declaring the header *structure* in YAML and rendering *values* from env vars before GoBricks reads the file (via your deployment tooling — see Recommended Approach above) gives you the best of both:

- ✅ Explicit header names — no automatic `_` → `-` conversion
- ✅ Secrets never live in YAML or source control (12-factor)
- ✅ Aligns with the "Explicit > Implicit" manifesto principle
- ✅ Matches industry standard (Docker Compose, Kubernetes ConfigMaps with secret refs)
- ✅ Simpler implementation, fewer edge cases
