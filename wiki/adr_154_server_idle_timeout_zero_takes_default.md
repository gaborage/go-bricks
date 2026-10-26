# ADR-154: A Zero Server Idle Timeout Takes the Default, and a Negative Fails

**Status:** Accepted
**Date:** 2026-10-25

## Context

Config validation never read `server.timeout.idle`. `checkServer` rejected
`server.timeout.{read,write,middleware,shutdown}` when zero or negative, and
`normalizeServer` filled only `server.bodylimit`; the idle default existed only
as a hand-written koanf default. `server/server.go` (`applyServerTimeouts`)
copies the value onto `http.Server.IdleTimeout` on the application listener and
the [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md) probe
listener alike, and net/http gives both edge values a meaning of its own:

- a zero means "use `ReadTimeout`", so an explicit zero (`SERVER_TIMEOUT_IDLE=0s`,
  YAML `idle: 0s`) and a hand-built `*config.Config` handed to `app.NewWithConfig`
  or `Builder.WithConfig` that left the field zero ran at the read timeout (15s
  by default), not at the idle default;
- a negative means no idle timeout at all, so an idle keep-alive connection was
  held until the peer closed it.

Neither shape produced an error or a log line.
[ADR-064](adr_064_app_validates_every_config.md) already runs `config.Validate`
on every application construction path, so a rule in the normalize phase reaches
each of them; the idle key had none. The 75s default itself is #2118's (#2149),
not this ADR's. Fixes #2144.

## Decision

**[ADR-075](adr_075_scheduler_timeout_single_default.md)'s duration rule —
zero takes the framework default, a negative is an operator error — governs
`server.timeout.idle` on every config door.**

`normalizeServer` applies it through `applyNonNegativeDefault`, the helper the
other defaulted duration keys use:

- a zero, unset or explicit, becomes `DefaultServerIdleTimeout` (75s), whichever
  door built the config: `config.Load`, `config.Validate`, `app.NewWithConfig`,
  `Builder.WithConfig`;
- a negative fails `Load` and `Validate` with a `*config.ConfigError` whose
  `Field` is `server.timeout.idle` and whose `Message` is `must be non-negative`;
- a positive value is untouched.

`checkServer` gains a mirroring `Idle < 0` guard for callers that reach it
directly; on the `Validate` path normalization has already decided.
`server.timeout.idle` moves from `koanfOnlyDefaults` to `derivedDefaultKeys`,
so the koanf default (`1m15s`) is rendered from the normalize fill, the
mechanism `server.bodylimit` uses with `DefaultBodyLimitBytes`, and one
constant decides the value on every door.

Idle is not in the fail-on-zero set. Read, write, middleware and shutdown stay
required-positive and hand-written in `koanfOnlyDefaults`: a zero there has no
safe meaning, while a zero idle has one natural reading, the default, which
net/http replaced with the read timeout.

## Alternatives considered

- **Reject zero, like read and write.** Rejected: the koanf default always
  supplies a value, so only an explicit `0s` or a hand-built config reaches the
  check, and both mean "the default". Refusing them breaks every fixture that
  never set idle for no safer outcome.
- **Default it in `server`.** Rejected: a fallback in `applyServerTimeouts` is a
  second copy of the default outside `config`, the drift ADR-075 removed, and a
  negative would still pass `Validate`.

## Consequences

- **Breaking**, for three shapes that booted silently before:
  - a negative idle now fails startup instead of disabling the idle timeout; to
    keep idle connections open for a long time, set a large positive value;
  - an explicit zero moves from the read timeout (15s by default) to 75s;
  - a hand-built config that leaves idle zero moves from the read timeout to 75s.
- Both listeners get the new value; the `server` package does not change.
- A caller that hands `server.New` or `server.NewWithOptions` a config that never
  passed `config.Validate` still reaches net/http with whatever it set; the
  doors covered are the ones ADR-064 validates.
- The other `server.timeout.*` keys, their defaults and their zero rule are
  unchanged.

## References

- `config/server_section.go` — `normalizeServer`, `checkServer`
- `config/config.go` — `derivedDefaultKeys`, `koanfOnlyDefaults`
- `config/defaults.go` — `DefaultServerIdleTimeout`, `applyNonNegativeDefault`
- [ADR-064](adr_064_app_validates_every_config.md) — every construction path
  runs `config.Validate` (amended 2026-10-25 for this key)
- [ADR-075](adr_075_scheduler_timeout_single_default.md) — the duration rule
- [ADR-120](adr_120_internal_probe_listener_and_minimal_ready_body.md) — the
  probe listener that shares the value
- gaborage/go-bricks#2118, #2149 — the 75s default
- See [migrations.md](migrations.md) `[C73.8]`.
