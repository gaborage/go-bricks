# ADR-120: Probes May Be Served on an Internal Listener, and `/ready` Answers Status Only

**Status:** Proposed — Part 1 (non-breaking; the listener is opt-in) may ship under this status;
the Part 2 PR flips it to Accepted, so the breaking change never ships under a Proposed ADR.
**Date:** 2026-09-23
**Issue:** #1791
**Breaking:** yes — the `/ready` body and `HealthStatus.PublicErr` (Part 2)
**Amends:** ADR-002 (base path), ADR-048 (503 text), ADR-057 (probe limiter coverage), ADR-066
(rule 3), ADR-094, ADR-114

## Context

`/health` and `/ready` register directly on the one Echo engine the application serves
(`server/server.go:195-210`), so every route a service exposes to the internet exposes its probes
too. [ADR-048](adr_048_ready_sanitize_by_default.md) calls `/ready` unauthenticated "by design", and
[ADR-066](adr_066_readiness_one_module.md) rule 3 gives it one body. That body is not minimal:

```json
{"status":"ready","time":1790000000,
 "app":{"name":"orders","environment":"production","version":"1.4.2"},
 "database":"healthy","database_stats":{"active_connections":4,"max_connections":25,...},
 "messaging":"healthy","messaging_stats":{"declared_consumers":3,...}}
```

An anonymous caller learns the service's name, environment and version, which backends it depends
on, and pool sizing and error counters for each. ADR-048 closed the 503 body's credential leak; the
503 body still names the blocking kind (`"cache":"unhealthy","error":"cache unavailable"`), which
tells an attacker which dependency to pressure.

Renaming the paths (`server.path.*`, ADR-002) hides them without securing them. Blocking them at the
ingress or load balancer works, but every deployment has to remember to, and the framework gives no
help: the probes share a port, a base path and the rate limiters with application traffic. The last
is a defect of its own — `wiki/startup_defaults.md` documents that a noisy client behind the
prober's source IP can push `/ready` to `429` and drop the instance from rotation.

The body is also the only signal some kinds produce. A failing non-critical kind (cache by default,
[ADR-094](adr_094_cache_readiness_non_critical_default.md); streams) answers `200` and logs nothing,
and the consumer counters [ADR-114](adr_114_critical_consumer_readiness.md) relies on and the
streams counters have no OTel instrument. The unredacted detail's gated home, `/_sys/health-debug`
(`app/debug_health.go`), is off by default (`debug.enabled: false`).

## Decision

### 1. An opt-in internal probe listener — `server.probes.*`

- **`server.probes.port`** (int, default `0`, env `SERVER_PROBES_PORT`). `0` disables the listener:
  probes stay on the application listener at `<base><path>`, inside its rate limiters, as before
  this ADR; only the stopping `503` and the coalesced judgment (below) apply at `0` as well. A
  zero-default opt-in key, it has no koanf default entry. Otherwise the value must be `1..65535`
  and pass the collision rule; startup refuses anything else, naming the key. A non-zero port
  starts a second `http.Server` that serves `/health` and `/ready` **and nothing else**, and the
  application listener stops serving them (see **Reserved routes**). There is no dual-serve mode:
  a probe reachable on both ports is the exposure this key exists to remove.
- **`server.probes.host`** (string, env `SERVER_PROBES_HOST`, unset by default). An exported
  `ServerConfig` accessor returns the effective probe host — `probes.host` when set, else
  `server.host` — and both config validation and `server.New` use it, so a Go-assembled config
  that skipped validation binds the same address. A delivered-empty value (`SERVER_PROBES_HOST=`)
  is rejected at config resolution (ADR-078): resolving it to `server.host` would silently widen a
  loopback bind. A deployment that probes over loopback only (a sidecar) sets `127.0.0.1`.
