# Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization, with a documented fallback hierarchy that lets you override per-component or set a single global cap.

## Startup Timeout Defaults

GoBricks applies component-specific startup timeouts for graceful initialization:

| Setting | Default | Purpose |
| --------- | --------- | --------- |
| `app.startup.timeout` | 10s | Overall startup timeout (also serves as fallback for unset components) |
| `app.startup.database` | 10s | Database connection establishment |
| `app.startup.messaging` | 10s | AMQP broker connection |
| `app.startup.cache` | 5s | Redis connection |
| `app.startup.observability` | 15s | OTLP endpoint connection (higher for TLS handshake) |

**Fallback Hierarchy:**

1. Explicit component value (e.g., `app.startup.database: 15s`) → preserved
2. Global timeout (if set): `app.startup.timeout: 30s` → applied to all unset components
3. Per-component default (shown in table) → used when neither is set

**Example - Global fallback:**

```yaml
app:
  startup:
    timeout: 30s  # All components inherit 30s (database, messaging, cache, observability)
```

**Override defaults** in `config.yaml`:

```yaml
app:
  startup:
    timeout: 30s          # Longer overall timeout
    database: 15s         # More time for slow databases
    observability: 30s    # More time for remote OTLP endpoints
```

## Server Request Body Limit

`server.bodylimit` (int64 bytes; env `SERVER_BODYLIMIT`) caps the accepted HTTP request body size, rejecting an over-cap request with `413 Request Entity Too Large`. A request with a known `Content-Length` above the cap is rejected up front, before the handler runs; a chunked / unknown-length body is bounded by a limited reader instead, so the 413 surfaces when the read crosses the cap while the handler consumes the body:

| Setting | Default | Purpose |
| --- | --- | --- |
| `server.bodylimit` | 10 MB (10485760 bytes) | Maximum accepted HTTP request body size |

Raise it for endpoints that accept large uploads or bulk imports, or lower it to tighten the boundary:

```yaml
server:
  bodylimit: 26214400   # 25 MB — allow larger uploads
```

## Server TLS Listener

`server.tls.*` enables HTTPS on the HTTP server listener (ADR-042). The default posture is **disabled** — every field defaults to its zero value, which leaves the listener plaintext, byte-for-byte unchanged from prior behavior:

| Setting | Default | Purpose |
| --------- | --------- | --------- |
| `server.tls.enabled` | `false` | Enable the HTTPS listener |
| `server.tls.certfile` / `server.tls.certvalue` | `""` | Server certificate: file path or base64-encoded PEM (exactly one) |
| `server.tls.keyfile` / `server.tls.keyvalue` | `""` | Server private key: file path or base64-encoded PEM (exactly one) |
| `server.tls.minversion` | `""` (resolves to a TLS 1.2 floor) | TLS floor; `""` and `"1.2"` are equivalent, `"1.3"` opts up |

Bad or unreadable material fails `Start()` fast — it never silently falls back to plaintext. Staging cert/key material ahead of a flip (`server.tls.enabled: false` with material already set) is fail-open but not silent: startup emits one WARN naming `server.tls.enabled`. See [server_tls.md](server_tls.md) for the full config reference and deployment guidance (ALB-terminated partner mTLS vs. app-terminated mTLS).

## ALB Forwarded-Client-Cert Identity Middleware

`server.forwardedclientcert.*` (ADR-043) parses ALB verify-mode `X-Amzn-Mtls-Clientcert-*` identity headers. The default posture is **disabled** — the middleware is not wired into the request path at all:

| Setting | Default | Purpose |
| --- | --- | --- |
| `server.forwardedclientcert.enabled` | `false` | Wire the middleware; parse and expose the identity |
| `server.forwardedclientcert.require` | `false` | Reject (401) requests missing both `-Subject` and `-Serial-Number` (a malformed `-Leaf` alone never rejects); requires `enabled: true` |

Health/ready probes always skip this middleware regardless of `require`. See [forwarded_client_cert.md](forwarded_client_cert.md) for the config reference, the trust model (including the AWS doc-silence finding on header spoofing), and an authorization recipe.

## Messaging Pre-Warm Readiness Wait

