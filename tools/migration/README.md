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

CLI releases are tagged `tools/migration/vX.Y.Z` (first: `v0.38.0`; latest: `v0.59.0`). Once a tag exists:

```bash
# Latest CLI release:
go install github.com/gaborage/go-bricks/tools/migration/cmd/go-bricks-migrate@latest

# Pin to a specific release:
go install github.com/gaborage/go-bricks/tools/migration/cmd/go-bricks-migrate@v0.59.0

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
| --- | --- |
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
| --- | --- |
| `--tenant ID` | One-shot operator run against a single tenant. |
| `--source-url URL` | Fleet run; lists tenants from a control-plane API matching the [HTTP listing contract](../../wiki/multi_tenant_migration.md#pre-defined-http-listing-contract). |
| `--source-config PATH` | Fleet run from a YAML file containing a `multitenant.tenants` block. |

### Credentials

| Flag | Source |
| --- | --- |
| `--credentials-from aws-secrets-manager` (default) | Per-tenant secrets fetched from AWS SM under `--secrets-prefix`. |
| `--credentials-from config-file` | Per-tenant credentials embedded in the YAML supplied via `--source-config`. |

### TLS validation (ADR-062)

Every resolved database config — both credentials sources, every tenant — is checked
against the same vendor rules go-bricks applies before it dials, and a rejected config
fails the run with the tenant named. A config the service would refuse to boot on
should not pass the migrate CLI either.

The check also infers a missing `database.type` from a recognized `connectionstring`
scheme, and enforces Oracle's connection-identifier requirement and a loadable
`database.timezone` — so it can reject configs for reasons unrelated to TLS. The four
TLS shapes it rejects:

| Shape | Why |
| --- | --- |
| PostgreSQL `tls.mode` outside pgx's sslmode set | Fails at connect time otherwise, with a redacted parse error. |
| `tls.cert`/`key`/`ca` under an unset, `disable`, `allow`, or `prefer` mode | pgx discards the material or downgrades to plaintext; an unset mode means `prefer`. |
| Lone `tls.cert` or lone `tls.key` | Client-certificate auth needs both. |
| Any `tls.*` on Oracle, or alongside a `connectionstring` | Oracle tcps/wallet is not implemented; a connection string is used verbatim, so the block is ignored. |

#### Reaching Flyway

For a PostgreSQL config using discrete fields (`host`/`port`/`database`), the framework
**builds the JDBC URL itself** and passes it as `-url=` on the Flyway command line
(ADR-085). A command-line flag outranks the conf, so **any `flyway.url` in your
`flyway.conf` is silently ignored** for these configs — delete it, or accept that it has
no effect. The built URL is:

```text
jdbc:postgresql://<host>[:<port>]/<database>?ApplicationName=<app.name>[&sslmode=…][&sslrootcert=…]
```

| Config key | URL parameter |
| --- | --- |
| `database.tls.mode` | `sslmode` |
| `database.tls.ca` | `sslrootcert` |
| `app.name` | `ApplicationName` (automatic; no config key — pgjdbc's spelling, not libpq's `application_name`, which pgjdbc ignores; it still lands in the server's `application_name` column) |

The port is omitted when `database.port` is unset or `0`, leaving the driver's
default; a negative port fails with `ErrInvalidMigrationPort` rather than being dropped. Unset TLS fields are omitted. Credentials are **not** on the URL — `DB_USER`/`DB_PASSWORD`
stay environment-delivered, because argv is world-readable in the process list. This
closes the gap where a conf-owned URL could migrate in cleartext while
`database.tls.mode: verify-full` validated cleanly, and where the migration target could
silently disagree with the runtime target.

**Client-certificate (mTLS) migrations are unsupported.** With `database.tls.cert` or
`database.tls.key` set, a PostgreSQL migrate fails closed rather than connecting without
the client certificate. The limitation is the framework's, not pgjdbc's: it does not
forward `database.tls.cert`/`key` as the JDBC `sslcert`/`sslkey` parameters, so it
refuses rather than silently migrating without them. Server-authenticated TLS (`mode` + `ca`) is
fully supported; runtime mTLS is unaffected.

**`database.host` must be an IP address or a plain DNS name.** It is the one URL
component that cannot be percent-encoded — it has to stay routable — so it is validated
instead. Left unescaped, a host like `db.internal/?sslmode=disable&x=` would end the URL
authority early and pgjdbc would read the injected parameters, connecting in cleartext to
a host of the value's choosing. Letters, digits, hyphen, underscore and dots are accepted
(underscore because internal DNS and Docker hostnames use it), as is an IPv6 literal in
EITHER spelling — bare (`::1`) or bracketed (`[::1]`), which the URL builder normalizes to
exactly one pair of brackets. Anything else, including a host carrying its own `:port`,
fails with `ErrInvalidMigrationHost`. The error never
echoes the value, since a misconfigured host can hold a whole DSN.

**`database.port` must be 1–65535, or `0` for the driver default.** The port is omitted
from the URL when it is zero; a negative one used to take that same branch and migrate
silently against the driver's 5432, so anything below 0 or above 65535 now fails with
`ErrInvalidMigrationPort`. The error names the field, not the value.

**`database.tls.mode` must be one of the libpq six**, matched case-sensitively:
`disable`, `allow`, `prefer`, `require`, `verify-ca`, `verify-full`. The mode was copied onto
the JDBC URL verbatim, so an unsupported one reached Flyway instead of failing here; it now
returns `ErrInvalidMigrationTLSMode`, which names the offending mode. Surrounding whitespace is
trimmed before matching, exactly as the runtime's own validation trims it, so ` require` is
normalized rather than refused; case is folded by neither, so `Require` fails in both places. An unset mode simply puts no `sslmode` on the URL.

**`database.tls.ca` needs `verify-ca` or `verify-full`, and `ca: system` is refused.**
pgjdbc reads `sslrootcert` only under those two modes — `require`, `allow` and `prefer` use a
non-validating socket factory, and an unset mode is `prefer` — so a CA named under any other
mode was written onto the URL and then ignored, leaving the migration authenticating nothing.
It now fails with `ErrMigrationTLSCARequiresVerify`. Note this is **stricter than the runtime**:
`require` + `ca` passes `config.Validate` because pgx treats that pair as `verify-ca`, so the
service verifies while the migration would not — the migrate door answers for pgjdbc. The
`ca: system` sentinel (the platform trust store, understood by pgx) is refused with
`ErrMigrationTLSCASystemUnsupported`: pgjdbc treats `sslrootcert` as a file path and has no
equivalent, and remapping it to the JVM trust store would authenticate against a different set
of CAs than the runtime does. Point `database.tls.ca` at the CA file itself for migrations.

**A partially filled block fails rather than falling back.** If `host`, `port`, or
`database` is set but not a usable `host` AND `database`, the run fails with
`ErrIncompleteMigrationTarget` instead of deferring to the conf — on a fleet run, a tenant
whose host arrived blank from a secret store would otherwise migrate against whatever host
`flyway.conf` names, which is another tenant's database. Any `database.tls` setting that
cannot be put on a URL fails the same way. Credentials do not count as a target:
`username`/`password` are environment-delivered under every shape and never reach the URL.

**`database.tls` beside a `connectionstring` fails rather than being dropped.** A DSN is
used verbatim and the framework does not parse it, so TLS configured in a `database.tls`
block could only be discarded. `config.Validate` already rejects that pair (ADR-062), but
`MigrateFor` also takes per-tenant configs that never passed it — a dynamic
`DBConfigProvider`, or this CLI's `tenants.yaml` — so the migrator refuses on its own with
`ErrMigrationTLSWithConnectionString`. The remedy is two-sided, and moving the settings into
the DSN is only half of it: the DSN secures the **runtime** pool, but a `connectionstring`
config gets no `-url=`, so Flyway still takes its JDBC URL from `flyway.conf` — give that URL
its own `sslmode`/`sslrootcert`, or the migration runs against whatever it names, unencrypted.
Encrypting that URL is necessary but not sufficient: confirm it names the **same host and
database** as the DSN, or you get an encrypted migration applied to the wrong database. Nothing
in the framework can cross-check a DSN it does not parse.

**Three config shapes keep the conf-owned URL:**

| Shape | Why |
| --- | --- |
| Oracle | `database.tls` is unsupported and rejected for Oracle (ADR-062) — the field exists in the shared config, validation refuses it — and the framework builds no Oracle JDBC URL. |
| `database.connectionstring` **with no `database.tls` block** | The framework does not parse DSNs, and the DSN carries its own TLS, so no guarantee is offered — but `tls.*` alongside one fails rather than deferring. |
| A block naming no `host`, `port`, or `database` (credentials only, or bare) | Nothing to build a URL from, so no guarantee is offered — but with `tls.*` set it fails instead of deferring. |

### Runtime tuning

| Flag | Purpose |
| --- | --- |
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
| --- | --- |
| `--applied-by` | Principal that triggered the run (operator, service account, pipeline). |
| `--git-sha` | Source commit SHA, for correlating an event to a deployment. |
| `--pipeline-run-id` | CI/CD run identifier. |

### Environment variable overrides

| Variable | Meaning |
| --- | --- |
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
  "flyway_version": "12.8.1"
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
make check               # fmt + lint + test + CLI smoke + vuln scan + gosec + mod-tidy check
make test                # unit tests only
make test-coverage       # writes coverage.html
```

The CLI tests use `httptest` for the control-plane source and a fake AWS SM
client for credentials; no Docker dependency at the unit-test layer.
Testcontainers-driven end-to-end coverage against real Postgres + Flyway lives
in `migrate_integration_test.go` / `quiesce_integration_test.go`
(`go test -tags=integration ./...` from `tools/migration`, Docker + Flyway
required); it is not yet wired into CI.
