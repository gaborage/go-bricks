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
    result, err, _ := r.registryOnce.Do("messaging-registry", func() (interface{}, error) {
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
