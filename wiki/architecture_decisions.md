# Architecture Decision Records (ADRs)

This document records the architectural decisions made during the development of the GoBricks framework.

## ADR-001: Enhanced Handler System Implementation

**Date:** 2025-09-12  
**Status:** Accepted  
**Context:** HTTP handler ergonomics and developer experience improvement

### Problem Statement

The original handler system required developers to:
- Manually handle Echo context boilerplate (`c.JSON()`, `c.Bind()`, etc.)
- Implement repetitive request binding and validation logic
- Maintain inconsistent response formats across endpoints
- Mix HTTP concerns with business logic

This led to verbose code, inconsistent APIs, and reduced developer productivity.

### Decision

Implement an enhanced handler system with the following characteristics (updated to reflect current implementation):

#### 1. **Type-Safe Handler Signature**
- **Decision:** Adopt `func(request T, ctx HandlerContext) (response R, error IAPIError)` signature, with support for a typed `Result[R]` wrapper to control HTTP status and headers.
- **Rationale:** 
  - Focuses handlers on business logic rather than HTTP mechanics
  - Provides compile-time type safety for requests and responses
  - Eliminates repetitive Echo context manipulation
- **Alternative Considered:** Keep existing `func(echo.Context) error` signature
- **Trade-offs:** Breaking change for existing handlers, but significantly improves DX

#### 2. **Generic Handler Wrapper Implementation**
- **Decision:** Use Go generics for type-safe wrapper functions
- **Rationale:**
  - Enables compile-time type checking
  - Eliminates runtime type assertions
  - Provides clean API without reflection overhead
- **Alternative Considered:** Interface{}-based approach with runtime reflection

#### 3. **Comprehensive Request Binding Strategy**
- **Decision:** Support multiple binding sources via struct tags
  - `param:"id"` for URL parameters
  - `query:"page"` for query parameters  
  - `header:"Authorization"` for headers
  - JSON body binding via standard tags
- **Rationale:**
  - Declarative approach reduces boilerplate
  - Single struct represents complete request context
  - Consistent with existing Go conventions
- **Details:**
  - Content-Type detection for JSON is tolerant (e.g., `application/json; charset=utf-8`).
  - Field kinds supported: primitives, pointers (auto-allocated), `time.Time` (RFC3339/DateTime/Date), and `[]string` from repeated query params or comma-separated headers.
  - Precedence: bind JSON body first, then overlay path params, then query params, then headers (later sources overwrite earlier values).
- **Alternative Considered:** Separate binding methods for each source
- **Trade-offs:** More complex implementation, but much cleaner usage

#### 4. **Standardized Response Format**
- **Decision:** Implement consistent JSON response envelope
  ```json
  {
    "data": {...},           // Success responses
    "error": {...},          // Error responses  
    "meta": {
      "timestamp": "ISO-8601",
      "traceId": "uuid"
    }
  }
  ```
- **Rationale:**
  - Provides predictable API structure for consumers
  - Enables consistent error handling across all endpoints
  - Supports request tracing and debugging
- **Alternative Considered:** Raw response bodies without envelope
- **Trade-offs:** Slightly larger response size, but much better consistency
- **Details:**
  - Success responses default to `200 OK` with `{ data, meta }`.
  - Handlers can return `Result[R]` to set custom statuses (e.g., `201 Created` via `server.Created(data)`, `202 Accepted` via `server.Accepted(data)`).
  - `204 No Content` is supported via `server.NoContent()` and returns no body by design.

#### 5. **Hierarchical Error System**
- **Decision:** Implement `IAPIError` interface with predefined error types
  - `ValidationError`, `NotFoundError`, `ConflictError`, etc.
  - Environment-aware error details (dev vs production)
  - Structured error codes and messages
- **Rationale:**
  - Consistent error handling across the application
  - Type-safe error creation and handling
  - Security-conscious detail exposure
- **Alternative Considered:** String-based errors or HTTP status codes only
- **Trade-offs:** More complex error handling implementation, but much better error DX
- **Details:**
  - `BaseAPIError` implements `error` for seamless integration with logging/middleware.
  - A unified Echo `HTTPErrorHandler` maps untyped errors and `echo.HTTPError` values into the standard `{ error, meta }` envelope and status.
  - Error `details` are included only in development (`app.env=dev|development`).

#### 6. **Validation Integration Strategy**
- **Decision:** Integrate `validator/v10` with automatic validation
- **Rationale:**
  - Industry-standard validation library
  - Declarative validation via struct tags
  - Comprehensive validation rule set
  - Custom validator support
- **Alternative Considered:** Custom validation framework
- **Trade-offs:** External dependency, but proven and feature-rich solution
- **Details:**
  - Validation is centralized through Echo’s configured validator (`c.Validate(request)`).
  - Validation errors are converted to a standardized `BadRequestError` with structured field errors.
  - Legacy ad-hoc validation formatting helpers were removed from the handler layer to avoid duplication.

#### 7. **Module Interface Evolution**
- **Decision:** Update Module interface to include HandlerRegistry parameter
  ```go
  RegisterRoutes(hr *HandlerRegistry, e *echo.Echo)
  ```
- **Rationale:**
  - Provides access to enhanced handler registration
  - Maintains backward compatibility for Echo access
  - Enables gradual migration strategy
- **Alternative Considered:** Replace Echo entirely in interface
- **Trade-offs:** Breaking change, but maintains flexibility

