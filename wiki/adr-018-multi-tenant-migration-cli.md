# ADR-018: Multi-Tenant Migration CLI

**Status:** Accepted
**Date:** 2026-05-09

## Context

Until v0.18.0, the `migration` package only knew how to migrate the single
database in `cfg.Database`. `migration.FlywayMigrator.RunMigrationsAtStartup`
was useful for first-run development environments, but it was not enough for
production multi-tenant deployments where:

- A modular monolith serves end customers; a separate back-office API handles
  tenant lifecycle.
- New-tenant creation already triggers Flyway against the freshly provisioned
  database, so brand-new tenants get the latest schema for free.
- A new release lands with N new migrations. CI/CD must apply those migrations
  to **every existing tenant** — there is no automated path for that today.

The user's requirements were specific:

1. The migration CLI fetches the tenant list from a control-plane API
   implemented by the back-office (or any external source) — not from a static
   YAML.
2. The control-plane API must conform to a **pre-defined contract** so any
   implementation can satisfy it.
3. Tenant database credentials must be retrieved from **AWS Secrets Manager**
   under a documented naming convention so ops can provision them
   independently from app code.

## Decision

Ship two pieces:

1. A new CLI binary, `tools/migration` (`go-bricks-migrate`), modeled on the
   existing `tools/openapi` cobra-based layout. Subcommands: `migrate`,
   `validate`, `info`, `list`, `version`.
2. A library entry point, `migration.MigrateAll(ctx, migrator, lister, configs,
   action, opts)`, so back-office code can run the same flow programmatically
   (e.g. as a release-time job triggered by an API call, in addition to the
   per-tenant-create flow that already exists).

The CLI is a thin wrapper around the library — both code paths exercise the
same `MigrateAll` core.

### Two extension points, deliberately decoupled

The framework defines two seams. Listing is new in this ADR; credential
resolution reuses the existing `database.DBConfigProvider` interface
(`database/manager.go`). Decoupling them means **the listing API never carries
secrets**.

```
                       ┌──────────────────────────┐
                       │   migration.MigrateAll   │
                       └────────────┬─────────────┘
              tenant IDs            │              DatabaseConfig
                ▲                   │                       ▲
   ┌────────────┴────────────┐      │      ┌────────────────┴────────────────┐
   │  migration.TenantLister │      │      │  database.DBConfigProvider      │
   └────────────┬────────────┘      │      │  (existing interface)           │
                │                   │      └────────────────┬────────────────┘
   ┌────────────┴───────────────┐   │           ┌───────────┴────────────┐
   │ HTTP source (this ADR)     │   │           │ migration.SecretsProvider  │
   │ ─────────────────────────  │   │           │  + SecretFetcher seam      │
   │ Standard APIResponse env.  │   │           └───────────┬────────────────┘
   └────────────────────────────┘   │                       │
   ┌────────────────────────────┐   │           ┌───────────┴────────────┐
   │ Static source (config YAML)│   │           │ AWS Secrets Manager    │
   └────────────────────────────┘   │           │ (CLI-only dep)         │
                                    │           └────────────────────────┘
```

### Listing contract (uses standard `APIResponse` envelope)

The contract is documented as the source of truth for all server
implementations. It uses the go-bricks standard `APIResponse` envelope
(`server/handler.go:35-47`) so a back-office Echo handler that returns the
shape via `server.GET(...)` gets the wrapping for free.

```
GET <base>/tenants?limit=<int>&cursor=<opaque>
Authorization: Bearer <optional>
Accept: application/json

200 OK
{
  "data": {
    "tenants":     [ { "id": "tenant-a" }, { "id": "tenant-b" } ],
    "next_cursor": "opaque-or-empty"
  },
  "meta": { "timestamp": "...", "traceId": "..." }
}

4xx / 5xx
{ "error": { "code": "...", "message": "..." }, "meta": { ... } }
```

- `id` is the only required field on each tenant object; additional fields are
  ignored (forward-compatible).
