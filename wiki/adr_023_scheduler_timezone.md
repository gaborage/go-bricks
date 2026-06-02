# ADR-023: Scheduler Timezone Configuration

**Status:** Accepted
**Date:** 2026-06-02

## Context

Scheduled jobs (`scheduler` package) had no timezone knob. The framework called
`gocron.NewScheduler()` with no options, so gocron defaulted to `time.Local` —
every job fired in the host's timezone, which in containers is UTC only by
accident (whatever `TZ`/the base image sets) and drifts across environments
exactly as ADR-016 describes for database sessions. A vestigial
`ScheduleConfiguration.Timezone *time.Location` field existed but was never read
or written. The triggering use case: a user needed to configure the timezone in
which scheduled jobs run.

## Decision

Add `scheduler.timezone`, a single optional config field on `SchedulerConfig`,
applied **scheduler-wide** via gocron's `WithLocation`. The contract mirrors
`database.timezone` (ADR-016) so the framework has exactly one timezone idiom.

| Value | Result |
|-------|--------|
| Unset (`""`) | Defaulted to `"UTC"` during `config.Validate()` |
| `"UTC"`, `"America/New_York"`, … | Validated via `time.LoadLocation`; applied scheduler-wide |
| `"-"` (sentinel) | `WithLocation` omitted → gocron uses `time.Local` (legacy) |
| Anything `time.LoadLocation` rejects (incl. numeric offsets) | Validation error at startup (fail-fast) |

### Why scheduler-wide, not per-job

gocron's `location` is a scheduler-level option; per-job timezones are only
possible by routing every job through a raw `CronJob` with a `CRON_TZ=<zone>`
crontab prefix. A single `WithLocation` at scheduler construction makes
Daily/Weekly/Monthly (via at-times) and Hourly (via a `CRON_TZ` prefix) all
honor the zone with no changes to schedule construction. `FixedRate`
(`DurationJob`) is correctly timezone-independent.

## Consequences

- **Breaking behavior change.** An unset zone now means UTC instead of
  `time.Local`. Deployments relying on host-local job times set
  `scheduler.timezone: "-"` to preserve the old behavior. Same posture as
  ADR-016.
- **No registration API change.** `DailyAt`/`WeeklyAt`/`MonthlyAt` keep their
  `localTime` parameter; its hours/minutes are now interpreted in the configured
  zone. Doc comments updated accordingly.
- **Dead code removed.** `ScheduleConfiguration.Timezone` deleted.
- **Shared validation.** `normalizeIANATimezone` is shared by `database.timezone`
  and `scheduler.timezone`, so the two contracts cannot drift. The default
  constant is `config.DefaultTimezone` (the database layer keeps
  `DefaultDatabaseTimezone` as an alias).
- **Observability.** The active zone appears in the "Scheduler initialized" log
  and in `meta.timezone` of `GET /_sys/job`.

### Rejected alternatives

| Alternative | Why rejected |
|-------------|--------------|
| Per-job timezones | gocron has no per-job `WithLocation`; would force raw `CRON_TZ` cron strings. Out of scope for the stated need (YAGNI). |
| Code-based `NewModule(WithTimezone(...))` | Not 12-factor; can't change per environment without recompile; diverges from the `database.timezone` config precedent. |
| Default to host-local | Perpetuates the cross-environment drift this change exists to kill. The `"-"` sentinel preserves the escape hatch without making it the default. |
| Numeric offsets (`+05:00`) | `time.LoadLocation` rejects them; use IANA `Etc/GMT∓N` (inverted sign), same as ADR-016. |
