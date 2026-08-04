# Cache Architecture (Deep Dive)

This document covers the GoBricks Redis-based cache subsystem in depth: lifecycle, performance characteristics, configuration, and the production-safe defaults applied by the framework.

GoBricks provides Redis-based caching with type-safe serialization, multi-tenant isolation, and automatic lifecycle management.

**Requires Redis 7.0+** because `GetOrSet` uses `SET … NX GET`, which Redis rejected as a syntax error before 7.0.0. The client fails construction when the server advertises an older version; because `CacheManager` builds clients lazily per tenant, a too-old server surfaces on the first request that touches the cache rather than at startup. The check is best-effort — it fails open when `INFO` is unavailable (ACL-restricted or redacted by a managed provider).

**Core Components:**
- **Redis Client**: Atomic operations (Get/Set/GetOrSet/CompareAndSet), connection pooling, health monitoring
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
|-----------|-------------|-------------|-------|
| CBOR Marshal (simple) | ~83 ns/op | 96 B/op, 2 allocs | 12M ops/sec |
| CBOR Unmarshal (simple) | ~167 ns/op | 88 B/op, 3 allocs | 6M ops/sec |
| CBOR Marshal (complex) | ~800 ns/op | 400 B/op, 8 allocs | Nested structs, maps, slices |
| CBOR Unmarshal (complex) | ~1200 ns/op | 600 B/op, 15 allocs | Full deserialization |

*Run benchmarks:* `go test -bench=BenchmarkCBOR -benchmem ./cache/`
*Redis benchmarks require:* `docker run -d -p 6379:6379 redis:7-alpine` then `go test -bench=BenchmarkRealRedis -benchmem -tags=integration ./cache/redis/`