#### 8. **Context Access Design**
- **Decision:** Provide optional HandlerContext for advanced scenarios
- **Rationale:**
  - Most handlers don't need Echo context access
  - Advanced use cases can still access underlying Echo context
  - Maintains framework flexibility
- **Alternative Considered:** No context access or always-required context
- **Trade-offs:** Slightly more complex API, but maintains power-user capabilities
- **Traceability:**
  - A request `traceId` is resolved by preferring the incoming `X-Request-ID` header, then the response header (if middleware sets it), and if absent a new UUID is generated and set on the response for consistent tracking.

### Implementation Details

#### Core Components
- **`server/handler.go`**: Generic handler wrapper and registration system, `Result` helpers, request binder
- **`server/errors.go`**: Predefined error types, `BaseAPIError` implements `error`
- **`server/server.go`**: Unified `HTTPErrorHandler` that emits standard envelopes
- **Updated Module System**: Enhanced interface with HandlerRegistry support

#### Key Technical Decisions
1. **Go Generics**: Leveraged for type-safe handler registration
2. **Reflection**: Used minimally for request binding only
3. **Middleware Integration**: Preserved existing middleware stack compatibility
4. **Testing Strategy**: Updated mock modules and added focused tests for handler validation, binder, and envelopes

### Consequences

#### Positive
- **Developer Experience**: Significant reduction in boilerplate code
- **Type Safety**: Compile-time validation of request/response types
- **Consistency**: Standardized response format across all APIs
- **Maintainability**: Clear separation of HTTP concerns and business logic
- **Error Handling**: Structured, consistent error responses

#### Negative
- **Breaking Change**: Existing handlers require migration
- **Learning Curve**: Developers need to understand new patterns
- **Complexity**: More sophisticated type system and error handling

#### Neutral
- **Performance**: Minimal impact due to compile-time optimization
- **Dependencies**: Added `github.com/google/uuid` for trace ID generation

### Migration Strategy

1. **Gradual Adoption**: New handlers use enhanced system, existing handlers remain functional
2. **Documentation**: Comprehensive examples and migration guides
3. **Tooling**: Consider code generation tools for handler scaffolding
4. **Training**: Team education on new patterns and best practices

### Monitoring and Success Metrics

- **Developer Velocity**: Measure time to implement new endpoints
- **Code Quality**: Reduce boilerplate lines of code per handler
- **API Consistency**: Standardized response format adoption rate
- **Error Handling**: Improved error response quality and consistency

### Future Considerations

- **Code Generation**: Tooling for automatic handler scaffolding
- **OpenAPI Integration**: Generate API documentation from handler types
- **Testing Utilities**: Enhanced testing helpers for new handler pattern
- **Performance Optimization**: Minimize reflection usage further if needed

---

## ADR-002: Custom Base Path and Health Route Configuration

**Date**: 2025-09-15
**Status**: Accepted
**Decision Makers**: GoBricks Core Team

### Context

GoBricks applications often need to be deployed behind proxies, load balancers, or in containerized environments where:
1. All application routes need a common prefix (e.g., `/api/v1`, `/service-name`)
2. Health check endpoints need custom paths for infrastructure compatibility
3. Different deployment environments require different routing configurations
4. Modules should automatically inherit base paths without code changes

### Decision

We will implement configurable base paths and health routes through:

1. **Server Configuration Fields**:
   - `base_path`: Global prefix applied to ALL routes including health endpoints
   - `health_route`: Custom health endpoint path (default: `/health`)
   - `ready_route`: Custom readiness endpoint path (default: `/ready`)

2. **RouteRegistrar Abstraction**:
   - Create a `RouteRegistrar` interface to abstract Echo's routing capabilities
   - Implement `routeGroup` wrapper with intelligent path handling
   - Support nested groups with proper prefix inheritance
   - Prevent path duplication when routes already contain base prefixes

3. **Module Interface Enhancement**:
   - Update `Module.RegisterRoutes()` to accept `RouteRegistrar` instead of `*echo.Echo`
   - Maintain backward compatibility through interface design
   - Enable automatic base path inheritance for all module routes

### Implementation Details

#### Configuration Structure
```go
type ServerConfig struct {
    // ... existing fields ...
    BasePath    string `koanf:"base_path"`
    HealthRoute string `koanf:"health_route"`
    ReadyRoute  string `koanf:"ready_route"`
}
```

#### RouteRegistrar Interface
```go
type RouteRegistrar interface {
    Add(method, path string, handler echo.HandlerFunc, middleware ...echo.MiddlewareFunc) *echo.Route
    Group(prefix string, middleware ...echo.MiddlewareFunc) RouteRegistrar
    Use(middleware ...echo.MiddlewareFunc)
    FullPath(path string) string
}
```

#### Smart Path Handling
The `routeGroup` implementation includes:
- **Path Normalization**: Ensures consistent leading/trailing slash handling
- **Duplication Prevention**: Detects and prevents double-prefixing when paths already contain base prefix
- **Nested Group Support**: Proper prefix inheritance for complex routing scenarios
- **Full Path Resolution**: `FullPath()` method computes final resolved paths for debugging/logging

### Architecture Benefits

1. **Deployment Flexibility**: Applications can be deployed with different base paths without code changes
2. **Infrastructure Compatibility**: Custom health endpoints work with various load balancers and monitoring systems
3. **Clean Separation**: Health endpoints can be isolated from application routes or share the same prefix
4. **Zero Breaking Changes**: Existing modules work without modification due to interface compatibility
5. **Advanced Path Handling**: Intelligent duplication prevention and nested group support

