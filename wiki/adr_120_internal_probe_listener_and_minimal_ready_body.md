# ADR-120: Probes May Be Served on an Internal Listener, and `/ready` Answers Status Only

**Status:** Proposed — Part 1 (opt-in, non-breaking) may ship under this status; the Part 2 PR
flips it to Accepted, so the breaking change never ships under a Proposed ADR.
**Date:** 2026-09-23
**Issue:** #1791
**Breaking:** yes — the `/ready` body and `HealthStatus.PublicErr` (Part 2)
**Amends:** ADR-002 (base path), ADR-048 (503 text), ADR-066 (rule 3), ADR-094, ADR-114

## Context

`/health` and `/ready` register directly on the one Echo engine the application serves
(`server/server.go:198-208`), so every route a service exposes to the internet exposes its probes
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
  probes stay on the application listener at `<base><path>`, as before this ADR. Otherwise the
  value must be `1..65535` and differ from `server.port` when the two hosts overlap (equal, or
  either unspecified); startup refuses anything else, naming the key. A non-zero port starts a
  second `http.Server` that serves `/health` and `/ready` **and nothing else**, and the application
  listener stops serving them. There is no dual-serve mode: a probe reachable on both ports is the
  exposure this key exists to remove.
- **`server.probes.host`** (string, env `SERVER_PROBES_HOST`, default `server.host`).
  `normalizeServer` resolves an empty value to `server.host`, and `server.New` applies the same
  helper so a Go-assembled config that skipped normalization binds the same address. A deployment
  that probes over loopback only (a sidecar) sets `127.0.0.1`.
- **Test seam.** Tests bind the probe listener to an ephemeral port through an unexported hook (`0` stays
  "disabled" in config); `Server.ProbeBoundAddr()` returns the bound address, like `BoundAddr()`.
- **Paths.** The probe listener serves `server.path.health` and `server.path.ready` exactly,
  **without** `server.path.base`. The base path routes public traffic through a shared ingress; the
  probe listener is never behind one. This amends ADR-002's "base applies to all routes including
  health endpoints" for the probe listener only.
- **Engine and middleware.** The probe listener is its own Echo engine, with the same
  panic-logging `HTTPErrorHandler` wrapper around `customErrorHandler` and both recover layers the
  application engine has. Its chain: outermost recover, request ID, request logger (with today's
  probe-path handling), `Recover` + `sanitizePanicValue`, Secure headers, middleware timeout. It
  gets no rate limiter or IP pre-guard, tenant resolution, forwarded client certificate, CORS,
  gzip, body limit or OTel HTTP middleware. (On the application listener the probe skipper exempts
  probes from OTel, tenant resolution and forwarded client certificate only; gzip, body limit and
  the limiters run on them.)
- **No limiter, but coalesced.** No IP-keyed or application-shared request-rate limiter may be
  added to the probe listener: removing the shared budget is what fixes the `429` defect. Concurrent
  `/ready` requests that reach the judgment (not stopping, `ReadyCh` closed) are coalesced behind a
  singleflight, so a burst runs one judgment and one application-listener check, not one per
  request; this shares an in-flight judgment and never reuses a finished one.
- **Timeouts.** The probe listener reuses `server.timeout.read`, `.write`, `.idle` and
  `.middleware`; there are no probe-specific timeout keys.
- **No TLS.** `server.tls.*` governs the application listener only. The probe listener is plain
  HTTP: no credential rides a probe, and after Part 2 the body carries one status word. A TLS
  opt-in would need its own certificate material and rotation for no confidentiality gain. Until
  Part 2 ships, the probe listener serves today's detailed body in plaintext, so a deployment that
  sets `probes.port` restricts the listener to its probe sources, keeps it off every public network
  path (see Consequences), and keeps probe traffic on a trusted, isolated network: restricting
  sources does not stop a passive observer on a shared one. A deployment that cannot guarantee that
  isolation waits for Part 2 before setting `probes.port`; the probe listener offers no TLS.
- **Seam.** `ServerRunner` is unchanged. The probe listener is reached through an optional
  interface (`ProbeErrors() <-chan error`, `ProbeBoundAddr() net.Addr`), type-asserted on the
  injected runner the way `applyGlobalMiddleware` asserts its seam. With `probes.port > 0` and an
  injected `Options.Server` that does not implement it, startup fails naming the key; the key is
  never silently ignored.

**Lifecycle.** Before `Start`, nothing listens on either port, as today; `/health` on the probe
port has today's semantics, and `startupProbe` sizing in `wiki/startup_defaults.md` stands.

| Concern | Rule |
| --- | --- |
| Storage | `probeServer atomic.Pointer[http.Server]`, stored under `lifecycleMu` in the critical section that reads `stopping`. A set latch vetoes the store: the listener closes and `Start` returns `http.ErrServerClosed`. |
| Start once | The existing `started` CAS covers both listeners; a second `Start` binds neither. |
| Bind order | Inside `Server.Start`, after the TLS config is built: the probe listener binds and serves, then the application listener binds immediately after. |
| Probe bind fails | `Start` returns the error before the application listener binds; `ReadyCh` never closes. |
| Later failure in `Start` | Any failure after the probe listener bound (the application bind included) closes the probe listener before `Start` returns. |
| Error fan-in | `serve()` makes the error channel capacity 2, with one sender per listener: the `Start` goroutine, and a forwarder reading `ProbeErrors()`, which sends at most one value and closes on every path out of `Start`. A `sync.WaitGroup` over both senders gates `close`. `drainServerError` reads until the channel closes, bounded by the outer timeout, on the shutdown path and on the server-error path alike. Either listener's serve error ends `Run`. |
| Shutdown | `Server.Shutdown` sets `stopping`, drains the application listener within the caller's ctx, then stops the probe listener at the end of `Server.Shutdown` — before `App.Shutdown` reaches `stopSlots` — with a 1s ctx detached from the caller's (`context.WithoutCancel`) and `Close()` on expiry. A deadline error from the probe listener alone logs WARN and is not returned. |
| Shutdown before `Start` | The latch vetoes both listeners; `Start` returns `http.ErrServerClosed` and binds neither. |