- **Collision rule.** Judged on the two effective hosts, as strings:

  | Hosts | `probes.port` equal to `server.port` |
  | --- | --- |
  | Equal | Refused, naming the key |
  | Either unspecified (`""`, `0.0.0.0`, `::`) | Refused, naming the key |
  | Distinct, aliases included (`localhost` and `127.0.0.1`) | Passes; the bind fails with the OS error only when the addresses the two names resolve to overlap |

  `Start` re-applies the rule to the configured keys before either bind: a Go-assembled config
  skips validation, and darwin binds `0.0.0.0:P` beside `127.0.0.1:P` without error. The test
  seam's ephemeral port leaves `probes.port` at `0`, so the rule does not judge it.
- **Test seam.** The ephemeral-port hook is an unexported option consumed by `New`, because
  middleware and routes are set up there; `0` stays "disabled" in config. `Server.ProbeBoundAddr()`
  is exported and returns the bound address, like `BoundAddr()`. A consumer test that wants a probe
  listener binds a real free port.
- **Paths.** The probe listener serves `server.path.health` and `server.path.ready` exactly,
  **without** `server.path.base`. The base path routes public traffic through a shared ingress; the
  probe listener is never behind one. This amends ADR-002's "base applies to all routes including
  health endpoints" for the probe listener only.
- **Engine and middleware.** The probe listener is its own Echo engine, with the same
  panic-logging `HTTPErrorHandler` wrapper around `customErrorHandler` and both recover layers the
  application engine has. Its chain: outermost recover, request ID, request logger (with its
  probe-path handling), `Recover` + `sanitizePanicValue`, Secure headers, middleware timeout. It
  gets no rate limiter or IP pre-guard, `requestEnrich`, tenant resolution, forwarded client
  certificate, CORS, gzip, body limit, timing, OTel HTTP or module global middleware. Leaving out
  `requestEnrich` and the opt-in timing middleware is deliberate: probes carry no trace context, as
  the OTel middleware already skips them, and without the lease scope a handle a probe borrows is
  released at once, the documented fallback for unscoped contexts. (On the application listener
  the probe skipper exempts probes from OTel, tenant resolution, forwarded client certificate and
  module global middleware (ADR-036), and the request logger has its own probe-path handling;
  `requestEnrich`, CORS, the IP pre-guard, Secure headers, the middleware timeout, body limit,
  gzip, the rate limiter and the opt-in timing middleware run on them.)
- **Reserved routes.** With `probes.port > 0` the application engine registers static `GET` and
  `HEAD` routes at `<base><health path>` and `<base><ready path>` whose handler returns
  `echo.ErrNotFound` through the normal error path, so they answer `404`. They are recorded in the
  conflict tracker and kept out of `DefaultRouteRegistry`. Echo matches a static route before a
  param route, a wildcard route or a group's `RouteNotFound` catch-all, whatever the registration
  order, so a module `/:id`, `/*` or middleware-bearing group under the base never serves the
  reserved path. Other methods at that path route as they do today.
- **Limiter exemption on the application listener.** Only with `probes.port > 0`, the rate limiter
  and the IP pre-guard skip a `GET` or `HEAD` whose matched route template (`c.Path()`) is the
  reserved `<base><ready path>`, so the application-listener check never spends or is refused
  limiter budget; such a request can only reach the `404` stub. `c.Path()` holds the template
  because both limiters sit in the `e.Use` chain, which Echo runs after routing, and the framework
  registers no `e.Pre` middleware; a request with no route has an empty `c.Path()` and is never
  exempt. The rule matches the template, never the decoded URL path: `<base>/%72eady` decodes to
  the reserved path but routes on its raw form, to a module param or wildcard route or a group
  catch-all. The method guard keeps a module's own `POST <base><ready path>`, which the tracker
  allows and which carries the same template, inside the limiters. At `probes.port: 0` the
  limiters apply to probes exactly as today (`wiki/startup_defaults.md`). The skipper compares
  values fixed at setup and adds no per-request allocation (ADR-026;
  `TestDefaultMiddlewareChainAllocsStable`, ceiling 64).