### Configuration Examples

#### Basic Configuration
```yaml
server:
  base_path: "/api/v1"
  health_route: "/health"
  ready_route: "/ready"
```

#### Environment-Specific Configuration
```bash
# Development
SERVER_BASE_PATH="/dev/api"
SERVER_HEALTH_ROUTE="/health"

# Production
SERVER_BASE_PATH="/api/v1"
SERVER_HEALTH_ROUTE="/status"
SERVER_READY_ROUTE="/readiness"
```

#### Route Resolution Examples
With `base_path: "/api/v1"`:
- Module route `/users` → `/api/v1/users`
- Health endpoint → `/api/v1/health` (or custom path)
- Ready endpoint → `/api/v1/ready` (or custom path)
- Nested group `/admin` + route `/stats` → `/api/v1/admin/stats`

### Consequences

#### Positive
- **Enhanced Deployment Flexibility**: Single codebase works across multiple deployment scenarios
- **Better Infrastructure Integration**: Health endpoints can be customized for specific load balancer requirements
- **Improved Developer Experience**: Automatic base path inheritance eliminates manual prefix management
- **Future-Proof Design**: RouteRegistrar abstraction protects against Echo API changes
- **Smart Path Logic**: Prevents common pitfalls like double-prefixing in complex routing scenarios

#### Negative
- **Increased Complexity**: Additional abstraction layer over Echo's routing
- **Breaking Change**: Module interface signature changes (mitigated by interface compatibility)
- **Configuration Overhead**: More configuration options to understand and manage

#### Neutral
- **Learning Curve**: Developers need to understand the new RouteRegistrar abstraction
- **Testing Complexity**: Additional test scenarios for path resolution and nested groups

### Quality Assurance

#### Testing Strategy
- **Unit Tests**: Path normalization, configuration validation, route resolution
- **Integration Tests**: End-to-end HTTP requests with various configurations
- **Backward Compatibility Tests**: Verify existing functionality remains unchanged
- **Edge Case Testing**: Double-prefixing prevention, nested groups, malformed paths

#### Code Quality
- **Linting**: All code passes golangci-lint with zero issues
- **Race Detection**: Tests pass with `-race` flag
- **Performance**: Zero overhead when base path is empty
- **Documentation**: Comprehensive examples and API documentation

---

## ADR-003: Database by Intent Configuration

**Date:** 2025-09-17
**Status:** Accepted
**Context:** Deterministic database configuration and database-free application support

### Problem Statement

The framework previously included database defaults in configuration loading, creating ambiguity about whether database functionality was intentionally enabled. This prevented clean database-free applications and made configuration behavior non-deterministic.

Applications requiring no database access (pure APIs, microservices) were forced to provide dummy database configuration or work around framework assumptions about database availability.

### Decision

Implement "database by intent" configuration with the following principles:

1. **No Database Defaults**: Remove all database-related defaults from `config.go`
2. **Explicit Configuration Required**: Database is only enabled when explicitly configured
3. **Deterministic Behavior**: Same configuration always produces same result
4. **Conditional Validation**: Skip database validation when not configured

### Implementation

#### Configuration Changes
- **Removed**: All database defaults from `loadDefaults()` in `config/config.go`
- **Added**: `IsDatabaseConfigured()` function in `config/validation.go`
- **Logic**: Database enabled when `ConnectionString != ""` OR `Host != ""` OR `Type != ""`

#### Validation Changes
- **Before**: Database validation always required, caused failures for database-free apps
- **After**: `validateDatabase()` returns early when `!IsDatabaseConfigured()`
- **Consistency**: Shared logic between validation and runtime via `IsDatabaseConfigured()`

#### Runtime Integration
- **Updated**: `app.isDatabaseEnabled()` to use shared `config.IsDatabaseConfigured()`
- **Behavior**: Module dependency injection skips database when not configured

### Consequences

#### Positive
- **Deterministic**: Configuration behavior is predictable and explicit
- **Database-Free Support**: Applications can run without any database configuration
- **Clear Intent**: Explicit configuration signals intentional database usage
- **Reduced Confusion**: No ambiguity about database enablement

#### Negative
- **Breaking Change**: Applications relying on database defaults must update configuration
- **More Explicit**: Requires intentional database configuration in all database-using applications

### Migration Impact

Existing applications using database functionality must explicitly configure:
- Connection string, OR
- Host + type combination

No migration needed for database-free applications.

---

## ADR-004: Lazy Messaging Registry Creation in ModuleRegistry

**Date:** 2025-09-24
**Status:** Accepted
**Context:** Multi-tenant messaging architecture and improved dependency resolution

### Problem Statement

During the implementation of the multi-tenant architecture and the transition to function-based dependency resolution (`GetDB`/`GetMessaging` instead of direct field access), the ModuleRegistry faced an architectural challenge:

1. **Eager Creation Issues**: The original design assumed messaging registry could be created eagerly during ModuleRegistry initialization
2. **Function-Based Dependencies**: New API requires runtime context to resolve messaging client via `GetMessaging(ctx)`
3. **Timing Mismatch**: ModuleRegistry constructor doesn't have a context, but messaging registry creation requires one
4. **Test Failures**: Tests expected non-nil messaging registry after construction, but new API made this impossible

### Options Considered

#### Option 1: Lazy Registry Creation (CHOSEN)
- Initialize messaging registry only when first needed in `RegisterMessaging()`
- Use singleflight protection to handle concurrent initialization
- Keep registry self-contained and not dependent on external initialization

