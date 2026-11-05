# ADR-167: Inbound Paths Render From the Matched Route Template

**Status:** Accepted
**Date:** 2026-11-03
**Related:** [ADR-026](adr_026_zero_overhead_request_path.md) (zero-overhead request path) · [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) (a recovered panic is reported by type) · [ADR-161](adr_161_unknown_tenant_is_a_client_fault.md) (the unknown-tenant WARN; amended here) · [ADR-164](adr_164_tenant_owned_route_family.md) (the family refusal ERROR) · GHSA-h4jw-4c64-48mh · #2212

## Context

A request path is caller-written. A card number, an account number or a token in a path segment
(`GET /cards/<PAN>`) reached every framework-owned inbound sink as sent:

- the access log's `url.path` field and its message (`GET <path> completed in …`), which printed
  the path the router keyed on (`URL.RawPath` when set, `URL.Path` otherwise);
- the incoming-request span's `url.path`, rewritten to that same router path, including the CORS
  preflight span;
- the tenant-rejection WARN, the IP pre-guard WARN, the three forwarded-client-cert WARNs (two of
  them fire on requests that succeed) and the outermost panic guard's `Panic recovered` ERROR,
  which printed the decoded `URL.Path`;
- the tenant-owned family's refusal ERROR (ADR-164), which printed the bare route template.

The log filter cannot help: it matches field names, and `url.path` and `path` are not sensitive
names. Earlier #2212 links built the door: every engine the framework builds carries a masking
router that learns, at registration, each accepted template's rendering with every parameter and
the first wildcard replaced by `REDACTED` (text after the first `*` is cut), and `maskedPath(c)`
looks that rendering up by `c.Path()` at zero allocations. An unlearned or empty template renders
the bare `REDACTED`. A lint rule forbids new request-URL reads in `server/`; the sinks above were
its reasoned exceptions.

## Decision

**Every framework-owned inbound path sink prints `maskedPath(c)`, never request bytes.** The access
log's `url.path` and message, the span's `url.path` and the seven WARN and ERROR lines above all
render the matched template masked: `GET /cards/<PAN>` prints `/cards/REDACTED` whether the value
arrives plain, percent-encoded or with an encoded slash. A request that matched no template (a
root 404) prints the bare `REDACTED`. The unknown-tenant WARN of ADR-161, which printed the route
template (`/:tenant/orders`), prints the masked rendering too (`/REDACTED/orders`), so every seat
prints one rendering.

**Each engine states the rule once at startup.** Before its listener serves a request (the
application listener before it binds, the probe listener once bound), every engine the framework
serves logs one INFO action line naming the rule, the listener and the token:

```text
[server.paths] inbound paths render from the matched route template rule=masked_route_template listener=application token=REDACTED unmatched=REDACTED
[server.paths] inbound paths render from the matched route template rule=masked_route_template listener=probes token=REDACTED unmatched=REDACTED
```

The probe line appears only with `server.probes.port` set. `unmatched=REDACTED` states what a
request that matched no template prints. An operator reading a log that holds `REDACTED` where a
path was expected finds the reason in the same stream.

`http.route` stays the registered template; the query string still reaches no sink.

The probe skip keeps its raw-path key (`CreateProbeSkipper`'s no-template fallback), because the
GHSA-h4jw-4c64-48mh fix requires it to compare the path the router matched. That read is now
inline in the skipper, a comparison key that is never printed, and the helper that also fed the
access log and the span is deleted, so no sink can reach a raw path through a lint-exempt helper.

## Alternatives considered

- **Mask values by pattern (digit runs, Luhn).** Rejected. It is a guess at what is sensitive and
  leaks everything it does not recognize; the template is a total answer.
- **Print the bare template (`/cards/:pan`).** Rejected. The masked rendering keeps the request's
  shape readable while staying value-free, and one rendering serves every seat.
- **Keep the raw path behind a debug flag.** Rejected. A flag that prints card data is a flag that
  gets left on.

## Consequences

- **Breaking**, silently: a dashboard, alert, log query or test that reads a concrete path from
  `url.path`, the access-log message, or a framework WARN or ERROR `path` field no longer finds it.
  Key it on `http.route`, or on the masked rendering.
- Path-level forensics move to the edge (load balancer, WAF), which this framework does not
  control; that is a named residual of #2212.
- No allocation moves: `maskedPath` and the deleted helper are both zero-allocation reads, so the
  ADR-026 guards are unchanged.
- Out of scope, and unchanged: consumer reads of `Request().URL` or `HandlerContext.Param`, consumer
  error text, the request method token, and path-read tenant IDs (later #2212 links).

## References

- `server/router.go`: `maskedPath`, `maskingRouter`
- `server/logger.go`: `extractRequestMetadata`
- `server/middleware.go`: `overrideSpanURLPath`
- `server/tenant_middleware.go`, `server/tenant_policy.go`, `server/ip_preguard.go`,
  `server/forwardedcert.go`, `server/panic_guard.go`: the WARN and ERROR sinks
- `server/server.go`: `logPathRendering`, the `[server.paths]` startup line
- `server/probe_skip.go`: `CreateProbeSkipper`
- See [migrations.md](migrations.md) `[C73.29]`.