- `next_cursor` empty / null / absent ends iteration.
- `limit` is advisory; servers may cap. The CLI defaults to 100.
- Errors surface as `migration/source/http.ContractError` carrying the status
  code and `code`/`message` from the envelope's `.error` block.

### Credential convention (AWS Secrets Manager)

Default secret name:

```
gobricks/migrate/<tenant_id>
```

The prefix is configurable via `--secrets-prefix` (and
`GOBRICKS_MIGRATE_SECRETS_PREFIX`). Final secret name is
`prefix + tenant_id` — no template language, no normalization. Tenant IDs from
the listing API are used verbatim.

The secret payload supports two JSON shapes; canonical wins when both are
present:

| Shape       | Field set                                                    |
|-------------|--------------------------------------------------------------|
| Canonical   | `type`, `host`, `port`, `database`, `username`, `password`   |
| RDS rotation| `engine`, `host`, `port`, `dbname`, `username`, `password`   |

`engine` maps to `type` (`postgres`/`postgresql`/`aurora-postgresql` →
`postgresql`; `oracle`/`oracle-se2` → `oracle`).

Minimum IAM for the runner role:

```json
{
  "Effect":   "Allow",
  "Action":   "secretsmanager:GetSecretValue",
  "Resource": "arn:aws:secretsmanager:<region>:<account>:secret:gobricks/migrate/*"
}
```

### Why we ship a `SecretsProvider` adapter rather than an AWS SDK in the framework

`migration.SecretsProvider` implements `database.DBConfigProvider` on top of a
small `SecretFetcher` function type. The framework module (`migration`) stays
free of any cloud SDK. The AWS Secrets Manager client lives in the CLI's
separate go.mod (`tools/migration/internal/awssm/fetcher.go`). Library users
who want AWS SM in-process either depend on `tools/migration/internal/awssm`
explicitly, write a 30-line equivalent against the same `SecretFetcher` seam,
or plug in HashiCorp Vault / Google Secret Manager / etc. with the same
contract.

### Sequential, fail-fast by default

The CLI iterates tenants serially and stops at the first failure. Operators
who'd rather see all failures at once opt in via `--continue-on-error`. Bulk
fleets use `--parallel <N>` (capped at 32; combining `--parallel >1` with
fail-fast is not coherent, so the implementation forces continue-on-error in
that case).

### What we did **not** do (explicit non-goals for v1)

- We do **not** ship a server-side handler that serves the listing contract.
  The user explicitly chose to implement the back-office side themselves; the
  framework just publishes the contract spec.
- We do **not** ship Vault / Google Secret Manager fetchers in this PR. The
  `SecretFetcher` seam keeps that a 30-line follow-up.

## Consequences

**Pros:**
- Multi-tenant fleets get a documented, repeatable migration story.
- Library entry point lets the back-office reuse the same flow on tenant
  events without shelling out to the binary.
- `database.DBConfigProvider` is reused as-is — no new interface burden on
  apps that already implement it for runtime DB resolution.
- The framework module's dep graph stays AWS-free; only the CLI tool pulls
  `aws-sdk-go-v2`.

**Cons:**
- The pre-defined HTTP contract is now a published API surface and changes
  must be backwards-compatible (versioned via `/v2/tenants` if ever needed).
- Operators must provision AWS Secrets Manager entries with a documented JSON
  payload; misshapen secrets surface at run time as
  `migration.ErrSecretMalformed` rather than at startup.

## Migration

No breaking changes. Existing `FlywayMigrator.Migrate`/`Validate`/`Info`
signatures are preserved; the new `*For` variants are additive. Apps that
don't enable multi-tenancy keep working unchanged.

## References

- `migration/multi_tenant.go` — `TenantLister`, `MigrateAll`, options.
- `migration/secrets.go` — `SecretsProvider`, `SecretFetcher`, payload parser.
- `migration/source/http/source.go` — listing client (envelope-aware).
- `tools/migration/cmd/go-bricks-migrate` — CLI binary.
- `tools/migration/internal/awssm/fetcher.go` — AWS SM `SecretFetcher`.
- `wiki/multi-tenant-migration.md` — operator/developer guide.