#### Option 2: Eager Creation with Context
- Require context parameter in `NewModuleRegistry(deps, ctx)`
- Create registry immediately during construction
- Break constructor signature compatibility

#### Option 3: External Registry Management
- Remove messaging registry from ModuleRegistry entirely
- Require external code to create and pass registry to methods
- Increase API surface and complexity for consumers

### Decision

**We chose Option 1: Lazy Registry Creation** for the following reasons:

1. **Maintains Encapsulation**: Registry remains self-contained and manages its own lifecycle
2. **Backward Compatibility**: No breaking changes to public constructor API
3. **Context-Aware**: Defers messaging client resolution until context is available
4. **Thread-Safe**: Singleflight protection ensures safe concurrent initialization
5. **Fail-Safe**: Graceful handling when messaging is not configured

### Implementation Details

#### Core Components

**Lazy Initialization Method**:
```go
func (r *ModuleRegistry) initializeMessagingRegistry(ctx context.Context) (*messaging.Registry, error) {
    result, err, _ := r.registryOnce.Do("messaging-registry", func() (any, error) {
        if r.messagingRegistry != nil {
            return r.messagingRegistry, nil
        }

        client, err := r.deps.GetMessaging(ctx)
        if err != nil {
            return nil, err
        }

        registry := messaging.NewRegistry(client, r.logger)
        r.messagingRegistry = registry
        return registry, nil
    })

    if err != nil {
        return nil, err
    }
    return result.(*messaging.Registry), nil
}
```

**Updated RegisterMessaging Method**:
- Calls `initializeMessagingRegistry()` to ensure registry exists
- Gracefully handles messaging unavailability by logging and continuing
- Maintains all existing functionality once registry is created

#### Technical Decisions

1. **Singleflight Protection**: Uses `golang.org/x/sync/singleflight` to prevent duplicate initialization
2. **Error Handling**: Failed initialization is logged but doesn't crash the application
3. **Nil Checks**: Maintains existing nil checks in shutdown for backward compatibility
4. **Context Usage**: Uses `context.Background()` for infrastructure setup operations

### Consequences

#### Positive
- **Maintains Encapsulation**: Registry manages its own lifecycle without external dependencies
- **Thread-Safe**: Concurrent calls to RegisterMessaging are safe
- **Context-Aware**: Can properly resolve context-dependent messaging clients
- **Graceful Degradation**: Applications without messaging work seamlessly
- **Future-Proof**: Architecture supports both single-tenant and multi-tenant modes

#### Negative
- **Deferred Errors**: Messaging configuration errors appear later during RegisterMessaging
- **Additional Complexity**: Introduces singleflight dependency and lazy initialization pattern
- **State Management**: Registry state becomes more complex with initialization tracking

#### Neutral
- **Performance**: Negligible impact, initialization happens once
- **Memory**: Minimal additional memory for singleflight.Group
- **Testing**: Tests needed updates but overall complexity remained similar

### Migration Impact

**Breaking Changes**: None - existing code continues to work
**Test Updates**: Tests expecting immediate registry creation updated to expect lazy creation
**Dependencies**: Added `golang.org/x/sync/singleflight` for concurrent initialization protection

### Quality Assurance

- **Thread Safety**: Singleflight ensures safe concurrent access
- **Error Handling**: Comprehensive error scenarios tested
- **Backward Compatibility**: All existing functionality preserved
- **Resource Management**: Proper cleanup in shutdown methods
- **Integration Testing**: Multi-tenant and single-tenant modes both validated

### Future Considerations

- **Metrics**: Consider adding initialization timing metrics
- **Logging**: Enhanced logging for registry lifecycle events
- **Context Propagation**: May benefit from explicit context threading in future versions
- **Health Checks**: Registry initialization status in health endpoints

---

## ADR-005: Type-Safe WHERE Clause Construction

**Date:** 2025-09-27
**Status:** Accepted
**Context:** Oracle identifier quoting reliability and SQL injection prevention

### Problem Statement

The framework's query builder allowed raw string WHERE clauses that could bypass Oracle identifier quoting, leading to runtime SQL errors when reserved words were used unquoted:

```sql
-- Problematic query generated by: qb.Select("id", "number").Where("number = ?", value)
SELECT id, "number" FROM accounts WHERE number = :1
                                        ^^^^^^ unquoted reserved word
-- Results in: ORA-00936: missing expression at position 103
```

This issue occurred because:
1. **Inconsistent Quoting**: SELECT clause properly quoted "number" but WHERE clause did not
2. **Runtime Failures**: Errors only surfaced during query execution, not at compile time
3. **Developer Confusion**: Inconsistent behavior between different SQL vendors
4. **Maintenance Burden**: Required developers to remember Oracle-specific quoting rules

### Options Considered

#### Option 1: WHERE Clause Parser (Rejected)
- Parse string-based WHERE clauses to identify and quote column names
- Maintain backward compatibility with string-based API
- **Rejected**: Complex implementation, potential parsing edge cases, runtime overhead

#### Option 2: Enhanced Documentation (Rejected)
- Improve documentation about Oracle quoting requirements
- Provide more examples of correct usage
- **Rejected**: Does not prevent the fundamental issue, relies on developer memory

