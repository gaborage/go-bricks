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
    type: "header"           # or "subdomain", "composite"
    header: "X-Tenant-ID"   # header name for tenant resolution
    domain: "api.example.com" # root domain for subdomain resolution
    proxies: true           # trust X-Forwarded-Host headers
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

Implement the `app.TenantStore` interface to integrate with external systems like AWS Secrets Manager, HashiCorp Vault, or custom databases. The interface requires implementing three methods:

- `DBConfig(ctx context.Context, key string) (*config.DatabaseConfig, error)` - Returns database configuration for a specific tenant
- `AMQPURL(ctx context.Context, key string) (string, error)` - Returns AMQP connection URL for a specific tenant
- `IsDynamic() bool` - Returns true if tenant configuration can change at runtime (dynamic) or false if static

The `IsDynamic()` flag controls framework behavior for caching and configuration refresh. Dynamic stores (external sources like AWS Secrets Manager) return `true`, while static stores (YAML-based) return `false`. The framework handles connection pooling, caching, and lifecycle management automatically once the store implementation returns the configuration.

## Tenant Resolution Strategies

The framework provides multiple built-in tenant resolution strategies:

- **HeaderResolver**: Extracts tenant ID from HTTP headers (e.g., `X-Tenant-ID`)
- **SubdomainResolver**: Derives tenant ID from request host subdomains with proxy support
- **CompositeResolver**: Tries multiple resolvers sequentially until one succeeds, with optional regex validation
- **ValidatingResolver**: Wraps any resolver with tenant ID format validation using regex patterns

All resolvers support context propagation and integrate seamlessly with the middleware layer.

## Multi-Tenant Module Implementation

Modules access tenant-specific resources through function-based injection in `ModuleDeps`:

- `GetDB(ctx context.Context)` - Returns database connection for the tenant in the context
- `GetMessaging(ctx context.Context)` - Returns messaging client for the tenant in the context

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
