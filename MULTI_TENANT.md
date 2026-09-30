# Multi-Tenant Implementation

GoBricks provides comprehensive multi-tenant support through tenant-specific database and messaging resolution with complete resource isolation.

## Architecture Overview

GoBricks implements multi-tenancy through several key components:

1. **Tenant Resolution**: Extract tenant ID from requests using headers, subdomains, composite strategies, or custom logic with built-in validation support.
2. **Resource Isolation**: Each tenant gets isolated database and messaging connections with complete separation.
3. **Context Propagation**: Tenant ID flows through request context automatically across all framework components.
4. **Function-Based Injection**: Database and messaging resources are resolved via functions in `ModuleDeps`, enabling lazy per-tenant connection management.
5. **Declaration Replay**: Messaging infrastructure declarations are validated once and replayed to per-tenant registries, ensuring isolated messaging topologies.

## Configuration Setup

Configure multi-tenancy in your `config.yaml`:

```yaml
multitenant:
  enabled: true
  resolver:
    type: "header"           # or "subdomain", "path", "composite"
    header: "X-Tenant-ID"   # header name for tenant resolution
    domain: "api.example.com" # root domain for subdomain resolution
    proxies: true           # trust X-Forwarded-Host headers
    # order: [subdomain, path, header]  # resolver.order REQUIRED for type: composite — no default; startup fails without it (ADR-039)
  limits:
    tenants: 100           # maximum number of tenants
  tenants:
    tenant1:
      database:
        type: "postgresql"
        host: "tenant1-db.example.com"
        port: 5432
        database: "tenant1_db"
        username: "tenant1_user"
        password: "secure_pass_1"
      messaging:
        url: "amqp://tenant1:pass@tenant1-rabbitmq.example.com:5672/"
    tenant2:
      database:
        type: "postgresql"
        host: "tenant2-db.example.com"
        port: 5432
        database: "tenant2_db"
        username: "tenant2_user"
        password: "secure_pass_2"
      messaging:
        url: "amqp://tenant2:pass@tenant2-rabbitmq.example.com:5672/"
```

## Custom Tenant Store Implementation

Implement the `app.TenantStore` interface to integrate with external systems like AWS Secrets Manager, HashiCorp Vault, or custom databases. The interface is composed of `database.DBConfigProvider`, `messaging.BrokerURLProvider`, and `cache.ConfigProvider` plus `IsDynamic()`, so it requires implementing four methods:

- `DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error)` - Returns database configuration for a specific tenant
- `BrokerURL(ctx context.Context, key string) (string, error)` - Returns the tenant's messaging broker connection URL
- `CacheConfig(ctx context.Context, key string) (*config.CacheConfig, error)` - Returns the tenant's cache configuration (`config.NewNotConfiguredError` when no cache is configured; never nil with a nil error)
- `IsDynamic() bool` - Returns true if the store resolves configuration at runtime from an external source, false if it serves static configuration

`IsDynamic()` must agree with `source.type`: return `true` exactly when `source.type: dynamic` is set. The app build refuses a disagreement, including `source.type: dynamic` with no `app.Options.ResourceSource` at all, before any resource is dialed ([ADR-125](wiki/adr_125_source_type_agrees_with_resource_source.md)). `IsDynamic()` also decides whether the build consults the store for the control-plane key `""` ([ADR-127](wiki/adr_127_resource_plan_rule.md)). A static store (`false`) is asked once per kind, in every mode — `DBConfig`, `BrokerURL` and `CacheConfig` for `""`, each a config lookup under its `app.startup.<kind>` budget — so answer `""` with a configuration when the store serves it and with `config.NewNotConfiguredError` when it does not: any other error, or a lookup that outlasts its budget, fails startup naming the kind and the key. Where a kind resolves on `""` (single-tenant, and messaging under `messaging.tenancy: shared`), a served `""` is pre-initialized at build, and a not-configured one makes the kind unavailable: its `ModuleDeps.*Configured` flag reads false, the database emits the absence WARN and fails an `app.DatabaseRequirer`, and messaging declarations fail startup. Messaging under `messaging.tenancy: per-tenant` is unavailable the same way when there is no `app.Options.ResourceSource` and static tenants exist but none sets `messaging.url`: `MessagingConfigured` reads false, a per-tenant-ledger outbox refuses `Init`, and declarations fail startup naming `multitenant.tenants.<id>.messaging.url`. The build cannot enumerate a caller store's tenants, so behind one the flag stays true ([ADR-128](wiki/adr_128_outbox_broker_check_reads_the_resource_plan.md)). A dynamic store (`true`) is never asked at build, so nothing is pre-initialized and nothing is unavailable, and the outbox's #366 broker check takes the same answer from `ModuleDeps` (`MessagingConfigured`, `ControlPlaneMessagingAbsent`), never from `source.type` ([ADR-128](wiki/adr_128_outbox_broker_check_reads_the_resource_plan.md)); the outbox and inbox still read `source.type: dynamic` to skip their startup database probe. An outbox or inbox fanning out per tenant rejects `source.type: dynamic` and enumerates the static `multitenant.tenants` keys, so behind one keep `source.type: static`, list the tenant IDs, and have the store return `false` from `IsDynamic()`, even when it resolves each tenant's configuration from an external source. The flag does not control caching or refresh: the framework pools connections per key either way.

## Tenant Resolution Strategies

The framework provides multiple built-in tenant resolution strategies:

- **HeaderResolver**: Extracts tenant ID from HTTP headers (e.g., `X-Tenant-ID`)
- **SubdomainResolver**: Derives tenant ID from request host subdomains with proxy support
- **PathResolver**: Extracts tenant ID from a 1-indexed URL path segment, with an optional prefix gate (e.g. `/itsp/{tenantID}/...`)
- **CompositeResolver**: Tries multiple resolvers sequentially until one succeeds, with optional regex validation. `resolver.order` is **required** — there is no default chain, and a composite resolver without it fails at startup (ADR-039; see [wiki/multi_tenant_resolvers.md](wiki/multi_tenant_resolvers.md)).
- **ValidatingResolver**: Wraps any resolver with tenant ID format validation using regex patterns

All resolvers support context propagation and integrate seamlessly with the middleware layer.

## Multi-Tenant Module Implementation

Modules access tenant-specific resources through function-based injection in `ModuleDeps`:

- `DB(ctx context.Context)` - Returns database connection for the tenant in the context
- `Messaging(ctx context.Context)` - Returns messaging client for the tenant in the context

The framework automatically resolves the tenant ID from the request context and provides the appropriate isolated resources. Messaging declarations made in `DeclareMessaging()` are validated once at startup and replayed to each tenant's registry, ensuring complete infrastructure isolation.

## Benefits

1. **Complete Isolation**: Each tenant gets isolated database and messaging resources with separate connection pools
2. **Context-Driven**: Tenant ID flows automatically through request context across all framework components
3. **Extensible**: Custom resource stores can integrate with any external system (AWS, HashiCorp, custom databases)
4. **Type-Safe**: Compile-time guarantees for tenant resolution functions and resource access
5. **Performance**: Built-in caching and connection pooling for tenant resources with lazy initialization
6. **Security**: Automatic tenant validation, configurable ID constraints via regex, and isolated infrastructure
7. **Declaration Replay**: Messaging infrastructure is validated once and replayed per-tenant, preventing configuration drift

This architecture enables scalable multi-tenant applications while maintaining strict isolation between tenants and providing flexibility for custom resource management strategies.

## Examples

See the [multitenant-aws example](https://github.com/gaborage/go-bricks-demo-project/tree/main/multitenant-aws) for a complete implementation with AWS Secrets Manager integration.