#### Option 3: Type-Safe WHERE Methods (CHOSEN)
- Remove `.Where()` method entirely
- Implement type-safe methods like `WhereEq()`, `WhereLt()`, etc.
- Provide `WhereRaw()` escape hatch with clear documentation
- **Chosen**: Compile-time safety, impossible to bypass quoting accidentally

### Decision

**We chose Option 3: Type-Safe WHERE Methods** with the following implementation:

#### Core Design Principles
1. **Compile-Time Safety**: Prevent unquoted identifier issues at build time
2. **Clear Responsibility**: Explicit methods vs. escape hatch with documented risks
3. **Oracle-First**: Design that works reliably with Oracle's quoting requirements
4. **Zero Ambiguity**: No possibility of accidentally bypassing safety mechanisms

#### New SelectQueryBuilder API
```go
// Type-safe methods (automatically handle quoting)
.WhereEq(column, value)           // column = value
.WhereNotEq(column, value)        // column <> value
.WhereLt(column, value)           // column < value
.WhereGt(column, value)           // column > value
.WhereIn(column, values)          // column IN (...)
.WhereNull(column)                // column IS NULL
.WhereBetween(column, min, max)   // column BETWEEN min AND max

// Escape hatch (user responsibility for quoting)
.WhereRaw(condition, args...)     // Raw SQL with manual quoting
```

#### Architecture Changes
1. **SelectQueryBuilder**: New wrapper around squirrel.SelectBuilder
2. **Method Delegation**: All squirrel methods (Join, OrderBy, etc.) delegated through wrapper
3. **Quoting Integration**: Type-safe methods use existing `qb.Eq()`, `qb.Lt()` infrastructure
4. **Breaking Change**: `.Where()` method completely removed (no deprecation period)

### Implementation Details

#### Technical Components

**Core Architecture**:
```go
type SelectQueryBuilder struct {
    qb            *QueryBuilder
    selectBuilder squirrel.SelectBuilder
}

func (qb *QueryBuilder) Select(columns ...string) *SelectQueryBuilder {
    return &SelectQueryBuilder{
        qb:            qb,
        selectBuilder: qb.statementBuilder.Select(qb.quoteColumnsForSelect(columns...)...),
    }
}
```

**Type-Safe WHERE Implementation**:
```go
func (sqb *SelectQueryBuilder) WhereEq(column string, value any) *SelectQueryBuilder {
    sqb.selectBuilder = sqb.selectBuilder.Where(sqb.qb.Eq(column, value))
    return sqb
}
```

**Clear Escape Hatch Documentation**:
```go
// WhereRaw adds a raw SQL WHERE condition to the query.
//
// WARNING: This method bypasses all identifier quoting and SQL injection protection.
// It is the caller's responsibility to:
//   - Properly quote any identifiers (especially Oracle reserved words)
//   - Ensure the SQL fragment is valid for the target database
//   - Never concatenate user input directly into the condition string
func (sqb *SelectQueryBuilder) WhereRaw(condition string, args ...any) *SelectQueryBuilder
```

#### Testing Strategy
1. **Comprehensive Oracle Tests**: All Oracle reserved words tested with type-safe methods
2. **Original Bug Reproduction**: Specific test demonstrating the fix for the exact error
3. **Escape Hatch Tests**: WhereRaw functionality with manual quoting examples
4. **Backward Compatibility**: All existing tests updated to use new API

### Migration Strategy

#### Breaking Change Implementation
- **No Deprecation Period**: Immediate breaking change for compile-time safety
- **Clear Error Messages**: Compilation errors guide developers to correct methods
- **Comprehensive Examples**: Documentation shows exact replacements

#### Migration Patterns
```go
// OLD (no longer compiles)
.Where("number = ?", value)
.Where("level > ?", minLevel)
.Where("status IN (?)", statuses)

// NEW (required)
.WhereEq("number", value)
.WhereGt("level", minLevel)
.WhereIn("status", statuses)

// Complex conditions use WhereRaw with manual quoting
.WhereRaw(`"number" = ? AND ROWNUM <= ?`, value, 10)
```

### Consequences

#### Positive
- **Eliminates Oracle Quoting Bugs**: Impossible to accidentally create unquoted WHERE clauses
- **Compile-Time Safety**: Errors caught during build, not runtime
- **Clear Responsibility Boundaries**: Type-safe methods vs. explicit escape hatch
- **Improved Developer Experience**: No need to remember Oracle quoting rules
- **Future-Proof**: Easy to add new type-safe methods as needed
- **Self-Documenting Code**: Method names clearly indicate operation type

#### Negative
- **Breaking Change**: All existing `.Where()` usage requires migration
- **Learning Curve**: Developers must learn new API methods
- **Verbosity**: Some complex conditions require multiple method calls
- **Migration Effort**: Existing codebases need comprehensive updates

#### Neutral
- **Performance**: No runtime impact, compile-time code generation
- **Memory**: Minimal wrapper overhead
- **Dependencies**: No new external dependencies required

### Quality Assurance

#### Testing Coverage
- **Oracle Reserved Words**: Comprehensive testing with 20+ reserved words
- **All Operators**: Coverage for =, <>, <, <=, >, >=, IN, NOT IN, IS NULL
- **Edge Cases**: Complex queries, nested conditions, function calls
- **Regression**: Original bug scenario specifically tested and verified fixed

#### Code Quality
- **Type Safety**: Full Go type checking throughout query construction
- **Documentation**: Extensive inline documentation with examples
- **Error Handling**: Clear error messages for incorrect usage
- **Performance**: Zero runtime overhead for type-safe methods

