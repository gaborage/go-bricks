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

The default is applied by config normalization: an unset (zero) value becomes 10 MB during `config.Validate`, and a negative value is rejected there rather than quietly reverting to the default.

Raise it for endpoints that accept large uploads or bulk imports, or lower it to tighten the boundary:

```yaml
server:
  bodylimit: 26214400   # 25 MB — allow larger uploads
```

## Server TLS Listener

`server.tls.*` enables HTTPS on the application listener (ADR-042); the probe listener stays plain HTTP (see [Internal Probe Listener](#internal-probe-listener)). The default posture is **disabled** — every field defaults to its zero value, which leaves the listener plaintext, byte-for-byte unchanged from prior behavior:

| Setting | Default | Purpose |
| --------- | --------- | --------- |
| `server.tls.enabled` | `false` | Enable the HTTPS listener |
| `server.tls.certfile` / `server.tls.certvalue` | `""` | Server certificate: file path or base64-encoded PEM (exactly one) |
| `server.tls.keyfile` / `server.tls.keyvalue` | `""` | Server private key: file path or base64-encoded PEM (exactly one) |
| `server.tls.minversion` | `""` (resolves to a TLS 1.2 floor) | TLS floor; `""` and `"1.2"` are equivalent, `"1.3"` opts up |
| `server.tls.clientauth` | `""` (off) | Client-certificate verification: `verify` (if given) or `require-verify` (mandatory), ADR-130 |
| `server.tls.clientcafile` / `server.tls.clientcavalue` | `""` | Client-CA bundle: file path or base64-encoded PEM (exactly one, required by a verifying `clientauth`) |

Bad or unreadable material fails `Start()` fast — it never silently falls back to plaintext. Staging cert/key material ahead of a flip (`server.tls.enabled: false` with material already set) is fail-open but not silent: startup emits one WARN naming `server.tls.enabled`; staged `clientauth`/client-CA keys count as material. With TLS enabled, `clientauth` values other than `""`, `verify` and `require-verify` (including Go's unverified `request`/`require`), a verifying `clientauth` without exactly one client-CA source, a client CA without a `clientauth`, and an unreadable, empty or corrupt bundle all fail startup; so does a leaf-validation hook (`app.Options.ServerOptions`) under an empty `clientauth`, while a hook with TLS disabled only WARNs. See [server_tls.md](server_tls.md) for the full config reference and deployment guidance (ALB-terminated partner mTLS vs. app-terminated mTLS).

## ALB Forwarded-Client-Cert Identity Middleware

`server.forwardedclientcert.*` (ADR-043) parses ALB verify-mode `X-Amzn-Mtls-Clientcert-*` identity headers. The default posture is **disabled** — the middleware is not wired into the request path at all:

| Setting | Default | Purpose |
| --- | --- | --- |
| `server.forwardedclientcert.enabled` | `false` | Wire the middleware; parse and expose the identity |
| `server.forwardedclientcert.require` | `false` | Reject (401) requests missing both `-Subject` and `-Serial-Number` (a malformed `-Leaf` alone never rejects); requires `enabled: true` |

On the application listener, health/ready probes — or, with `server.probes.port` set, their reserved `<base><path>` — always skip this middleware regardless of `require`; the probe listener does not install it at all. See [forwarded_client_cert.md](forwarded_client_cert.md) for the config reference, the trust model (including the AWS doc-silence finding on header spoofing), and an authorization recipe.

## Messaging Pre-Warm Readiness Wait

In single-tenant mode, startup pre-warms the messaging publisher and then waits for it to report `IsReady()`, bounded by `messaging.reconnect.readytimeout` (default 5s — the same key and budget as the per-publish readiness pre-flight; see [context_deadlines.md](context_deadlines.md)). A publisher that isn't ready in time logs a WARN and startup continues — the wait never fails startup; the publish-time pre-flight still absorbs a slow first publish. The wait (`App.awaitPublisherReady`, `app/prewarm.go`) is context-aware and reports a distinct cancellation outcome when its `ctx` is canceled, rather than mislabeling it as a readiness timeout — but that path only fires for callers that pass a cancelable context. Pre-warm runs on `prepareRuntime`'s own `ctx` parameter, not a context it creates itself; on the framework's own boot path, `Run()` seeds `prepareRuntime` with `context.Background()` (never canceled) and installs the OS signal handler only after `prepareRuntime` returns (`waitForShutdownOrServerError`), so a shutdown signal received during pre-warm does **not** abort the wait — it runs to ready-or-`readytimeout` regardless.

**Consumer-side readiness is opt-in and off by default.** The messaging `/ready` probe reads the publisher's `IsReady()` and nothing else unless `messaging.consumers.critical: true` is set, which makes the kind critical and folds in a consumer that has given up re-subscribing (five consecutive failed attempts — the threshold that escalates the log to WARN). Pre-warm is unaffected either way: it waits on the publisher, and the key governs the probe, not the boot window. Gate `livenessProbe` on `/health` before turning it on, or a long broker outage restarts the pod instead of only draining it. `cache.critical` is the same shape for the cache probe — see [cache.md](cache.md#readiness). Details: [messaging.md](messaging.md#failing-readiness-when-a-consumer-gives-up-messagingconsumerscritical), [ADR-114](adr_114_critical_consumer_readiness.md).

**Waiting for an exchange another service owns is opt-in and off by default.** `messaging.declare.externalwait` (duration, default `0`, env `MESSAGING_DECLARE_EXTERNALWAIT`) bounds an in-process wait for an external exchange that does not exist yet, so a consumer can deploy before its owner. On a 404 the control-plane startup declare pass — single-tenant, or multi-tenant under `messaging.tenancy: shared` — is re-run with backoff (first gap `min(1s, externalwait/4)`, doubling to a 5s ceiling) until it succeeds or the wait elapses, then startup aborts with the broker's own 404 naming the exchange. `0` aborts at once, which is the pre-key behavior. The wait only delays an abort that would otherwise happen, so a publisher-only service is never held, only a 404 is retried, and per-tenant lazy passes never wait. **Size a `startupProbe` from more than this number:** the key bounds when the last attempt *starts*, so the worst-case boot window is the first attempt, plus `externalwait`, plus one final attempt that runs to its own infrastructure-setup timeout. Details: [messaging.md](messaging.md#startup-wait), [ADR-119](adr_119_external_exchange_passive_verification.md).

**Operator guidance:** because neither the application listener nor the probe listener starts until pre-warm completes, raising `messaging.reconnect.readytimeout` directly stretches the pre-listen boot window whenever the broker is unreachable. `messaging.declare.externalwait` adds its own budget on top, but under the opposite condition — a REACHABLE broker that answers 404 for a missing external exchange; an unreachable broker is not a 404, so that failure still aborts at once. Size Kubernetes `startupProbe`/`livenessProbe` initial-delay and failure-threshold settings (or any other external "is it up yet" check) to comfortably exceed the whole pre-listen budget, not just the steady-state startup time: `readytimeout` when the broker is unreachable, and — when `externalwait` is set — the first declare attempt plus `externalwait` plus one final attempt that runs to its own infrastructure-setup timeout. The two are alternatives, not addends: a given boot hits one condition or the other. A single-tenant cache adds its own term: with Redis unreachable, the cache pre-warm redials after the failed pre-init, outside `app.startup.cache`, so budget one more Redis connect timeout (about 5s) on top ([ADR-127](adr_127_resource_plan_rule.md)).

## Startup Route Logging

Set `server.logroutes` (bool; env `SERVER_LOGROUTES`) to emit one `Info` line per registered HTTP route at startup:

```text
Route registered  module=events method=POST path=/v1/events listener=application
```

It is a **tri-state** flag: an explicit `server.logroutes` value always wins; when the key is absent it defaults to `app.env` being development (on in `dev`/`development`/`local`, off in `prod`/`staging` per ADR-022). So routes are visible at first `go run` while production stays silent — an N-route service pays **zero** extra boot lines in prod unless an operator opts in. Turn it on in production for a smoke-check with `server.logroutes: true`; silence a dev boot with `server.logroutes: false`.

Attribution is by **registration order** (`module.Name()`), covering both typed (`server.GET/POST`) and raw (`RouteRegistrar.Add`) routes — the log ignores `RouteDescriptor.ModuleName` and derives the module from the registration span. Routes registered before the module loop — the `health`/`ready` probes (one line per method, GET and HEAD) and debug / `_sys` — are attributed to `framework`.

Every line carries `listener`: `application`, or `probes` for the probes when `server.probes.port` is set, which then log their unprefixed paths (`path=/ready`) and no line for the application listener's `404` reservation at `<base><path>` ([ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md)).

## Duplicate Route Detection

Startup fails when two registrations claim the same **method + full path**, compared the way echo's router identifies a route: templates that differ only in a parameter or wildcard name (`/users/:id` vs `/users/:uid`, `/files/*` vs `/files/*rest`) are the same route. Echo v5's router refuses a duplicate only when `AllowOverwritingRoute` is off; `echo.New()` turns it on, and go-bricks builds its engines with `echo.New()`, so `Add` overwrites the handler while keeping the route template — and a service built on `server.New` without `app` used to boot with the later registration serving and the first handler dead on arrival. The check now lives in the server, at its registration seam (`server.RouteRegistrar`): a duplicate is recorded and **not** added, so the first registration keeps the route, and `server.Start` refuses to serve while any conflict is recorded — no listener binds. It holds without `app`; `app` still fails earlier, at registration time, before the route table hook runs. It covers typed (`server.GET/POST`) and raw (`RouteRegistrar.Add`) routes, plus anything registered through nested `Group()`s.

**Coverage notes:**

- `health`/`ready` probes register directly on the HTTP engine (not through `RouteRegistrar`), but `server.New` records their method+path pairs in the conflict tracker (and a descriptor per method in `server.DefaultRouteRegistry` — at the unprefixed path on the probe listener when `server.probes.port` is set, with none for the reservation) explicitly and first, so a module claiming `GET /health` (or the configured probe paths) is the refused duplicate: the probe keeps its handler, and startup fails like any other collision. Deleting the probe is no exit: serve custom readiness through `Server.RegisterReadyHandler`, move the probe with `server.path.health`/`server.path.ready`, or move the module route.
- `server.path.health` equal to `server.path.ready` is a duplicate too — nothing validates the pair, so readiness (`dispatchReady`) is the refused second registration and startup fails until the two paths differ.
- Param-name-differing route templates (e.g. `/users/:id` vs `/users/:uid`) are detected: the tracker keys a route by echo's node identity, so the conflict line names the duplicate's template and, where it differs, the first one's (`first: getUser (pkg) at /users/:id`). A static segment and a parameter (`/users/me` vs `/users/:id`) are distinct routes and do not conflict.

**Error shape:** startup (`app` at registration, or `server.Start`) aborts with one aggregate error naming every collision and both registrants (`HandlerName` + caller `Package`; the module name is not reported):

```text
duplicate route registration (1 conflict(s))
GET /v1/events — first: createEvent (github.com/example/events), duplicate: legacyCreateEvent (github.com/example/legacy)
```

The error is a `*server.DuplicateRouteError`: `errors.As` recovers it and its `Conflicts` field lists every collision structurally, and `errors.Is(err, server.ErrDuplicateRoute)` matches the sentinel. Its `Unwrap() []error` still exposes the children — the head line (which wraps the sentinel), then one error per conflict line.

There is no disable knob — a colliding route is always a startup-blocking bug, never a warning. Fix by removing or renaming the colliding route.

## Route Table Hook

`app.Options.PostRegisterRoutes func([]server.RouteDescriptor) error` lets a service veto its own route table before traffic arrives — for example, reject a route outside the versioned prefix, or a raw route (nil `RequestType`) where only typed handlers are allowed. It runs once per `App.Run`, after every module's `RegisterRoutes`, the route log and the duplicate-route check above, and before either listener opens. A non-nil error aborts startup the same way a route conflict does, wrapped as `app.Options.PostRegisterRoutes rejected the route table: <err>`. A nil hook changes nothing.

The slice holds every route this `App` registered and, as long as `App`s start one at a time, no other `App`'s — so several `App`s built in one test binary each see their own:

- module routes, typed and raw — the scheduler's `/_sys/job*` included;
- debug `/_sys/*` endpoints;
- the `health`/`ready` probes, **one descriptor per method** (GET and HEAD, so four), with `Package` set to `github.com/gaborage/go-bricks/server` and nil `RequestType`/`ResponseType`. At `server.probes.port: 0` they are base-path-qualified with an empty `Listener`; with the port set they carry `Listener: server.ListenerProbes` (`"probes"`), the unprefixed path and a `HandlerID` carrying the `probes:` prefix (`probes:GET:/ready`), so it stays unique across listeners — a `RootGroup` route at the same unprefixed path keeps `GET:/ready` — and the application listener's `404` reservation at `<base><path>` has no descriptor ([ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md)). With an injected `app.Options.Server`, the slice carries no probe descriptors.

`ModuleName` on this slice is the registering module's `Name()`, taken from the registration span, unless the route set its own with `server.WithModule`; routes the framework registers itself (probes, debug) carry an empty `ModuleName`. `Listener` is empty for every route but the probe listener's. The descriptors in `server.DefaultRouteRegistry` are not changed.

## Internal Probe Listener

`server.probes.*` ([ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md)) serves `/health` and `/ready` on a second, plain-HTTP **probe listener**, so they are never reachable where module routes are. The default posture is **disabled**:

| Setting | Default | Purpose |
| --- | --- | --- |
| `server.probes.port` | `0` (off) | Probe port, `1..65535`; `0` keeps the probes on the application listener at `<base><path>` |
| `server.probes.host` | unset (takes `server.host`) | Probe listener bind host; `127.0.0.1` for a loopback-only prober such as a sidecar |

The env vars are `SERVER_PROBES_PORT` and `SERVER_PROBES_HOST`. A delivered-empty `server.probes.host` — `SERVER_PROBES_HOST=`, or YAML `host: ""`, a bare `host:`, or a whitespace- or comma-only value — fails configuration resolution instead of widening a loopback bind to `server.host`; remove the key to take the default. The probe port may equal `server.port` only when the two effective hosts are distinct specific addresses: equal hosts, or either one unspecified (`""`, `0.0.0.0`, `::`), fail startup naming `server.probes.port` — at config validation, and again in `Start` before either bind for a config assembled in Go. The same two checks refuse the port beside `server.tls.clientauth: require-verify`, naming both keys: the application-listener check below presents no client certificate, so `/ready` could never pass; `verify` is allowed ([server_tls.md](server_tls.md#d-the-probe-listener-stays-plain-http), ADR-130). With the port set, an injected `app.Options.Server` that does not implement the probe seam (`ProbeErrors()`, `ProbeBoundAddr()`) also fails startup.

**What moves.** The probe listener serves `server.path.health` and `server.path.ready` exactly, **without** `server.path.base` (`/api/v1/ready` becomes `/ready`), and nothing else. The application listener keeps `<base><path>` reserved for `GET` and `HEAD` and answers `404` there; there is no dual-serve mode. The probe listener runs recover, request ID, the access logger (probe paths skipped), Secure headers and the `server.timeout.middleware` deadline — no rate limiter or IP pre-guard, tenant resolution, forwarded client certificate, CORS, gzip, body limit, OTel or module global middleware, and no TLS ([server_tls.md](server_tls.md#d-the-probe-listener-stays-plain-http)). It reuses `server.timeout.read`, `.write`, `.idle` and `.middleware`. Both listeners bind in `Start`, after pre-warm, the probe listener first; on shutdown it stops last, within a 1s budget of its own, so `/ready` answers `503` instead of refusing connections across the application drain.

**`/ready` gates.** Before the registered handler runs, `/ready` answers `503` `{"status":"not ready"}` when:

| Gate | Probe listener | Application listener (`server.probes.port: 0`) |
| --- | --- | --- |
| Shutdown has begun | Yes | Yes |
| The application listener is not serving yet (`ReadyCh` still open) | Yes | No |
| The application-listener check fails | Yes, with WARN `Application listener unresponsive` | No |

The check is a `HEAD` of the reserved `<base><ready path>` on the application listener with a fixed 500ms timeout, dialing `127.0.0.1` or `::1` when `server.host` is unspecified, else `server.host`. Any answer below `500` passes — the reservation's `404`, or a limiter's `429`; a timeout, connection error or `5xx` fails. Under `server.tls` it speaks HTTPS pinned to the listener's own leaf. No WARN is logged when the failure comes from shutdown beginning mid-check or from the prober abandoning its request. The check runs before the judgment inside the same `server.timeout.middleware` deadline, so the two share that budget. Concurrent requests share one in-flight check, never a finished result; for the judgment and a `RegisterReadyHandler` override see [observability.md](observability.md#readiness-endpoint).

**`/health` means the process is alive; `/ready` also watches the application listener.** A wedged application listener fails `/ready`, so the instance leaves rotation, but `/health` keeps passing and nothing restarts it — alert on an instance that stays unready.

**Operator guidance:**

- **Retarget port and path.** Kubernetes probes get `port: <probes.port>` and the unprefixed path ([manifest](cache.md#wiring-kubernetes-probes)); an ALB target group gets a health-check port override instead of `traffic-port`, and the unprefixed path; GKE Ingress needs a `BackendConfig` `healthCheck` with the port and path; the AWS Load Balancer Controller needs the `healthcheck-port` and `healthcheck-path` annotations. Under `server.tls`, also switch the probe scheme or `HealthCheckProtocol` to HTTP. External monitors that probed the public URL lose `/ready`, by design.
- **HTTP against `/ready`, never TCP.** A TCP connect succeeds as soon as the probe listener binds and throughout the application drain, so it says nothing about the application listener. A deployment limited to TCP health checks keeps `server.probes.port: 0`.
- **Load-balancer cutover.** A target group health-checks one port for every target, so during a rolling deploy old targets (no probe port) and new ones (no probes on the traffic port) cannot both pass. Cut over with blue/green or weighted target groups, or relax the unhealthy threshold for the rollout window. Kubelet probes are per-pod and need no cutover. ECS bridge mode with dynamic host ports is unsupported, since the health-check port override is fixed; use `awsvpc` or a static host port.
- **Network posture is the deployment's.** The framework binds the probe port; it does not restrict who reaches it, and `server.probes.host` defaults to `server.host`, typically `0.0.0.0`. Expose the port in the container but not on the public Service or Ingress, and restrict it with a NetworkPolicy, security groups admitting only the load balancer's subnets, or a host firewall; `docker -P`, NodePort and `hostNetwork` publish it unless excluded. Under a mesh that rewrites probes, check that the rewritten probe targets the probe port.
- **Plain HTTP, and a body that discloses nothing.** The probe listener carries no TLS ([server_tls.md](server_tls.md#d-the-probe-listener-stays-plain-http)), so its traffic is unencrypted — but since ADR-120 Part 2 that traffic is the verdict alone: `/ready` answers `{"status":"ready"}` or `{"status":"not ready"}` and `/health` `{"status":"ok"}`. The service name, version, backend kinds and pool statistics the old body carried are off the wire entirely; that detail now lives behind the access-controlled debug endpoints ([ADR-049](adr_049_debug_endpoints_fail_closed.md)).

## Probe Endpoints and Rate Limiting

`/health` and `/ready` on the application listener are **not** exempt from the framework's rate limiters. Both limiters are installed engine-globally with echo's never-skip skipper, so probe requests consume limiter budget like any other route:

| Setting | Default | Applies to probes |
| --- | --- | --- |
| `app.rate.limit` | 100 rps | Yes — global limiter; a value `<= 0` disables it entirely |
| `app.rate.ippreguard.enabled` | `true` | Registers the per-IP pre-guard |
| `app.rate.ippreguard.threshold` | 2000 rps/IP | Yes — per-IP abuse ceiling |

No path is exempt from either limiter. With `server.probes.port` set, `/health` and `/ready` leave both by moving to the probe listener, which has none; on the application listener the reserved probe paths stay inside both, the `HEAD` the application-listener check sends included, and a `429` there still counts as a live listener ([ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md)).

That per-IP ceiling is only a ceiling because the client IP is derived through the trusted-proxy chain (`echo.ExtractIPFromXFFHeader` in `server.New`, plus `server.trustedproxies` for a proxy on a public address) — before [ADR-057](adr_057_trusted_proxy_ip_extraction.md) the key was the caller-written left-most `X-Forwarded-For` entry, which any client could rotate to get a fresh bucket per request.

Probe traffic is always keyed by **client IP**, never by tenant: the probe skipper bypasses tenant resolution for the requests the probe routes serve — the matched health/ready route, on `GET`/`HEAD`. The two limiters differ in what else lands in that same IP bucket: `app.rate.ippreguard.threshold` (`ipPreGuardEcho`) runs *before* tenant resolution and keys every request — probe or tenant-resolved — by client IP, so it is one shared per-IP budget across all traffic from that address, while `app.rate.limit` (`rateLimitEcho`) runs *after* tenant resolution and keys by the resolved **tenant ID** first, falling back to client IP only when no tenant was resolved — true for probes, but not for a tenant's own ordinary traffic, which draws on that tenant's separate budget instead.

**Operational consequence.** A saturating client that shares a source IP with the prober — an L3/L4 NAT, or any hop that forwards without rewriting the client address — can push the probes themselves to `429` on the pre-guard regardless of that client's own tenant, and on the global limiter too whenever the client's traffic is itself untenanted. The two outcomes differ: a rejected `/ready` drops the instance from the load balancer's rotation, while a rejected `/health` fails one liveness probe; per the wiring documented under [Wiring Kubernetes probes](cache.md#wiring-kubernetes-probes), repeated `/health` failures — `failureThreshold` consecutive ones, not a single `429` — can restart the container, and a failing readiness probe never restarts anything. Mitigate by raising `app.rate.limit` / `app.rate.ippreguard.threshold` for that deployment, or by giving probe traffic a path to the instance that does not share a source IP with application traffic — structurally, [`server.probes.port`](#internal-probe-listener), which takes the probes out of both limiters.

These are *koanf* defaults. A `*config.Config` assembled in Go rather than loaded through configuration leaves both at zero, and the global limiter is a pass-through at `<= 0` — such a deployment has no ceiling at all (see [ADR-049](adr_049_debug_endpoints_fail_closed.md)).
