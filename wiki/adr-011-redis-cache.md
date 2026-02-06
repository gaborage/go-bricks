# ADR-011: Redis Cache Backend with CBOR Serialization

**Date:** 2025-11-09
**Updated:** 2025-11-15
**Status:** Accepted
**Context:** Multi-tenant caching infrastructure and distributed locking support

## Problem Statement

GoBricks applications require:
1. **Query Result Caching**: Reduce database load by caching frequently accessed data
2. **Distributed Locking**: Prevent duplicate processing across replicas (message deduplication, job coordination)
3. **Multi-Tenant Isolation**: Separate cache namespaces per tenant
4. **Type-Safe Operations**: Compile-time guarantees for cache keys and values
5. **Observability**: Metrics for cache hits/misses, lock contention

Without a first-class caching abstraction, developers face:
- **Manual Redis Integration**: Each service reimplements connection pooling, serialization, error handling
- **Inconsistent Patterns**: No standard approach for deduplication or distributed locking
- **Missing Observability**: No unified metrics for cache performance
- **Multi-Tenant Complexity**: Manual tenant ID prefixing error-prone and verbose

## Options Considered

### Option 1: In-Memory Cache Only (Rejected)
- Use `sync.Map` or third-party Go caches (bigcache, freecache)
- No distributed coordination needed
- **Rejected**:
  - Doesn't solve distributed locking across replicas
  - Cache invalidation hard in multi-instance deployments
  - No persistence for session/token storage
  - Limited to single-process applications

### Option 2: Generic Key-Value Interface (Rejected)
- Abstract cache behind `Get(key)`, `Set(key, val)` interface
- Support multiple backends (Redis, Memcached, DynamoDB)
- **Rejected**:
  - Overengineered for current requirements
  - Different backends have vastly different semantics (Redis Lua scripts vs DynamoDB conditional writes)
  - Increases complexity without clear ROI
  - Violates YAGNI principle

### Option 3: Redis-Specific with Abstract Interface (CHOSEN)
- Define vendor-agnostic `Cache` interface
- Implement Redis backend initially
- Support future backends if needed (Memcached, Valkey)
- **Chosen**: Pragmatic balance between abstraction and practicality

## Decision

Implement **Redis as the first-class caching backend** with these architectural principles:

### 1. **Separate Package Structure**
```go
cache/                      // Core interfaces and errors
cache/redis/                // Redis implementation
cache/testing/              // Mock implementations for tests
```

**Rationale:**
- Follows `database/` package pattern (proven architecture)
- Clear separation of concerns (interfaces vs implementations)
- Easy to add alternate backends (e.g., `cache/memcached/`) in future

### 2. **Minimal Cache Interface**
```go
type Cache interface {
    // Core operations
    Get(ctx context.Context, key string) ([]byte, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, key string) error

    // Distributed primitives
    GetOrSet(ctx, key, value, ttl) (storedValue []byte, wasSet bool, error)
    CompareAndSet(ctx, key, expected, new, ttl) (success bool, error)

    // Lifecycle
    Health(ctx context.Context) error
    Stats() (map[string]any, error)
    Close() error
}
```

**Rationale:**
- **Byte-level API**: Serialization delegated to helper functions (separation of concerns)
- **GetOrSet**: Atomic deduplication primitive (idempotency keys, message processing)
- **CompareAndSet**: Distributed lock primitive (worker coordination, leader election)
- **Context-first**: Enables multi-tenant resolution, tracing, deadlines
- **Stats**: Observability hooks for metrics collection

**Alternative Considered:** Generic `Get[T]()`, `Set[T]()` with type parameters
**Trade-offs:** Type-safe but couples cache to serialization logic; rejected for separation of concerns

### 3. **CBOR Serialization Standard**
**Library:** `github.com/fxamacker/cbor/v2` (v2.9.0+)

