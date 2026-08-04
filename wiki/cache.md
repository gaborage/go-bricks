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
  critical: false         # true = /ready returns 503 when the cache probe errors
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
- **Health**: A probe registered in the `/ready` probe set whenever the cache manager exists. It leases an instance from the manager (`cacheManager.Get(ctx, "")`) and then calls `Cache.Health(ctx)` on it — a Redis `PING` on every poll. Its status is surfaced as the top-level `cache` and `cache_stats` keys in the `/ready` body, and it fails `/ready` with `503` only when `cache.critical: true`. See [Readiness](#readiness) below

## Readiness

`GET /ready` always reports the cache, whatever `cache.critical` is set to. The 200 body
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

| `cache` value | When | Probe error | 503 with `critical: true`? |
|---------------|------|-------------|----------------------------|
| `healthy` | An instance was leased and its `Health(ctx)` `PING` succeeded; `cache_stats.status` is `healthy` | none | no |
| `not_configured` | `cache.enabled: false` — the connector declines by design; `cache_stats.status` is `not_configured` | none | no |
| `unhealthy` | The lease failed — the manager is closed, or a cold pool tried to build the instance and the construction-time `PING` failed; `cache_stats.status` is `connection_failed` | yes | **yes** |
| `unhealthy` | The lease succeeded but the per-probe `Health(ctx)` `PING` failed or timed out — a live Redis outage against a warm pool; `cache_stats.status` is `unhealthy` | yes | **yes** |
| `disabled` | No probe ran — the manager failed to construct at startup; `cache_stats` is `{}` | n/a | no — even with `critical: true` |

**`cache.critical` (default `false`)**

- `false` — a failing cache probe is reported in the body but never changes the status code.
  Readiness stays green.
- `true` — a failing cache probe short-circuits `/ready` with
  `503 {"status": "not ready", "cache": "unhealthy", "error": "<probe error>"}` — no
  `cache_stats`, and no other component's status. The `error` is the connector error verbatim
  (the same passthrough the database probe already has), so it names the Redis host, port and
  resolved IP on an endpoint that carries no IP allowlist: setting `true` changes what `/ready`
  discloses, so keep it reachable only from inside the cluster. This holds for a hung Redis
  (packets dropped rather than refused) too — `redis.Client.Health` wraps every ping failure,
  `context deadline exceeded` included, in a `cache.ConnectionError` that always renders the
  address. A custom `Options.CacheConnector` puts its own `Health` error string here verbatim.
  The per-probe `PING` is capped at **500ms** independent of both `server.timeout.middleware`
  and `cache.redis.readtimeout` (a shorter caller deadline still wins; neither budget can
  extend it). That cap covers the warm poll only: a cold poll first builds the instance, whose
  own construction-time `PING` carries a 5s budget racing the 5s default
  `server.timeout.middleware`, and only then spends the 500ms — so a cold probe can run well
  past 500ms and issue two `PING`s, not one.

**Choosing a value.** `true` is right for a service that cannot serve correct results without
the cache — a rate limiter, a session store, an idempotency ledger — because pulling the pod
from the load-balancer rotation is preferable to serving wrong answers. `false` is right when
the cache is an optimisation in front of a database that can absorb the miss: a Redis blip
would otherwise drain every replica from rotation at the same moment, converting a latency
regression into an outage. The default is `false`, so an existing deployment's readiness
behaviour is unchanged until it opts in.

Each poll costs one `PING` — `Cache.Health` is contracted fast (<100ms) and safe to call
frequently — and emits one `db.client.operation.duration` sample from inside the Redis client,
tagged `error.type` during an outage, so readiness traffic reaches cache dashboards and any
"cache error rate > 0" alert; the HTTP-layer probe skipper that keeps `/ready` out of traces
and HTTP metrics does not reach one layer down. The probe's lease also resets `manager.idlettl`
and LRU position for the default (`""`) entry, so a continuously polled pod stays on the
warm-pool path.

`cache.critical` is process-global, and so is what the probe observes: it leases key `""`,
which resolves to the top-level `cache.*` connection. A deployment whose caches live only
under `multitenant.tenants.<id>.cache` gets nothing from the flag — with top-level
`cache.enabled: false` the probe reports `not_configured` and no error, so `/ready` stays
`200` however many tenant Redis instances are down. A value under
`multitenant.tenants.<id>.cache.critical` parses (the per-tenant schema reuses `CacheConfig`)
but is ignored for the same reason. Neither setting replaces a Redis-side alert.

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
