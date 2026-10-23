# ADR-150: A Route Masks Its Sensitive Path Parameters in url.path Telemetry

**Status:** Accepted
**Date:** 2026-10-22

## Context

The framework's two `url.path` sinks write the concrete request path, not the route template:

- `overrideSpanURLPath` (`server/middleware.go`) replaces the incoming-request span's `url.path`
  attribute with `routerPath(r)`, the path the router keyed on (#1817).
- `extractRequestMetadata` (`server/logger.go`) sets the access log's `URLPath` to the same
  value, which feeds both the structured `url.path` field and the human message built by
  `createActionMessage`.

A path parameter carrying regulated data — a card number in `GET /cards/:pan` — therefore reached
the tracing exporter and the access log verbatim on every request to that route.

The log filter (`logger.SensitiveDataFilter`) cannot fix this. It masks by field NAME and never
inspects a value, and `pan` is deliberately absent from its defaults. Naming `url.path` in
`log.sensitivefields` would mask the path of every request on every route, destroying the field
for the routes that carry nothing sensitive, and would still leave the span attribute untouched,
since the filter only sees log records.

## Decision

**A route declares which of its path parameters are sensitive, and the span's `url.path` and the
access log mask exactly those values for requests that route serves.**

- `server.WithSensitiveParams(names ...string)` is a `RouteOption`, following `WithTags`: it
  appends to the new `RouteDescriptor.SensitiveParams`, which `cloneDescriptor` deep-copies like
  `Tags` and `Middleware`.
- **Names compile to a plan at registration.** `RegisterHandler` runs `compileSensitivePlan` on the
  full path once, before the route is published or recorded. A name is valid only when a segment
  before any wildcard is exactly `:name`, and `*` only on a wildcard route. A param after the
  wildcard (`/files/*/:pan`), embedded after literal text (`/cards/pan-:pan`), prefixed (`:pan-x`)
  or absent from the path panics at startup (Fail Fast, like an unbindable request type). The plan
  holds segment indices only: the template's segment count, the wildcard's index, the indices to
  mask, and whether the wildcard tail is masked. `segmentParam` is the one reader of the segment
  grammar; nothing parses the route at request time.
- The lookup table is **per-server state**, not part of `DefaultRouteRegistry`. It lives on the
  `Server`'s `routeConflictTracker`, keyed by `{method, path}` (the full route template, as
  `c.Path()` reports it). `routeGroup.addEcho` records a typed route's compiled plan only once
  the conflict check admits it to the engine, so a refused duplicate declares nothing. The table is
  written during startup registration and read per request: it is copy-on-write behind an
  `atomic.Pointer`, so the read is lock-free, and a server that registered no sensitive route
  returns before any map lookup.
- `newServer` hands the tracker's lookup to the middleware chain before any route exists (the
  table fills afterwards). `maskSensitivePathParams(c, rawPath, lookup)` (`server/path_mask.go`)
  looks the matched route (`c.Request().Method`, `c.Path()`) up through it. With no plan it returns
  `rawPath` itself and allocates nothing. Otherwise it splits the raw path once and replaces each
  planned segment index with `logger.DefaultMaskValue`; with `*` declared, the whole tail from the
  wildcard's index collapses into one `***` segment, hiding its values and its depth. The path must
  have exactly the template's segment count (at least the wildcard's index on a wildcard route);
  otherwise the masker **fails closed** and returns the route template (`c.Path()`), so a shape
  mismatch never leaks a value. Masking is by position, never by searching for a value: a `:pin`
  value of `1` on `/v1/:pin/*` masks that segment, not the `1` in `v1`. The exported
  `SetupMiddlewares` has no routes of its own and passes no lookup.
- `overrideSpanURLPath` and `extractRequestMetadata` pass `routerPath(r)` through it. Masking the
  access log's `URLPath` covers its field and its message at once.
- `http.route` (`c.Path()`) is never touched in either sink: the template carries no value.

## Alternatives considered

- **Add `url.path` to `log.sensitivefields`.** Rejected: it masks every route's path and never
  reaches the span.
- **Report the route template as `url.path`.** Rejected: `url.path` is the concrete path by
  semantic convention, and #1817 pinned it to the routed path so it agrees with `http.route`.
- **Detect PAN-shaped values automatically.** Rejected: value scanning is a heuristic with false
  positives and negatives on every path; the route author knows which parameter is regulated.
- **Linear scan of `ByPath` per request.** Rejected: the lookup sits on every request's hot path.
- **Side index on `DefaultRouteRegistry`.** The first implementation. Rejected: that registry is
  process-global and introspection-only, shared by every `Server` in the process, and tests
  `Clear()` it — which silently switched masking off for a live server, a security control that
  failed open. Its key also ignored the listener. Per-server state lives exactly as long as the
  routes it describes.

## Consequences

- **Additive only** (`feat(server):`). New exported API: the `RouteDescriptor.SensitiveParams`
  field and the `WithSensitiveParams` route option. No existing exported signature changes, and a
  route that declares nothing behaves exactly as before. `RouteDescriptor.SensitiveParams` stays
  in the registry for discovery only; masking never reads it there.
- Opt-in only: a consumer must declare every sensitive parameter on every route that carries one.
  Nothing detects an undeclared one.
- A declaration the masker cannot honour fails startup instead of silently masking nothing,
  including one made through a `RouteRegistrar` that is not the framework's route group (only it
  records the plan). A name the route repeats (`/cards/:id/transfers/:id`) masks every segment.
- Typed routes only (follow-up): a raw `RouteRegistrar.Add` route takes no `RouteOption`, so it
  cannot declare a sensitive parameter today.
- Only the span's `url.path` and the access log are masked. Known limit: other framework log lines
  still print the decoded request path unmasked on a sensitive route — the `Panic recovered` line
  (`server/panic_guard.go`), and the rejection logs in `tenant_middleware.go`, `ip_preguard.go` and
  `forwardedcert.go`. Routing them through one masked-path accessor is a follow-up. A value a
  handler logs or sets on a span itself remains the consumer's responsibility.

## References

- `server/descriptor.go` — `RouteDescriptor.SensitiveParams`, `WithSensitiveParams`,
  `cloneDescriptor`
- `server/sensitive_plan.go` — `sensitivePlan`, `compileSensitivePlan`, `segmentParam`
- `server/route_conflicts.go` — `routeConflictTracker.recordSensitive`, `.lookupSensitivePlan`
- `server/route_registrar.go` — `routeGroup.addEcho`
- `server/path_mask.go` — `sensitiveParamLookup`, `maskSensitivePathParams`, `maskBySegment`
- `server/middleware.go` — `overrideSpanURLPath`
- `server/logger.go` — `extractRequestMetadata`
- `server/path_mask_test.go`, `server/descriptor_test.go`
- [observability.md](observability.md) — *Sensitive path parameters*