- **No limiter on the probe listener.** No IP-keyed or application-shared request-rate limiter may
  be added to the probe listener: removing the shared budget is what fixes the `429` defect.
  Coalescing (below) bounds what a burst costs the backends instead — for the framework's own
  work only: a consumer `RegisterReadyHandler` override runs once per request, so an override that
  reaches a backend bounds its own cost.
- **Timeouts.** The probe listener reuses `server.timeout.read`, `.write`, `.idle` and
  `.middleware`; there are no probe-specific timeout keys. On the probe listener the middleware
  timeout bounds the application-listener check and the judgment together.
- **No TLS.** `server.tls.*` governs the application listener only. The probe listener is plain
  HTTP: no credential rides a probe, and after Part 2 the body carries one status word. A TLS
  opt-in would need its own certificate material and rotation, and after Part 2 it would protect
  nothing but that status word. Until
  Part 2 ships, the probe listener serves today's detailed body in plaintext, so a deployment that
  sets `probes.port` restricts the listener to its probe sources, keeps it off every public network
  path (see Consequences), and keeps probe traffic on a trusted, isolated network: restricting
  sources does not stop a passive observer on a shared one. A deployment that cannot guarantee that
  isolation waits for Part 2 before setting `probes.port`; the probe listener offers no TLS.
- **Seam.** `ServerRunner` is unchanged. The probe listener is reached through an optional
  interface (`ProbeErrors() <-chan error`, `ProbeBoundAddr() net.Addr`), type-asserted on the
  injected runner the way `applyGlobalMiddleware` asserts its seam. With `probes.port > 0` and an
  injected `Options.Server` that does not implement it, startup fails naming the key; the key is
  never silently ignored. `ProbeErrors()` is non-nil from `New`. It sends at most one serve error,
  never `http.ErrServerClosed`, and is closed exactly once on every path: immediately when the
  probe listener is disabled, when `Start` returns without binding it, or when its `Serve`
  goroutine exits. The app's forwarder therefore needs no nil or port guard.

**Lifecycle.** Before `Start`, nothing listens on either port, as today; `/health` on the probe
port has today's semantics, and `startupProbe` sizing in `wiki/startup_defaults.md` stands.

| Concern | Rule |
| --- | --- |
| Storage | `probeServer atomic.Pointer[http.Server]`, stored in a new `lifecycleMu` critical section inside `Start`, before the application bind, that reads `stopping`. `onBeforeServe`'s section cannot hold it: it runs inside Echo's start, after the application bind. A set latch vetoes the store: the listener closes and `Start` returns `http.ErrServerClosed`. |
| Start once | The existing `started` CAS covers both listeners; a second `Start` binds neither. |
| Start-time refusals | Before either bind, with the probe listener enabled: the collision rule, re-applied to the configured keys; and with `server.tls.enabled` too, a leaf certificate with no SAN (see **Application-listener check**). |
| Bind order | Inside `Server.Start`, after the TLS config is built: the probe listener binds and serves, then the application listener binds immediately after. |
| Probe bind fails | `Start` returns the error before the application listener binds; `ReadyCh` never closes. |
| Later failure in `Start` | Any failure after the probe listener bound (the application bind included) closes the probe listener before `Start` returns. |
| `Start` return | `nil` after a graceful `Shutdown`, because Echo filters `http.ErrServerClosed`; `http.ErrServerClosed` comes only from a latch veto. |
| Error fan-in | A change: `serve()` goes from a capacity-1 channel with one sender to capacity 2 with one sender per listener, the `Start` goroutine and a forwarder reading `ProbeErrors()`. A `sync.WaitGroup` over both senders gates `close`. `drainServerError` changes from one receive to reading until the channel closes, bounded by the outer timeout, and `Run` calls it on the server-error path as well as the shutdown path. Either listener's serve error ends `Run`. |
| Shutdown | `Server.Shutdown` sets `stopping`, drains the application listener within the caller's ctx, and then stops the probe listener — on every path, including when the application drain returns an error, which is returned only after the probe cleanup. The cleanup runs at the end of `Server.Shutdown`, before `App.Shutdown` reaches `stopSlots`, with a 1s ctx detached from the caller's (`context.WithoutCancel`) and `Close()` on expiry. A deadline error from the probe listener alone logs WARN and is not returned. |
| In-flight judgments | A judgment on the probe listener is bounded by its flight budget (see **Coalescing**), not by the probe listener's stop: `Close()` does not wait for handlers, and `stopSlots` does not wait for a flight that outlives it. A late flight reads a stopping slot and reports unhealthy, which is harmless: `/ready` already answers `503` from the latch. |
| Shutdown before `Start` | The latch vetoes both listeners; `Start` returns `http.ErrServerClosed` and binds neither. |

