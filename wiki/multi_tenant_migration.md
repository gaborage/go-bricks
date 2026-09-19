# Multi-Tenant Migration Guide

This guide explains how to roll out Flyway migrations to every tenant in a
go-bricks deployment using the `go-bricks-migrate` CLI or the
`migration.MigrateAll` library entry point. Background and design rationale
live in [ADR-018](adr_018_multi_tenant_migration_cli.md).

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
    ids, next, err := h.tenants.Page(ctx.RequestContext(), req.Limit, req.Cursor)
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

The payloads in this guide carry the tenant's runtime role (`tenant_a_app`) and
assume a [migrator identity](#migrator-identity) is set, so Flyway connects as
the migrator instead — `MigrateAllOptions.MigratorIdentity` in process, or
`GOBRICKS_MIGRATE_MIGRATOR_USER` / `GOBRICKS_MIGRATE_MIGRATOR_PASSWORD` on
`go-bricks-migrate`. Without one, Flyway connects with each tenant secret's
`username` *and* `password` as stored, so the fleet must carry the shared
migrator's own username and password in every tenant secret; swapping in the
migrator's username alone authenticates it with the runtime role's password, and
a secret left on the runtime role connects without DDL rights on the tenant
schema.

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

## Migrator identity

`MigrateAllOptions.MigratorIdentity` makes Flyway connect as one shared migrator
role across the fleet (see [library usage](#library-usage-in-process-from-your-back-office)).
For each tenant, `MigrateAll` copies the resolved `DatabaseConfig` and replaces
only `username` and `password`; host, port, database, schema targeting and TLS
stay the tenant's, and the provider's own value is never mutated. It applies to
migrate, validate and info on PostgreSQL and Oracle. An empty username or
password, a password shorter than `config.MinDatabasePasswordLength` (too short
to redact from Flyway output), or a CR, LF or NUL in either fails `MigrateAll`
with `migration.ErrInvalidMigratorIdentity` before any tenant is listed. The role-separation model in
[migration_roles.md](migration_roles.md) needs the overlay. `go-bricks-migrate`
reads it from `GOBRICKS_MIGRATE_MIGRATOR_USER` and
`GOBRICKS_MIGRATE_MIGRATOR_PASSWORD` — both or neither, and no flag carries the
password.

Its pair is [`WithSharedMigrator`](#schema-targeting-postgresql). Setting
`MigratorIdentity` does **not** arm that guard: database-per-tenant PostgreSQL
with one migrator role across every database and the target schema (typically
`public`) in each is a legitimate deployment where the role-level `search_path`
is correct everywhere, so inferring the requirement here would break real
setups.

The overlay presents one credential with DDL rights on every tenant schema to
every tenant's host, so a tenant document naming a wrong or hostile host exposes
that shared credential rather than one tenant's. Keep tenant host fields under
the same control as the secret store, and set `tls.mode: verify-full` on
PostgreSQL tenant documents so Flyway verifies the server before authenticating.

## Schema targeting (PostgreSQL)

When the target `DatabaseConfig` carries a non-empty `postgresql.schema`, the
runner passes `-schemas=<schema> -defaultSchema=<schema>` to every Flyway
invocation (migrate, info, validate). Flyway CLI args override any
`flyway.schemas` in a shared conf file, so one conf cannot misroute a tenant —
the schema on the tenant's `DatabaseConfig` always wins, and
`flyway_schema_history` is created inside that same target schema. This is what
makes schema-per-tenant topologies safe: without it, every tenant's tables land
wherever the connection's `search_path` resolves (typically `public`) and report
success regardless.

The per-tenant secret already carries the canonical `DatabaseConfig` shape, so
targeting a schema is the whole wiring — add a `postgresql` block to the secret:

```json
{
  "type":     "postgresql",
  "host":     "tenant-a.db.example.com",
  "port":     5432,
  "database": "tenant_a",
  "username": "tenant_a_app",
  "password": "...",
  "postgresql": { "schema": "tenant_a" }
}
```

An empty schema keeps legacy behavior unchanged **by default** — the conf file
or the connection's `search_path` decides where migrations land. A migrator
shared across tenants has no role-level `search_path` to fall back on (see
[Shared and out-of-band migrators](migration_roles.md#shared-and-out-of-band-migrators)),
so build that runner with `migration.NewFlywayMigrator(cfg, log).WithSharedMigrator()`:
an empty `postgresql.schema` is then refused with `ErrSharedMigratorSchemaRequired`
before Flyway runs, for `migrate`, `validate` and `info` alike, rather than
silently targeting `public` and reporting success. The flag is not limited to a
shared role — a hand-provisioned migrator, a partially applied
`PGRoleProvisioningSQL` script, or `search_path` drift under `SkipFloorReassert`
leave the same gap, and it is library-only today (see
[migration_roles.md](migration_roles.md#the-model) for the CLI caveat).

The guard is deliberately fail-closed on one legitimate shape: a `flyway.conf`
owning `flyway.defaultSchema` also aims a run, but the framework never reads
`flyway.conf` (the same boundary `ErrIncompleteMigrationTarget` draws), so it
cannot see that target and refuses anyway. Under this flag,
`postgresql.schema` is what must carry the target. Note this koanf key
(`database.postgresql.schema`) now has two consumers: the observability
namespace and this migration-targeting path.

Under `MigrateAll` the refusal is per tenant: it lands as that tenant's
`TenantResult.Err`, whose `TenantID` names it, and `res.Verdict()` is
`ErrFleetSplit` — a refused tenant was dispatched and failed, and
[ADR-115](adr_115_fleet_migration_run_verdict.md) classifies strictly by
dispatch, with no "skipped" state. Fix the tenant's `postgresql.schema` and
re-run.

Schema names must match `^[A-Za-z_][A-Za-z0-9_$]*$` within 63 bytes; an invalid name fails
fast with `ErrInvalidPGIdentifier` before Flyway runs (the value is formatted
into subprocess argv, and `-schemas` is comma-separated, so an unvalidated name
could smuggle a second schema). Oracle is not applicable — its schema is the
connecting user, which is already per-tenant.

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

# Manage the deployment quiesce flag (pauses worker pickup and tenant fan-out)
go-bricks-migrate quiesce set    --source-url https://control-plane.example.com/api
go-bricks-migrate quiesce status --source-url https://control-plane.example.com/api
go-bricks-migrate quiesce clear  --source-url https://control-plane.example.com/api
```

> See [wiki/migration_quiesce.md](migration_quiesce.md) for the full `quiesce set|clear|status` subcommand reference, TTL auto-release, and fail-open behaviour.

### Flag reference

| Flag | Default | Description |
| --- | --- | --- |
| `--source-url` | | Control-plane base URL (required for fleet runs) |
| `--source-token` | `$GOBRICKS_MIGRATE_SOURCE_TOKEN` | Bearer token for the control-plane API |
| `--source-config` | | YAML file with `multitenant.tenants` (dev fallback) |
| `--secrets-prefix` | `gobricks/migrate/` | Secret-name prefix (final = prefix + tenant_id) |
| `--aws-region` | `$AWS_REGION` | AWS region |
| `--aws-profile` | `$AWS_PROFILE` | AWS profile |
| `--aws-endpoint` | | LocalStack / private VPC endpoint override |
| `--credentials-from` | `aws-secrets-manager` | `aws-secrets-manager` or `config-file` |
| `--flyway-path` | `flyway` | Flyway executable |
| `--flyway-config` | (per-vendor default) | `flyway.conf` path |
| `--migrations-dir` | (per-vendor default) | Migrations directory |
| `--continue-on-error` | `false` | Don't stop after the first per-tenant failure |
| `--parallel <N>` | `1` | Concurrent tenants (1 = sequential, max 32) |
| `--tenant <id>` | | Run for a single tenant; bypasses listing |
| `--json` | `false` | NDJSON progress + summary records |
| `--applied-by` | `$GOBRICKS_MIGRATE_APPLIED_BY` | Principal recorded in `migration.applied` audit events |
| `--git-sha` | `$GOBRICKS_MIGRATE_GIT_SHA` | Source commit SHA recorded in the audit event |
| `--pipeline-run-id` | `$GOBRICKS_MIGRATE_PIPELINE_RUN_ID` | CI/CD run ID recorded in the audit event |
| `--allow-insecure-scheme` | `false` | Allow `http://` base URLs for `--source-url` (dev/LocalStack only; bearer token would be cleartext) |
| `--verbose` / `-v` | `false` | Enable debug-level logging |
| `--timeout` | `0` (vendor default, 5m) | Per-tenant Flyway timeout override (e.g. `30m`); raise for large index builds/backfills |

The [migrator identity](#migrator-identity) has no flag, because no flag carries
a password. Set both `GOBRICKS_MIGRATE_MIGRATOR_USER` and
`GOBRICKS_MIGRATE_MIGRATOR_PASSWORD`, or neither; exactly one set is a startup
error naming the missing variable, and a set-but-empty value fails with
`migration.ErrInvalidMigratorIdentity` before any tenant is listed. It applies to
`migrate`, `validate` and `info`; `quiesce` opens its control plane with the
tenant secret's own credentials and ignores both variables.

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
      - uses: actions/checkout@v7

      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::123456789012:role/gobricks-migrate
          aws-region: us-east-1

      - name: Install Flyway
        run: |
          set -euo pipefail
          # Flyway 13.x is distributed from Redgate's server — Maven Central
          # stopped publishing the self-contained -linux-x64.tar.gz mid-11.x.
          # Pin the SHA256 and verify before extracting (fail-closed) so a
          # tampered or swapped tarball aborts the job — never pipe curl|tar.
          # Keep in sync with .github/workflows/ci-v2.yml (source of truth for these pins).
          FLYWAY_VERSION=13.7.0
          FLYWAY_SHA256=b9ac9846e6d2254bc045168ee080d56df3d3ff52b865aea53c18fb305b1145ba
          url="https://download.red-gate.com/maven/release/com/redgate/flyway/flyway-commandline/${FLYWAY_VERSION}/flyway-commandline-${FLYWAY_VERSION}-linux-x64.tar.gz"
          curl -fsSL "$url" -o flyway.tar.gz
          echo "${FLYWAY_SHA256}  flyway.tar.gz" | sha256sum -c -
          tar xzf flyway.tar.gz
          echo "$PWD/flyway-${FLYWAY_VERSION}" >> "$GITHUB_PATH"

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
    "errors"
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
        MigratorIdentity: &migration.MigratorIdentity{Username: migratorUser, Password: migratorPassword},
        Hook: func(r migration.TenantResult) {
            myLogger.Info().Str("tenant", r.TenantID).Dur("dur", r.Duration).Msg("tenant migrated")
        },
    })
    // An empty listing returns a nil error and an empty Failed(); only the
    // verdict says the run attempted nothing.
    if verdict := res.Verdict(); verdict != nil {
        return errors.Join(verdict, err)
    }
    return err
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

## Running more than one migration tree

The defaults assume one tree per vendor: `flyway/flyway-<vendor>.conf` plus
`migrations/<vendor>/`. A second tree for the same vendor — for example a
control-plane tree applied once and a tenant tree applied to every tenant —
needs no extra API: give each tree its own `migration.Config`, copied from the
vendor defaults and overridden.

```go
fm := migration.NewFlywayMigrator(myCfg, myLogger)
defaults := fm.DefaultMigrationConfigForVendor("postgresql")

tenantTree := *defaults
tenantTree.ConfigPath = "flyway/tenant-postgresql.conf"
tenantTree.MigrationPath = "migrations/tenant/postgresql"

controlTree := *defaults
controlTree.ConfigPath = "flyway/control-plane-postgresql.conf"
controlTree.MigrationPath = "migrations/control-plane/postgresql"
controlTree.Audit.Target = "control-plane"
```

Run the tenant tree across the fleet through `BaseConfig`:

```go
_, err := migration.MigrateAll(ctx, fm, lister, provider, migration.ActionMigrate, migration.MigrateAllOptions{
    BaseConfig: &tenantTree,
    Logger:     myLogger,
})
if err != nil { // fail-fast: the first tenant failure stops the run and is returned here
    return err
}
```

> **Always set `ConfigPath` and `MigrationPath` together** (on the CLI,
> `--flyway-config` and `--migrations-dir`). `BaseConfig` is merged over the
> vendor defaults field by field, so supplying only `ConfigPath` silently pairs
> your conf with the default `migrations/<vendor>` directory. For the same
> reason, don't name a tree-specific conf `flyway-<vendor>.conf`: that basename
> is the vendor default, so a run that dropped the path still looks correct.

`BaseConfig` paths are used as given for every listed tenant, with no vendor
interpolation, so in a mixed-vendor fleet each run — library or CLI — needs a
tenant source that returns only the tenants of the vendor its tree targets.

Run a single-database tree with `MigrateFor`, `ValidateFor` or `InfoFor`. These
use the `Config` exactly as passed, with no merge — which is why the trees above
start from a copy of the defaults:

```go
controlDB, err := provider.DBConfig(ctx, "control-plane")
if err != nil {
    return err
}
if _, err := fm.MigrateFor(ctx, controlDB, &controlTree); err != nil {
    return err
}
```

`ValidateFor` and `InfoFor` take the same arguments and return only an error.

`Audit.Target` labels which tree a `migration.applied` event came from; when
empty it defaults to the database name. Set it on a single-database run only:
`BaseConfig.Audit.Target` gives every tenant's event the same label instead of
its database name (the Flyway target is unaffected), which is why `tenantTree`
leaves it empty.

On the CLI, `migrate`, `validate` and `info` accept the same path flags.
`--tenant` runs one database through the same credential lookup (with the
default source, the secret `<prefix>control-plane`):

```bash
# Tenant tree across the fleet
go-bricks-migrate migrate \
  --source-url https://control-plane.example.com/api \
  --aws-region us-east-1 \
  --flyway-config flyway/tenant-postgresql.conf \
  --migrations-dir migrations/tenant/postgresql

# Control-plane tree, one database
go-bricks-migrate migrate \
  --tenant control-plane \
  --aws-region us-east-1 \
  --flyway-config flyway/control-plane-postgresql.conf \
  --migrations-dir migrations/control-plane/postgresql
```

The CLI has no flag for `Audit.Target`; its events carry each database name.

## Decorating the config provider

`MigrateAll` resolves each tenant through its `database.DBConfigProvider`
before running Flyway for that tenant. Wrapping the provider is the supported
pre-Flyway seam; the CLI wraps its own provider the same way to validate each
resolved configuration.

```go
var ErrTenantRefused = errors.New("tenant refused by migration guard")

type guardedProvider struct {
    inner        database.DBConfigProvider
    allowedHosts map[string]bool
}

func (p guardedProvider) DBConfig(ctx context.Context, tenantID string) (*config.DatabaseConfig, error) {
    cfg, err := p.inner.DBConfig(ctx, tenantID)
    if err != nil {
        return nil, err
    }
    if cfg == nil {
        return nil, database.ErrNoDatabaseConfig
    }
    out := *cfg // copy: the inner provider may cache its document
    // Host is Flyway's target only for PostgreSQL discrete fields; refuse what it cannot judge.
    if out.Type != config.PostgreSQL || out.ConnectionString != "" || !p.allowedHosts[out.Host] {
        return nil, fmt.Errorf("%w: tenant %q", ErrTenantRefused, tenantID)
    }
    return &out, nil
}
```

Pass `guardedProvider{inner: provider, allowedHosts: map[string]bool{"tenants.db.internal": true}}`
to `MigrateAll` in place of `provider`. A decorator guards only the calls that
go through it: resolve a single-database `MigrateFor` target through the same
wrapper. The example judges `Host` only where Flyway targets it — a PostgreSQL
config with discrete fields, whose framework-built `-url=` outranks the conf
([ADR-085](adr_085_framework_owned_flyway_url.md)) — and refuses every other
shape. For Oracle or a `connectionstring` config the conf's `flyway.url` sets the
target, so a guard that admits them must validate that URL instead.

What a decorator can do:

- Validate or overlay a tenant's coordinates, always on a copy. A decorator is
  not needed to run Flyway as a dedicated migrator role: use
  [`MigrateAllOptions.MigratorIdentity`](#migrator-identity) in process, or
  `GOBRICKS_MIGRATE_MIGRATOR_USER` / `GOBRICKS_MIGRATE_MIGRATOR_PASSWORD` on
  `go-bricks-migrate` ([#1694](https://github.com/gaborage/go-bricks/issues/1694)).
- Refuse a tenant. Flyway never starts for it, and the error lands in that
  tenant's `TenantResult.Err` wrapped with `%w`, so
  `errors.Is(r.Err, ErrTenantRefused)` holds on `res.Failed()` entries.

What it cannot do:

- Run anything after Flyway: it returns before Flyway starts.
  `MigrateAllOptions.Hook` observes each tenant's result afterwards but cannot
  change it.
- See which action is running. `DBConfig` receives only the context and tenant
  ID, so build a separate provider per action when the check differs.
- Mark a tenant "skipped". Any error it returns is a per-tenant failure, and
  under the default fail-fast mode the first one stops the run — in a parallel
  run that cancels other tenants' in-flight Flyway processes, leaving their
  schema state unknown. Set `ContinueOnError` whenever refusals are expected. Fleet-level outcome
  reporting is tracked in [#1692](https://github.com/gaborage/go-bricks/issues/1692).

A caller that needs a pre/post protocol around Flyway — a lock held across its
own DDL and the Flyway run, say — should loop over tenants and call `MigrateFor`
itself. `MigrateAll` will not grow `Prepare`/`Finish` interceptor callbacks; see
[the rejected interceptor proposal](../.out-of-scope/migrateall-tenant-interceptor.md).

When the need is only a different secret-name grammar, no decorator is needed:
set `SecretsProvider.NameFor` (for example to compose `/env/platform/<id>/db`).
It replaces the default `Prefix + tenantID` composition, receives the tenant ID
already trimmed and allowlist-validated, and is library-only; the CLI exposes
`--secrets-prefix`.

## Run verdicts

`MigrateAll`'s `Results` hold one row per tenant it **dispatched**. A listed tenant that the run
stopped before dispatching (context done, quiesce flag, fail-fast) has no row; its ID is in
`NeverDispatched`, and `Listed()` says how many IDs the lister returned. `res.Verdict()` classifies
the whole run ([ADR-115](adr_115_fleet_migration_run_verdict.md)):

| Verdict | When | Schema state | Operator action |
| --- | --- | --- | --- |
| `nil` (clean) | At least one tenant listed, every listed tenant dispatched, none failed | The action succeeded on every tenant (for a non-dry-run `ActionMigrate`, the fleet is at head) | Proceed |
| `migration.ErrFleetSplit` | At least one tenant dispatched, and at least one failed or was never dispatched | Mixed versions possible; a failed tenant's state may be unknown | Repair the failed tenants, then re-run; the never-dispatched IDs still need the new SQL |
| `migration.ErrNothingAttempted` | No tenant dispatched: empty listing, listing failure (nil result), context done or quiesce set before the first dispatch | Untouched | Fix the cause outside the database, then re-run |

A dispatched tenant that ends in `ErrFlywayTimeout` or `ErrFlywayCanceled` is a failure, not a
never-dispatched tenant: its schema state is unknown. The verdict does not replace `MigrateAll`'s
error, so check both.

The `go-bricks-migrate` CLI still exits `0` on success and `1` on any error; the three-way exit-code
mapping recorded in ADR-115 ships in a later CLI release.

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
