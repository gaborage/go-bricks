# go-bricks-migrate

Operator and CI CLI for rolling Flyway migrations against a single tenant or a
fleet of tenants behind a control plane. Wraps `migration.MigrateAll` from the
go-bricks framework so the runtime engine and the CLI honor the same contract
(credentials never appear in audit events, vendor-specific defaults applied per
tenant, advisory-lock concurrency provided by Flyway natively).

Deep dives:
- [wiki/multi_tenant_migration.md](../../wiki/multi_tenant_migration.md) — full
  architecture, control-plane response shape, AWS Secrets Manager layout.
- [wiki/migration_audit.md](../../wiki/migration_audit.md) — `migration.applied`
  audit event schema, OTel emission, `AuditRecorder` opt-in.
- [ADR-018](../../wiki/adr_018_multi_tenant_migration_cli.md) — design rationale.
- [ADR-019](../../wiki/adr_019_migration_audit_delivery.md) — audit delivery
  guarantees.

## Install

CLI releases are tagged `tools/migration/vX.Y.Z` — the first, `tools/migration/v0.38.0`, is published with this release. Once a tag exists:

```bash
# Latest CLI release:
go install github.com/gaborage/go-bricks/tools/migration/cmd/go-bricks-migrate@latest

# Pin to a specific release:
go install github.com/gaborage/go-bricks/tools/migration/cmd/go-bricks-migrate@v0.38.0

# From a clone (contributors):
cd tools/migration && make build   # produces ./go-bricks-migrate
```

> `@latest` and `@vX.Y.Z` resolve `tools/migration/vX.Y.Z` tags. Before the first such tag exists, `@latest` installs an unversioned default-branch pseudo-version, so wait for the tag.

## Quick start

**Single tenant from a YAML config:**

```bash
go-bricks-migrate migrate \
    --source-config ./config.yaml \
    --credentials-from config-file \
    --tenant tenant_acme
```

**Fleet rollout from a control-plane API + AWS Secrets Manager:**

```bash
export GOBRICKS_MIGRATE_SOURCE_TOKEN=$(cat /run/secrets/cp_token)
go-bricks-migrate migrate \
    --source-url https://control.internal/v1/tenants \
    --credentials-from aws-secrets-manager \
    --secrets-prefix gobricks/migrate/ \
    --parallel 10 \
    --continue-on-error \
    --json
```

## Subcommands

| Command | Purpose |
|---|---|
| `migrate` | Apply pending migrations. Default action for CI/CD rollouts. |
| `validate` | Validate the locally-checked-in migration set against the schema history without applying anything. |
| `info` | Print Flyway's migration status table for each target. Operator-facing; not for CI parsing. |
| `list` | List tenant IDs the configured source would target. Useful for dry-running the rollout shape. |
| `quiesce set\|clear\|status` | Manage the deployment quiesce flag in the control-plane database (pause/resume provisioning). |
| `version` | Print the CLI version. |

### Quiesce

While the quiesce flag is set, provisioning workers park pending jobs and `MigrateAll` stops dispatching new tenants; in-flight work drains, nothing is interrupted. The flag **auto-releases at its TTL** (crash-safe — no sweeper) and can be cleared by any operator. It lives in a control-plane PostgreSQL table located via the standard `--tenant` + credential resolution.

```bash
# Pause (default 30m TTL, max 2h):
go-bricks-migrate quiesce set --tenant control-plane --source-config tenants.yaml \
  --credentials-from config-file --applied-by "$USER" --reason "deploy 2026.06" --ttl 1h

# Inspect (--json for machine output):
go-bricks-migrate quiesce status --tenant control-plane --source-config tenants.yaml --credentials-from config-file

# Resume:
go-bricks-migrate quiesce clear --tenant control-plane --source-config tenants.yaml \
  --credentials-from config-file --applied-by "$USER"
```

`set` and `clear` emit `quiesce.set` / `quiesce.cleared` audit events (the `--applied-by` principal is the audited actor). PostgreSQL-only in v1.

## Flag groups

### Tenant selection (mutually exclusive)

