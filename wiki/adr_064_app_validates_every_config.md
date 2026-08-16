# ADR-064: The App Validates Every Config It Is Handed

**Status:** Accepted
**Date:** 2026-08-16

## Context

`config.Load` validates; `app.NewWithConfig` did not. ADR-050 documented the
obligation — "hand-built configs must run `config.Validate` before
`NewWithConfig`" — but nothing enforced it, so parallel machinery softened the
bypass: `app.Builder.ConfigureRuntimeHelpers` re-walked the database tree for
untyped DSNs, `app/managers.go` mirrored config defaults ("kept in sync with
config/validation.go"), `messaging.NewMessagingManager` carried a
single-tenant-only fallback, and `app/lifecycle.go` guarded cleanup intervals
"only for Validate-bypassing callers". Every mirror was drift risk, and the
mode-blind ones could not honor the multi-tenant defaults.

## Decision

`app.Builder.WithConfig` runs `config.Validate` on the config it receives.
Every construction path — `New`, `NewWithOptions`, `NewWithConfig`, direct
`Builder` use — therefore validates and stamps defaults. `Validate` is
idempotent, so revalidating `config.Load` output costs microseconds and no
hidden "already validated" state is introduced.

## Consequences

- **Breaking:** a hand-built config that `config.Validate` rejects — missing
  `app.name`/`app.version`, zero server timeouts, an invalid vendor — now
  fails at construction instead of booting on whatever the mirrors papered
  over. Remedy per field is the `ConfigError`'s own action line. See
  [migrations.md](migrations.md) [C59.12].
- The app-side mirrors become dead weight and are deleted in the follow-up PR
  (`app/managers.go` default constants, the mode fallbacks in
  `resolveMaxSize`/`resolveIdleTTL`). `lifecycle.go`'s bypass guard is kept —
  only its comment is re-scoped, from describing a Validate bypass to
  describing direct-construction defense-in-depth.
  `messaging.NewMessagingManager` keeps its fallback: it is that standalone
  package's interface default for bare callers, not a mirror.
- Test fixtures must be valid configs — a fixture that could not boot in
  production should not boot in a test.
