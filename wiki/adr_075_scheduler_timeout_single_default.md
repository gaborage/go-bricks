# ADR-075: One normalized default per scheduler timeout key

> **Amendment (2026-09-02, #1315).** The deferred `scheduler.timezone` collapse is
> done. `Init` refuses an empty value with the same did-not-pass-normalization
> error class the timeouts use, records host-local for `"-"` without loading,
> and otherwise calls `time.LoadLocation` once and stores the `*time.Location`
> for the lazy gocron construction. The empty-string use-time fallback is gone,
> so the module has one timezone door, at Init, and an unloadable zone fails
> there instead of at first job registration.

- **Status**: Accepted
- **Date**: 2026-08-20
- **Related**: [ADR-064](adr_064_app_validates_every_config.md) — every APPLICATION construction path (`app.New`, `app.NewWithConfig`, `Builder.WithConfig`) runs `config.Validate`, which is what makes normalization the single place a default can live. It does not cover a `*config.Config` handed straight to `Module.Init` or `app.NewModuleRegistry`; that gap is why Init enforces its own precondition below

## Context

`scheduler.timeout.shutdown` and `scheduler.timeout.slowjob` had two defaults each.
The koanf loader advertised 30s and 25s; the scheduler module carried its own
`defaultShutdownTimeout` (30s) and `defaultSlowJobThreshold` (**30s**) and applied
them at use time behind `> 0` guards. Neither key was ever normalized.

The two copies had already drifted. A YAML- or env-loaded deployment ran a 25s
slow-job threshold while a config assembled in Go ran 30s — same key, same
release, different behavior depending on how the config was built. Nothing
detected the divergence, because each copy was correct in its own file.

The module's `> 0` guard also silently absorbed negative values, and the shipped
godoc for `SlowJob` documented that as a feature ("Zero or negative = disabled").
It never was: `threshold` was seeded from the 30s constant and only overwritten
when the configured value was positive, so a negative value ran the default and
the accompanying `threshold > 0` check could never be false.

## Decision

The normalize phase owns both keys. `normalizeScheduler` applies them through
`applyNonNegativeDefault`, the same helper every other duration key in the config
package uses: zero applies the default, negative fails validation naming the key.
25s wins for slowjob — the advertised value every YAML deployment already ran —
and shutdown stays 30s. The koanf loader renders those same two constants rather
than repeating their values as strings, so one edit moves the default everywhere.

The scheduler module trusts the normalized config: both constants and both
use-time guards are gone.

That trust needs an enforced precondition. Reading a timeout with no fallback
turns a nil `deps.Config` from a defaulted value into a nil dereference — a hard
panic in `Shutdown`, and in `determineJobSeverity` a panic the job-execution
recover catches and reports as a failed, panicking job for every job that in fact
SUCCEEDED. So `Init` now rejects a nil `deps.Config`, and the module's two
remaining `m.config != nil` guards are removed: the invariant is stated once,
enforced once, at the only door.

## Consequences

- A config assembled in Go and handed to `app.NewWithConfig` / `Builder.WithConfig`
  gets 25s for slowjob where it used to get 30s. Framework construction paths all
  run `config.Validate`, so normalization reaches them.
- A negative value for either key now aborts startup instead of running the
  default. The `SlowJob` godoc that suggested writing one is corrected; deployments
  that followed it are the realistic hits.
- The slow-job WARN can no longer be disabled. It never could — the disable path
  the godoc described was unreachable.
- `scheduler.Module.Init` requires a normalized `deps.Config` — non-nil, with both
  timeouts positive. A `*config.Config` handed straight to `Module.Init` or
  `app.NewModuleRegistry` is never normalized, and zero timeouts are the damaging
  shape: a zero shutdown budget makes `time.After(0)` race the drain signal, so
  `Shutdown` abandons in-flight jobs to the teardown of the database and messaging
  resources they are still using, and a zero slow-job threshold logs every
  successful job at WARN. Rejecting it at Init keeps the precondition where the
  fallbacks used to be.
- `scheduler.timezone` keeps its use-time fallback. It is normalized too, so the
  fallback is redundant rather than divergent; collapsing it belongs with the
  broader owned-key derivation work, not here. (Superseded by the amendment
  above: the fallback is collapsed.)

Migration: [C60.12](migrations.md).

## Alternatives considered

**Keep the module fallbacks and fix only the value.** Setting the module's
`defaultSlowJobThreshold` to 25s would have made the two copies agree today and
left them free to diverge again — the failure this ADR exists to remove.

**Normalize but keep the `> 0` guards.** The guards would then be unreachable code
asserting a state normalization forbids, and they would keep absorbing negatives
that the operator meant as configuration.

**Default `deps.Config` to an empty config in `Init` instead of failing.** That
re-creates a second default — an empty config's zero timeouts — under a different
name, and it is silent: the deployment boots and misreports every job.
