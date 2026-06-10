# ADR-027: Wire `database.tls.cert/key/ca` Into the Drivers (Fail Closed on Oracle)

**Status:** Accepted
**Date:** 2026-06-10

## Context

`config.TLSConfig` advertises four fields — `mode`, `cert`, `key`, `ca` (documented in `config.example.yaml`) — but only `mode` was ever consumed. The PostgreSQL DSN builder appended `sslmode=<mode>` and nothing else; `CertFile`, `KeyFile`, and `CAFile` had **zero** non-test references. The Oracle connector referenced `cfg.TLS` nowhere at all.

Security impact (a High finding from the 2026-06-10 audit):

- An operator setting `mode: require` **plus** `ca: /etc/ssl/db-ca.pem` reasonably expects libpq-style upgrade to `verify-ca` (which pgx performs when `sslrootcert` is supplied). Instead the CA was dropped and the connection was **encrypted but unauthenticated** — i.e. MITM-able.
- mTLS via `cert`/`key` was silently impossible on PostgreSQL.
- For Oracle, even `mode` did nothing, and `cert/key/ca` were silently ignored.

No validation rejected these fields, so the degradation was completely silent.

Separately, the existing `sslmode=%s` interpolation was unquoted (Low finding: a `mode` value containing whitespace/quotes could inject extra DSN parameters).

## Decision

**PostgreSQL** — `buildPostgresDSN` now emits the full TLS material when configured: `sslrootcert` (CAFile), `sslcert` (CertFile), and `sslkey` (KeyFile) alongside `sslmode`, with **every** value (including `sslmode`) run through `quoteDSN`. pgx then loads the certs and performs the expected verification. Quoting closes the DSN-injection vector in the same change. The `ConnectionString` escape hatch is unchanged (the operator owns the full DSN there). The migration CLI's parallel control-plane DSN builder (`tools/migration` `controlPlaneDSN`) is fixed identically (it had the same sslmode-only gap), there via URL query params.

Config validation also now rejects a **half-configured client certificate** for PostgreSQL — `sslcert` without `sslkey` or vice versa (`validatePostgreSQLFields`) — because pgx silently drops a lone one under `sslmode=disable`, so the pairing is checked up front rather than failing far from the config site.

**Oracle** — TLS over `tcps`/wallet is not implemented, so rather than silently ignore TLS material, config validation now **rejects** `database.tls.cert/key/ca` for Oracle connections (`validateOracleFields`). `mode` alone is still accepted (it is a harmless no-op today) to avoid breaking existing Oracle configs that set it. Failing closed prevents an operator from believing an Oracle connection is authenticated when it is not.

## Consequences

**Breaking (intended):**

- A PostgreSQL deployment that set `mode: require` + `ca:` and "worked" was connecting **unauthenticated**. It now upgrades to CA verification, so a wrong/missing/mismatched CA will now make the connection **fail** instead of silently succeeding. This is the correct behavior — review your `ca` path and server certificate before upgrading.
- An Oracle deployment that set `database.tls.cert/key/ca` (which did nothing) now **fails validation at startup**. Remove those fields for Oracle.

**Non-breaking:**

- PostgreSQL configs without `cert/key/ca` are unaffected (only `sslmode` is emitted, now quoted).
- `database.tls.mode` continues to work as before on both vendors (no-op on Oracle).

See [migrations.md](migrations.md) for the upgrade note.