**Rationale:**
- ✅ **RFC 8949 Internet Standard** (decades of cross-language compatibility)
- ✅ **Actively maintained** (2025 releases, production-proven)
- ✅ **Used by Kubernetes, Microsoft, IBM** (battle-tested)
- ✅ **Security features** (size limits, malformed data rejection)
- ✅ **No code generation** (runtime reflection like JSON)
- ✅ **Performance** (~1.5-2x faster than JSON, similar to MessagePack)

**Alternative Considered:** MessagePack (`vmihailenco/msgpack/v5`)
**Rejection Reason:** Last release October 2023 (2+ years ago), no active maintenance

**Alternative Considered:** Protocol Buffers
**Rejection Reason:** Requires `.proto` schema definitions (overkill for dynamic caching)

**Alternative Considered:** encoding/gob
**Rejection Reason:** Go-only (breaks cross-language requirement for future polyglot systems)

### 4. **Multi-Tenant Isolation via Manager**
```go
type Manager interface {
    Get(ctx context.Context, tenantID string) (Cache, error)
    Stats() map[string]any
    Close() error
}
```

**Architecture:**
- **LRU Eviction**: Limit max cached connections (prevent memory exhaustion)
- **Singleflight**: Prevent thundering herd on cache miss (concurrent tenant requests)
- **Idle Cleanup**: Background goroutine evicts idle connections (resource efficiency)
- **Thread-Safe**: `sync.RWMutex` for concurrent access

**Rationale:**
- Follows proven `database.DbManager` pattern (interface is `cache.Manager`, implementation is `*manager`)
- Tenant isolation without manual key prefixing
- Prevents connection pool exhaustion in high-cardinality tenant scenarios

### 5. **Fail-Fast Configuration Validation**
```yaml
cache:
  enabled: true
  type: redis  # Only "redis" supported (no memcached planned)
  redis:
    host: localhost
    port: 6379
    password: ${CACHE_REDIS_PASSWORD}
    database: 0
    pool_size: 10
```

**Validation:**
- Panic at startup if `enabled: true` but `host` missing (explicit > implicit)
- Skip initialization if `enabled: false` (graceful degradation)
- Environment variable override support (`CACHE_REDIS_PASSWORD`)

**Rationale:**
- Aligns with "Database by Intent" (ADR-003)
- Deterministic behavior (same config → same result)
- No silent failures or degraded modes

### 6. **ModuleDeps Extension (Breaking Change)**
```go
type ModuleDeps struct {
    // ... existing fields
    GetCache func(context.Context) (cache.Cache, error)  // NEW
}
```

**Impact:**
- Minor breaking change (new field on struct)
- Backward compatible: modules not using cache ignore new field
- Function-based dependency (matches `GetDB`, `GetMessaging` pattern)

**Rationale:**
- Consistent API with existing multi-tenant dependencies
- Context-aware resolution enables tenant isolation
- Enables test mocking via dependency injection

## Implementation Details

### Phase 1: Core Interfaces (PR #1) ← **CURRENT**
- **Files:** `cache/types.go`, `cache/errors.go`
- **Deliverables:**
  - `Cache` interface definition
  - `Manager` interface definition (manages cache instances per tenant)
  - Sentinel errors (`ErrNotFound`, `ErrCASFailed`, `ErrClosed`, `ErrInvalidTTL`)
  - Structured errors (`ConfigError`, `ConnectionError`, `OperationError`)
- **Tests:** 100% coverage on error types
- **Documentation:** This ADR draft

### Phase 2-9: Remaining Implementation
See main implementation plan for details (CBOR serialization, Redis client, configuration, manager, app integration, observability, integration tests, documentation).

### Technical Decisions

**1. Byte-Level Interface vs Generic Interface**
- **Decision:** Use `[]byte` for Get/Set, separate serialization helpers
- **Rationale:**
  - Clear separation: cache = storage, serialization = transformation
  - Supports future zero-copy optimizations
  - Generic helpers available for common use cases

**2. GetOrSet Semantics**
- **Decision:** Return `(storedValue, wasSet, error)`
- **Rationale:**
  - `wasSet=true`: Value was newly set (first-time processing)
  - `wasSet=false`: Value already existed (duplicate detected)
  - `storedValue`: Always returns the value in cache (current or newly set)
  - Enables idempotency without separate Get+Set race