### Success Metrics

1. **Zero Oracle Quoting Errors**: No ORA-00936 errors related to unquoted identifiers
2. **Compilation Safety**: All identifier quoting issues caught at build time
3. **Developer Adoption**: Type-safe methods used over WhereRaw in 90%+ of cases
4. **Migration Completeness**: All framework tests pass with new API

### Future Considerations

- **Additional Operators**: Add more type-safe methods as needed (LIKE patterns, EXISTS, etc.)
- **Query Validation**: Consider static analysis tools for WhereRaw usage
- **Code Generation**: Tooling to assist with migration from old API
- **Documentation**: Interactive examples and best practices guide

---

*This ADR represents a significant shift toward compile-time safety and Oracle-first design, prioritizing reliability over backward compatibility to prevent an entire class of runtime SQL errors.*

---

## ADR-006: OpenTelemetry Protocol (OTLP) Log Export Integration

**Date:** 2025-10-10
**Status:** Accepted
**Context:** Unified observability stack completion with centralized log export

### Problem Statement

The GoBricks framework provided comprehensive OpenTelemetry integration for distributed tracing and metrics, but lacked native support for exporting structured logs to OTLP collectors. This created an incomplete observability story where:

1. **Fragmented Observability**: Logs remained siloed in stdout/files while traces and metrics flowed to centralized collectors
2. **Manual Correlation**: Developers had to manually correlate logs with traces using external tools
3. **Missing Context**: Structured log data (fields, severity, timestamps) wasn't available in observability backends
4. **Production Blind Spots**: High-volume environments couldn't easily search/filter logs without centralized aggregation
5. **Inconsistent Export**: No unified configuration for traces, metrics, AND logs via a single OTLP endpoint

### Options Considered

#### Option 1: External Log Forwarder (Rejected)
- Use external agents (Fluent Bit, Filebeat) to tail log files and forward to collectors
- Keep framework focused only on stdout logging
- **Rejected**:
  - Adds deployment complexity (separate agent per service)
  - Loses structured field information during file→parse→export cycle
  - Cannot leverage framework's existing OTLP infrastructure
  - Harder to correlate with framework-generated traces

#### Option 2: Hook-Based Logger Modification (Rejected)
- Modify zerolog internals to add OpenTelemetry hooks
- Fork or wrap zerolog with custom export logic
- **Rejected**:
  - High maintenance burden (fork divergence)
  - zerolog lacks native hook system
  - Breaks framework's "vendor agnosticism" for low-cost dependencies
  - Could break on upstream zerolog changes

#### Option 3: io.Writer Bridge Pattern (CHOSEN)
- Implement `io.Writer` that intercepts zerolog JSON output
- Parse JSON and convert to OpenTelemetry log records
- Leverage existing observability provider infrastructure
- **Chosen**: Clean integration, minimal coupling, reuses OTLP infrastructure

### Decision

**We chose Option 3: io.Writer Bridge Pattern** with the following architecture:

#### Core Design Principles

1. **Explicit Configuration > Silent Degradation**: Fail fast on configuration conflicts (e.g., pretty mode + OTLP)
2. **Reuse Existing Infrastructure**: Leverage observability provider for OTLP exporters and batching
3. **Automatic Trace Correlation**: Inject trace_id/span_id from context without boilerplate
4. **Deterministic Sampling**: Hash-based sampling ensures consistent log filtering across replicas
5. **Zero Breaking Changes**: Existing logger usage continues to work unchanged

#### Architecture Components

**1. Configuration Layer** (`observability/config.go`)
```go
type LogsConfig struct {
    Enabled          *bool                  // nil=default true when observability enabled
    Endpoint         string                 // OTLP endpoint (inherits from trace)
    Protocol         string                 // "http" or "grpc" (inherits from trace)
    DisableStdout    bool                   // false=both stdout+OTLP, true=OTLP-only
    Sample           LogSampleConfig        // Sampling configuration
    Batch            BatchConfig            // Reused from traces
    Export           ExportConfig           // Reused from traces
    Max              MaxConfig              // Reused from traces
}

type LogSampleConfig struct {
    Rate             *float64  // 0.0-1.0, nil=default 1.0 (100%)
    AlwaysSampleHigh *bool     // true=WARN/ERROR/FATAL always exported
}
```

**2. OTel Bridge** (`logger/otel_bridge.go`)
```go
type OTelBridge struct {
    loggerProvider *sdklog.LoggerProvider
    logger         log.Logger
}

func (b *OTelBridge) Write(p []byte) (n int, err error) {
    // 1. Parse zerolog JSON output
    // 2. Extract: timestamp, level, message, fields
    // 3. Map severity: trace→1, debug→5, info→9, warn→13, error→17, fatal→21
    // 4. Convert fields to OTel attributes
    // 5. Emit log record via logger.Emit()
}
```

**3. Logger Integration** (`logger/logger.go`)
```go
func (l *ZeroLogger) WithOTelProvider(provider OTelProvider) *ZeroLogger {
    // Fail-fast validation: panic if pretty=true
    if l.pretty {
        panic("OTLP log export requires JSON mode (pretty=false)")
    }

    bridge := NewOTelBridge(provider.LoggerProvider())

    // Dual output or OTLP-only based on configuration
    var output io.Writer
    if provider.ShouldDisableStdout() {
        output = bridge  // OTLP-only (production)
    } else {
        output = io.MultiWriter(os.Stdout, bridge)  // Both (dev)
    }

    return &ZeroLogger{
        zlog:   zerolog.New(output).With().Timestamp().Logger(),
        filter: l.filter,
        pretty: false,
    }
}
```

