# Cache Architecture (Deep Dive)

This document covers the GoBricks Redis-based cache subsystem in depth: lifecycle, performance characteristics, configuration, and the production-safe defaults applied by the framework.

GoBricks provides Redis-based caching with type-safe serialization, multi-tenant isolation, and automatic lifecycle management.

**Requires Redis 7.0+** because `GetOrSet` uses `SET … NX GET`, which Redis rejected as a syntax error before 7.0.0. The client fails construction when the server advertises an older version; because `CacheManager` builds clients lazily per tenant, a too-old server surfaces on the first request that touches the cache rather than at startup. The check is best-effort — it fails open when `INFO` is unavailable (ACL-restricted or redacted by a managed provider).

**Core Components:**

- **Redis Client**: Atomic operations (Get/Set/GetOrSet/CompareAndSet/CompareAndDelete), connection pooling, health monitoring
- **CacheManager**: Per-tenant cache lifecycle with lazy initialization, LRU eviction, idle cleanup, singleflight
- **CBOR Serialization**: Type-safe encoding with security limits (max 10k array/map elements, max nesting depth 16)
- **Multi-tenant integration**: Automatic tenant resolution from context via `deps.Cache(ctx)`

**Lifecycle Management (CacheManager):**

- **Lazy Initialization**: Cache created on first access per tenant (no upfront connections)
- **LRU Eviction**: Oldest cache evicted when MaxSize exceeded (default: 100 tenants)
- **Idle Cleanup**: Unused caches closed after IdleTTL (default: 15m, checked every 5m)
- **Singleflight**: Prevents duplicate cache creation during concurrent access
- **Lock-Free Close**: Cache close operations don't block Get/Set/Delete operations

**Performance Characteristics:**

- **Latency**: <1ms for Get/Set (localhost), ~2ms for atomic operations (Lua scripts)
- **Throughput**: 100k reads/sec, 80k writes/sec (single Redis instance)
- **CBOR Serialization**: ~83ns/op marshal, ~167ns/op unmarshal (simple structs)
- **Connection Pool**: Default 10, configurable via `cache.redis.poolsize`
- **Network Impact**: +0.5-1ms (same datacenter), +50-200ms (cross-region, not recommended)

**Benchmark Results** (Apple M4 Pro, localhost Redis):

| Operation | Performance | Allocations | Notes |
| ----------- | ------------- | ------------- | ------- |
| CBOR Marshal (simple) | ~83 ns/op | 96 B/op, 2 allocs | 12M ops/sec |
| CBOR Unmarshal (simple) | ~167 ns/op | 88 B/op, 3 allocs | 6M ops/sec |
| CBOR Marshal (complex) | ~800 ns/op | 400 B/op, 8 allocs | Nested structs, maps, slices |
| CBOR Unmarshal (complex) | ~1200 ns/op | 600 B/op, 15 allocs | Full deserialization |

*Run benchmarks:* `go test -bench=BenchmarkCBOR -benchmem ./cache/`
*Redis benchmarks require:* `docker run -d -p 6379:6379 redis:7.4.9-alpine` then `go test -bench=BenchmarkRealRedis -benchmem -tags=integration ./cache/redis/`

**Configuration Example:**

```yaml
cache:
  enabled: true
  type: redis
  # critical: true        # opt-in; unset (the default) = the cache probe never changes
                          # the /ready status code (ADR-094)
  manager:
    maxsize: 100          # Max tenant cache instances
    idlettl: 15m          # Idle timeout per cache
    cleanupinterval: 5m   # Cleanup goroutine frequency
  redis:
    host: localhost
    port: 6379
    password: ${CACHE_REDIS_PASSWORD}  # From environment
    database: 0
    poolsize: 10
```

**Module Setup Pattern:**

