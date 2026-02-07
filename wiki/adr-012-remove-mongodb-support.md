# ADR-012: Remove MongoDB Support

**Date:** 2026-02-06
**Status:** Accepted

## Problem Statement

GoBricks supported three database vendors: PostgreSQL, Oracle, and MongoDB. MongoDB support was not being used in production applications, yet it imposed maintenance overhead across the codebase: a dedicated package (`database/mongodb/`), document-oriented interfaces (`DocumentInterface`, `DocumentCollection`, etc.), MongoDB-specific configuration validation, testcontainer integration, and vendor-specific code paths in the column parser, tracking, and query builder.

## Options Considered

### Option A: Keep MongoDB (Status Quo)
- **Pros:** Feature parity, potential future use
- **Cons:** Maintenance burden (~4,500 lines of unused code), additional dependency (`go.mongodb.org/mongo-driver/v2`), complexity in vendor-agnostic abstractions

### Option B: Deprecate MongoDB
- **Pros:** Gradual migration path
- **Cons:** Extended maintenance period, deprecation warnings add noise, no active users to migrate

### Option C: Remove MongoDB Entirely (Chosen)
- **Pros:** Immediate reduction in complexity, smaller dependency tree, clearer framework scope
- **Cons:** Breaking change for any hypothetical MongoDB users

## Decision

Remove MongoDB support entirely with no deprecation period. The framework focuses exclusively on PostgreSQL and Oracle — both relational SQL databases with similar query patterns. This alignment simplifies the `database.Interface` contract and eliminates the need for the parallel `DocumentInterface` abstraction.

## Implementation Details

### Removed
- `database/mongodb/` package (11 files, ~4,500 lines)
- `internal/database/document_interface.go` (~430 lines of document-oriented interfaces)
- `testing/containers/mongodb.go` (testcontainer helper)
- `MongoDB` vendor constant from `database/types`
- MongoDB panic case from SQL `QueryBuilder`
- MongoDB case from column parser vendor quoting
- `MongoConfig`, `ReplicaConfig`, `AuthConfig`, `ConcernConfig` from config types
- MongoDB validation functions from config package
- `go.mongodb.org/mongo-driver/v2` and `testcontainers-go/modules/mongodb` dependencies

### Unchanged
- `database.Interface` — MongoDB implemented it but the interface itself is generic
- `database/factory.go` — already only handled PostgreSQL and Oracle
- All PostgreSQL and Oracle functionality

## Consequences

### Positive
- ~5,000 lines of code removed
- Two fewer direct dependencies (`mongo-driver`, `testcontainers-go/modules/mongodb`)
- Simplified vendor abstraction (no document vs. SQL interface split)
- Faster CI (no MongoDB container pre-pull or integration tests)

### Negative
- Breaking change for any applications using `database/mongodb` package
- Breaking change for any applications using `DocumentInterface` type assertions

### Neutral
- The `normalizeDBVendor` function in tracking still handles unknown vendors gracefully

## Migration Impact

Applications using MongoDB with GoBricks must:
1. Remove `database.type: mongodb` from configuration files
2. Remove `DATABASE_MONGO_*` environment variables
3. Replace `database/mongodb` imports with a direct MongoDB driver dependency
4. Remove any `DocumentInterface` type assertions

## Related ADRs

- [ADR-003: Database by Intent Configuration](adr-003-database-by-intent.md) — database-only-when-configured principle
- [ADR-007: Struct-Based Column Extraction](adr-007-struct-based-columns.md) — column system now only handles PostgreSQL and Oracle
- [ADR-010: Panic-to-Error Conversion](adr-010-panic-to-error-conversion.md) — previously referenced MongoDB transactions
