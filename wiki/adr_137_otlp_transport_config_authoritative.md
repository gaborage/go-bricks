# ADR-137: OTLP Endpoint and TLS Config Are Authoritative Over `OTEL_*` Env

- **Status**: Accepted
- **Date**: 2026-10-02
- **Related**: [ADR-062](adr_062_database_tls_fail_closed.md) · [ADR-085](adr_085_framework_owned_flyway_url.md) · [ADR-108](adr_108_cache_redis_tls.md) (the fail-closed transport family) · [New Relic OTLP](new_relic_otlp.md) (the "derives TLS solely from the `insecure` field" contract)

## Context

The OpenTelemetry OTLP exporters read `OTEL_EXPORTER_OTLP_*` environment variables
themselves. The trace and metric exporters apply the environment before their options; the
log exporters fill every setting no option set from it. GoBricks passed the host through
`WithEndpoint` and an insecure option only when `insecure: true`. It never passed anything
that marks the transport secure. So, on HTTP and gRPC alike:

- `OTEL_EXPORTER_OTLP_INSECURE=true`, or its per-signal variant, turned an exporter configured
  with `insecure: false` into a plaintext one;
- an `OTEL_EXPORTER_OTLP_ENDPOINT` (or per-signal endpoint) with an `http://` scheme did the
  same, because the exporter derives insecure from that scheme, and on HTTP it also supplied
  the URL path;
- `OTEL_EXPORTER_OTLP_PROTOCOL=http/json` switched the trace HTTP exporter to JSON.

None of it was logged. An ambient `OTEL_EXPORTER_OTLP_ENDPOINT=http://…` set for another SDK, a
sidecar or a platform default turned `https://otlp.nr-data.net/v1/traces` into plaintext on
port 80, with the vendor `api-key` header in cleartext. That breaks the contract
[new_relic_otlp.md](new_relic_otlp.md) states: GoBricks "derives TLS solely from the
`insecure` field".

Endpoint grammar had the same blind spot. An HTTP endpoint with userinfo, a query, a fragment,
an encoded slash or no host booted and never exported ([C72.2]); a gRPC `grpc://host:4317`
booted and failed every RPC with "too many colons".

This is not an attacker boundary: whoever controls the environment already controls
`OBSERVABILITY_<SIGNAL>_INSECURE` and `OBSERVABILITY_<SIGNAL>_ENDPOINT`, which GoBricks reads
on purpose. The change enforces the stated contract against variables nobody meant for this
service. It cannot wait like other transport hardening because the failure is silent and
exposes credentials in cleartext.

## Decision

**The configured endpoint and `insecure` key alone decide each OTLP exporter's address,
scheme and path.**

1. **HTTP exporters** (traces, metrics, logs) get `WithEndpointURL`, built from the endpoint's
   parsed parts and never from the raw string. The scheme is `https`, or `http` when
   `insecure: true`. The host is the endpoint's. The path is the configured path, used
   exactly, or `/v1/<signal>` when there is none or it is `/`. An explicit URL leaves no
   endpoint, scheme or path for the environment to fill. The constructor refuses an endpoint
   that does not split cleanly with `ErrInvalidEndpointFormat`, because a URL the exporter
   cannot parse is silently ignored and the environment would win again.
2. **The trace HTTP exporter pins protobuf** (`WithEncoding(EncodingProtobuf)`). Metrics and
   logs have no JSON path.
3. **gRPC exporters with `insecure: false`** get `WithEndpointURL("https://localhost")` and
   then `WithEndpoint(endpoint)` last. `WithEndpointURL` is the only option that marks the
   transport secure explicitly; the later `WithEndpoint` overwrites only the address, so the
   target stays byte-identical. The placeholder is a constant because `"https://"+endpoint`
   does not parse for `unix-abstract:<name>` or a relative `unix:<path>`, and a placeholder
   that fails to parse is dropped, which reopens the downgrade. With `insecure: true` the
   exporters keep their explicit insecure credentials.