The probe listener stops last so `/ready` answers `503`, not a refused connection, across the drain;
its detached budget keeps a probe in flight at the deadline from failing a clean exit.

**`dispatchReady`.** It serves `/ready` on whichever listener carries it, runs these gates in
order, and only then calls the registered handler:

| Gate | Probe listener | Application listener (`probes.port: 0`) | Fails with |
| --- | --- | --- | --- |
| `stopping` set | Yes | Yes | `503` `{"status":"not ready"}` |
| `ReadyCh` not yet closed | Yes | No | The same `503` |
| Application-listener check | Yes | No | The same `503` and WARN `Application listener unresponsive` |

The body word is a server-package constant, `statusNotReady`, equal to `app`'s `notReadyStatus`,
which `server` cannot import. The `ReadyCh` gate and the check sit only on the probe listener's
dispatch path: in-process tests call `/ready` on servers that never started, and the application
listener serves only after `ReadyCh` closes. `RegisterReadyHandler` replaces the handler behind
the gates on whichever listener serves `/ready`. The status-only rule of Part 2 binds the
framework's handlers; a consumer override's body is the consumer's own disclosure. The latch and
the application listener's close happen in one call, so the `503` is not a deregistration signal:
a `preStop` sleep (or the load balancer's deregistration delay) remains what takes an instance out
of rotation before `SIGTERM`.

**Application-listener check.** A `HEAD` of the reserved `<base><ready path>` sent to the
application listener with a 500ms timeout, a fixed constant. It dials a host derived from the
configured `server.host`, never the bound address string (`BoundAddr()` reports `[::]:P` for
`0.0.0.0`, and Windows refuses a connect to an unspecified address), and the port from
`BoundAddr()`:

| `server.host` | Dial host |
| --- | --- |
| `""` or `0.0.0.0` | `127.0.0.1` |
| `::` or `[::]` | `::1` |
| Anything else | `server.host` as configured |

Every listen and dial address is built with `net.JoinHostPort` after trimming any surrounding
brackets from the host, so `::` and `[::]` both yield `[::]:P`; string formatting would produce the
invalid `:::P`, and `JoinHostPort` on a still-bracketed host the invalid `[[::]]:P`.

It speaks HTTPS when `server.tls.enabled` is set and HTTP otherwise. Under TLS, verification is
pinned, never skipped: `RootCAs` is a pool holding only the application listener's own configured
leaf certificate, and `ServerName` is the leaf's first DNS SAN, else its first IP SAN. A SAN
covering the dialed address cannot be assumed, so the pin names the leaf instead. Go's verifier
accepts a leaf found in `RootCAs` as its own root without `IsCA`, and a wildcard SAN
(`*.example.com`) matches itself as `ServerName`. The leaf is loaded once in `Start`, so the pin
cannot go stale within a process; its `NotAfter` still applies (see Consequences). A leaf with no
SAN makes `Start` fail before either bind when `server.probes.port > 0` and `server.tls.enabled`,
naming both keys. The check never sets `InsecureSkipVerify`: SonarCloud `go:S4830` is an active
CRITICAL vulnerability rule in the project profile. `server.tls` verifies no client certificates,
so the check needs none; a future client-certificate mode must revisit it.

The reserved route answers `404` through the application engine's whole middleware chain, minus
the limiters (see **Limiter exemption**); any non-`5xx` answer passes, and a timeout, connection
error, flight error or `5xx` fails the gate. A TCP connect alone would not do: the kernel completes
the handshake into the accept backlog even when the process has stopped serving.

**Coalescing.** Two singleflights, each sharing an in-flight result and never reusing a finished
one:

| Flight | Key | Budget |
| --- | --- | --- |
| The application-listener check | Per `Server` | 500ms |
| The framework's readiness judgment, inside `App.readyCheck` | Per `App` | `server.timeout.middleware` |

The judgment flight sits in `App.readyCheck`, so it coalesces on either listener and at
`probes.port: 0`; a consumer `RegisterReadyHandler` override is not coalesced. Each flight runs on
`context.WithoutCancel` of the leader's request context, bounded by its budget, so the leader's
cancellation reaches no follower. Waiters join through `DoChan` and also select on their own
request context: a waiter whose request is canceled leaves early, and on the judgment it logs
today's abandoned-request WARN. A flight returns a verdict and writes no response, because a
handler writes to one request's `echo.Context`; each waiter logs and renders its own answer from
the verdict. The flight function recovers its own panic and returns it as an error naming the
panic by type only (`%T`, ADR-081), so `DoChan`'s `go panic(e)`, which no recover layer catches,
is unreachable. A waiter treats that error as a failed flight: the check's gate fails, and the
judgment's waiters return it through the engine's error handler, as a handler panic answers today.

**Route table.** `/health` and `/ready` stay reserved in the application listener's conflict
tracker at `<base><path>` whatever `probes.port` says, so flipping the key never changes which
module routes are legal. With the port set the reservation is the `404` stub (see **Reserved
routes**), which carries no descriptor. `RouteDescriptor` gains `Listener string`: empty for the
application listener, `"probes"` for the probe listener. With the port set, the four probe
descriptors (`GET` and `HEAD` for each probe) carry `Listener: "probes"`, `Path` the unprefixed
probe path, and `HandlerID` `formatHandlerID(method, path)` (`GET:/ready`). For a probe-listener
descriptor `HandlerID` identifies the route on its own listener; it intentionally differs from the
application listener's reservation key `<base><path>` in the conflict tracker (`GET:/api/ready`
under base `/api`), and `formatHandlerID`'s doc says so. `PostRegisterRoutes` and
`server.logroutes` see these descriptors; the route log line adds a `listener` field.

