# ADR-062: Fail Closed on `database.tls` Misconfiguration (Mode Allowlist + Material/Mode Coherence)

**Status:** Accepted (amended 2026-08-24, 2026-09-13, 2026-11-10, 2026-11-12)
**Date:** 2026-08-14

## Amendment (2026-11-12, #2376): the typed fields' client certificate includes `PGSSLCERT`/`PGSSLKEY`

The 2026-11-10 amendment judged `disable` on the typed fields only beside
`database.tls.cert`/`key`. The framework-built DSN writes `sslcert`/`sslkey` only when those
keys are set, so with neither set pgx v5.13.0 takes `PGSSLCERT`/`PGSSLKEY` from the
environment, and `PGSSLCERTMODE=disable` beside them passed validation and connected with no
client certificate.

The typed-field arm now counts the config keys when set, else a non-empty `PGSSLCERT` or
`PGSSLKEY` (pgx ignores an empty one), the same precedence pgx gives those two sources. The
coherence rule already pairs the two keys, so a set key shadows both variables. The variables
count only where pgx builds TLS for them: not when every host entry is a unix socket, and not
under `database.tls.mode: disable` unless the effective `sslrootcert` (`database.tls.ca`, else
`PGSSLROOTCERT`) is exactly `system`, which pgx answers with `verify-full` over `disable`. `PGSSLMODE` is not read, so an unset mode counts as
pgx's `prefer`, which loads them; under `PGSSLMODE=disable` that refuses a shape pgx would
load nothing for, failing closed. The refusal is the existing `ConfigError` on `database.tls`,
and its exit names the source that supplied the certificate (`database.tls.cert and
database.tls.key`, or `PGSSLCERT/PGSSLKEY in the environment`), never a value. The
connection-string arm's verdicts do not move. A shared fixture table pins the verdicts against
`pgconn.ParseConfig`. Known limit: pgx's default `~/.postgresql/postgresql.crt`/`postgresql.key`
pair is not counted by either arm, because reading files under `HOME` at validation is a wider
change, tracked separately.

Considered and rejected: refusing every env-supplied client certificate on the typed fields.
It is wider than the defect and breaks deployments that mount a certificate through the
environment under `allow` or `require`. See `[C74.9]`.

## Amendment (2026-11-10, #2310): the effective `sslcertmode` is judged on both doors

pgx v5.12.0, the pin this amendment ships with, reads `sslcertmode` from the DSN and
`PGSSLCERTMODE` from the environment (`disable`, `allow` by default, `require`). Under
`disable`, `configTLS` clears `sslcert`/`sslkey` before loading them, so a section with
`database.tls.cert`/`key`, or a `connectionstring` whose `sslcert`/`sslkey` is in effect,
passed validation and connected with no client certificate. Any other value, a set but empty
`PGSSLCERTMODE=` or `sslcertmode=` included, passed validation and failed pgx's parse when the
connection opened, behind the redacted `failed to parse PostgreSQL config`.

`validatePostgreSQLFields` now computes the effective value with pgx's precedence: a
`sslcertmode` key present in the connection string wins, empty included; otherwise
`PGSSLCERTMODE` when it is set, where set but empty counts as set (pgx keeps that value to
reject it, unlike every other `PGSSL*` variable); otherwise nothing is judged and pgx's
`allow` applies. The framework-built DSN carries no such key, so the typed-field arm reads the
variable alone, the first environment read on that arm. Two refusals follow, each after every
existing rule of its arm, so no earlier verdict moves:

- a value other than `disable`, `allow` or `require`, compared exactly as pgx does (no case
  folding, no trimming), is refused for every PostgreSQL section, TLS block or not;
- `disable` is refused when a client certificate or key is in effect: `database.tls.cert`/`key`
  on the typed fields; in a connection string, a non-empty effective `sslcert` or `sslkey`
  (the DSN key, else `PGSSLCERT`/`PGSSLKEY` when the DSN names no such key).