```go
type Module struct {
    svc *Service
}

// The service holds the accessor function, never a resolved cache instance.
type Service struct {
    getCache func(context.Context) (cache.Cache, error)
    logger   logger.Logger
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    // Hand both dependencies over here; a Service built any other way has a nil
    // getCache and panics on first use.
    m.svc = &Service{getCache: deps.Cache, logger: deps.Logger}
    return nil
}

func (s *Service) GetUser(ctx context.Context, id int64) (*User, error) {
    c, err := s.getCache(ctx)  // Resolves tenant from context
    if err != nil {
        return nil, err
    }

    // Try cache first
    data, err := c.Get(ctx, fmt.Sprintf("user:%d", id))
    if err == nil {
        return cache.Unmarshal[*User](data)
    }

    // Cache miss - query database. Check this error: without it a DB failure
    // returns (nil, nil) and then caches a CBOR null, so every later Get is a
    // hit that decodes to nil for the whole TTL.
    user, err := s.queryDatabase(ctx, id)
    if err != nil {
        return nil, err
    }

    // Store in cache with TTL. Write-back failures are logged, not returned —
    // the caller already has the value.
    serialized, err := cache.Marshal(user)
    if err != nil {
        s.logger.Warn().Err(err).Int64("id", id).Msg("Cache marshal failed, value not cached")
        return user, nil
    }
    if err := c.Set(ctx, fmt.Sprintf("user:%d", id), serialized, 5*time.Minute); err != nil {
        s.logger.Warn().Err(err).Int64("id", id).Msg("Cache write-back failed")
    }

    return user, nil
}
```

**Is a cache configured?** `deps.Cache` is never nil — with no cache configured it is a
function whose every call returns a `*config.ConfigError` satisfying
`config.IsNotConfigured`, so `if deps.Cache == nil` is dead code. Test the error when you
have already resolved, or read `deps.CacheConfigured` when you only need the answer (the
`DB` and `Messaging` accessors share the contract and the flag shape: `DBConfigured`,
`MessagingConfigured`). The flags speak for the ROOT config, which is the only thing knowable
before a request carries a tenant. False is definitive: the root resolver would fail every call.
True means the root cache is wired, or that the answer is per key at runtime — multi-tenant, a
dynamic config source, a caller-supplied `ResourceSource`, a custom `CacheConnector`. In every
per-key mode a resolve can still return `IsNotConfigured` for the tenant in hand, so the flag
never replaces the accessor's error path — it only spares you a throwaway resolve when the
answer is already no.

## Key Operations

| Operation | Method | Use Case | Atomicity |
| ----------- | -------- | ---------- | ----------- |
| Basic read | `Get(ctx, key)` | Query result caching | Single-key |
| Basic write | `Set(ctx, key, value, ttl)` | Store computed result | Single-key |
| Deduplication | `GetOrSet(ctx, key, value, ttl)` | Idempotency keys | Atomic SET NX |
| Distributed lock | `CompareAndSet(ctx, key, expectedValue, newValue, ttl)` | Job coordination | Lua script CAS |
| Lock release | `CompareAndDelete(ctx, key, expectedValue)` | Token-verified release, conditional eviction | Lua script CAD |
| Type-safe store | `Marshal(v)` + `Set()` | Struct serialization | CBOR encoding |
| Load-through read | `LoadThrough[T](ctx, c, key, ttl, loader)` | Read-through with origin fallback | `cache.loadtimeout`-bounded cache leg + per-instance single-flight |

**Releasing a distributed lock:** acquire with `CompareAndSet` and a **positive** TTL, then
release with `CompareAndDelete` carrying the same token — never a bare `Delete`, which
removes whoever holds the key at that moment rather than verifying it is still you:

```go
// A fresh token per acquisition: a worker identity would let a stale release
// delete a later acquisition's lock.
token := []byte(uuid.New().String())
acquired, err := c.CompareAndSet(ctx, lockKey, nil, token, 30*time.Second)
if err != nil {
    return err
}
if !acquired {
    return ErrLockHeld
}
defer func() {
    // A canceled ctx cannot reach Redis, so release on a detached, bounded context.
    releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
    defer cancel()
    _, _ = c.CompareAndDelete(releaseCtx, lockKey, token)
}()
```

The token must be unique to a single **acquisition** — mint a fresh one where the lock is
taken, never a reusable worker identity. A stable identity makes a release that lands after
the TTL lapsed match whatever the next acquisition stored under that same identity, so it
deletes the next holder's lock: the unconditional-`Delete` hazard this pair exists to remove.

