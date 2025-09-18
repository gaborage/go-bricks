# Architecture Decision Records (ADRs)

This document records the architectural decisions made during the development of the GoBricks framework.

## ADR-001: Enhanced Handler System Implementation

**Date:** 2025-11-12  
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

**Date**: 2025-01-15
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

*This ADR documents a significant architectural evolution that prioritizes developer experience while maintaining the robustness and flexibility of the GoBricks framework.*