4. **`Validate` rejects** with the `ErrInvalidEndpointFormat` sentinel, never wrapping the
   parse error and never echoing the endpoint (a userinfo password would otherwise reach the
   startup WARN):
   - HTTP: no `http://`/`https://` scheme (as before), unparseable, no host, userinfo, a
     query, a fragment, or an encoded slash or other non-canonical escape (`u.RawPath != ""`),
     which no exporter can send as written;
   - gRPC: an `http://`/`https://` scheme (as before) and `grpc://`. `dns:///`, `unix:`,
     `unix-abstract:` and `passthrough:///` targets stay accepted.
5. **No scheme/insecure mismatch rejection.** `http://` with `insecure: false` exports over
   TLS and `https://` with `insecure: true` in plaintext, as documented.

Environment channels after this change:

| Status | Channel | Note |
| --- | --- | --- |
| Cut | endpoint host, scheme and path | config endpoint only |
| Cut | `*_INSECURE`, insecure from an endpoint scheme | `insecure` key only |
| Cut | `*_PROTOCOL` | trace HTTP exporter; the `protocol` key picks HTTP or gRPC |
| Kept | `*_HEADERS` | only when the config sets no headers for that signal |
| Kept | `*_CERTIFICATE`, `*_CLIENT_CERTIFICATE`, `*_CLIENT_KEY` | TLS path only; they can never turn TLS on. With `insecure: true`, an env CA still makes HTTP exporter construction fail (upstream, unchanged) |
| Kept | `*_TIMEOUT` | |
| Kept | gRPC `*_COMPRESSION` other than gzip | |

## Alternatives considered

- **Reject a scheme/insecure mismatch.** Rejected: an `http://` endpoint with `insecure:
  false` has always meant TLS here, and rejecting it would break working configurations for
  no security gain.
- **`"https://"+endpoint` as the gRPC placeholder.** Rejected: it does not parse for
  `unix-abstract:` and relative `unix:` targets, so those would stay downgradable.
- **Explicit TLS credentials for gRPC.** Rejected: an explicit credential replaces the one the
  exporter builds from `*_CERTIFICATE`, cutting a channel this decision keeps.
- **Clear `OTEL_EXPORTER_OTLP_*` at startup.** Rejected: it mutates process state other
  libraries read.
- **Pass the raw configured string to `WithEndpointURL`.** Rejected: a string that fails to
  parse is ignored without error, and the environment fills the gap.

## Consequences

- **Breaking.** A deployment that set the endpoint, its path, insecure or the trace protocol
  through `OTEL_EXPORTER_OTLP_*` loses that setting; it renames the variables to
  `OBSERVABILITY_<SIGNAL>_ENDPOINT` / `OBSERVABILITY_<SIGNAL>_INSECURE`, which still override
  YAML. See [migrations.md](migrations.md) `[C72.4]`; the endpoint rejections are `[C72.3]`.
- **A rejection in an App is total.** `Validate` runs inside provider construction, and the
  bootstrap turns a construction failure into a WARN plus a no-op provider for **every**
  signal. A malformed endpoint on one signal therefore stops traces, metrics and logs alike,
  where before only that signal was dead.
- Six exporters, both downgrade vectors, every gRPC target form, the kept channels and the
  protobuf pin are covered by tests that observe the transport on the wire.

## References

- [migrations.md](migrations.md) `[C72.2]`, `[C72.3]`, `[C72.4]`
- `observability/provider.go` (`parseOTLPHTTPEndpoint`, `otlpHTTPEndpointURL`, `otlpGRPCSecurePlaceholder`), `observability/metrics.go`, `observability/logs.go`, `observability/config.go` (`validateEndpointFormat`)
- [observability.md](observability.md), [new_relic_otlp.md](new_relic_otlp.md), [otel_collector.md](otel_collector.md), [observability_headers_auth.md](observability_headers_auth.md)