### 2. `/ready` answers status only — breaking

- **200 body:** `{"status":"ready"}`, from `App.readyCheck` and from the fallback
  `server.readyCheck`, on either listener. `time`, `app`, the per-kind status keys and every
  `<kind>_stats` object are removed. This replaces ADR-066 rule 3.
- **503 body:** `{"status":"not ready"}`, the same body `dispatchReady` serves while stopping. The
  blocking kind and ADR-048's `"<kind> unavailable"` text leave the body; they stay in the
  `Readiness check failed` log line (`component=<kind>`, full error), unchanged.
- **`/health`** is unchanged: `{"status":"ok"}`.
- **Dead surface is deleted.** `publicProjection`, `statsSuffix`, the `*PublicStats` allowlists
  (`app/readiness.go:229-248`), `probeDescription.publicStats` and its four slot sites,
  `publicProbeError` and `errorKey` lose their last reader. So does `HealthStatus.PublicErr`; an
  exported field that silently does nothing is worse than a compile break with its own atom. The
  `Name` and `Prober` SECURITY comments in `app/health.go` are rewritten: no body carries `Name`.

**Replacement signal, in the same PR.** The trim never lands without it.

- **Readiness gauge.** `app.readiness.status` (Int64 observable gauge) reports each kind's **last
  verdict** — the status the most recent readiness judgment (`/ready` or `/_sys/health-debug`)
  recorded for it — with attributes `readiness.kind` (`database`, `messaging`, `cache`, `streams`)
  and `readiness.critical` (the kind's critical setting). The value is `1` for `healthy` and `0`
  for `unhealthy`. A kind has a series only while
  its last verdict is one of those two; `disabled`, `not_configured`, `per_tenant` and a kind not
  yet judged have none, so a dashboard never shows an unused kind as healthy and a deploy never
  starts at `0`. The last verdict is a view, never an input: judging never consults it, and the
  callback never runs a probe, so export adds no backend round-trip. It is only as fresh as the
  last judgment — with nothing polling `/ready` it holds a stale value — so alerting keys on the
  probe itself, not on this series alone.
- **Consumer and streams gauges.** Int64 observable gauges read from the managers' in-memory
  `Stats()` at collection, covering the `_stats` counters OTel lacks:

  | Metric | Former `/ready` key |
  | --- | --- |
  | `messaging.consumer.registries` | `messaging_stats.consumer_registries` |
  | `messaging.consumer.declared` | `messaging_stats.declared_consumers` |
  | `messaging.consumer.subscribed` | `messaging_stats.subscribed_consumers` |
  | `messaging.consumer.resubscribes` | `messaging_stats.consumer_resubscribes` |
  | `messaging.consumer.max_fail_streak` | `messaging_stats.consumer_max_fail_streak` |
  | `messaging.streams.consumers` | `streams_stats.consumers` |
  | `messaging.streams.publishers` | `streams_stats.publishers` |

  Database pool and cache manager counters already have instruments (`db.client.connection.*`,
  `cache.manager.*`). The remaining manager counters (messaging publisher pool, database manager
  `removals`/`errors`, streams offset settings) stay on `/_sys/health-debug` only.
- **Non-critical WARN.** When a judgment records a non-critical kind as `unhealthy`, the framework
  logs WARN `Readiness component unhealthy` (`component=<kind>`, `critical=false`, full error) on
  the transition into `unhealthy`, then at most once per minute per kind while it stays so (a fixed
  constant, not a config key), and one INFO `Readiness component recovered` on the transition out,
  so the end of an incident is visible in logs alone. Critical kinds keep the ERROR line. With
  observability disabled these lines are the only in-process signal.

## Alternatives Considered

- **Bind the probe listener before pre-warm, so a `startupProbe` answers during the boot window.**
  Rejected: `/health` would pass through a hung boot, so a liveness or startup probe on it could no
  longer restart one; `/ready` would answer `503`, the same verdict as today's refused connection;
  and an early `/ready` races the judge's startup writes (`startSlots`, `seal`).
- **An IP allowlist on the probe paths** (reusing the debug allowlist). Rejected as the primary
  mechanism: kubelet probes come from the node IP, ALB health checks from the load balancer's
  subnets, and a correct answer depends on the trusted-proxy chain (ADR-057, ADR-080). A separate
  port needs no IP reasoning.
- **A bearer token on the probes.** Rejected: ALB target health checks cannot send headers, and a
  kubelet `httpGet` header puts the token in the pod spec.
- **Keep the detailed body behind a flag** (`server.probes.detail: true`). Rejected: a second body
  shape, a default an operator can get wrong, and the detail already has a gated home.
- **Move `/_sys` onto the probe listener too.** Deferred, not rejected. The debug endpoints and the
  scheduler's `GET /_sys/job` and `POST /_sys/job/:jobId` (gated by
  `scheduler.security.cidrallowlist`) are already access-controlled, and moving them changes the
  ADR-049 contract. That deserves its own decision.