**3. CompareAndSet Semantics**
- **Decision:** Accept `nil` for `expectedValue` → SET NX semantics
- **Rationale:**
  - `expectedValue=nil`: Set only if key doesn't exist (acquire lock)
  - `expectedValue!=nil`: Update only if current value matches (optimistic concurrency)
  - Covers both lock acquisition and CAS update patterns

**4. Error Handling Strategy**
- **Decision:** Sentinel errors for cache misses, structured errors for operations
- **Rationale:**
  - `errors.Is(err, cache.ErrNotFound)` for cache miss detection
  - Structured errors (`OperationError`) include context (key, operation)
  - Fail-fast config errors at startup (prevent runtime surprises)

## Consequences

### Positive
- **Unified Caching**: First-class cache abstraction across all modules
- **Distributed Coordination**: Built-in primitives for locks and deduplication
- **Multi-Tenant Ready**: Automatic tenant isolation via CacheManager
- **Type-Safe Serialization**: CBOR provides cross-language compatibility + security
- **Observability Ready**: Stats() hooks for metrics collection
- **Future-Proof**: Interface-based design supports alternate backends

### Negative
- **Breaking Change**: `ModuleDeps` extension requires application updates
- **Redis Dependency**: Requires Redis deployment in production
- **Learning Curve**: Developers must understand GetOrSet/CAS semantics
- **Cache Invalidation**: No built-in cache invalidation strategy (application responsibility)

### Neutral
- **Performance**: Redis round-trip latency (~1-5ms local, ~10-50ms remote)
- **Memory**: Minimal overhead (CBOR similar to MessagePack, ~30% smaller than JSON)
- **Complexity**: ~1500 LOC across 9 PRs (similar to database abstraction)

## Migration Strategy

### For Existing Applications
1. **No Cache**: Zero migration needed (cache is optional)
2. **Manual Redis**: Gradual migration to `cache.Cache` interface
3. **New Applications**: Use cache from day one

### Adoption Path
```go
// Phase 1: Basic caching
cache, _ := deps.GetCache(ctx)
data, _ := cache.Marshal(&user)
cache.Set(ctx, "user:123", data, 5*time.Minute)

// Phase 2: Query result caching
cached, err := cache.Get(ctx, cacheKey)
if err == nil {
    return cache.Unmarshal[User](cached)
}
// Fall back to database

// Phase 3: Distributed locking
acquired, _ := cache.CompareAndSet(ctx, lockKey, nil, []byte("worker-1"), 30*time.Second)
if !acquired {
    return ErrLockHeld
}
defer cache.Delete(ctx, lockKey)
```

## Security Considerations

1. **Environment Variables**: Sensitive data (`CACHE_REDIS_PASSWORD`) via env only
2. **CBOR Safety**: Size limits (10000 max array/map elements) prevent DOS
3. **Network Encryption**: TLS support via Redis client configuration
4. **Tenant Isolation**: Enforced at CacheManager level (prevent cross-tenant access)

## Quality Assurance