Two hazards this pair introduces, both of which turn the safe release back into an unsafe
one if ignored:

- **A positive TTL is mandatory.** `CompareAndSet` accepts `ttl == 0` (stored without
  expiration). A token-verified release that declines to remove the key then leaves it
  held forever, with no expiry to recover it — `Delete` used to paper over that.
- **`false` and any error are both terminal.** Never fall back to `Delete`, and never
  retry the release with it; that reinstates the unconditional-release hazard behind an
  API that reads as safe. A `false` result authorizes stopping, not compensating: a
  client-side timeout on a delete that actually landed is indistinguishable from another
  holder owning the key. `false` also does not prove another holder's value is present —
  it covers "already gone" too.

See [ADR-060](adr_060_cache_compare_and_delete.md).

### Load-through reads

`cache.LoadThrough[T]` is the recommended read path when the cache fronts an origin (a
database, an upstream API). It owns the four steps a hand-rolled read-through gets wrong
somewhere: a **bounded** cache lookup, an origin call on the caller's **untouched** context,
a **detached** write-back, and **single-flight** collapsing of concurrent misses.

```go
func (s *UserService) User(ctx context.Context, id int64) (User, error) {
    c, err := s.getCache(ctx) // stored accessor, resolved per call
    if err != nil {
        return s.repo.User(ctx, id) // no cache at all: straight to the origin
    }
    return cache.LoadThrough(ctx, c, fmt.Sprintf("user:%d", id), 5*time.Minute,
        func(ctx context.Context) (User, error) { return s.repo.User(ctx, id) })
}
```

What it guarantees:

- **The cache leg is bounded; the origin is not.** Each cache step runs under a timeout
  derived from `ctx` — the deployment's `cache.loadtimeout` (500 ms by default) or a
  per-call `cache.WithCacheTimeout(d)` — so a slow-but-reachable Redis costs at most that slice of
  the request budget before the loader runs. The loader receives `ctx` itself, deadline
  and values intact, and keeps whatever budget remains after the cache leg: under the
  default `server.timeout.middleware` of 5 s that is the request's remaining time less
  at most 500 ms, never a fixed figure, because the middleware deadline is already
  running when the handler starts.
- **Every cache-side failure degrades to the origin, never to the caller.** A timeout, a
  connection error, `ErrNotFound`, or an entry that no longer decodes as `T` (a schema
  change) all fall through to the loader, and a successful fill overwrites the bad entry.
- **The write-back is detached.** The value is CBOR-encoded before `LoadThrough` returns
  (the caller owns it from then on), and the `Set` runs on its own goroutine under
  `context.WithoutCancel(ctx)`, bounded by the same timeout, so a caller that gives up right
  after the origin answered still fills the cache. A value that cannot be encoded fails the
  call with the `Marshal` error rather than silently never caching; a failed `Set` is
  dropped — the cache client's `db.client.operation.duration{error.type}` carries it. A
  `nil` pointer, map, slice, interface, channel or func result is returned but never
  stored, so a CBOR null cannot pin the key for the whole TTL.
- **Concurrent misses collapse.** Callers missing on the same cache instance, key and `T`
  share one loader call: one leader loads, the rest wait on their own contexts. The scope
  is the cache instance, so the same key on two tenants' caches never shares a load. A
  follower whose own context is still live when the leader's cancellation fails the shared
  load starts a fresh fill instead of inheriting that error — collapsing never becomes an
  availability loss. A loader panic becomes an error naming only the panic value's type
  (ADR-081), delivered to every waiter.
- **Bad arguments fail before any I/O.** A nil `Cache` (including a typed nil) returns
  `ErrNilCache`; `ttl < 0` returns `ErrInvalidTTL`; a non-positive `WithCacheTimeout`
  returns `ErrInvalidCacheTimeout`.

**Configuring the bound.** `cache.loadtimeout` sets it per deployment:

```yaml
cache:
  loadtimeout: 250ms   # default 500ms; absent or 0 takes the default
```