- **Keep `/health` on the application listener, move only `/ready`.** It would restore liveness
  restarts for a wedged application listener, but leaves a probe reachable publicly and splits the
  probes across two listeners. The application-listener check takes the pod out of rotation
  instead.
- **Make the probe port the default** (for example `8081`). Rejected for now: it would silently
  break every deployment whose probes target `server.port`. A later ADR can flip the default.
- **Leave the reserved paths without an engine route on the application listener.** Rejected: Echo
  would route `<base><ready path>` to a module param or wildcard route or a group catch-all, so the
  check would run consumer code and pass or fail on its status, and the limiter exemption would
  cover consumer traffic.
- **Exempt on the decoded URL path, as `CreateProbeSkipper` matches.** Rejected: Echo routes on the
  raw path, so `<base>/%72eady` would carry the exemption to whichever route serves it.
- **Skip certificate verification in the check.** Rejected: `InsecureSkipVerify` trips SonarCloud
  `go:S4830`, a CRITICAL vulnerability, and gosec G402. Pinning the process's own leaf costs no
  more and confirms the peer presents this process's certificate.

## Consequences

**Every deployment (Part 1):**

- Once `Shutdown` sets the latch, `/ready` answers `503` `{"status":"not ready"}` on whichever
  listener serves it, `probes.port: 0` included, instead of judging until the listener closes.