Both are `ConfigError`s on `database.tls` for the typed fields and on
`database.connectionstring` otherwise, naming the source (`sslcertmode` in the connection
string, or `PGSSLCERTMODE` in the environment), the exit and the allowed values, never a value
or DSN text. An env-sourced value is unset, or, on the connection-string arm, shadowed with an
explicit `sslcertmode=allow`; a DSN-sourced one is changed, not dropped, since dropping it would
expose the variable. `sslcertmode` is not a TLS claim: it joins the `PGSSL*` table and the
scanner's key record, but `allow`, `disable` or `require` on a unix-socket host does not trigger
R6 or `[C66.1]`; only the `Judged:` tail of the `[C66.1]` Action grows to six keys and six
variables. Out of scope: `sslcertmode=require` where TLS is not guaranteed, which pgx
fails at connect, and emitting `sslcertmode` in the framework-built DSN. See `[C74.9]`.

## Amendment — 2026-09-13: the fail-closed rules reason about host transport (#1555)

R1–R5 judged `mode` against material and never asked how the connection travels. pgx v5
classifies a host as a unix socket by shape — `isAbsolutePath` in `pgconn/config.go`: a
leading `/`, or one ASCII uppercase letter followed by `:\` — and appends a nil TLS config
for a unix network. So `host: /var/run/postgresql` with `mode: verify-full` and a `ca`
satisfied R2 and connected with no TLS at all: the material was coherent with the mode, and
the transport discarded both.

R6 closes it: PG, no connectionstring, `host` an absolute path by exactly that predicate,
and any of `cert`/`key`/`ca` set or `mode` set to anything but `disable` → reject on
`database.tls` (section-qualified), naming the unix socket host, with two exits: remove the
`database.tls` block, or use a TCP host. The mode is judged as the operator wrote it —
nothing on this seam defaults an unset mode; pgx's `prefer` applies only at parse time — so
a socket host with no block, or `mode: disable` and no material, is accepted unchanged.
Socket hosts are not refused by themselves. R6 applies per comma-separated `host` entry,
matching pgx's own untrimmed split, so `db.internal,/var/run/postgresql` is refused the same
way. The same amendment extends `[C64.8]`'s empty-host rule to each entry: an empty entry
(`db.internal,`, `,db.internal`, `db1,,db2`) is newly refused with `[C64.8]`'s
`MissingFieldError` on `database.host`, TLS or not, because pgx swaps it for the socket
directory — where `[C64.8]` refused only a wholly empty host and these used to boot.

R6 runs after R1, so a typo'd mode on a socket host is still reported as a mode problem, and
before R2 and R3, so material on a socket host gets the transport message rather than a
mode or pairing complaint that would steer the operator toward a mode the socket still
cannot carry. The predicate is `config.isUnixSocketHost`, a mirror of pgx: a divergence
reopens the hole; the predicate and `pgSSLModes` were re-diffed against `pgconn/config.go` at
the pin this amendment ships with, `github.com/jackc/pgx/v5@v5.11.0`, so the Consequences entry
below naming v5.10.0 records when that allowlist was last changed, not a stale pin. It covers
every door `validatePostgreSQLFields` covers — static YAML
through `config.Validate`, `DBConfigProvider` results through `ApplyDatabasePoolDefaults` /
`ApplyDatabasePoolDefaultsForKey`, and `go-bricks-migrate` at its next pin bump, whose
`tlsValidatingProvider` calls `config.ApplyDatabasePoolDefaults`, so
`TestApplyDatabasePoolDefaultsRefusesTLSOnUnixSocketHost` covers that path. A raw
`connectionstring` is unchanged (#1551). See `[C65.6]`.

## Amendment — 2026-08-24: the Flyway leg is no longer conf-owned (ADR-085, #1047)

The Consequences entry below describing `database.tls` reaching Flyway as
`DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY`, and the once-per-migrator WARN that
accompanied it, is **superseded and marked historical**. That export and that WARN are gone.

[ADR-085](adr_085_framework_owned_flyway_url.md) has the framework build the PostgreSQL JDBC
URL from `database.*` and pass it as `-url=`, which outranks the operator's `flyway.conf`, for
every PostgreSQL discrete-field config. `database.tls.mode` and `database.tls.ca` ride that URL
as `sslmode` and `sslrootcert`, so the residual "a conf that ignores those variables still
migrates without TLS" gap this ADR could only warn about is closed rather than advised.

A `database.tls.cert`/`database.tls.key` migration now FAILS (`ErrMigrationMTLSUnsupported`)
instead of downgrading: the framework does not forward the pair as the JDBC
`sslcert`/`sslkey` parameters, so it refuses rather than migrating without the client
certificate. Oracle and `database.connectionstring` keep the conf-owned
URL — for Oracle because this ADR rejects `database.tls` there outright, so there is no
guarantee to make. See `[C61.4]`.

The Context and Decision sections below are the original record and are unchanged.

## Context

`database.tls` reached pgx completely unvalidated. `config.Validate` checked
exactly one thing — that `cert` and `key` were configured together — and let
every other shape through to `buildPostgresDSN`, which pastes `sslmode`,
`sslrootcert`, `sslcert` and `sslkey` into the DSN verbatim. Five shapes booted
green while doing something other than what the operator configured:

1. **`mode: disable` (or unset, or `allow`/`prefer`) plus `cert`+`key`** —
   connects **without client certificates**. Under `disable` pgx returns a nil
   TLS config before it ever reads the cert files; under unset/`allow`/`prefer`
   it permits a plaintext fallback and skips server verification. The operator
   believes mTLS is on; it is not.
2. **An ordinary path-valued `ca:` with no `mode`** — **zero server
   authentication**. pgx defaults to `prefer`, which sets `InsecureSkipVerify`
   and never runs the CA verification path. The sentinel `ca: system` was the
   lone exception: pgx rewrites it to `verify-full` before the mode switch
   (config.go:806-814), so that exact value did verify — every path-valued
   `ca:` did not. R2 rejects both shapes uniformly; the split matters for this
   record's accuracy, not for the rule.
3. **A typo'd mode** (`requird`, `Require`, `verify_full`) — passes
   `config.Validate` and dies inside `NewConnection`, where go-bricks
   deliberately redacts pgx parse errors (PR #945): the operator sees a generic
   "failed to parse PostgreSQL configuration" with no hint that the sslmode was
   at fault. In multi-tenant deployments connections are created lazily, so this
   surfaces at first request rather than at boot.
4. **`connectionstring:` plus a `tls:` block** — the entire TLS block is
   **silently ignored**; `NewConnection` uses the connection string verbatim and
   never consults `cfg.TLS`.
5. **Oracle `database.tls.mode`** — silently ignored. The existing Oracle check
   rejected `cert`/`key`/`ca` but not `mode`, so a mode alone implied TLS that
   go-ora never negotiates.

This is the same advertised-but-inert config class that PR #582 fixed for the
`cert`/`key`/`ca` wiring (migration atom `[C42.1]`), and the same fail-closed
posture as ADR-047, ADR-050 and ADR-051.

### pgx ground truth

Verified against `github.com/jackc/pgx/v5@v5.10.0`, `pgconn/config.go`,
`configTLS`:

| Config reaching pgx | pgx behavior |
| --- | --- |
| `sslmode` absent | defaults to `prefer` |
| `sslmode=disable` | returns a nil TLS config — plaintext. `sslcert`/`sslkey` are handled **after** the mode switch, so client certs are never read |
| `sslmode=allow` / `prefer` | sets `InsecureSkipVerify` plus a nil-TLS fallback — opportunistic, silently downgradeable to plaintext, server never verified |
| `sslmode=require` + `sslrootcert` | upgraded to verify-ca semantics (a documented libpq quirk) |
| `sslmode=require`, no `sslrootcert` | TLS forced but `InsecureSkipVerify` (config.go:833-847) — encryption **without server authentication**; passes R2, so the prose around "raising the mode" must not equate `require` alone with verification |
| `sslrootcert=system` | a sentinel processed **before** the mode switch: system cert pool, and `sslmode` rewritten to `verify-full` — overriding even `disable` |
| unknown `sslmode` | `sslmode is invalid` at `ParseConfig` — i.e. at connect time, which go-bricks redacts |

Two of these run **before** the mode switch and are recorded so this ADR does
not inherit the simpler-but-wrong claim "under `disable`, no TLS field is ever
read": `sslrootcert` is read (an unreadable file errors even under `disable`)
and then discarded, and `sslrootcert=system` overrides the configured mode.
Neither changes a rule below — every shape they touch carries material under a
non-mandatory mode, which R2 rejects regardless.

## Decision

Validate `database.tls` and reject every shape pgx would discard or downgrade —
at **startup** for static configuration (the root block, named databases, static
tenants), and at **connection acquisition** for dynamic `DBConfigProvider`
records, where #1002's seam runs the same checks. All four fields are `TrimSpace`d once in
`validateVendorSpecificFields` — before the vendor dispatch — so both vendors
and the downstream DSN builder see canonical values (externally-sourced strings
carry whitespace until proven otherwise; the write-back seams that already carry
pool and session defaults carry the trim too).

| # | Condition | Verdict |
| --- | --- | --- |
| R1 | PG, no connectionstring: `mode` outside `{"", disable, allow, prefer, require, verify-ca, verify-full}` | reject, listing the valid values |
| R2 | PG, no connectionstring: any of `cert`/`key`/`ca` set while `mode` is not `require`/`verify-ca`/`verify-full` | reject — material demands a TLS-mandatory mode |
| R3 | PG, no connectionstring: `cert` set XOR `key` set | reject (the pre-existing check, now running after R1/R2) |
| R4 | PG with connectionstring: any of `mode`/`cert`/`key`/`ca` set | reject — the block never reaches the DSN |
| R5 | Oracle: any of `mode`/`cert`/`key`/`ca` set | reject (extends the previous cert/key/ca check to `mode`) |
| R6 | PG, no connectionstring (2026-09-13 amendment): any `host` entry an absolute path while any of `cert`/`key`/`ca` is set or `mode` is set and not `disable` | reject — pgx skips TLS on a unix socket; runs after R1, before R2/R3 |

Check order is load-bearing: R4 short-circuits so a connection-string config
gets the "move it into the DSN" message rather than a mode complaint; R1 precedes
R2 so a typo'd mode is reported as a mode problem; R3 runs last so a partial pair
under a mandatory mode gets the pairing message.

**Still allowed**: on a TCP host, every valid mode **without** material — `disable`, `allow`
and `prefer` included, since opportunistic TLS with nothing to discard is a
legitimate operator choice (on a unix-socket host only an unset mode or `disable`, per R6); `require`/`verify-ca`/`verify-full` with any paired
material; CA-only, and cert+key+CA, under a mandatory mode.

**Escape hatch**: an operator who genuinely wants pgx-native semantics these
rules refuse (e.g. `prefer` plus a client certificate) can put the ssl
parameters in a `connectionstring` — pgx semantics apply there verbatim, because
go-bricks passes that DSN through untouched.

## Alternatives considered

- **Silently lowercase `Mode`** (`Require` → `require`). Rejected: Explicit >
  Implicit. pgx itself is case-sensitive, and a normalizing shim hides the typo
  instead of teaching the correct value.
- **File-existence checks on `cert`/`key`/`ca`.** Rejected as a startup check:
  paths are routinely provisioned after config load (init containers, mounted
  secrets), `ca: system` is a sentinel rather than a path, and pgx reports
  unreadable files clearly at connect time.
- **Reject `allow`/`prefer` outright.** Rejected: without material there is no
  silently-discarded intent, so this would break benign configs for no gain.
- **Warn instead of fail** on material under a non-mandatory mode. Rejected: the
  framework's posture is fail-fast at startup (ADR-047, ADR-049, ADR-054), and a
  WARN in a boot log is precisely how the current fail-open survived.

## Consequences

- **Breaking.** Configurations that booted green now abort at startup. That is
  the point — each rejected shape was doing something other than what it
  advertised — but every rejection carries a `Field` of `database.tls` (or
  `database.tls.mode`) and an `Action` naming the fix. See
  [migrations.md](migrations.md) `[C59.11]`.
- The Oracle rejection widened from "cert/key/ca" to the whole block. `[C42.1]`
  documented that `database.tls.mode` alone still passed for Oracle; it no
  longer does. That atom stays as the historical record of PR #582.
- Rules R1–R4 fire wherever `validateVendorSpecificFields` runs: the primary
  database, every named database, and every static tenant entry. They do **not**
  reach a config that never passes through `config.Validate`.
- **Dynamic `DBConfigProvider` records are covered since PR #1002 merged.**
  `ApplyDatabasePoolDefaults` routes every dynamic config through
  `validateVendorSpecificFields` (atom C59.6), so R1–R5 and the trim fire on
  that seam too — at connection acquisition rather than at boot.
- **Unrecognized-scheme DSNs are not covered.** `validateVendorSpecificFields`
  dispatches on `cfg.Type`, and its `default` arm returns nil. A connection
  string whose scheme is unrecognized — legal for deployments supplying
  `Options.DatabaseConnector` (ADR-050) — plus a `tls:` block still boots with
  the block inert. Closing that needs a rule that does not depend on a resolved
  vendor; tracked as a follow-up. Narrowed 2026-09-13: a keyword/value DSN now
  infers `postgresql`, so it reaches R4's co-presence refusal — the only rule
  here a connectionstring section can hit, since R1–R3 and R6 require no
  connectionstring and R5 is Oracle-only — and the raw-DSN host rules of
  `[C65.2]` (ADR-050 amendment, `[C65.3]`); only a DSN that infers no vendor at
  all is still uncovered.
- **The `tools/migration` CLI was not covered at the time of this ADR; closed by
  #1006.** `loadTenantStoreFromFile` koanf-unmarshals its source config without
  ever calling `config.Validate`, and `controlPlaneDSN` emits
  `sslmode`/`sslrootcert` from the same `TLSConfig` struct unvalidated. The CLI
  now wraps its `DBConfigProvider` in a validator that calls the exported
  `config.ApplyDatabasePoolDefaults` (`tools/migration/internal/commands/dbtls.go`),
  so these rules reach the CLI with no second copy to keep in sync. That seam is
  deliberately wider than `database.tls`: it also infers a missing `type` from a
  recognized `connectionstring` scheme and enforces Oracle's connection-identifier
  rules and `time.LoadLocation` on `database.timezone`. Accepting a config the
  service would refuse to boot on is the trap being closed, so the wider net is the
  point. Validation runs on a copy, because `config.TenantStore` returns a cached
  shared pointer for the single-tenant and `named:` keys and the seam normalizes in
  place. The CLI pins a released go-bricks, so the rules land at each pin bump
  rather than at merge.

  **[HISTORICAL — superseded by the 2026-08-24 amendment above.]** #1006 also
  closed the second half of the same gap: `database.tls` used to be
  **inert on the Flyway path**, because `buildEnvironmentVariables` forwarded only
  host/port/user/password/database — so a `verify-full` block validated cleanly
  while the migration ran with whatever TLS the operator's `flyway.conf` URL
  specified, possibly none. PostgreSQL TLS settings are now exported as
  `DB_SSLMODE`/`DB_SSLROOTCERT`/`DB_SSLCERT`/`DB_SSLKEY` (unset ones omitted, so a
  conf interpolating them unconditionally never receives `sslmode=`). The residual
  boundary is structural and cannot be validated away: the JDBC URL belongs to the
  operator and the framework does not parse it, so a conf that ignores those
  variables still migrates without TLS. That case is now a WARN on every run
  carrying `database.tls`, which is the strongest honest signal available —
  fail-closed would require parsing arbitrary Flyway configuration and would
  false-positive on URLs assembled outside the conf file. Tracked as #1047; the
  only option that can actually guarantee TLS is for the framework to construct
  the JDBC URL and pass `-url=` itself, which takes URL ownership from every
  consumer and so needs its own ADR.

- A `database:` block containing **only** `tls.*` fields remains invisible to
  `IsDatabaseConfigured` and is silently ignored, because none of the TLS keys
  is an ADR-047 identity marker. Unchanged by this ADR; a follow-up.
- `pgSSLModes` mirrors pgx v5.10.0's `configTLS` switch. On a pgx major bump — or
  a minor that adds an sslmode — re-diff `pgconn/config.go` against the
  allowlist.

## Related ADRs

- [ADR-047](adr_047_database_absence_vs_misconfiguration.md) — database absence
  vs misconfiguration; the identity-marker seam this ADR deliberately does not
  widen.
- [ADR-050](adr_050_connectionstring_type_inference.md) — connection-string type
  inference; the reason vendor validation runs at all in connectionstring mode,
  and the source of the custom-connector exemption noted above.
