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
- **Shutdown races an export it cannot cancel.** Two different durations, easy to merge and worth keeping apart. `Provider.Shutdown(ctx)` returns when YOUR context expires — it does not block past it. But an export the batch processor's timer already started runs on a context of the SDK's own, never derived from yours, bounded only by `trace.export.timeout`; `Shutdown` races it and, against an unreachable collector, loses. So it returns `context deadline exceeded` while the dial keeps retrying in the background after you were told shutdown finished. Size the deployment's shutdown budget above `observability.trace.export.timeout`, or lower that timeout: with the 10s default — `observability.environment: development`, or any signal whose endpoint is `stdout` — and a shorter budget, a graceful shutdown reports a failure when nothing is wrong beyond the collector being down

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

Every log line emitted via the framework logger passes through a `logger.SensitiveDataFilter` that masks values whose **field names** match an allowlist (case-insensitive substring). The filter is applied uniformly — including in the framework's own request/response middleware, AMQP consumer panic recovery, slow-request warnings, scheduler job traces, and any module-level `log.Info()/Error()/...` call. **It never protected a recovered panic's VALUE**, which is why [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) reports those by type instead: the filter matches FIELD names, the field was `panic` — no needle — and a bare `panic("secret")` has no inner field name to match at all. There is no opt-in surface to wrap "after the fact" — the filter is wired into the logger before any subsystem captures a reference to it.

### Default field list

`logger.DefaultFilterConfig()` (in [`logger/filter.go`](../logger/filter.go)) ships these names (all matched case-insensitive substring, so `password` matches `Password`, `db_password`, `oldPasswordHash`, etc.):

| Category | Default names |
| --- | --- |
| Credentials | `password`, `passwd`, `pwd`, `secret`, `token`, `access_token`, `refresh_token` |
| Key material | `api_key`, `apikey`, `api-key`, `private_key`, `privatekey`, `private-key`, `signing_key`, `signingkey`, `signing-key`, `encryption_key`, `encryptionkey`, `encryption-key` |
| Auth headers | `auth`, `authorization` |
| Generic | `credential`, `credentials` |
| Connection strings | `broker_url`, `database_url`, `db_url` |
| Card data (PCI) & PII | `cardholder`, `card_number`, `cardnumber`, `primary_account_number`, `cvv`, `cvc`, `track1`, `track2`, `track_data`, `iban`, `otp` |

A URL value on the sensitive path is masked in full, never structure-preserved — query strings and fragments routinely carry the secret itself. The default mask value is `***`.

Bare `key` is deliberately absent too, and was removed in v0.60.0: it masked every field whose name merely contained the word — `keys`, `tenant_key`, `cache_key`, and the framework's own `key` identifier on tenant and resource log lines — with no way to unmask one short of replacing the whole list. Key material is named needle by needle instead, in both spellings, because substring matching treats them as unrelated: `api_key` does not contain `apikey`, nor the reverse. `secret_key` and `secretkey` need no entry — the `secret` needle already covers both. The hyphenated spellings are there because `httpclient` logs whole `http.Header` maps through this filter under `LogPayloads`, and a header is spelled `X-Api-Key`; a bare `-key` needle would catch every such header and also mask `routing-key`, so the shapes are named instead. A spelling the table does not name — `license_key`, `hmac_key`, `master_key`, `session_key`, or a vendor header like `Ocp-Apim-Subscription-Key` — logs in clear until you add it via `log.sensitivefields`.

Bare `pan`, `card`, `pin`, and `track` are deliberately absent: substring matching would mask `span_id`, `discard_reason`, `pinned_at`, and `tracking_id`, so differently-named PAN fields still need a per-service entry via `log.sensitivefields`. `otp` does over-mask camelCase `…otP…` names (e.g. `snapshotPath`) and that trade is intentional — masking a debugging detail costs less than leaking an OTP. Needle lists are normalized where they become a filter, so EVERY door gets the same rule: entries are trimmed, de-duplicated, and dropped when empty afterwards. That matters most for `app.Options.LoggerFilterConfig`, which replaces the whole config — a single empty entry there used to make `strings.Contains` true for every field name and mask the entire log stream. Setting `app.Options.LoggerFilterConfig` (or calling `logger.NewWithFilter` directly) with a `SensitiveFields` that is empty — or whose entries all normalize away — logs a WARN at startup (suppressed at `log.level: error` and above) — an empty YAML `log.sensitivefields` list is not this case: it resolves to `nil` and falls back to the defaults, so it neither disables masking nor warns.

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
- **Recursive into structures**. All log-event methods are covered — `Str`, `Int`, `Int64`, `Uint64`, `Dur`, `Bytes`, and `Interface(...)` — as well as nested `map[string]any`, `map[string]string`, `http.Header` (`map[string][]string`), struct fields (using `json` tags when present), and slice/array elements. Recursion is bounded (`logger.DefaultMaxDepth = 8`) and cycle-safe (visited pointer set). Depth exhaustion fails **closed** — values past the depth limit are replaced with the mask rather than logged verbatim. A slice whose elements the filter can rewrite (maps, slices, arrays, structs, pointers, `[]any`) is emitted as `[]any`; a typed nil slice stays `null`; one it cannot (scalars, `[]byte`, which stays base64) keeps its concrete type. Serialized output is the same either way — the distinction is visible only to a caller type-asserting the result of the public `FilterValue`.
- **URLs are masked in full, not partially**. A masked field whose value is an HTTP/AMQP URL (e.g. `database_url`, `broker_url`) is replaced with the default mask value (`***`) in its entirety — host, path, query string, and fragment included, not just the `user:password@host` component. Query strings and fragments routinely carry the secret itself, so partial masking would leave it exposed.