- Concurrent `/ready` requests share one in-flight framework judgment; a consumer
  `RegisterReadyHandler` override runs once per request, as today.

**Operators who set `server.probes.port`:**

- **Retarget port and path.** Kubernetes `livenessProbe`, `readinessProbe` and `startupProbe` get
  `port: <probes.port>` and lose the base prefix (`/api/v1/ready` becomes `/ready`). The ALB target
  group gets a health-check port override instead of `traffic-port`, and the unprefixed path. GKE
  Ingress needs a `BackendConfig` `healthCheck` with the port and path; the AWS Load Balancer
  Controller needs the `healthcheck-port` and `healthcheck-path` annotations. External monitors
  that probed the public URL lose `/ready`, by design.
- **TLS deployments** also switch the probe scheme (`scheme: HTTP`) or `HealthCheckProtocol` to
  HTTP; changing only the port fails every probe. The application-listener check pins the
  application listener's own leaf: a leaf with no SAN fails `Start`, and a leaf that expires while
  the process runs fails `/ready`, as it already fails every verifying client.
- **HTTP against `/ready` only, never TCP.** A TCP connect succeeds as soon as the probe listener
  binds and throughout the application drain, so it says nothing about the application port. A
  deployment limited to TCP health checks keeps `probes.port: 0`.
- **`/ready` watches the application listener; `/health` does not.** The application-listener
  check makes a wedged application listener fail `/ready`, so the pod leaves rotation. `/health`
  keeps meaning "the process is alive" and does not restart it, which avoids restart loops under
  load; a pod that stays unready is the deployment's alert to raise.
- **Load-balancer cutover.** A target group health-checks one port for every target, so in a
  rolling deploy old tasks (no probe port) and new ones (no probes on the traffic port) cannot both
  pass. Cut over with blue/green or weighted target groups, or relax the unhealthy threshold for
  the rollout window. Kubelet probes are per-pod and need no cutover. ECS bridge mode with dynamic
  host ports is unsupported (the health-check port override is fixed); use `awsvpc` or a static
  host port.
- **Network posture is the deployment's.** The framework binds; it does not enforce who can reach
  the port. `probes.host` defaults to `server.host`, typically `0.0.0.0`. Expose the port in the
  container but not on the public Service or Ingress, and restrict it with a NetworkPolicy,
  security groups admitting the load balancer's subnets, or a host firewall on VMs; `docker -P`,
  NodePort and `hostNetwork` publish it unless excluded. Under a mesh that rewrites probes, check
  the rewritten probe targets the probe port.
- Probes on the probe listener stop sharing rate-limit budget and source-IP buckets with application
  traffic, and ADR-057's per-IP pre-guard ceiling stops covering `/ready`; coalescing bounds a
  burst's backend cost instead, for the framework's work (a consumer override bounds its own). Until Part 2 ships, the probe listener serves today's body in
  plaintext, so its traffic stays on a trusted, isolated network (see **No TLS**).

**Consumers of the `/ready` body (Part 2):**

