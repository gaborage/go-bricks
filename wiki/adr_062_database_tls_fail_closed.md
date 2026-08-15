# ADR-062: Fail Closed on `database.tls` Misconfiguration (Mode Allowlist + Material/Mode Coherence)

**Status:** Accepted
**Date:** 2026-08-14

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

Validate `database.tls` at startup and reject every shape pgx would discard or
downgrade. All four fields are `TrimSpace`d once in
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

Check order is load-bearing: R4 short-circuits so a connection-string config
gets the "move it into the DSN" message rather than a mode complaint; R1 precedes
R2 so a typo'd mode is reported as a mode problem; R3 runs last so a partial pair
under a mandatory mode gets the pairing message.

**Still allowed**: every valid mode **without** material — `disable`, `allow`
and `prefer` included, since opportunistic TLS with nothing to discard is a
legitimate operator choice; `require`/`verify-ca`/`verify-full` with any paired
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
- **Dynamic `DBConfigProvider` records are not covered yet.**
  `DbManager.createConnection` applies only `ApplyDatabasePoolDefaults`, which
  never calls the vendor gate — a provider-returned config with `prefer` plus
  cert/key connects ungated and untrimmed. Open PR #1002 routes exactly that
  seam through `validateVendorSpecificFields`; these rules cover dynamic
  records the moment it lands.
- **Unrecognized-scheme DSNs are not covered.** `validateVendorSpecificFields`
  dispatches on `cfg.Type`, and its `default` arm returns nil. A connection
  string whose scheme is unrecognized — legal for deployments supplying
  `Options.DatabaseConnector` (ADR-050) — plus a `tls:` block still boots with
  the block inert. Closing that needs a rule that does not depend on a resolved
  vendor; tracked as a follow-up.
- **The `tools/migration` CLI is not covered.** `loadTenantStoreFromFile`
  koanf-unmarshals its source config without ever calling `config.Validate`, and
  `controlPlaneDSN` emits `sslmode`/`sslrootcert` from the same `TLSConfig`
  struct unvalidated. These rules therefore neither protect nor break
  `go-bricks-migrate` source configs.
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