### What this does *not* do

- **No content-pattern scanning.** A PAN embedded in a free-text error message (e.g., `errors.New("card 4111111111111111 failed")` logged via `log.Err(err)`) is *not* caught by field-name masking — the filter cannot see inside a value. One exception, and it is whole-field rather than content-aware: naming `error` in `log.sensitivefields` masks the ENTIRE message at `Err`, message and all, so nothing of it reaches the sink. For anything short of that, wire an error redactor at the `Err(err)` seam (see *Redacting error messages*, below); elsewhere, build a `sensitive.Scrub(...)` helper in your service layer.
- **No per-tenant policies.** The filter is configured once at bootstrap and applied uniformly to every log line, regardless of tenant context. If different tenants have different masking requirements, you need either separate deployments or a custom logger wrapper at the handler layer.
- **No metric/trace masking.** The filter only intercepts log records. OTel span attributes and metric labels go through different code paths. Treat span attributes as "would I publish this on a dashboard?" — never put a PAN in a span attribute.
- **The framework's OWN span sinks never carry an error message, and that is not the filter's doing.** Every framework site that records an error on a span goes through `observability.RecordErrorByType`, which emits one `exception` event carrying `exception.type` (the error's outer `%T`) and NO `exception.message`, and sets `codes.Error` with that same type as the status description ([ADR-083](adr_083_span_sinks_record_errors_by_type.md)). The status DESCRIPTION is framework-authored rather than always the Go type: the HTTP client prefers its own classification (`transport_error`, `interceptor_failed`, …) and a 5xx's `HTTP 503`, and a scheduler job panic reads `panic` with the recovered value's type in the `job.panic_type` attribute. Every one of those is a framework constant; none is consumer text. A span exception event and a span status description leave the platform with the tracing exporter, under the vendor's retention and access model, so an error message the framework did not write — a job's, a handler's, an interceptor's — is not put there at all. The corresponding LOG line still carries the message: that sink is on-platform, and what it writes is the operator's to control — including through `FilterConfig.ErrorRedactor` below.
- **Span attributes a consumer adds stay the consumer's responsibility.** The rule above covers the framework's own sinks only. `RecordErrorByType` is exported and available for a span your code started, but nothing enforces its use, and nothing scrubs an attribute you set.

### Redacting error messages

`FilterConfig.ErrorRedactor func(error) string` is the one seam that sees error *content* — field-name masking also applies at `Err`, but only to replace the whole field, never to rewrite part of a message. When it is non-nil, every `LogEvent.Err(err)` call — the framework's own included — writes its return value under the `error` field instead of `err.Error()`. That matters because the framework calls `Err(err)` with consumer-authored errors at dozens of sites (handler failures, job failures, message-handler failures), which no service-layer scrub helper can reach.

```go
var panRegexp = regexp.MustCompile(`\d{13,19}`) // package level: compile once

base := logger.DefaultFilterConfig()
base.ErrorRedactor = func(err error) string {
    return panRegexp.ReplaceAllString(err.Error(), "****")
}

fw, _, err := app.NewWithOptions(&app.Options{LoggerFilterConfig: base})
```

- **Code door only.** `Options.LoggerFilterConfig` replaces the whole config, so start from `logger.DefaultFilterConfig()` and set the field — a bare struct literal drops the default needle list. There is no YAML key: the value is a function, so the `log.sensitivefields` merge path always leaves the redactor nil.
- **Nil is the default.** With no redactor, and with `error` not marked sensitive, `Err` output is byte-identical to zerolog's own. The framework ships no scrubbing pattern.
- **Field-name masking applies at `Err` too.** Naming `error` in `log.sensitivefields` masks the message at this door exactly as it does at `Str`, so the two agree; with both a needle and a redactor configured the mask wins, the redactor's output being a value under a field the operator called sensitive. `error` is not a default needle, so this changes nothing unless you ask for it.
- **Scoped to `Err`.** A nil error still emits nothing and never reaches the redactor. `Interface`, `WithFields` and `Msgf` are unchanged — an error logged through those goes through field-name masking only. Recovered panic values are governed by ADR-081 (reported by type, never by value) and are not routed to the REDACTOR (field-name masking still applies, as everywhere). Read that precisely: the panic VALUE never reaches the redactor, but the server's recovered-panic log line does hand its ADR-081 type-rendered error to `Err` under `app.debug` (#1182), so a redactor sees `panic (type: string)` — the framework's own rendering — and not the value the handler panicked with.
- **Runs inside the log call**, so a panicking redactor is covered by the same guards that already wrap framework log calls in deferred paths.
- **Covers the OTLP sink too.** The OTel log bridge is an `io.Writer` over zerolog's emitted JSON, so the redacted string is what the log exporter receives — the raw message never reaches it.

### Opaque payloads (JSON bodies, JWKs, PEM keys)

Field-name masking sees a tree of named Go fields. An **opaque payload** — bytes or a string
whose structure the filter cannot see into — used to be a single leaf named by whatever key
carried it, so a marshalled request body logged its own `password` in clear. Since
[ADR-086](adr_086_mask_inside_opaque_payloads.md) the filter looks inside.

**What is inspected.** A `json.RawMessage`, `[]byte`, `[]json.RawMessage`, the `Bytes()` door,
and a string whose first non-space byte is `{` or `[`. The payload is parsed, walked with the
same needle list, and re-encoded **only when something was masked** — so a payload with
nothing to mask ships the bytes it arrived with, key order and number spelling intact. Nothing
else is parsed: an id, a message, a bare number and a non-JSON byte slice never reach the
decoder, and a benchmark pins zero extra allocations for a non-JSON string field.

**Shape rules.** Two kinds of key material carry names no needle can match, so they are
matched by shape instead:

| Shape | Marker | Masked |
| --- | --- | --- |
| JWK / JWKS | an object carrying `kty`, at the root, inside a `keys` array, or nested | `d`, `p`, `q`, `dp`, `dq`, `qi`, `k`, `oth` — matched exactly, never by substring |
| PEM block | a header whose label ends in `PRIVATE KEY` | the whole string; a `CERTIFICATE` or `PUBLIC KEY` block stays readable |

If you added `log.sensitivefields: [keys]` for JWKS on ADR-072's advice, the `kty` rule now
covers it; leaving the needle in place costs only a masked field named `keys`.

**Fail-closed.** A payload that looks like JSON and does not parse, nests deeper than
`logger.DefaultMaxDepth`, or exceeds `FilterConfig.MaxPayloadBytes` is masked **whole** — the filter cannot say
what is inside it, and that is exactly the case this door exists for.
`MaxPayloadBytes` defaults to 64 KiB (`logger.DefaultMaxPayloadBytes`): zero means the default,
so a bare struct literal cannot silently opt out, and a negative value disables the payload
door entirely, restoring name-only filtering. Raise it through
`app.Options.LoggerFilterConfig`, which REPLACES the whole config — start from
`logger.DefaultFilterConfig()` and set the field.

**Deliberately not inspected.** JWT strings (recognising one means decoding it, and its claims
are usually what the operator needs — the signing key is covered where it appears as a JWK or
a PEM block), XML and form-encoded bodies, and the log MESSAGE text: `Msg` is a format string
the caller wrote, and the field seam is where the filter has names to judge.
`FilterConfig.ErrorRedactor` ([ADR-083](adr_083_span_sinks_record_errors_by_type.md)) remains
the seam for error text.

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
expected, not a gap. Scheduler job metrics behave the same way: `job.execution.total`,
`job.execution.duration`, and `job.panic.total` record inside the `job.execute`
span, which is a root on both the scheduled and the manually-triggered path —
an exemplar on a hand-triggered job resolves to that job's own trace, not to the
`POST /_sys/job/:jobId` request's.

**One configuration is worth naming**, because it fails quietly:
`observability.enabled: true` with `observability.trace.enabled: false` leaves
metrics flowing while every exemplar is silently dropped and `trace_id`/`span_id`
vanish from log lines — leaving `correlation_id` as the only correlation
anywhere. Nothing errors and nothing warns.

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
