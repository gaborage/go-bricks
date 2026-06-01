# ADR-002: Custom Base Path and Health Route Configuration

**Date**: 2025-09-15
**Status**: Accepted
**Decision Makers**: GoBricks Core Team

> **Amended**: The configuration keys were restructured from a flat layout
> (`server.base_path` / `server.health_route` / `server.ready_route`) into a
> nested `server.path` sub-struct (`server.path.base` / `server.path.health` /
> `server.path.ready`). The decision below is unchanged; only the key names and
> struct shape differ. Examples in this document reflect the current nested
> layout.

## Context

GoBricks applications often need to be deployed behind proxies, load balancers, or in containerized environments where:
1. All application routes need a common prefix (e.g., `/api/v1`, `/service-name`)
2. Health check endpoints need custom paths for infrastructure compatibility
3. Different deployment environments require different routing configurations
4. Modules should automatically inherit base paths without code changes

## Decision

We will implement configurable base paths and health routes through:

1. **Server Configuration Fields** (under `server.path`):
   - `base`: Global prefix applied to ALL routes including health endpoints
   - `health`: Custom health endpoint path (default: `/health`)
   - `ready`: Custom readiness endpoint path (default: `/ready`)

2. **RouteRegistrar Abstraction**:
   - Create a `RouteRegistrar` interface to abstract Echo's routing capabilities
   - Implement `routeGroup` wrapper with intelligent path handling
   - Support nested groups with proper prefix inheritance
   - Prevent path duplication when routes already contain base prefixes

3. **Module Interface Enhancement**:
   - Update `Module.RegisterRoutes()` to accept `RouteRegistrar` instead of `*echo.Echo`
   - Maintain backward compatibility through interface design
   - Enable automatic base path inheritance for all module routes

## Implementation Details

### Configuration Structure
```go
type ServerConfig struct {
    // ... existing fields ...
    Path PathConfig `koanf:"path"`
}

type PathConfig struct {
    Base   string `koanf:"base"`
    Health string `koanf:"health"`
    Ready  string `koanf:"ready"`
}
```

### RouteRegistrar Interface

```go
type RouteRegistrar interface {
    Add(method, path string, handler echo.HandlerFunc, middleware ...echo.MiddlewareFunc) echo.RouteInfo
    Group(prefix string, middleware ...echo.MiddlewareFunc) RouteRegistrar
    Use(middleware ...echo.MiddlewareFunc)
    FullPath(path string) string
}
```

### Smart Path Handling
The `routeGroup` implementation includes:
- **Path Normalization**: Ensures consistent leading/trailing slash handling
- **Duplication Prevention**: Detects and prevents double-prefixing when paths already contain base prefix
- **Nested Group Support**: Proper prefix inheritance for complex routing scenarios
- **Full Path Resolution**: `FullPath()` method computes final resolved paths for debugging/logging

## Architecture Benefits

1. **Deployment Flexibility**: Applications can be deployed with different base paths without code changes
2. **Infrastructure Compatibility**: Custom health endpoints work with various load balancers and monitoring systems
3. **Clean Separation**: Health endpoints can be isolated from application routes or share the same prefix
4. **Zero Breaking Changes**: Existing modules work without modification due to interface compatibility
5. **Advanced Path Handling**: Intelligent duplication prevention and nested group support

## Configuration Examples

### Basic Configuration
```yaml
server:
  path:
    base: "/api/v1"
    health: "/health"
    ready: "/ready"
```

### Environment-Specific Configuration

```bash
# Development
SERVER_PATH_BASE="/dev/api"
SERVER_PATH_HEALTH="/health"

# Production
SERVER_PATH_BASE="/api/v1"
SERVER_PATH_HEALTH="/status"
SERVER_PATH_READY="/readiness"
```

### Route Resolution Examples

With `server.path.base: "/api/v1"`:
- Module route `/users` → `/api/v1/users`
- Health endpoint → `/api/v1/health` (or custom path)
- Ready endpoint → `/api/v1/ready` (or custom path)
- Nested group `/admin` + route `/stats` → `/api/v1/admin/stats`

## Consequences

### Positive
- **Enhanced Deployment Flexibility**: Single codebase works across multiple deployment scenarios
- **Better Infrastructure Integration**: Health endpoints can be customized for specific load balancer requirements
- **Improved Developer Experience**: Automatic base path inheritance eliminates manual prefix management
- **Future-Proof Design**: RouteRegistrar abstraction protects against Echo API changes
- **Smart Path Logic**: Prevents common pitfalls like double-prefixing in complex routing scenarios

### Negative
- **Increased Complexity**: Additional abstraction layer over Echo's routing
- **Breaking Change**: Module interface signature changes (mitigated by interface compatibility)
- **Configuration Overhead**: More configuration options to understand and manage

### Neutral
- **Learning Curve**: Developers need to understand the new RouteRegistrar abstraction
- **Testing Complexity**: Additional test scenarios for path resolution and nested groups

## Quality Assurance

### Testing Strategy
- **Unit Tests**: Path normalization, configuration validation, route resolution
- **Integration Tests**: End-to-end HTTP requests with various configurations
- **Backward Compatibility Tests**: Verify existing functionality remains unchanged
- **Edge Case Testing**: Double-prefixing prevention, nested groups, malformed paths

### Code Quality
- **Linting**: All code passes golangci-lint with zero issues
- **Race Detection**: Tests pass with `-race` flag
- **Performance**: Zero overhead when base path is empty
- **Documentation**: Comprehensive examples and API documentation