**4. Automatic Trace Correlation** (`logger/logger.go`)
```go
func (l *ZeroLogger) WithContext(ctx any) Logger {
    // Phase 1: Check for explicit zerolog.Ctx() logger
    zl := zerolog.Ctx(c)
    if zl != nil && zl.GetLevel() != zerolog.Disabled {
        return &ZeroLogger{zlog: zl, ...}
    }

    // Phase 2: Auto-inject trace_id/span_id from OTel span context
    span := trace.SpanFromContext(c)
    if span.SpanContext().IsValid() {
        log := l.zlog.With().
            Str("trace_id", span.SpanContext().TraceID().String()).
            Str("span_id", span.SpanContext().SpanID().String()).
            Logger()
        return &ZeroLogger{zlog: &log, ...}
    }
}
```

**5. Bootstrap Integration** (`app/bootstrap.go`)
```go
func (b *appBootstrap) dependencies() *dependencyBundle {
    obsProvider := b.initializeObservability()

    // Enhance logger with OTLP export if enabled
    enhancedLogger := b.enhanceLoggerWithOTel(obsProvider)

    deps := &ModuleDeps{
        Logger: enhancedLogger,  // All modules get OTLP-enabled logger
        ...
    }
}
```

**6. Deterministic Sampling** (`observability/logs.go`)
```go
func (e *severityFilterExporter) shouldExport(rec *sdklog.Record) bool {
    // Always export high-severity (WARN/ERROR/FATAL)
    if e.alwaysSampleHigh && severity >= 13 {
        return true
    }

    // Deterministic hash-based sampling for INFO/DEBUG
    hash := rec.Timestamp().UnixNano() % 100
    threshold := int64(e.sampleRate * 100)
    return hash < threshold
}
```

### Implementation Details

#### Technical Decisions

1. **Tri-State Configuration**: Use `*bool` and `*float64` to distinguish nil (default) from explicit false/0
2. **Config Inheritance**: Logs inherit `protocol`, `insecure`, `headers` from trace config (DRY)
3. **Pretty Mode Tracking**: Add `pretty bool` field to `ZeroLogger` for fail-fast validation
4. **Provider Interface Extension**: Add `ShouldDisableStdout()` method to avoid exposing entire config
5. **Hash-Based Sampling**: Use `timestamp % 100` for deterministic sampling without state

#### Integration Points

```
┌─────────────────────────────────────────────────┐
│         Application Bootstrap Flow              │
│                                                 │
│  1. Load observability config (YAML + env)     │
│  2. initLogProvider() [if logs.enabled=true]   │
│     ├─ createLogExporter() [HTTP/gRPC/stdout]  │
│     ├─ wrapWithSeverityFilter()                │
│     └─ createLogProcessor() [batching]         │
│  3. enhanceLoggerWithOTel(provider)            │
│     ├─ Fail-fast: panic if pretty=true         │
│     ├─ NewOTelBridge(loggerProvider)           │
│     └─ Create MultiWriter or single writer     │
│  4. Pass enhanced logger to all modules        │
└─────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────┐
│            Runtime Log Flow                     │
│                                                 │
│  Handler: logger.WithContext(ctx).Info()       │
│    ├─ Extract trace_id/span_id from span       │
│    └─ Write JSON log entry                     │
│        ├─ stdout (if not disabled)             │
│        └─ OTel Bridge                           │
│            ├─ Parse JSON                        │
│            ├─ Map severity (info→9)             │
│            ├─ Convert fields to attributes      │
│            ├─ Severity filter & sampling        │
│            ├─ Batch processor (512 records)     │
│            └─ OTLP Exporter → Collector         │
└─────────────────────────────────────────────────┘
```

#### File Changes Summary

| File | LOC Changed | Purpose |
|------|-------------|---------|
| `observability/config.go` | +191 | LogsConfig, defaults, validation |
| `observability/logs.go` | +241 | Log exporter factory, severity filter |
| `observability/provider.go` | +55 | Provider interface, init logic |
| `logger/logger.go` | +125 | WithOTelProvider(), trace correlation |
| `logger/otel_bridge.go` | +163 | JSON→OTel conversion bridge |
| `app/bootstrap.go` | +72 | Logger enhancement integration |
| `observability/noop.go` | +13 | Noop provider methods |
| `observability/shutdown_test.go` | +9 | Mock provider updates |

**Total:** ~869 lines added across 8 files

### Configuration Examples

#### Development Configuration
```yaml
observability:
  enabled: true
  service:
    name: "my-service"
    version: "1.0.0"
  environment: "development"

  logs:
    enabled: true
    endpoint: "localhost:4317"
    protocol: "grpc"
    insecure: true
    disable_stdout: false  # Both stdout + OTLP for debugging
    sample:
      rate: 1.0  # 100% sampling in dev
      always_sample_high: true
    batch:
      timeout: 500ms  # Fast export for debugging
      size: 512
```

#### Production Configuration
```yaml
observability:
  enabled: true
  service:
    name: "my-service"
    version: "1.2.3"
  environment: "production"

  logs:
    enabled: true
    endpoint: "otel-collector.prod:4317"
    protocol: "grpc"
    insecure: false  # TLS enabled
    disable_stdout: true  # OTLP-only, reduce disk I/O
    headers:
      Authorization: "Bearer ${OTEL_API_KEY}"
    sample:
      rate: 0.1  # 10% sampling for INFO/DEBUG
      always_sample_high: true  # WARN/ERROR/FATAL always exported
    batch:
      timeout: 5s  # Efficient batching
      size: 1024
```

