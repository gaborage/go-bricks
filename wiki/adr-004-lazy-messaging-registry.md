# ADR-004: Lazy Messaging Registry Creation in ModuleRegistry

**Date:** 2025-09-24
**Status:** Accepted
**Context:** Multi-tenant messaging architecture and improved dependency resolution

## Problem Statement

During the implementation of the multi-tenant architecture and the transition to function-based dependency resolution (`GetDB`/`GetMessaging` instead of direct field access), the ModuleRegistry faced an architectural challenge:

1. **Eager Creation Issues**: The original design assumed messaging registry could be created eagerly during ModuleRegistry initialization
2. **Function-Based Dependencies**: New API requires runtime context to resolve messaging client via `GetMessaging(ctx)`
3. **Timing Mismatch**: ModuleRegistry constructor doesn't have a context, but messaging registry creation requires one
4. **Test Failures**: Tests expected non-nil messaging registry after construction, but new API made this impossible

## Options Considered

### Option 1: Lazy Registry Creation (CHOSEN)
- Initialize messaging registry only when first needed in `RegisterMessaging()`
- Use singleflight protection to handle concurrent initialization
- Keep registry self-contained and not dependent on external initialization

### Option 2: Eager Creation with Context
- Require context parameter in `NewModuleRegistry(deps, ctx)`
- Create registry immediately during construction
- Break constructor signature compatibility

### Option 3: External Registry Management
- Remove messaging registry from ModuleRegistry entirely
- Require external code to create and pass registry to methods
- Increase API surface and complexity for consumers

## Decision

**We chose Option 1: Lazy Registry Creation** for the following reasons:

1. **Maintains Encapsulation**: Registry remains self-contained and manages its own lifecycle
2. **Backward Compatibility**: No breaking changes to public constructor API
3. **Context-Aware**: Defers messaging client resolution until context is available
4. **Thread-Safe**: Singleflight protection ensures safe concurrent initialization
5. **Fail-Safe**: Graceful handling when messaging is not configured

## Implementation Details

### Core Components

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

### Technical Decisions

1. **Singleflight Protection**: Uses `golang.org/x/sync/singleflight` to prevent duplicate initialization
2. **Error Handling**: Failed initialization is logged but doesn't crash the application
3. **Nil Checks**: Maintains existing nil checks in shutdown for backward compatibility
4. **Context Usage**: Uses `context.Background()` for infrastructure setup operations

## Consequences

### Positive
- **Maintains Encapsulation**: Registry manages its own lifecycle without external dependencies
- **Thread-Safe**: Concurrent calls to RegisterMessaging are safe
- **Context-Aware**: Can properly resolve context-dependent messaging clients
- **Graceful Degradation**: Applications without messaging work seamlessly
- **Future-Proof**: Architecture supports both single-tenant and multi-tenant modes

### Negative
- **Deferred Errors**: Messaging configuration errors appear later during RegisterMessaging
- **Additional Complexity**: Introduces singleflight dependency and lazy initialization pattern
- **State Management**: Registry state becomes more complex with initialization tracking

### Neutral
- **Performance**: Negligible impact, initialization happens once
- **Memory**: Minimal additional memory for singleflight.Group
- **Testing**: Tests needed updates but overall complexity remained similar

## Migration Impact

**Breaking Changes**: None - existing code continues to work
**Test Updates**: Tests expecting immediate registry creation updated to expect lazy creation
**Dependencies**: Added `golang.org/x/sync/singleflight` for concurrent initialization protection

## Quality Assurance

- **Thread Safety**: Singleflight ensures safe concurrent access
- **Error Handling**: Comprehensive error scenarios tested
- **Backward Compatibility**: All existing functionality preserved
- **Resource Management**: Proper cleanup in shutdown methods
- **Integration Testing**: Multi-tenant and single-tenant modes both validated

## Future Considerations

- **Metrics**: Consider adding initialization timing metrics
- **Logging**: Enhanced logging for registry lifecycle events
- **Context Propagation**: May benefit from explicit context threading in future versions
- **Health Checks**: Registry initialization status in health endpoints
