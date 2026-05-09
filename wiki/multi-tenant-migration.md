# Multi-Tenant Migration Guide

This guide explains how to roll out Flyway migrations to every tenant in a
go-bricks deployment using the `go-bricks-migrate` CLI or the
`migration.MigrateAll` library entry point. Background and design rationale
live in [ADR-018](adr-018-multi-tenant-migration-cli.md).

## Architecture at a glance

```text
┌─────────────┐    list IDs       ┌─────────────────────┐
│ CI/CD job   │───────────────────▶│ go-bricks-migrate   │
└─────────────┘                   └─────────┬───────────┘
                                            │
                  ┌─────────────────────────┼─────────────────────────┐
                  ▼                         ▼                         ▼
   ┌──────────────────────────┐  ┌─────────────────────┐  ┌────────────────────────┐
   │ Control-plane API        │  │ AWS Secrets Manager │  │ Flyway CLI             │
   │ GET /tenants (envelope)  │  │ gobricks/migrate/<id│  │ flyway migrate ...     │
   └──────────────────────────┘  └─────────────────────┘  └────────────────────────┘
```

For each tenant ID returned by the control-plane API, the CLI fetches the
matching secret, parses the credentials, and runs Flyway against the tenant
database.

## Pre-defined HTTP listing contract

Implement this on your back-office or any control-plane service. The shape
matches the standard go-bricks `APIResponse` envelope so you can serve it
with a normal `server.GET(handlerRegistry, e, "/tenants", h.listTenants)`.

```text
GET <base>/tenants?limit=<int>&cursor=<opaque>
Authorization: Bearer <optional>

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

Rules:
- `id` is the only required field per tenant; extra fields are ignored.
- Empty/absent `next_cursor` ends iteration.
- `limit` is advisory; the CLI defaults to 100.
- `Authorization: Bearer ...` is sent when `--source-token` (or the
  `GOBRICKS_MIGRATE_SOURCE_TOKEN` env var) is set.

### Reference handler (Go/Echo, on top of go-bricks)

```go
type ListTenantsReq struct {
    Limit  int    `query:"limit"  validate:"omitempty,min=1,max=500"`
    Cursor string `query:"cursor" validate:"omitempty"`
}

type Tenant struct {
    ID string `json:"id"`
}

type ListTenantsResp struct {
    Tenants    []Tenant `json:"tenants"`
    NextCursor string   `json:"next_cursor"`
}

func (h *Handler) listTenants(req ListTenantsReq, ctx server.HandlerContext) (server.Result[ListTenantsResp], server.IAPIError) {
    ids, next, err := h.tenants.Page(ctx.Echo.Request().Context(), req.Limit, req.Cursor)
    if err != nil {
        return server.Result[ListTenantsResp]{}, server.NewInternalServerError(err.Error())
    }
    out := ListTenantsResp{NextCursor: next, Tenants: make([]Tenant, 0, len(ids))}
    for _, id := range ids {
        out.Tenants = append(out.Tenants, Tenant{ID: id})
    }
    return server.NewResult(http.StatusOK, out), nil
}
```

## AWS Secrets Manager convention

Default secret name (configurable via `--secrets-prefix`):

```text
gobricks/migrate/<tenant_id>
```

Secret payload — canonical shape (preferred):

```json
{
  "type":     "postgresql",
  "host":     "tenant-a.db.example.com",
  "port":     5432,
  "database": "tenant_a",
  "username": "tenant_a_app",
  "password": "..."
}
```

RDS rotation fallback (existing AWS-managed secrets work as-is):

```json
{
  "engine":   "postgres",
  "host":     "...",
  "port":     5432,
  "dbname":   "...",
  "username": "...",
  "password": "..."
}
```

Engine normalization: `postgres`/`postgresql`/`aurora-postgresql` →
`postgresql`; `oracle`/`oracle-se2`/`oracle-ee` → `oracle`.

Minimum IAM for the runner role:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect":   "Allow",
      "Action":   "secretsmanager:GetSecretValue",
      "Resource": "arn:aws:secretsmanager:<region>:<account>:secret:gobricks/migrate/*"
    }
  ]
}
```

## Installing the CLI

```bash
cd tools/migration
make install
go-bricks-migrate version
```

## CLI usage

```bash
# Migrate every tenant returned by the control-plane API
go-bricks-migrate migrate \
  --source-url https://control-plane.example.com/api \
  --source-token "$GOBRICKS_MIGRATE_SOURCE_TOKEN" \
  --secrets-prefix gobricks/migrate/ \
  --aws-region us-east-1 \
  --flyway-config flyway/flyway-postgresql.conf \
  --migrations-dir migrations/postgresql

# Validate without applying (CI gate before merging migrations)
go-bricks-migrate validate \
  --source-url https://control-plane.example.com/api \
  --aws-region us-east-1

# Inspect status across the fleet
go-bricks-migrate info \
  --source-url https://control-plane.example.com/api \
  --aws-region us-east-1

# Smoke-test the listing endpoint without touching credentials or DBs
go-bricks-migrate list \
  --source-url https://control-plane.example.com/api

# Run for one tenant (debugging, manual remediation)
go-bricks-migrate migrate \
  --tenant tenant-a \
  --aws-region us-east-1

# JSON progress for CI/CD log parsing
go-bricks-migrate migrate \
  --source-url https://control-plane.example.com/api \
  --aws-region us-east-1 \
  --json
```

### Flag reference

