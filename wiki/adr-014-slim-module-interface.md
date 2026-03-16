# ADR-014: Slim Module Interface + Remove Stutter

**Status:** Accepted
**Date:** 2026-03-16

## Context

The `app.Module` interface required 5 methods (`Name`, `Init`, `RegisterRoutes`, `DeclareMessaging`, `Shutdown`), but several framework modules had no-op implementations for `RegisterRoutes` and/or `DeclareMessaging`:

- **outbox**: Both `RegisterRoutes` and `DeclareMessaging` were no-ops
- **keystore**: Both `RegisterRoutes` and `DeclareMessaging` were no-ops
- **scheduler**: `DeclareMessaging` was a no-op (but `RegisterRoutes` is real — it registers `/_sys/job` endpoints)

This violated the Interface Segregation Principle (ISP). Additionally, all three framework modules had stuttered type names (`outbox.OutboxModule`, `scheduler.SchedulerModule`, `keystore.KeystoreModule`) suppressed with `//nolint:revive` comments.

## Decision

### 1. Slim the Module interface to 3 core methods

```go
type Module interface {
    Name() string
    Init(deps *ModuleDeps) error
    Shutdown() error
}
```

### 2. Add optional interfaces via Go duck typing

```go
type RouteRegisterer interface {
    RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar)
}

type MessagingDeclarer interface {
    DeclareMessaging(decls *messaging.Declarations)
}
```

The `ModuleRegistry` type-asserts each module before calling these methods. Modules without the interface are silently skipped — the same pattern already used for `JobProvider`, `OutboxProvider`, and `KeyStoreProvider`.

### 3. Remove stutter from framework module names

| Package | Old Type | New Type | Old Constructor | New Constructor |
|---------|----------|----------|-----------------|-----------------|
| `outbox` | `OutboxModule` | `Module` | `NewOutboxModule()` | `NewModule()` |
| `scheduler` | `SchedulerModule` | `Module` | `NewSchedulerModule()` | `NewModule()` |
| `keystore` | `KeystoreModule` | `Module` | `NewKeystoreModule()` | `NewModule()` |

## Consequences

### Breaking Changes

1. **Constructor renames**: `outbox.NewOutboxModule()` → `outbox.NewModule()`, etc.
2. **Module interface slimmed**: Existing 5-method implementations still compile (superset satisfies subset).
3. **New optional interfaces**: Modules with `RegisterRoutes`/`DeclareMessaging` auto-satisfy via duck typing.

### Migration

```go
// ❌ OLD
fw.RegisterModules(
    scheduler.NewSchedulerModule(),
    outbox.NewOutboxModule(),
    keystore.NewKeystoreModule(),
    &myapp.OrderModule{},
)

// ✅ NEW
fw.RegisterModules(
    scheduler.NewModule(),
    outbox.NewModule(),
    keystore.NewModule(),
    &myapp.OrderModule{},
)
```

Application modules that implement all 5 methods continue to work without changes — they naturally satisfy both the core `Module` interface and the optional `RouteRegisterer`/`MessagingDeclarer` interfaces.

### Benefits

- **ISP compliance**: Modules only implement what they need
- **No more `//nolint:revive`**: Eliminated 3 suppressed stutter warnings
- **Cleaner framework modules**: Removed 5 no-op methods across outbox, keystore, and scheduler
- **Consistent pattern**: Matches existing `JobProvider`, `OutboxProvider`, `KeyStoreProvider` duck typing
