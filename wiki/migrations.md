# Breaking Change Migrations

Historical migration tables for upgrading existing GoBricks-based applications. Greenfield work can ignore this file — the new APIs are the only ones documented in CLAUDE.md.

## Go Naming Conventions (S8179) — Getter Methods

Per [SonarCloud rule S8179](https://rules.sonarsource.com/go/RSPEC-8179/), getter methods should NOT have the `Get` prefix.

| Package | Old Method | New Method |
|---------|------------|------------|
| `config.Config` | `GetString()`, `GetInt()`, `GetInt64()`, `GetFloat64()`, `GetBool()` | `String()`, `Int()`, `Int64()`, `Float64()`, `Bool()` |
| `config.Config` | `GetRequiredString()`, `GetRequiredInt()`, `GetRequiredInt64()`, `GetRequiredFloat64()`, `GetRequiredBool()` | `RequiredString()`, `RequiredInt()`, `RequiredInt64()`, `RequiredFloat64()`, `RequiredBool()` |
| `app.ResourceProvider` | `GetDB()`, `GetMessaging()`, `GetCache()` | `DB()`, `Messaging()`, `Cache()` |
| `app.ModuleDeps` | `GetDB`, `GetMessaging`, `GetCache` (fields) | `DB`, `Messaging`, `Cache` (fields) |
| `app.Builder` | `GetError()` | `Error()` |
| `messaging.Manager` | `GetPublisher()` | `Publisher()` |
| `server.Validator` | `GetValidator()` | `Validator()` |
| `validation.TagInfo` | `GetMin()`, `GetMax()`, `GetMinLength()`, `GetMaxLength()`, `GetPattern()`, `GetEnum()`, `GetConstraints()` | `Min()`, `Max()`, `MinLength()`, `MaxLength()`, `Pattern()`, `Enum()`, `AllConstraints()` |
| `migration.FlywayMigrator` | `GetDefaultMigrationConfig()` | `DefaultMigrationConfig()` |
| `config.TenantStore` | `GetTenants()` | `Tenants()` |
| `app.MetadataRegistry` | `GetModules()`, `GetModule()` | `Modules()`, `Module()` |
| `app.App` | `GetMessagingDeclarations()` | `MessagingDeclarations()` |
| `database.Interface` | `GetMigrationTable()` | `MigrationTable()` |
| `database/testing.TestDB` | `GetQueryLog()`, `GetExecLog()` | `QueryLog()`, `ExecLog()` |
| `database/testing.TenantDBMap` | `GetTenantDB()` | `TenantDB()` |
| `messaging.Registry` | `GetDeclarations()` | `Declarations()` |
| `server.RouteRegistry` | `GetRoutes()` | `Routes()` |

**Example:**
```go
// OLD
host := cfg.GetString("server.host", "0.0.0.0")
db, err := deps.GetDB(ctx)

// NEW
host := cfg.String("server.host", "0.0.0.0")
db, err := deps.DB(ctx)
```

## Interface Naming Conventions (S8196)

Per [SonarCloud rule S8196](https://rules.sonarsource.com/go/RSPEC-8196/) and [ADR-013](adr-013-interface-naming-conventions.md).

| Package | Old Interface | New Interface |
|---------|---------------|---------------|
| `scheduler` | `Job` | `Executor` |
| `app` | `HealthProbe` | `Prober` |
| `database` | `TenantStore` | `DBConfigProvider` |
| `messaging` | `TenantMessagingResourceSource` | `BrokerURLProvider` |
| `server` | `ResultLike` | `ResultMetaProvider` |
| `cache` | `TenantCacheResourceSource` | `ConfigProvider` |

## Standardized `ToSQL()` Across Query Builders (S8179)

Per [ADR-017](adr-017-insert-query-builder.md), `qb.Insert*` constructors return `types.InsertQueryBuilder` (a go-bricks-owned interface) instead of `squirrel.InsertBuilder` directly. The render method is renamed from `ToSql()` to `ToSQL()` — matching `Select`/`Update`/`Delete`.

| Constructor | Old return | New return | Render method |
|---|---|---|---|
| `qb.Insert(table)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertWithColumns(table, cols...)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertStruct(table, instance)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |
| `qb.InsertFields(table, instance, fields...)` | `squirrel.InsertBuilder` | `types.InsertQueryBuilder` | `ToSQL()` |

**Example:**
```go
// OLD
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSql()

// NEW
sql, args, err := qb.Insert("users").Columns("name").Values("Alice").ToSQL()
```

The new interface preserves all common chaining methods (`Columns`, `Values`, `SetMap`, `Options`, `Prefix`, `Suffix`, `Select`). For specialized squirrel-only methods (e.g., `RunWith`, `PlaceholderFormat`), keep the rendered SQL via `ToSQL()` and execute with `db.Exec(ctx, sql, args...)`.