In single-tenant mode, startup pre-warms the messaging publisher and then waits for it to report `IsReady()`, bounded by `messaging.reconnect.readytimeout` (default 5s — the same key and budget as the per-publish readiness pre-flight; see [context_deadlines.md](context_deadlines.md)). A publisher that isn't ready in time logs a WARN and startup continues — the wait never fails startup; the publish-time pre-flight still absorbs a slow first publish. The wait (`ConnectionPreWarmer.awaitPublisherReady`) is context-aware and reports a distinct cancellation outcome when its `ctx` is canceled, rather than mislabeling it as a readiness timeout — but that path only fires for callers that pass a cancelable context. On the framework's own boot path (`app/lifecycle.go`'s `prepareRuntime`), pre-warm runs with `context.Background()` and the OS signal handler is installed later (`waitForShutdownOrServerError`, after `prepareRuntime` returns), so a shutdown signal received during pre-warm does **not** abort the wait — it runs to ready-or-`readytimeout` regardless.

**Operator guidance:** because the HTTP listener starts only after pre-warm completes, raising `messaging.reconnect.readytimeout` directly stretches the pre-listen boot window whenever the broker is unreachable — size Kubernetes `startupProbe`/`livenessProbe` initial-delay and failure-threshold settings (or any other external "is it up yet" check) to comfortably exceed the configured `readytimeout`, not just the steady-state startup time.

## Startup Route Logging

Set `server.logroutes` (bool; env `SERVER_LOGROUTES`) to emit one `Info` line per registered HTTP route at startup:

```text
Route registered  module=events method=POST path=/v1/events
```

It is a **tri-state** flag: an explicit `server.logroutes` value always wins; when the key is absent it defaults to `app.env` being development (on in `dev`/`development`/`local`, off in `prod`/`staging` per ADR-022). So routes are visible at first `go run` while production stays silent — an N-route service pays **zero** extra boot lines in prod unless an operator opts in. Turn it on in production for a smoke-check with `server.logroutes: true`; silence a dev boot with `server.logroutes: false`.

Attribution is by **registration order** (`module.Name()`), covering both typed (`server.GET/POST`) and raw (`RouteRegistrar.Add`) routes — `RouteDescriptor.ModuleName` is empty for every route, so the module is derived from the registration span, not the descriptor field. Routes registered before the module loop (debug / `_sys`) are attributed to `framework`. Note: `health`/`ready` are registered directly on the HTTP engine (not the route registry) and are therefore **not** included.

## Duplicate Route Detection

Startup fails when two registrations claim the same **exact method + full path**. The echo engine is constructed with `AllowOverwritingRoute: true`, so without this check the second registration silently wins and the first module's handler is dead on arrival — no error, no warning, unless the shadowed route happens to be exercised. This closes that gap at the framework's own registration seam (`server.RouteRegistrar`), covering both typed (`server.GET/POST`) and raw (`RouteRegistrar.Add`) routes, plus anything registered through nested `Group()`s.

**Coverage notes:**

- `health`/`ready` probes register directly on the HTTP engine (not through `RouteRegistrar` — same seam note as route logging above), but `server.New` records their method+path pairs in the conflict tracker explicitly, so a module claiming `GET /health` (or the configured probe paths) fails startup like any other collision.
- Param-name-differing route templates (e.g. `/users/:id` vs `/users/:uid`) are **excluded** — these are distinct strings and are not detected as duplicates, even though they collide in echo's radix tree at request time; echo's own behavior governs there.

**Error shape:** startup aborts with one aggregate error naming every collision and both registrants (`HandlerName` + caller `Package`; module name is not available — see the route-logging note above on why `RouteDescriptor.ModuleName` stays empty):

```text
duplicate route registration (1 conflict(s))
GET /v1/events — first: createEvent (github.com/example/events), duplicate: legacyCreateEvent (github.com/example/legacy)
```

The error is built with `errors.Join`, so the individual collisions can be traversed structurally (each child is a plain formatted error — there is no sentinel or typed error to match with `errors.Is`/`errors.As`).

There is no disable knob — a colliding route is always a startup-blocking bug, never a warning. Fix by removing or renaming the colliding route.

## Probe Endpoints and Rate Limiting

`/health` and `/ready` are **not** exempt from the framework's rate limiters. Both limiters are installed engine-globally with echo's never-skip skipper, so probe requests consume limiter budget like any other route:

| Setting | Default | Applies to probes |
| --- | --- | --- |
| `app.rate.limit` | 100 rps | Yes — global limiter; a value `<= 0` disables it entirely |
| `app.rate.ippreguard.enabled` | `true` | Registers the per-IP pre-guard |
| `app.rate.ippreguard.threshold` | 2000 rps/IP | Yes — per-IP abuse ceiling |

Probe traffic is always keyed by **client IP**, never by tenant: the probe skipper bypasses tenant resolution on the health and ready paths, so the global limiter's identifier extractor falls through to the request's real IP.

**Operational consequence.** Both probe paths draw on the same per-IP budget as any other traffic from the same source address, so a saturating client that shares a source IP with the prober — an L3/L4 NAT, or any hop that forwards without rewriting the client address — can push the probes themselves to `429`. The two outcomes differ: a rejected `/ready` drops the instance from the load balancer's rotation, while a rejected `/health` fails the liveness check and, with the wiring documented under [Wiring Kubernetes probes](cache.md#wiring-kubernetes-probes), restarts the container — a failing readiness probe never restarts anything. Mitigate by raising `app.rate.limit` / `app.rate.ippreguard.threshold` for that deployment, or by giving probe traffic a path to the instance that does not share a source IP with application traffic.

These are *koanf* defaults. A `*config.Config` assembled in Go rather than loaded through configuration leaves both at zero, and the global limiter is a pass-through at `<= 0` — such a deployment has no ceiling at all (see [ADR-049](adr_049_debug_endpoints_fail_closed.md)).
