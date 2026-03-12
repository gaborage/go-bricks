# ADR-013: Interface Naming Conventions (S8196)

**Date:** 2026-03-11
**Status:** Accepted

## Problem Statement

SonarCloud rule S8196 flags 5 public interfaces whose names either stutter with the package name or don't follow Go naming conventions. Go's official style guide recommends single-method interfaces end with `-er`, and that interface names should describe behavior, not repeat the package name.

Additionally, `cache.TenantCacheResourceSource` follows the same pattern as the database/messaging interfaces and is renamed for consistency.

## Decision

Rename 6 interfaces to follow Go idiomatic naming:

| Package | Old Name | New Name | Rationale |
|---------|----------|----------|-----------|
| `scheduler` | `Job` | `Executor` | Avoids ambiguity with `jobEntry` struct; describes behavior |
| `app` | `HealthProbe` | `Prober` | `-er` suffix for single-method interface per Go conventions |
| `database` | `TenantStore` | `DBConfigProvider` | Describes what it provides, not what it is |
| `messaging` | `TenantMessagingResourceSource` | `BrokerURLProvider` | Concise, describes the single method's purpose |
| `server` | `ResultLike` | `ResultMetaProvider` | Matches the method name `ResultMeta()` |
| `cache` | `TenantCacheResourceSource` | `ConfigProvider` | Avoids stutter (`cache.CacheConfigProvider`); consistent with database/messaging pattern |

## Implementation Details

### Changed
- Interface definitions in their respective packages
- All internal references (field types, parameters, type assertions, godoc)
- Composite `app.TenantStore` interface embedding updated names
- CLAUDE.md migration table added for downstream consumers

### Unchanged
- `config.TenantStore` (struct, not an interface — different type)
- `app.TenantStore` (composite interface name kept — it's the user-facing abstraction)
- All method signatures on the interfaces remain identical

## Consequences

### Positive
- Resolves 5 SonarCloud S8196 issues
- Aligns with Go community naming conventions
- More descriptive interface names improve code readability

### Negative
- Breaking change for any code that references the old interface names
- Downstream applications must update type assertions and variable declarations

## Migration Impact

Applications referencing old interface names must update:
1. `scheduler.Job` → `scheduler.Executor`
2. `app.HealthProbe` → `app.Prober`
3. `database.TenantStore` → `database.DBConfigProvider`
4. `messaging.TenantMessagingResourceSource` → `messaging.BrokerURLProvider`
5. `server.ResultLike` → `server.ResultMetaProvider`
6. `cache.TenantCacheResourceSource` → `cache.ConfigProvider`

## Related ADRs

- [ADR-007: Struct-Based Column Extraction](adr-007-struct-based-columns.md) — previous breaking change for type safety
- S8179 getter rename (documented in CLAUDE.md) — previous naming convention cleanup
