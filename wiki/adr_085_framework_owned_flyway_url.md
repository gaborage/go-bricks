# ADR-085: The framework owns the PostgreSQL Flyway JDBC URL

- **Status**: Accepted
- **Date**: 2026-08-24
- **Related**: [ADR-062](adr_062_database_tls_fail_closed.md) (`database.tls` validation, whose residual "reaching Flyway" gap this closes) · [ADR-018](adr_018_multi_tenant_migration_cli.md) (the per-tenant `*config.DatabaseConfig` this builds from) · [ADR-019](adr_019_migration_audit_delivery.md) (the audit stream that records what the migration did)

## Context

`database.tls` reached Flyway only as `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY`
environment variables. The JDBC URL itself came from the operator's `flyway.conf`, which the
framework does not parse — so it could export the variables but could not confirm the URL
interpolated them.

That gap has two failure modes, and neither is loud.

**A cleartext migration under a `verify-full` config.** A `flyway.conf` whose
`flyway.url` names no `sslmode` migrates in plaintext while `database.tls.mode:
verify-full` validates cleanly and the runtime connects encrypted. The framework's only
signal was a once-per-migrator WARN — an advisory that says "this may not be applied",
which is exactly the shape operators learn to mute.

**A migration target that disagrees with the runtime target.** The runtime never reads
`flyway.conf`; it builds its DSN from `database.*`. A conf pointing at a different host or
database migrates the wrong schema, silently, and the two config sources drift with nothing
comparing them.

Separately, fleet `flyway.conf` URLs were hand-carrying `application_name` so a DBA
watching `pg_stat_activity` could tell which service was migrating — duplicated per conf,
and easy to omit.

## Decision

**The framework builds the URL and passes `-url=` on the command line, for every
PostgreSQL discrete-field config.** A command-line flag outranks the conf; the
`-schemas=`/`-defaultSchema=` flags are the in-repo precedent for exactly this. The URL is

```text
jdbc:postgresql://<host>[:<port>]/<database>?ApplicationName=…[&sslmode=…][&sslrootcert=…]
```

Not gated on TLS being configured: the target-drift half of the gap is present in every PG
deployment, TLS or not, so every PG deployment gets the framework's URL.

**No escape hatch.** No config key restores a conf-owned URL. An opt-out reopens both
failure modes on precisely the deployments that would set it, and the migration would then
be un-auditable from `database.*` alone.

**`database.tls.mode` → `sslmode`, `cafile` → `sslrootcert`,** as URL query parameters. The
WARN and its `sync.Once` guard, and the whole `DB_SSL*` export, are removed: a second,
unread channel for the same setting is a drift source, not a fallback.

**Credentials stay environment-delivered.** The URL carries no username, password, or any
other secret — argv is world-readable in the process list, and Flyway already reads
`DB_USER`/`DB_PASSWORD` from the environment.

**`ApplicationName` is set from `app.name`, URL-encoded, with no new config key.** It is
what the fleet's confs were hand-carrying, and the framework already knows it. The key is
pgjdbc's own spelling, NOT libpq's `application_name`: pgjdbc assembles startup parameters
from a whitelist of known `PGProperty` keys (`PGProperty.APPLICATION_NAME` is
`"ApplicationName"`), so a libpq-spelled parameter is silently dropped rather than
forwarded. It still lands in the server's `application_name` column.

**PostgreSQL client-certificate migrations fail closed.** With `database.tls.certfile` or
`keyfile` set, any PostgreSQL run the framework builds a URL for — `migrate`, `validate` and
`info` alike — returns `ErrMigrationMTLSUnsupported` naming the pgjdbc limitation:
pgjdbc's `sslkey` requires a PKCS-8 DER file, while `database.tls.keyfile` is
validated as a libpq PEM (`[C60.2]`). The framework does not convert — that means writing
key material to a temp file. Server-authenticated TLS (`mode` + `cafile`) is fully
supported, and runtime mTLS is untouched. Connecting without the client certificate the
config asked for would be the silent-downgrade this ADR exists to remove.

**`database.host` is validated, not escaped.** It is the one component that must
stay a routable address, so it cannot be percent-encoded the way the database name
and the query values are. Unescaped it is an injection point: a host of
`h/?sslmode=disable&x=` ends the authority early, and pgjdbc — which splits the
query at the FIRST `?` — then reads the injected parameters instead of the
framework's, so a `verify-full` config connects in cleartext to a host the value
chose. The host must therefore be an IP literal or a plain DNS name
(`ErrInvalidMigrationHost`); the error never echoes the value, which can hold a
whole DSN. This mirrors `schemaArgs`, which already validates its schema name
against `safePGIdentifier` before it reaches argv.

