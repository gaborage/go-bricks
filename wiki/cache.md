# Cache Architecture (Deep Dive)

This document covers the GoBricks Redis-based cache subsystem in depth: lifecycle, performance characteristics, configuration, and the production-safe defaults applied by the framework.

GoBricks provides Redis-based caching with type-safe serialization, multi-tenant isolation, and automatic lifecycle management.

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
    getCache func(context.Context) (cache.Cache, error)  // Store function, NOT instance
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    m.getCache = deps.Cache  // Tenant-aware resolution
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

    // Cache miss - query database
    user, err := s.queryDatabase(ctx, id)

    // Store in cache with TTL
    data, _ = cache.Marshal(user)
    c.Set(ctx, fmt.Sprintf("user:%d", id), data, 5*time.Minute)

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
- **Health**: Automatic integration with `/health` endpoint (Redis PING command)

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