**Configuration Example:**
```yaml
cache:
  enabled: true
  type: redis
  # critical: false       # opt-out only; unset = /ready returns 503 when the cache
                          # probe errors. Setting false WARNs on every boot.
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

**Key Operations:**
| Operation | Method | Use Case | Atomicity |
|-----------|--------|----------|-----------|
| Basic read | `Get(ctx, key)` | Query result caching | Single-key |
| Basic write | `Set(ctx, key, value, ttl)` | Store computed result | Single-key |
| Deduplication | `GetOrSet(ctx, key, value, ttl)` | Idempotency keys | Atomic SET NX |
| Distributed lock | `CompareAndSet(ctx, key, expectedValue, newValue, ttl)` | Job coordination | Lua script CAS |
| Type-safe store | `Marshal(v)` + `Set()` | Struct serialization | CBOR encoding |

**Multi-Tenant Isolation:**
- Each tenant gets separate Redis database (configurable per-tenant)
- Cache instances managed by CacheManager with automatic lifecycle
- Context propagation ensures tenant resolution via `deps.Cache(ctx)`
- No key collision between tenants (different Redis databases)

**Observability Integration:**
When `observability.enabled: true`, cache operations automatically emit:
- **Metrics**: `db.client.operation.duration` (histogram, tagged with `error.type` on failure), `cache.hit`/`cache.miss` (counters), `cache.manager.active_caches`, `cache.manager.evictions`, `cache.manager.idle_cleanups`, `cache.manager.total_created`, `cache.manager.errors` — no distributed-tracing spans are emitted today
- **Health**: A probe registered in the `/ready` probe set whenever the cache manager exists. It leases an instance from the manager (`cacheManager.Get(ctx, "")`) and then calls `Cache.Health(ctx)` on it — one round trip on a warm poll, which is a Redis `PING` under the default connector and whatever a custom `Options.CacheConnector`'s implementation does otherwise, since `Health` is connector-defined. A cold poll costs three: the construction-time `PING`, the `INFO` version check, then the probe's own `PING`. Its status is surfaced as the top-level `cache` and `cache_stats` keys in the `/ready` body, and it fails `/ready` with `503` by default — `cache.critical: false` opts out and emits a startup WARN. See [Readiness](#readiness) below

## Readiness

`GET /ready` reports the cache in its 200 body whatever `cache.critical` is set to — and in a
503 body only when the cache probe is the one that failed, because a critical database failure
short-circuits before the cache probe's result is rendered. The 200 body
carries `cache` (a status string) alongside `cache_stats` (the manager counters), mirroring
`database`/`db_stats` and `messaging`/`messaging_stats` (abridged below — the `database`,
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
|---------------|------|-------------|------|
| `healthy` | An instance was leased and its `Health(ctx)` `PING` succeeded; `cache_stats.status` is `healthy` | none | no |
| `not_configured` | `cache.enabled: false` — the *default Redis* connector declines by design; `cache_stats.status` is `not_configured`. A custom `Options.CacheConnector` never reads `cache.enabled` and is probed regardless | none | no |
| `unhealthy` | The lease failed — the manager is closed, or a cold pool tried to build the instance and the construction-time `PING` failed; `cache_stats.status` is `connection_failed` | yes | **yes**, unless `critical: false` |
| `unhealthy` | The lease succeeded but the per-probe `Health(ctx)` `PING` failed or timed out — a live Redis outage against a warm pool; `cache_stats.status` is `unhealthy` | yes | **yes**, unless `critical: false` |
| `disabled` | No probe ran — the manager failed to construct at startup; `cache_stats` is `{}` | n/a | no — under the strict default either |

**`cache.critical` (strict by default)**

- **absent (the default)** — a failing cache probe short-circuits `/ready` with
  `503 {"status": "not ready", "cache": "unhealthy", "error": "cache unavailable"}` — no
  `cache_stats`, and no other component's status. The key is a pointer tri-state and is
  deliberately **not** registered as a koanf default, so "unset" is a state the framework can
  tell apart from an explicit value: unset means critical (ADR-045).
- `false` — a failing cache probe is reported in the body but never changes the status code.
  Readiness stays green while the cache is dead. Any deployment whose cache probe can
  actually fail — an enabled cache, or any custom `Options.CacheConnector` — emits a startup
  WARN on every boot naming the key, the consequence, and the remedy; that
  WARN is the visible marker of a deliberately weakened readiness posture and is not
  suppressible.
- `true` — the same as leaving it unset. Set it explicitly only to state the intent in config
  review.

**What the `503` discloses.** The `error` is the fixed string `cache unavailable`, **not** the
probe error: the connector error names the Redis host, port and resolved dial IP, and `/ready`
carries no IP allowlist and no authentication. (No tenant identity is exposed: the probe leases
the empty top-level key, so `CacheManager.Get`'s `failed to create cache for key %q` wrap on a
cold-pool poll renders `key ""`.) The full error still reaches
the application log (`readyCheck` logs it at ERROR with a `component` field on every `503`) and
the IP-allowlisted debug health endpoint at `<debug.pathprefix>/health-debug` (default
`/_sys/health-debug`, gated on `debug.enabled` and `debug.endpoints.health`), where it renders
verbatim in `data.components.cache.error`. The sanitization is per-probe: the `database` and `messaging` `503`
bodies still carry their raw error. A custom `Options.CacheConnector`'s `Health` error is
sanitized on `/ready` too, and reaches the same two channels.

A hung Redis (packets dropped rather than refused) is reported the same way —
`redis.Client.Health` wraps every ping failure, `context deadline exceeded` included, in a
`cache.ConnectionError`.
The per-probe `PING` is capped at **500ms** independent of both `server.timeout.middleware`
and `cache.redis.readtimeout` (a shorter caller deadline still wins; neither budget can
extend it). That cap covers the warm poll only: a cold poll first builds the instance, whose
own construction-time `PING` carries a 5s budget racing the 5s default
`server.timeout.middleware`, and only then spends the 500ms — so a cold probe can run well
past 500ms and issue two `PING`s plus the `INFO` version-floor check, not one round trip.

**Choosing a value.** The default is strict, so the decision to make is whether to *opt out*.
Leave it alone for a service that cannot serve correct results without the cache — a rate
limiter, a session store, an idempotency ledger — because pulling the pod from the
load-balancer rotation is preferable to serving wrong answers, and a service that configured
a cache most likely configured it because it needs one. Set `false` when the cache is an
optimisation in front of a database that can absorb the miss: a Redis blip would otherwise
drain every replica from rotation at the same moment, converting a latency regression into an
outage. That correlated-eviction risk is real and accepted rather than engineered around —
the mitigation is `readinessProbe.failureThreshold` (see [Wiring Kubernetes
probes](#wiring-kubernetes-probes)), which makes a transient blip cost three consecutive
failed polls instead of one; GoBricks does not add its own consecutive-failure counter on top
of the orchestrator's. The opt-out is loud by design: every boot logs the WARN, so a lenient
readiness posture stays visible in the same place an operator looks for everything else, and
it is deliberately kept rather than banned (ADR-045).

A warm poll costs one `PING` — `Cache.Health` is contracted fast (<100ms) and safe to call
frequently — and emits one `db.client.operation.duration` sample from inside the Redis client,
tagged `error.type` during a live outage, so a warm-pool outage does reach cache dashboards; the
HTTP-layer probe skipper that keeps `/ready` out of traces and HTTP metrics does not reach one
layer down. A cold poll costs three round trips instead: the construction `PING` and the `INFO`
Redis-7.0 version-floor check inside `redis.NewClient`, then the probe's own `PING` — see the
cold-poll caveat on the 500ms cap above.

When the lease itself fails, none of that happens: on boot with Redis unreachable — or on any
poll after a failed create, since failed builds are not pooled — `Cache.Health` is never
reached and **no `db.client.operation.duration` sample is recorded at all**, because the
construction-time `PING` in `redis.NewClient` is untracked. Do not build the boot-time alert on
a cache metric; on that path the signal is the probe result itself. Under the strict default
the `503` body is trimmed to `status`/`cache`/`error` with no `cache_stats`, so the in-body
signal is gone and what remains is the external prober, the `Readiness check failed` ERROR line
`readyCheck` logs with the full error, and the `Builder.preInitCache` WARN — see [Wiring
Kubernetes probes](#wiring-kubernetes-probes). Under `cache.critical: false` the `200` body
carries `cache: "unhealthy"` and a climbing `cache_stats.errors` instead, and no readiness
ERROR is logged — that body is the only in-process signal a warm-pool outage produces, which
is the cost of opting out.

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

Under the strict default the pod never reports Ready while Redis is unreachable: it is kept
out of the Service endpoints, and a Deployment rollout stalls after taking down at most
`maxUnavailable` old replicas (25% under the default RollingUpdate strategy). Set
`maxUnavailable: 0` if you want a rollout during a Redis outage to cost no serving capacity at
all. That holds from the very first poll, including at boot — `cache.NewCacheManager`
validates options without dialing, so the first lease is what connects, and it fails.

What it does **not** do: a failing readiness probe never restarts or kills a container — only a
liveness probe does. Do not point liveness at `/ready` to get that. It would turn one shared
Redis blip into a simultaneous restart of every replica, which is strictly worse than the
rotation drain described under *Choosing a value* above: restarts also drop in-flight requests
and can settle into `CrashLoopBackOff`. Nor is this a "refuse to start" switch — the process
boots either way and simply never reports Ready; `Builder.preInitCache` logs a WARN and
continues, mirroring the manager contract that a failing cache is disabled, not fatal.

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
Under the strict default a cache outage therefore produces one ERROR line per poll per
replica, which is a real volume at `periodSeconds: 10` — alert on it, don't tail it. Setting
`cache.critical: false` removes that line along with the `503`. Beyond it, which in-process
signal you get depends on where the probe fails:

- **No pooled instance yet** (boot, or after the instance was evicted or idle-cleaned): the
  probe's lease triggers a fresh connect, and the default connector logs `Creating Redis cache
  instance` (INFO) then `Failed to create Redis cache client` (ERROR). Failed builds are not
  cached, so this repeats on every poll, alongside go-redis's own dial-failure lines.
  `Failed to create Redis cache client` is the most reliable boot-time alert signal, at the
  same per-poll volume as the readiness line above. (A custom `Options.CacheConnector`
  replaces this logging.)
- **Instance already pooled and its `PING` fails** (Redis died after a healthy start): the
  connector logs nothing, so under `cache.critical: false` the only in-process signals are the
  `db.client.operation.duration` sample described above and the `200` body itself. Under the
  strict default the `Readiness check failed` ERROR line covers it.

Readiness itself stays observable through the external prober either way — the Pod's `READY`
column, the `Unhealthy` kubelet event, `kube_pod_status_ready`. Size
`initialDelaySeconds`/`failureThreshold` against the pre-listen boot window too — see
[startup_defaults.md](startup_defaults.md#messaging-pre-warm-readiness-wait).

## Cache Manager Defaults

GoBricks applies production-safe cache manager defaults when cache is configured:

| Setting | Default (single-tenant) | Default (multi-tenant) | Purpose |
|---------|-------------------------|------------------------|---------|
| `manager.maxsize` | 100 | `multitenant.limits.tenants` | Maximum tenant cache instances (LRU cap) |
| `manager.idlettl` | 15m | 15m | Close idle cache connections |
| `manager.cleanupinterval` | 5m | 5m | Frequency of idle cache cleanup |

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

---

For comprehensive code-snippet examples (cache operations, multi-tenant patterns, testing), see [llms.txt](../llms.txt).