| Flag | When to use |
|---|---|
| `--tenant ID` | One-shot operator run against a single tenant. |
| `--source-url URL` | Fleet run; lists tenants from a control-plane API matching the [HTTP listing contract](../../wiki/multi_tenant_migration.md#pre-defined-http-listing-contract). |
| `--source-config PATH` | Fleet run from a YAML file containing a `multitenant.tenants` block. |

### Credentials

| Flag | Source |
|---|---|
| `--credentials-from aws-secrets-manager` (default) | Per-tenant secrets fetched from AWS SM under `--secrets-prefix`. |
| `--credentials-from config-file` | Per-tenant credentials embedded in the YAML supplied via `--source-config`. |

### Runtime tuning

| Flag | Purpose |
|---|---|
| `--parallel N` | Concurrency for fleet runs. `1` = sequential (default). Capped at 32 in the engine. |
| `--continue-on-error` | Keep iterating after the first per-tenant failure instead of fail-fast. |
| `--json` | Emit structured per-tenant and summary events on stdout for CI ingestion. |
| `--flyway-path PATH` | Override the default `flyway` executable lookup. |
| `--flyway-config PATH` | Override Flyway's `-configFiles=` argument. |
| `--migrations-dir PATH` | Override Flyway's `-locations=filesystem:` argument. |
| `--verbose` | Switch the embedded logger from `info` to `debug`. |

### Audit context (ADR-019)

Recorded on every `migration.applied` audit event. The principal is **never inferred** — pass it explicitly or it emits `<unspecified>` with a warning.

| Flag | Purpose |
|---|---|
| `--applied-by` | Principal that triggered the run (operator, service account, pipeline). |
| `--git-sha` | Source commit SHA, for correlating an event to a deployment. |
| `--pipeline-run-id` | CI/CD run identifier. |

### Environment variable overrides

| Variable | Meaning |
|---|---|
| `GOBRICKS_MIGRATE_SOURCE_TOKEN` | Bearer token passed to the control-plane API. Used when `--source-url` is set. |
| `GOBRICKS_MIGRATE_SECRETS_PREFIX` | Default `--secrets-prefix`. An explicit flag still wins. |
| `GOBRICKS_MIGRATE_APPLIED_BY` | Default `--applied-by`. An explicit flag still wins. |
| `GOBRICKS_MIGRATE_GIT_SHA` | Default `--git-sha` (e.g. `--git-sha "$GITHUB_SHA"`). |
| `GOBRICKS_MIGRATE_PIPELINE_RUN_ID` | Default `--pipeline-run-id` (e.g. `--pipeline-run-id "$GITHUB_RUN_ID"`). |

## JSON output (for CI consumers)

With `--json`, the CLI streams a JSON object per tenant and a final summary
object. Both are newline-delimited; pipe through `jq -c .` to consume.

**Per-tenant event:**

```json
{
  "event": "tenant_complete",
  "tenant_id": "tenant_acme",
  "vendor": "postgresql",
  "duration": "152ms",
  "status": "ok",
  "applied_versions": ["1", "2"],
  "ending_version": "2",
  "duration_millis": 142,
  "flyway_version": "10.22.0"
}
```

Fields whose underlying `migration.Result` was zero-valued (e.g. for
`validate`/`info` actions, or when Flyway crashed before emitting its JSON
envelope) are omitted rather than emitted as empty strings. Consumers should
treat absence as "no signal" rather than "zero".

**Final summary:**

```json
{
  "event": "summary",
  "action": "migrate",
  "total": 3,
  "failed": 0
}
```

A failed tenant adds `"status": "fail"` and an `"error"` field; the process
exits non-zero whenever `failed > 0`. Idempotent reruns against already-
migrated tenants omit `applied_versions` (zero-length slices follow the same
omit-when-empty rule as the other Result-derived keys) with `ending_version`
mirroring `starting_version`.

## CI integration

GitHub Actions example for a fleet rollout step:

```yaml
- name: Run migrations
  env:
    AWS_REGION: us-east-1
    GOBRICKS_MIGRATE_SOURCE_TOKEN: ${{ secrets.CP_TOKEN }}
  run: |
    go-bricks-migrate migrate \
      --source-url https://control.internal/v1/tenants \
      --credentials-from aws-secrets-manager \
      --secrets-prefix gobricks/migrate/ \
      --parallel 10 \
      --continue-on-error \
      --json > migrate.log
    cat migrate.log | jq -c 'select(.status=="fail")'
```

`--continue-on-error` ensures one tenant's failure doesn't strand the rest;
the post-step `jq` filter surfaces per-tenant failures in the CI log without
needing a separate parser.

## Audit events

Every `migrate` invocation emits a `migration.applied` event per tenant via
OpenTelemetry (always-on) and, when configured at the library layer, via the
optional `AuditRecorder` durable-delivery seam. The event carries
`Version` (the schema version after the run), `Outcome`, `ErrorClass` on
failure, and `Attributes` (Flyway engine version, applied versions CSV,
vendor, dry-run flag). See
[wiki/migration_audit.md](../../wiki/migration_audit.md) for the full schema.

Pass `--applied-by` (and optionally `--git-sha` / `--pipeline-run-id`) to stamp
ADR-019's `AuditContext` on every event. The principal is never inferred — when
left empty the pipeline emits `<unspecified>` with a warning, so the gap is
itself auditable. Example:

```bash
go-bricks-migrate migrate \
  --source-url https://control-plane.example.com/api \
  --applied-by "$GITHUB_ACTOR" \
  --git-sha "$GITHUB_SHA" \
  --pipeline-run-id "$GITHUB_RUN_ID"
```

## Development

```bash
make check               # fmt + lint + test + CLI smoke
make test                # unit tests only
make test-coverage       # writes coverage.html
```

The CLI tests use `httptest` for the control-plane source and a fake AWS SM
client for credentials; no Docker dependency at the unit-test layer.
Testcontainers-driven end-to-end coverage against real Postgres + Flyway is
tracked separately (see the parent issue for the migration epic).