Absent or `0` takes the 500 ms default and a negative value fails startup naming
`cache.loadtimeout`, so a deployment cannot express an unbounded leg. The value travels on
the **resolved cache instance**, so a per-tenant `cache.loadtimeout` is honoured without any
process-wide state, and `cache.WithCacheTimeout(d)` overrides it for one call. Keep it well
under `server.timeout.middleware` (5 s): it is the slice of the request budget a slow cache
may spend before the origin is consulted.

One trap if you wrap a `Cache` (metrics, tracing, a test spy): the bound reaches
`LoadThrough` through an optional `cache.LoadTimeoutProvider` interface, and embedding
`cache.Cache` in your wrapper promotes only that interface's methods. A wrapper that does not
forward `LoadTimeout()` silently falls back to 500 ms rather than the configured value:

```go
func (w *myCache) LoadTimeout() time.Duration {
    if p, ok := w.Cache.(cache.LoadTimeoutProvider); ok {
        return p.LoadTimeout()
    }
    return 0
}
```

Each fill records `cache.fill.duration` (seconds) with `cache.fill.role=leader|follower`,
`db.namespace` when a tenant is on the context, and `error.type` on failure; hits show up
as `cache.hit` on the underlying `Get`. Not covered: negative caching (a loader error is
never stored) and cross-process fill locking — two processes missing at once both load,
and the later write-back wins.

**Multi-Tenant Isolation:**

- Each tenant gets separate Redis database (configurable per-tenant)
- Cache instances managed by CacheManager with automatic lifecycle
- Context propagation ensures tenant resolution via `deps.Cache(ctx)`
- No key collision between tenants (different Redis databases)

**Observability Integration:**
When `observability.enabled: true`, cache operations automatically emit:

- **Metrics**: `db.client.operation.duration` (histogram, tagged with `error.type` on failure), `cache.hit`/`cache.miss` (counters), `cache.fill.duration` (histogram per `LoadThrough` fill, `cache.fill.role=leader|follower`), `cache.manager.active_caches`, `cache.manager.evictions`, `cache.manager.idle_cleanups`, `cache.manager.total_created`, `cache.manager.errors` — no distributed-tracing spans are emitted today
- **Health**: A probe registered in the `/ready` probe set whenever the cache manager exists. It leases an instance from the manager (`cacheManager.Get(ctx, "")`) and then calls `Cache.Health(ctx)` on it — under the default connector that is one Redis `PING` on a warm poll and three round trips on a cold one (the construction-time `PING`, the `INFO` version check, then the probe's own `PING`); `Health` is connector-defined, so a custom `Options.CacheConnector` costs whatever its own implementation does, which need not touch the network. Its status is surfaced as the top-level `cache` and `cache_stats` keys in the `/ready` **200** body (a `503` carries only `status`, `cache` and `error`), and it fails `/ready` with `503` only under `cache.critical: true` — the default is informational (ADR-094). See [Readiness](#readiness) below

## Readiness

`GET /ready` reports the cache in its 200 body whatever `cache.critical` is set to — and in a
503 body only when the cache probe is the one that failed, because a critical database failure
short-circuits before the cache probe's result is rendered. The 200 body
carries `cache` (a status string) alongside `cache_stats` (the manager counters), mirroring
`database`/`database_stats` and `messaging`/`messaging_stats` (abridged below — the `database`,
`messaging`, `time`, and `app` entries are omitted):

```json
{
  "status": "ready",
  "cache": "healthy",
  "cache_stats": {
    "active_caches": 3,
    "total_created": 5,
    "evictions": 1,
    "idle_cleanups": 1,
    "errors": 0,
    "max_size": 100,
    "idle_ttl": 900,
    "status": "healthy"
  }
}
```

| `cache` value | When | Probe error | 503? |
| --------------- | ------ | ------------- | ------ |
| `healthy` | An instance was leased and its `Health(ctx)` `PING` succeeded; `cache_stats.status` is `healthy` | none | no |
| `not_configured` | With the default connector and `cache.enabled: false`, nothing can resolve under the probe's fixed `""` key, so the probe reports `not_configured` without attempting a lease; `cache_stats` carries the manager counters with `status` `not_configured`. A custom `Options.CacheConnector` never reads `cache.enabled` and is probed regardless | none | no |
| `unhealthy` | The lease failed — the manager is closed, or a cold pool tried to build the instance and the construction-time `PING` failed; `cache_stats.status` is `unhealthy` | yes | only under `critical: true` |
| `unhealthy` | The lease succeeded but the per-probe `Health(ctx)` `PING` failed or timed out — a live Redis outage against a warm pool; `cache_stats.status` is `unhealthy` | yes | only under `critical: true` |
| `disabled` | **The manager is nil**, so readiness registers a `disabled` description for the kind (nothing is leased). Since [`[C58.3]`](migrations.md) the framework can no longer reach that state on its own: a cache manager that fails to construct aborts startup instead of leaving a nil behind, so this row now describes only an `App` value assembled directly, without a manager. `cache.enabled: false` does **not** land here — that is `not_configured` above; `cache_stats` is `{"status":"disabled"}` | n/a | no — there is no probe to error, so not even under `critical: true` |

**`cache.critical` (non-critical by default)**

- **absent (the default)** — a failing cache probe is reported in the body but never changes
  the status code: `/ready` stays `200` while the cache is dead, with `cache: "unhealthy"`
  and a climbing `cache_stats.errors` as the signal (ADR-094). The key is a pointer tri-state
  and is deliberately **not** registered as a koanf default, so "unset" is a state the
  framework can tell apart from an explicit value.
- `true` — a failing cache probe short-circuits `/ready` with
  `503 {"status": "not ready", "cache": "unhealthy", "error": "cache unavailable"}` — no
  `cache_stats`, and no other component's status. This is the only way into readiness
  gating; nothing is derived from the rest of the config.
- `false` — the same as leaving it unset. Set it explicitly only to state the intent in config
  review; it emits no WARN.

**What the `503` discloses.** The `error` is the fixed string `cache unavailable`, **not** the
probe error: the connector error names the Redis host, port and resolved dial IP, and `/ready`
carries no IP allowlist and no authentication. (No tenant identity is exposed: the probe leases
the empty top-level key, so `CacheManager.Get`'s `failed to create cache for key %q` wrap on a
cold-pool poll renders `key ""`.) The full error still reaches
the application log (`readyCheck` logs it at ERROR with a `component` field on every `503`) and
the IP-allowlisted debug health endpoint at `<debug.pathprefix>/health-debug` (default
`/_sys/health-debug`, gated on `debug.enabled` and `debug.endpoints.health`), where it renders
verbatim in `data.components.cache.error`. The sanitization is not specific to this probe: it is
the shared default for every critical probe (ADR-048) — an empty `HealthStatus.PublicErr`
renders `<component> unavailable`, so the `database` `503` reads `database unavailable`, and
messaging is never critical, so it renders no `503` body at all. A custom
`Options.CacheConnector`'s `Health` error is sanitized on `/ready` too, and reaches the same
two channels.

A hung Redis (packets dropped rather than refused) is reported the same way —
`redis.Client.Health` wraps every ping failure, `context deadline exceeded` included, in a
`cache.ConnectionError`.
The per-probe `PING` is capped at **500ms** independent of both `server.timeout.middleware`
and `cache.redis.readtimeout` (a shorter caller deadline still wins; neither budget can
extend it). That cap covers the warm poll only: a cold poll first builds the instance, whose
own construction-time `PING` carries a 5s budget racing the 5s default
`server.timeout.middleware`, and only then spends the 500ms — so a cold probe can run well
past 500ms and issue two `PING`s plus the `INFO` version-floor check, not one round trip.

**Choosing a value.** The default is non-critical, so the decision to make is whether to *opt
in*. Leave it alone when the cache is an optimisation in front of a database that can absorb
the miss — the common shape — because a Redis blip under `true` drains every replica from
rotation at the same moment, converting a latency regression into an outage, and a local or
CI boot without a Redis would never report Ready. Set `true` for a service that cannot serve
correct results without the cache — a rate limiter that must fail closed, a session store, an
idempotency ledger — because pulling the pod from the load-balancer rotation is then
preferable to serving wrong answers. The correlated-eviction risk that comes with `true` is
real and accepted rather than engineered around — the mitigation is
`readinessProbe.failureThreshold` (see [Wiring Kubernetes
probes](#wiring-kubernetes-probes)), which makes a transient blip cost three consecutive
failed polls instead of one; GoBricks does not add its own consecutive-failure counter on top
of the orchestrator's. Neither value is derived from anything else in the config — a declared
fallback does not make the cache non-critical, and a missing one does not make it critical
(ADR-094).

**Probe cost is conditional, not flat.** A `disabled` or `not_configured` deployment issues no
Redis traffic at all — the first registers no probe, and the second (under the default connector
with `cache.enabled: false`) makes no lease attempt at all, so the pool's `errors` counter stays
flat. Under the **default Redis connector**, a warm poll costs one `PING` — `Cache.Health` is
contracted fast (<100ms) and safe to call frequently — and emits one
`db.client.operation.duration` sample from inside the Redis client,
tagged `error.type` during a live outage, so a warm-pool outage does reach cache dashboards; the
HTTP-layer probe skipper that keeps `/ready` out of traces and HTTP metrics does not reach one
layer down. A cold poll costs three round trips instead: the construction `PING` and the `INFO`
Redis-7.0 version-floor check inside `redis.NewClient`, then the probe's own `PING` — see the
cold-poll caveat on the 500ms cap above. A custom `Options.CacheConnector` runs its own `Health`
implementation, so it need not issue a Redis `PING` at all and need not emit that sample —
budget its cost from that implementation, not from this paragraph.

When the lease itself fails, none of that happens: on boot with Redis unreachable — or on any
poll after a failed create, since failed builds are not pooled — `Cache.Health` is never
reached and **no `db.client.operation.duration` sample is recorded at all**, because the
construction-time `PING` in `redis.NewClient` is untracked. Do not build the boot-time alert on
a cache metric; on that path the signal is the probe result itself. Under `cache.critical: true`
the `503` body is trimmed to `status`/`cache`/`error` with no `cache_stats`, so the in-body
signal is gone and what remains is the external prober, the `Readiness check failed` ERROR line
`readyCheck` logs with the full error, and the cache pre-init WARN (`Builder.performPreInitialization`, over the cache slot) — see [Wiring
Kubernetes probes](#wiring-kubernetes-probes). Under the non-critical default the `200` body
carries `cache: "unhealthy"` and a climbing `cache_stats.errors` instead, and no readiness
ERROR is logged — that body is the only in-process signal a warm-pool outage produces, which
is the cost of staying non-critical.

The probe's lease also resets `manager.idlettl` and LRU position for the default (`""`) entry,
so a continuously polled pod stays on the warm-pool path.

`cache.critical` is process-global, and so is what the probe observes: it leases key `""`,
which resolves to the top-level `cache.*` connection. A deployment whose caches live only
under `multitenant.tenants.<id>.cache` gets nothing from the flag — with top-level
`cache.enabled: false` the probe reports `not_configured` and no error, so `/ready` stays
`200` however many tenant Redis instances are down. A value under
`multitenant.tenants.<id>.cache.critical` parses (the per-tenant schema reuses `CacheConfig`)
but is ignored for the same reason. Neither setting replaces a Redis-side alert.

### Wiring Kubernetes probes

Point the **readiness** probe at `/ready` and the **liveness** probe at `/health`. `/health`
checks no dependency at all — it returns `200 {"status":"ok"}` for as long as the process is
serving HTTP (`server.healthCheck`), and unlike `/ready` it has no override seam. Both paths
are configurable (`server.path.health`, `server.path.ready`) and are prefixed by
`server.path.base`, so a service on `base: /api/v1` must be probed at `/api/v1/ready`.

Under `cache.critical: true` a pod that boots while Redis is unreachable never becomes Ready: it
is kept out of the Service endpoints, and a Deployment rollout stalls after taking down at most
`maxUnavailable` old replicas (25% under the default RollingUpdate strategy). Set
`maxUnavailable: 0` if you want a rollout during a Redis outage to cost no serving capacity at
all. That holds from the very first poll — `cache.NewCacheManager` validates options without
dialing, so the first lease is what connects, and it fails. An already-Ready pod whose Redis
dies leaves the endpoints only after `failureThreshold` consecutive failed polls (three at the
setting below), which is the correlated-eviction mitigation described under *Choosing a value*.

What it does **not** do: a failing readiness probe never restarts or kills a container — only a
liveness probe does. Do not point liveness at `/ready` to get that. It would turn one shared
Redis blip into a simultaneous restart of every replica, which is strictly worse than the
rotation drain described under *Choosing a value* above: restarts also drop in-flight requests
and can settle into `CrashLoopBackOff`. Nor is this a "refuse to start" switch — the process
boots either way and simply never reports Ready; the cache pre-init (`Builder.performPreInitialization`, over the cache slot) logs a WARN and
continues. That covers *reaching* the cache: an unreachable Redis at boot is a runtime
condition and stays non-fatal. A cache the framework cannot **construct** — a negative
`cache.manager.maxsize` or `idlettl` — is the other case, and it does abort startup
([ADR-054](adr_054_cache_construction_fails_startup.md), [`[C58.3]`](migrations.md)).

```yaml
readinessProbe:
  httpGet:
    # Default path. Both probe paths come from server.path.ready / server.path.health
    # prefixed by server.path.base — a service on base: /api/v1 is probed at
    # /api/v1/ready, and this manifest has to match whatever those keys resolve to.
    path: /ready
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 10
  # Kubernetes defaults this to 1s, which a cold poll loses: building the instance
  # costs a 5s-budget construction PING plus the INFO version check before the
  # probe spends its own 500ms cap. 6s covers that and still fits inside the period.
  timeoutSeconds: 6
  # 3 consecutive failures (~30s here) rides out a Redis blip, so a transient
  # outage does not evict every replica from rotation at the same moment.
  failureThreshold: 3
livenessProbe:
  httpGet:
    path: /health  # Default path; server.path.health + server.path.base, as above.
    port: 8080
  initialDelaySeconds: 15
  periodSeconds: 20
  # /health touches no dependency, so it needs no cold-poll headroom — this is
  # slack for a busy event loop, not for Redis.
  timeoutSeconds: 2
  failureThreshold: 3
```

Probe traffic is excluded from request logging and from HTTP spans and metrics, so the `503`
gets no access-log line and no HTTP telemetry. It is not silent, though: whenever a critical
probe fails, `readyCheck` logs its own `Readiness check failed` line at ERROR carrying the
**full** probe error and a `component` field — that is where the Redis host, port and dial
error go now that the response body is sanitized. (A failure that is only the caller
abandoning its own request is logged at WARN instead, so an aborted probe request cannot mint
ERROR lines on an unauthenticated endpoint. A custom `Options.CacheConnector`'s error text is
written to this log verbatim on every failing poll — do not embed a DSN or a password in it.)
Under `cache.critical: true` a cache outage therefore produces one ERROR line per poll per
replica, which is a real volume at `periodSeconds: 10` — alert on it, don't tail it. The
non-critical default has neither that line nor the `503`. Beyond it, which in-process
signal you get depends on where the probe fails:

- **No pooled instance yet** (boot, or after the instance was evicted or idle-cleaned): the
  probe's lease triggers a fresh connect, and the default connector logs `Creating Redis cache
  instance` (INFO) then `Failed to create Redis cache client` (ERROR). Failed builds are not
  cached, so this repeats on every poll, alongside go-redis's own dial-failure lines.
  `Failed to create Redis cache client` is the most reliable boot-time alert signal, at the
  same per-poll volume as the readiness line above. (A custom `Options.CacheConnector`
  replaces this logging.)
- **Instance already pooled and its `PING` fails** (Redis died after a healthy start): the
  connector logs nothing, so under the non-critical default the only in-process signals are the
  `db.client.operation.duration` sample described above and the `200` body itself. Under
  `cache.critical: true` the `Readiness check failed` ERROR line covers it.

Readiness itself stays observable through the external prober either way — the Pod's `READY`
column, the `Unhealthy` kubelet event, `kube_pod_status_ready`. Size
`initialDelaySeconds`/`failureThreshold` against the pre-listen boot window too — see
[startup_defaults.md](startup_defaults.md#messaging-pre-warm-readiness-wait).

## Cache Manager Defaults

GoBricks applies production-safe cache manager defaults when cache is configured:

| Setting | Default (single-tenant) | Default (multi-tenant) | Purpose |
| --------- | ------------------------- | ------------------------ | --------- |
| `manager.maxsize` | 100 | `multitenant.limits.tenants` | Maximum tenant cache instances (LRU cap) |
| `manager.idlettl` | 15m | 15m | Close idle cache connections |
| `manager.cleanupinterval` | 5m | 5m | Frequency of idle cache cleanup |

**A negative `maxsize` or `idlettl` is fatal even when the cache is disabled.** `config.Validate`
fills and checks `cache.manager.*` only under `cache.enabled: true`, but the manager is still
constructed for a disabled cache (that is how `/ready` reports `not_configured`), and
`cache.NewCacheManager` rejects a negative `maxsize` or `idlettl` — so a disabled cache carrying
one aborts startup ([ADR-054](adr_054_cache_construction_fails_startup.md),
[`[C58.3]`](migrations.md)). Absent and `0` are safe in both states. `cleanupinterval` is the
exception: an enabled cache rejects a negative one at validation, while a disabled cache has the
manager fall back to its 5m default instead of failing. After that fallback the manager WARNs
(`cache.manager.cleanupinterval is >= cache.manager.idlettl`) when the effective cleanup interval
is not strictly below `idlettl`, since idle eviction then lags by up to one extra sweep — the same
advisory the database and messaging managers emit.

**Override defaults** in `config.yaml`:

```yaml
cache:
  manager:
    maxsize: 200         # Support more tenants
    idlettl: 30m         # Keep caches longer
    cleanupinterval: 10m # Less frequent cleanup
```

### Sizing `maxsize` for multi-tenant deployments

`maxsize` is an LRU cap, not a per-tenant guarantee. When more tenants are active than `maxsize`, every request that targets a not-currently-cached tenant evicts the least-recently-used instance and recreates a fresh one — **eviction thrash** that silently degrades latency (each miss pays the full connect cost) without any error.

Size the pool to hold every concurrently-active tenant: set `cache.manager.maxsize` (and `multitenant.limits.tenants`) to at least the number of tenants you expect to serve simultaneously. An **unset** `cache.manager.maxsize` in multi-tenant mode auto-scales the pool to `multitenant.limits.tenants` — which itself defaults to 100, so fleets above that must raise `limits.tenants` too; an explicit value pins a fixed size in both modes. For **statically-configured** tenants (`multitenant.tenants`), the framework counts them at startup and emits a **WARN** when the pool's `maxsize` is below the configured tenant count, so under-provisioning is visible in logs. For **dynamic** tenant sources the count is unknown at startup, so no warning can be emitted — size `maxsize` against your expected fleet manually.

> Eviction closes the evicted instance **outside** the manager lock, so a slow `Close()` on an evicted tenant never blocks concurrent `Get()` calls for other tenants. It does, however, still incur a recreate on the next request for the evicted tenant.
>
> An instance that is **still in use** when evicted (held by an in-flight request, message, or job) is detached immediately but its `Close()` is **deferred until the last borrower releases its lease** — so an in-use cache is never closed under an active caller ([ADR-032](adr_032_lease_refcount_tenant_handles.md), the M3 fix). The lease is reference-counted by `CacheManager` and released by the framework at each request/message/job boundary; **application code is unchanged** (`deps.Cache(ctx)` keeps its `(Cache, error)` signature). Direct callers of `CacheManager.Get` see a new `ReleaseFunc` third return — see [migrations.md](migrations.md).
>
> A per-tenant cache that fails to resolve reports which tenant failed: `ConfigError.Field` reads `multitenant.tenants.<id>.cache.<leaf>` at the runtime door as it already did at startup — or the bare `multitenant.tenants.<id>.cache` where the failure names no leaf, as a nil config and a disabled cache do, and the remediation hint names that tenant's own key rather than the root `CACHE_*` variable ([C61.24](migrations.md), ADR-076). Match the family, not `cache.redis.host` by equality.

---

For comprehensive code-snippet examples (cache operations, multi-tenant patterns, testing), see [llms.txt](../llms.txt).