- **Migrate to metrics first**: `app.readiness.status`, the consumer and streams gauges above, and
  the existing `db.client.connection.*` and `cache.manager.*` instruments.
- **`/_sys/health-debug` second**, for per-kind details and full errors. It needs
  `debug.enabled: true`, `debug.endpoints.health` (default `true`), and `debug.allowedips` or
  `debug.bearertoken` (ADR-049 refuses neither). It is more sensitive than the old body. Behind an
  ALB, set `debug.trustedproxies` to the ALB's subnets: with it empty every request's source is the
  ALB, so allowlisting the ALB or VPC CIDR admits every client. Token-only access is not for
  internet-facing deployments.
- Orchestrators read only the status code, so Part 2 changes no probe configuration. Code that sets
  `HealthStatus.PublicErr` stops compiling; delete the assignment.

**Inventory.** Every consumer of the old body in the repository is found by:

```sh
git grep -nE '(database|messaging|cache|streams)(_stats| unavailable)|/ready`? (reports|publishes|carries|gains|and `?Stats)|public-stats|publicStats|PublicErr|publicProbeError|publicProjection|errorKey|\["time"\]' \
  -- app/ server/ 'wiki/*.md' llms.txt README.md .claude/skills/ .out-of-scope/ \
  ':!wiki/adr_*' ':!wiki/migrations.md' ':!wiki/architecture_decisions.md'
```

It finds 23 files today: `app/`, `server/server_test.go`,
`wiki/{cache,database,messaging,observability,streams,troubleshooting}.md`, `llms.txt`, the
`SKILL.md` entries for ADR-047/048/094 and `.out-of-scope/readiness-contribution-door.md`. ADRs and
existing atoms get amendment blockquotes, not rewrites. Part 1 docs it cannot find:
`wiki/startup_defaults.md` (`probes.port` as the `429` mitigation, the exemption only with it set,
and the forwarded-client-cert probe line), `wiki/server_tls.md`, the `wiki/cache.md` probe
manifests and their `timeoutSeconds` arithmetic (the check spends up to 500ms before the
judgment), `wiki/observability.md` (an override replaces `/ready` behind the gates, not
wholesale), the `wiki/testing.md` port-0 server recipe (a probe listener needs a real free port),
and the limiter comments on `App.readyCheck` (`app/lifecycle.go`) and in `app/lifecycle_test.go`.
Tests assert the status-only body, that no kind name appears, and that `/ready` answers `503`
during the application drain.

## Delivery

The ADR file, its `architecture_decisions.md` index entry, the counter bump and the `CONTEXT.md`
glossary entries (*Application listener*, *Probe listener*) merged in #1798. Part 1 ships as a
stack, bottom to top; it breaks nothing, so it may ship under Proposed:

1. This contract.
2. `dispatchReady` answers `503` while stopping; `statusNotReady`.
3. The config keys, the server-package probe listener lifecycle and the reserved routes. Probe
   serve errors reach `Run` only from the next layer.
4. App integration: the optional-interface assert failing closed, error fan-in, drain, shutdown
   order.
5. Route table: the `Listener` field, probe descriptors, the `server.logroutes` field,
   `PostRegisterRoutes`, and the ADR-002 amendment blockquote.
6. The application-listener check, pinned TLS, coalescing, the limiter and pre-guard exemption,
   and the ADR-057 amendment blockquote.
7. Operator docs: the Part 1 list under **Inventory**.

Part 2 is one PR, `fix(app)!: answer /ready with status only`, with the gauges and the non-critical
WARN in the same diff. It adds amendment blockquotes to ADR-048, ADR-066, ADR-094 and ADR-114, the
`wiki/migrations.md` atoms (the body; `PublicErr`), the breaking-changes `SKILL.md` entry and the
inventory sweep, and flips this ADR to Accepted.

The listener goes first because it closes the exposure independently of the body's shape, in a
release that breaks nothing. The trim is the only break; last, its replacement signal, amendments
and atoms land in one diff with the flip to Accepted.