The probe listener stops last so `/ready` answers `503`, not a refused connection, across the drain;
its detached budget keeps a probe in flight at the deadline from failing a clean exit.

**`dispatchReady`.** It is the probe listener's `/ready` handler, and it checks `stopping` first:
set, it answers `503` `{"status":"not ready"}`. It then answers the same `503` until `ReadyCh` has
closed (a no-op on the application listener, which only serves after `ReadyCh` closes). On the
probe listener it then runs the **application-listener check**: a `HEAD` of the reserved
`<base><ready path>` sent to the application listener's bound address (`BoundAddr()`, loopback when
bound to all interfaces) with a 500ms timeout, a fixed constant. It speaks HTTPS when
`server.tls.enabled` is set and HTTP otherwise; under TLS it skips certificate verification,
because it dials the process's own bound address, which no certificate SAN names, and it tests
liveness, not identity. `server.tls` verifies no client certificates, so the check needs none; a
future client-certificate mode must revisit it. That path has no route there, so a
live engine answers `404` through its whole middleware chain; any non-`5xx` answer passes, and a
timeout, connection error or `5xx` answers `503` and logs WARN `Application listener unresponsive`.
A TCP connect alone would not do: the kernel completes the handshake into the accept backlog even
when the process has stopped serving. The reserved paths are exempt from the rate limiters and the
IP pre-guard on the application listener, alongside the probe skipper's existing exemptions, so the
check never spends or is refused limiter budget; the exemption is safe because those paths only
ever answer `404` there. Only then does it call the registered handler. `RegisterReadyHandler` replaces that handler on whichever
listener serves `/ready`. The status-only rule of Part 2 binds the framework's handlers; a consumer
override's body is the consumer's own disclosure. The latch and the application listener's close
happen in one call, so the `503` is not a deregistration signal: a `preStop` sleep (or the load
balancer's deregistration delay) remains what takes an instance out of rotation before `SIGTERM`.

**Route table.** `/health` and `/ready` stay reserved in the application listener's conflict
tracker at `<base><path>` whatever `probes.port` says, so flipping the key never changes which
module routes are legal. With the port set the reservation has no engine route, and the application
listener answers `404` there. `RouteDescriptor` gains `Listener string`: empty for the application
listener, `"probes"` for the probe listener. With the port set, the four probe descriptors (`GET`
and `HEAD` for each probe) carry `Listener: "probes"`, `Path` the unprefixed probe path, and
`HandlerID` `formatHandlerID(method, path)` (`GET:/ready`). `PostRegisterRoutes` and
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

## Consequences

**Operators who set `server.probes.port`:**

- **Retarget port and path.** Kubernetes `livenessProbe`, `readinessProbe` and `startupProbe` get
  `port: <probes.port>` and lose the base prefix (`/api/v1/ready` becomes `/ready`). The ALB target
  group gets a health-check port override instead of `traffic-port`, and the unprefixed path. GKE
  Ingress needs a `BackendConfig` `healthCheck` with the port and path; the AWS Load Balancer
  Controller needs the `healthcheck-port` and `healthcheck-path` annotations. External monitors
  that probed the public URL lose `/ready`, by design.
- **TLS deployments** also switch the probe scheme (`scheme: HTTP`) or `HealthCheckProtocol` to
  HTTP; changing only the port fails every probe.
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
  traffic. Until Part 2 ships, the probe listener serves today's body in plaintext.

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
`wiki/startup_defaults.md` (`probes.port` as the `429` mitigation), `wiki/server_tls.md` and the
`wiki/cache.md` probe manifests. Tests assert the status-only body, that no kind name appears, and
that `/ready` answers `503` during the application drain.

## Delivery

Two PRs, each shippable alone, listener first:

1. `feat(server): serve probes on an internal listener` — Part 1. Opt-in and non-breaking, so it
   may ship under Proposed. Carries this ADR, its `architecture_decisions.md` index entry and the
   counter bump to ADR-001 through ADR-120, the ADR-002 amendment blockquote, and the Part 1 docs.
2. `fix(app)!: answer /ready with status only` — Part 2, with the gauges and the non-critical WARN
   in the same diff. Adds amendment blockquotes to ADR-048, ADR-066, ADR-094 and ADR-114, the
   `wiki/migrations.md` atoms (the body; `PublicErr`), the breaking-changes `SKILL.md` entry and
   the inventory sweep, and flips this ADR to Accepted.

The listener goes first because it closes the exposure independently of the body's shape, in a
release that breaks nothing. The trim is the only break; last, its replacement signal, amendments
and atoms land in one diff with the flip to Accepted.
