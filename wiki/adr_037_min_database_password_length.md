# ADR-037: Minimum Database Password Length

**Status:** Accepted
**Date:** 2026-07-10

## Context

The migration engine's `redactPassword` (migration/flyway.go) scrubs the database
password from Flyway's stdout/stderr so connection-string echoes in error logs don't
leak credentials. A password shorter than 8 bytes cannot be safely substring-redacted —
short needles collide with unrelated output bytes — so the engine suppresses the **whole**
output instead of risking partial redaction.

After ADR-less #674 made the migrate path fail on unparsable output, that suppression
turned a **successful** migration with a short DB password into a hard failure, and
`migration.applied` audited it as `Outcome=failed` with an empty version — an ADR-019
audit false-negative. #676 hardened redaction for `≥8`-byte passwords but deliberately
kept the `<8` suppression as a safe fallback.

Nothing enforced a password-length floor: `config.Validate` checked host/port/database/
username but never the password, and the stale `minRedactablePasswordLength` comment
claimed a config-validation minimum that did not exist. Per-tenant configs (from a tenant
store or AWS Secrets Manager) never pass through `config.Validate` at all.

## Decision

Reject a **non-empty** database password shorter than `config.MinDatabasePasswordLength`
(8) at **both** boundaries:

1. **`config.Validate`** — for static config (primary `database.*` and named
   `databases.*`), failing fast at startup with a `database.password` field error that
   never echoes the value.
2. **The migrate path (`FlywayMigrator.runFor`)** — rejects before running Flyway with
   `ErrDatabasePasswordTooShort`, covering per-tenant configs that never reach
   `config.Validate`. `migration.redactPassword`'s `minRedactablePasswordLength` is
   single-sourced from `config.MinDatabasePasswordLength`.

**Empty passwords are exempt** (trust / IAM auth): they never reach the redaction-
suppression path (`redactPassword` returns the output untouched for an empty password).

## Consequences

- **Breaking:** a static config with a 1–7 byte DB password that previously booted now
  fails at startup; a per-tenant migration with such a password fails with a clear error
  instead of a suppressed-output false-negative. Documented in migrations **E50 (C50.2)**.
- The `<8` whole-output suppression in `redactPassword` remains only as a fallback for the
  non-migrate (info/validate) verbs, whose output is not parsed.
- The audit false-negative from #674 is closed for both single- and multi-tenant paths.

## Alternatives considered

- **Config validation only** — rejected: static-only, misses per-tenant runtime configs,
  which are the primary source of the audit false-negative.
- **Migration boundary only** — rejected: no startup fail-fast for a static config; the
  operator would only learn of the problem at first migrate.
- **Reject empty passwords too** — rejected: breaks legitimate trust/IAM-auth deployments,
  and empty passwords never trigger the redaction-suppression path this ADR addresses.
