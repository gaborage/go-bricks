# Cache Architecture (Deep Dive)

This document covers the GoBricks Redis-based cache subsystem in depth: lifecycle, performance characteristics, configuration, and the production-safe defaults applied by the framework.

GoBricks provides Redis-based caching with type-safe serialization, multi-tenant isolation, and automatic lifecycle management.

**Core Components:**
- **Redis Client**: Atomic operations (Get/Set/GetOrSet/CompareAndSet), connection pooling, health monitoring
- **CacheManager**: Per-tenant cache lifecycle with lazy initialization, LRU eviction, idle cleanup, singleflight
- **CBOR Serialization**: Type-safe encoding with security limits (max 10k array/map elements)
- **TenantStore Integration**: Automatic tenant resolution from context via `deps.Cache(ctx)`

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
- **Connection Pool**: Default `NumCPU * 2`, configurable via `cache.redis.pool_size`
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
    max_size: 100          # Max tenant cache instances
    idle_ttl: 15m          # Idle timeout per cache
    cleanup_interval: 5m   # Cleanup goroutine frequency
  redis:
    host: localhost
    port: 6379
    password: ${CACHE_REDIS_PASSWORD}  # From environment
    database: 0
    pool_size: 10
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
    cache, err := s.getCache(ctx)  // Resolves tenant from context
    if err != nil {
        return nil, err
    }

    // Try cache first
    data, err := cache.Get(ctx, fmt.Sprintf("user:%d", id))
    if err == nil {
        return cache.Unmarshal[User](data)
    }

    // Cache miss - query database
    user, err := s.queryDatabase(ctx, id)

    // Store in cache with TTL
    data, _ = cache.Marshal(user)
    cache.Set(ctx, fmt.Sprintf("user:%d", id), data, 5*time.Minute)

    return user, nil
}
```

**Key Operations:**
| Operation | Method | Use Case | Atomicity |
|-----------|--------|----------|-----------|
| Basic read | `Get(ctx, key)` | Query result caching | Single-key |
| Basic write | `Set(ctx, key, value, ttl)` | Store computed result | Single-key |
| Deduplication | `GetOrSet(ctx, key, value, ttl)` | Idempotency keys | Atomic SET NX |
| Distributed lock | `CompareAndSet(ctx, key, expected, new, ttl)` | Job coordination | Lua script CAS |
| Type-safe store | `Marshal(v)` + `Set()` | Struct serialization | CBOR encoding |

**Multi-Tenant Isolation:**
- Each tenant gets separate Redis database (configurable per-tenant)
- Cache instances managed by CacheManager with automatic lifecycle
- Context propagation ensures tenant resolution via `deps.Cache(ctx)`
- No key collision between tenants (different Redis databases)

**Observability Integration:**
When `observability.enabled: true`, cache operations automatically emit:
- **Traces**: Spans for Get/Set/Delete with `cache.operation`, `cache.key`, `cache.hit` attributes
- **Metrics**: `cache.operation.duration`, `cache.errors.total`, `cache.manager.active_caches`
- **Health**: Automatic integration with `/health` endpoint (Redis PING command)

## Cache Manager Defaults

GoBricks applies production-safe cache manager defaults when cache is configured:

| Setting | Default | Purpose |
|---------|---------|---------|
| `manager.max_size` | 100 | Maximum tenant cache instances |
| `manager.idle_ttl` | 15m | Close idle cache connections |
| `manager.cleanup_interval` | 5m | Frequency of idle cache cleanup |

**Override defaults** in `config.yaml`:
```yaml
cache:
  manager:
    max_size: 200         # Support more tenants
    idle_ttl: 30m         # Keep caches longer
    cleanup_interval: 10m # Less frequent cleanup
```

---

For comprehensive code-snippet examples (cache operations, multi-tenant patterns, testing), see [llms.txt](../llms.txt).
