# Architecture Decision Records (ADR)

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

*This ADR documents a significant architectural evolution that prioritizes developer experience while maintaining the robustness and flexibility of the GoBricks framework.*