**An incomplete target fails closed; a bare one defers.** `usesFrameworkOwnedURL`
needs a host and a database, so the gate is three-way rather than two.
`ErrIncompleteMigrationTarget` rejects a run when the block is PARTIALLY filled —
one of the three fields that BECOME the URL (`host`, `port`, `database`) is set but
not a usable host AND database — because that is a target broken in transit, not a
deliberate hand-off. The live case is a tenant whose host arrived blank from a
secret store, reaching the migrator through `dbStrictnessConnect`, the path that
deliberately skips the identity check that would otherwise catch it: deferring
there would migrate that tenant against whatever host `flyway.conf` names, which is
another tenant's database. Credentials are NOT counted — `username`/`password` are
env-delivered under every shape and never reach the URL, so a block carrying only
credentials beside a conf-owned URL names no target and still defers (ADR-047 counts
them as identity markers for the different question of whether a database is
intended at all). It also
rejects any config carrying `database.tls.*` that cannot produce a URL: there is no
shape in which a TLS setting silently fails to reach the connection, which is the
whole of #1047.

A block naming NO identity field and no TLS is the **third documented boundary**,
alongside Oracle and `connectionstring`: it is conf-owned by construction — there
is nothing to build a URL from, and no TLS guarantee to lose. ADR-047 already
rejects a type-only `database:` block at application startup, so this shape exists
only for migration-only processes (the CLI, tests) that never dial.

**URL construction happens on the per-run path**, where the per-tenant
`*config.DatabaseConfig` is already in scope — so a fleet whose tenants have distinct hosts
or databases gets a distinct URL per tenant, from the same source the runtime uses.

## Consequences

- **Breaking**: a `flyway.url` in a `flyway.conf` is now **silently outranked** for
  PostgreSQL discrete-field configs. Flyway does not warn about a flag beating a conf key.
  Delete the URL from the conf, or accept that it has no effect. `[C61.4]`.
- **Breaking**: a PostgreSQL migration with `database.tls.certfile`/`keyfile` set now fails
  instead of running. Match with `errors.Is(err, migration.ErrMigrationMTLSUnsupported)`.
  `[C61.4]`.
- `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY` are no longer exported. A conf
  interpolating `${env.DB_SSLMODE}` into a `flyway.url` breaks twice over — the URL is
  ignored anyway, and the variable is gone.
- A migration now reports `<app.name>` in `pg_stat_activity.application_name`. A conf that
  also set one is outranked with the rest of its URL.
- **Oracle keeps the conf-owned URL.** No `database.tls` exists for Oracle (ADR-062), so
  there is no TLS guarantee to make, and no Oracle JDBC URL builder here. Reopen trigger:
  Oracle migrations enter production use.
- **`database.connectionstring` deployments keep the conf-owned URL.** The framework does
  not parse DSNs, and `tls.*` alongside a connection string is already rejected (ADR-062),
  so no guarantee is lost.
- **A `database:` block carrying only `type` keeps the conf-owned URL.** Nothing to build a
  URL from, so no guarantee is offered — and with TLS set it fails rather than deferring.
- The control-character check that guarded the subprocess environment now also runs before
  the URL is built, because the same fields are formatted into argv.
- **Breaking**: a `database.host` that is not an IP literal or a plain DNS name now fails a
  PostgreSQL migration with `ErrInvalidMigrationHost`. Hosts that worked before and still
  parse as addresses are unaffected. `[C61.4]`.
- **Breaking**: a PostgreSQL config that is partially filled (`host`, `port`, or `database`
  set, but not a usable `host` AND `database`), or that sets `database.tls.*` without being able to
  produce a URL, now fails with `ErrIncompleteMigrationTarget` instead of silently deferring
  to the conf's URL. A block carrying only `type` and no TLS is unchanged. `[C61.4]`.

## Alternatives considered

**Keep the WARN.** Rejected: it is the status quo. An advisory that a setting may not have
been applied is not a guarantee that it was, and a once-per-migrator WARN in a fleet run is
one line among thousands.

**Inspect the operator's `flyway.conf` and warn or fail on a URL that lacks the TLS
params.** Rejected: it makes the framework a JDBC-URL parser for a format it does not own,
across Flyway's placeholder and `${env.*}` substitution layers, and it still cannot fix the
target-drift half.

**Build the URL only when `database.tls` is set.** Rejected: the runtime-vs-migration target
drift is independent of TLS, and a rule that changes the URL's owner based on an unrelated
key is the kind of conditional behavior operators cannot reason about.

**An escape hatch key restoring the conf-owned URL.** Rejected: see Decision.

**A generic URL-parameter passthrough key.** Rejected as YAGNI. The maintainer's fleet
survey found `application_name` and nothing else; a passthrough is easy to add when a
second parameter appears, and impossible to remove once shipped.

**Convert the PEM key to PKCS-8 DER for the Flyway leg.** Rejected for now: it means
writing private key material to a temp file, with its own lifetime, permissions, and
cleanup-on-crash story. Reopen trigger: a real PostgreSQL-mTLS migration deployment.