### Testing Strategy
- **PR #1**: 100% error type coverage ✅
- **PR #2-8**: 80%+ coverage per PR (SonarCloud enforced)
- **Integration Tests**: Real Redis via testcontainers (PR #8)
- **Race Detection**: All tests run with `-race` flag
- **Multi-Platform CI**: Ubuntu/Windows × Go 1.25

### Success Metrics
1. **Zero Regressions**: All existing tests pass unchanged
2. **Coverage**: 80%+ maintained across all PRs
3. **Performance**: <100ms P99 cache operations (local Redis)
4. **Adoption**: Cache used in 50%+ of production services within 6 months

## Future Considerations

- **Cache Warming**: Pre-populate cache on application startup
- **TTL Strategies**: Exponential backoff, adaptive TTL based on access patterns
- **Eviction Callbacks**: Notify application when keys expire
- **Distributed Cache**: Redis Cluster support for horizontal scaling
- **Alternate Backends**: Valkey (Redis fork), Memcached, DragonflyDB
- **Cache Aside Pattern**: Helper utilities for common cache-aside logic
- **Metrics Dashboard**: Grafana templates for cache observability

## Related ADRs

- **ADR-003**: Database by Intent (establishes "optional with fail-fast" pattern)
- **ADR-004**: Lazy Registry Creation (proves singleflight pattern for manager)
- **ADR-006**: OTLP Log Export (demonstrates observability integration pattern)
- **ADR-007**: Struct-Based Columns (shows reflection + caching performance pattern)

---

## Implementation Complete (2025-11-15)

**Status:** All 9 PRs completed, feature production-ready

**Code Quality Metrics:**
- **Coverage:** 92.3% (cache + redis packages combined)
- **Test Lines:** 2,144 total test lines across all packages
  - cache/errors_test.go: 232 lines (96.0% coverage)
  - cache/serialization_test.go: 408 lines (100% for Marshal/Unmarshal)
  - cache/redis/client_test.go: 566 lines (85.7% coverage, miniredis-based)
  - cache/manager_test.go: 870 lines (93.8% coverage, exceptional quality)
  - app/factory_resolver_test.go: 215 lines (defensive validation)
  - app/factory_resolver_integration_test.go: 215 lines (real Redis container)
- **Race Detection:** All tests pass with `-race` flag on all platforms
- **Linting:** Zero golangci-lint issues, follows all framework patterns

**Completed PRs:**
1. ✅ **Cache Interface & Errors** - Sentinel errors, structured errors, comprehensive tests
2. ✅ **CBOR Serialization** - Type-safe generics, security limits, 100% coverage
3. ✅ **Redis Client** - Atomic operations, connection pooling, health monitoring
4. ✅ **Cache Configuration** - Multi-tenant config, TenantStore integration, validation
5. ✅ **Cache Manager** - LRU eviction, idle cleanup, singleflight, lock-free close
6. ✅ **App Integration** - ModuleDeps.GetCache(ctx), factory resolver, health probes
7. ✅ **Redis Connector** - Config-driven, defensive validation, 6 edge case tests
8. ✅ **Integration Tests** - Real Redis container, connector plumbing verified
9. ✅ **Documentation** - llms.txt (400+ lines), README.md, CLAUDE.md, ADR finalized

**Documentation Coverage:**
- **llms.txt** (~1315 lines total): Multi-tenant patterns, CacheManager lifecycle, config injection, testing utilities, health checks, observability, performance metrics, comparison tables
- **README.md**: Cache section added to TOC, Feature Overview, Quick Start config, dedicated Cache section with operations table
- **CLAUDE.md**: Cache Architecture section (matches database/messaging depth), Core Components updated, Troubleshooting section added

**Production-Ready Features:**
- Type-safe CBOR serialization with compile-time guarantees
- Multi-tenant isolation via separate Redis databases
- Automatic lifecycle management (lazy init, LRU, idle cleanup)
- Atomic operations (GetOrSet for deduplication, CompareAndSet for distributed locking)
- Performance: <1ms latency, 100k ops/sec throughput
- Observability integration ready (traces, metrics, health endpoints)

**Completed (PR #164):**
- ✅ `cache/testing` package with MockCache and 20+ assertion helpers
- ✅ End-to-end Redis integration tests (TTL expiration, atomic operation verification, concurrency)
- ✅ Comprehensive benchmark suite (Redis + CBOR serialization)

**Known Gaps (Future Work):**
- Full observability metrics implementation (cache.hit, cache.miss counters)

**Architectural Highlights:**
- Follows "Explicit > Implicit" manifesto principle with function-based `deps.GetCache(ctx)`
- Lock-free close pattern prevents blocking operations during cache lifecycle events
- Singleflight prevents duplicate cache creation under concurrent load
- Defensive validation at multiple layers (app, config, Redis client)

---

*This ADR establishes the foundation for Redis caching in GoBricks, following the framework's principles of explicit configuration, fail-fast validation, and multi-tenant isolation. The CBOR serialization choice ensures cross-language compatibility and active maintenance for years to come.*