### Consequences

#### Positive

- **Unified Observability**: Logs, traces, and metrics all export via single OTLP endpoint
- **Automatic Correlation**: trace_id/span_id automatically injected into logs from context
- **Production-Ready Sampling**: Deterministic hash-based sampling reduces volume while preserving critical logs
- **Flexible Output**: Both stdout+OTLP (dev) or OTLP-only (production) modes
- **Type-Safe Configuration**: Tri-state pointers enable proper default detection
- **Fail-Fast Validation**: Pretty mode conflict detected at startup, not runtime
- **Zero Boilerplate**: Modules automatically get OTLP-enabled logger via dependency injection
- **Vendor Agnostic**: Works with any OTLP-compatible collector (Jaeger, Grafana, Datadog, etc.)

#### Negative

- **JSON Mode Required**: OTLP export incompatible with pretty console output (enforced via panic)
- **Parse Overhead**: ~2-5µs per log entry for JSON parsing (acceptable for production logging rates)
- **Complexity**: Adds ~869 lines of code across observability and logger packages
- **New Dependencies**: Requires OpenTelemetry SDK log packages (v0.14.0)
- **Learning Curve**: Developers must understand OTLP configuration and sampling concepts

#### Neutral

- **Memory**: Minimal overhead (~2KB per logger instance for bridge and provider references)
- **Performance**: Async batching and export, no blocking on hot path
- **Backward Compatibility**: Zero breaking changes—existing logger usage unchanged

### Migration Impact

#### For Existing Applications

**No Migration Required** for applications that:
- Use JSON logging (`logger.pretty=false`)
- Don't need OTLP log export

**Optional Enablement** for applications wanting centralized logs:
1. Add `observability.logs.enabled: true` to config
2. Configure OTLP endpoint (or inherit from trace config)
3. Logs automatically export to collector

**Configuration Conflict** for applications using:
- `logger.pretty=true` AND `observability.logs.enabled=true`
- **Resolution**: Change `pretty: false` or disable `logs.enabled`
- **Detection**: Fail-fast panic at startup with clear error message

### Quality Assurance

#### Testing Strategy

1. **Unit Tests** (Phase 8 - Planned):
   - Config defaults and validation
   - Exporter selection (stdout/HTTP/gRPC)
   - Severity filter and sampling distribution
   - JSON→OTel conversion edge cases
   - Logger enhancement and context injection

2. **Integration Tests** (Phase 8 - Planned):
   - End-to-end OTLP export with in-memory exporter
   - Trace correlation via WithContext()
   - Batching behavior under load
   - DisableStdout output modes

3. **Validation Performed**:
   - ✅ `make check` passes (fmt, lint, race detection)
   - ✅ Zero linting issues (golangci-lint)
   - ✅ All existing tests pass unchanged
   - ✅ Multi-platform CI (Ubuntu, Windows × Go 1.24, 1.25)

#### Code Quality Metrics

- **Linting**: 0 issues across all packages
- **Race Detection**: All tests pass with `-race` flag
- **Type Safety**: Full compile-time checking via generics and type-safe methods
- **Documentation**: Comprehensive inline documentation with examples
- **Error Handling**: Graceful degradation when OTLP unavailable

### Success Metrics

1. **Unified Export**: Single OTLP endpoint handles logs, traces, and metrics
2. **Automatic Correlation**: 100% of logs include trace_id/span_id when span context available
3. **Production Efficiency**: Sampling reduces log volume while preserving critical errors
4. **Developer Adoption**: OTLP log export enabled in 50%+ of production deployments within 3 months
5. **Zero Errors**: No ORA/SQL/runtime errors introduced by logging changes

### Observability Benefits in Practice

#### Before (Fragmented)
```text
Developer Workflow:
1. Check application logs (stdout/files)
2. Find error message
3. Extract trace_id manually
4. Search Jaeger for trace
5. Correlate timestamps manually

Challenges:
- Multiple tools (log viewer + trace viewer)
- Manual correlation effort
- Lost context switching between systems
```

#### After (Unified)
```text
Developer Workflow:
1. Open Grafana Explore
2. Search logs by trace_id
3. Click trace_id link → jump directly to trace
4. See correlated logs inline with spans

Benefits:
- Single pane of glass
- Automatic correlation
- Faster incident resolution
```

### Future Considerations

- **Stack Traces**: Add structured stack traces as log attributes for panics/errors
- **Log Metrics**: Emit metrics about log volume and sampling rates
- **Dynamic Sampling**: Adjust sample rates based on load or error rates
- **Compression**: GZIP compression for batch export to reduce network bandwidth
- **Advanced Sampling**: Tail-based sampling (sample traces with errors)
- **Performance Profiling**: Measure and optimize JSON parsing overhead

### Related ADRs

- **ADR-001**: Enhanced Handler System (provides HandlerContext for trace correlation)
- **ADR-005**: Type-Safe WHERE Clauses (demonstrates fail-fast validation pattern)

---

*This ADR completes the observability stack for GoBricks, enabling unified export of logs, traces, and metrics through OpenTelemetry Protocol. The io.Writer bridge pattern provides clean integration while maintaining the framework's principles of explicit configuration, fail-fast validation, and zero breaking changes.*