| Flag                     | Default                | Description                                         |
|--------------------------|------------------------|-----------------------------------------------------|
| `--source-url`           |                        | Control-plane base URL (required for fleet runs)    |
| `--source-token`         | `$GOBRICKS_MIGRATE_SOURCE_TOKEN` | Bearer token for the control-plane API   |
| `--source-config`        |                        | YAML file with `multitenant.tenants` (dev fallback) |
| `--secrets-prefix`       | `gobricks/migrate/`    | Secret-name prefix (final = prefix + tenant_id)     |
| `--aws-region`           | `$AWS_REGION`          | AWS region                                          |
| `--aws-profile`          | `$AWS_PROFILE`         | AWS profile                                         |
| `--aws-endpoint`         |                        | LocalStack / private VPC endpoint override          |
| `--credentials-from`     | `aws-secrets-manager`  | `aws-secrets-manager` or `config-file`              |
| `--flyway-path`          | `flyway`               | Flyway executable                                   |
| `--flyway-config`        | (per-vendor default)   | `flyway.conf` path                                  |
| `--migrations-dir`       | (per-vendor default)   | Migrations directory                                |
| `--continue-on-error`    | `false`                | Don't stop after the first per-tenant failure       |
| `--parallel <N>`         | `1`                    | Concurrent tenants (1 = sequential, max 32)         |
| `--tenant <id>`          |                        | Run for a single tenant; bypasses listing           |
| `--json`                 | `false`                | NDJSON progress + summary records                   |

## CI/CD recipe (GitHub Actions, OIDC → AWS)

```yaml
name: migrate-tenants
on:
  push:
    branches: [main]
    paths:
      - 'migrations/**'

permissions:
  id-token: write   # OIDC for AWS auth
  contents: read

jobs:
  migrate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789012:role/gobricks-migrate
          aws-region: us-east-1

      - name: Install Flyway
        run: |
          curl -L https://repo1.maven.org/maven2/org/flywaydb/flyway-commandline/10.x/flyway-commandline-10.x-linux-x64.tar.gz \
            | tar xz
          echo "$PWD/flyway-10.x" >> "$GITHUB_PATH"

      - name: Build go-bricks-migrate
        run: |
          cd tools/migration
          go build -o ../../go-bricks-migrate ./cmd/go-bricks-migrate

      - name: Apply migrations to every tenant
        env:
          GOBRICKS_MIGRATE_SOURCE_TOKEN: ${{ secrets.CONTROL_PLANE_TOKEN }}
        run: |
          ./go-bricks-migrate migrate \
            --source-url https://control-plane.example.com/api \
            --aws-region us-east-1 \
            --flyway-config flyway/flyway-postgresql.conf \
            --migrations-dir migrations/postgresql \
            --json
```

## Library usage (in-process from your back-office)

The CLI's AWS Secrets Manager wrapper at `tools/migration/internal/awssm/`
lives under `internal/` and is not importable. Library callers implement the
`migration.SecretFetcher` function type themselves — typically a 30-line
adapter over the AWS SDK (or HashiCorp Vault, GCP Secret Manager, etc.):

```go
import (
    "context"
    "fmt"
    "os"

    "github.com/aws/aws-sdk-go-v2/aws"
    awsconfig "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/secretsmanager"

    "github.com/gaborage/go-bricks/migration"
    httpsource "github.com/gaborage/go-bricks/migration/source/http"
)

// awsSecretFetcher is a public-API equivalent of the CLI's awssm package.
func awsSecretFetcher(ctx context.Context, region string) (migration.SecretFetcher, error) {
    awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
    if err != nil {
        return nil, err
    }
    sm := secretsmanager.NewFromConfig(awsCfg)
    return func(ctx context.Context, name string) ([]byte, error) {
        out, err := sm.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
            SecretId: aws.String(name),
        })
        if err != nil {
            return nil, fmt.Errorf("get secret %q: %w", name, err)
        }
        if out.SecretString != nil {
            return []byte(*out.SecretString), nil
        }
        return out.SecretBinary, nil
    }, nil
}

func RunReleaseMigrations(ctx context.Context) error {
    lister, err := httpsource.New("https://control-plane.example.com/api", httpsource.Options{
        BearerToken: os.Getenv("CONTROL_PLANE_TOKEN"),
    })
    if err != nil { return err }

    fetcher, err := awsSecretFetcher(ctx, "us-east-1")
    if err != nil { return err }

    provider := &migration.SecretsProvider{Fetch: fetcher}
    if err := provider.Validate(); err != nil { return err }

    fm := migration.NewFlywayMigrator(myCfg, myLogger)

    res, err := migration.MigrateAll(ctx, fm, lister, provider, migration.ActionMigrate, migration.MigrateAllOptions{
        Logger: myLogger,
        Hook: func(r migration.TenantResult) {
            myLogger.Info().Str("tenant", r.TenantID).Dur("dur", r.Duration).Msg("tenant migrated")
        },
    })
    if err != nil { return err }
    if failed := res.Failed(); len(failed) > 0 {
        return fmt.Errorf("%d tenants failed", len(failed))
    }
    return nil
}
```

### Plugging in a non-AWS secret store

`migration.SecretFetcher` is a function type. Wire any store you already use:

```go
provider := &migration.SecretsProvider{
    Prefix: "vault/migrate/",
    Fetch: func(ctx context.Context, name string) ([]byte, error) {
        return myVaultClient.ReadJSON(ctx, name)
    },
}
```

## Operational notes

- **Idempotency**: Flyway tracks applied migrations in
  `flyway_schema_history`. Re-running `migrate` is safe.
- **Partial failures**: Default fail-fast halts after the first error so the
  failure is obvious. Use `--continue-on-error` when you'd rather see every
  failure at once (e.g., to triage).
- **Backwards compatibility**: The contract is versioned implicitly via the
  base URL. Breaking changes will be served at `/v2/tenants` with the existing
  `/tenants` kept on the v1 shape.
- **Observability**: ERROR-level lines from the CLI include the failing
  tenant ID and the resolved secret name to help with triage.
