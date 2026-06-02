# Design: Service-Wide Scheduler Timezone Configuration

**Status:** Approved (brainstorming) — pending implementation plan
**Date:** 2026-06-02
**Area:** `scheduler`
**Related precedent:** [ADR-016](../../../wiki/adr_016_database_session_timezone.md) (database session timezone)

## Problem

A user needs to configure the timezone in which scheduled jobs run. Today GoBricks
does **not** support this:

- `scheduler/module.go` calls `gocron.NewScheduler()` with **no options**, so gocron
  falls back to its default `location: time.Local` (verified in gocron v2.21.2
  `scheduler.go:161`). Every job therefore fires in the **host's local timezone** —
  which in containers is UTC only by accident (whatever `TZ`/the base image sets),
  and drifts between dev/staging/prod exactly as ADR-016 describes for databases.
- `ScheduleConfiguration.Timezone *time.Location` exists (`scheduler/schedule.go`) but
  is **dead code**: `scheduleWithGocron` never reads it and no registration method ever
  sets it. Its own comment says *"For MVP, this is always nil (local time). Future
  enhancement: allow explicit timezone specification."*
- `SchedulerConfig` (`config/types.go`) has only `Security` and `Timeout` sub-structs —
  there is no `scheduler.timezone` key.

The `DailyAt`/`WeeklyAt`/`MonthlyAt` registration methods take a parameter literally
named `localTime` whose hour/minute are interpreted in `time.Local`. The capability the
user wants — "run jobs in a chosen timezone" — has no knob.

## Decision

Add `scheduler.timezone`, a single optional config field on `SchedulerConfig`, applied
**scheduler-wide** via gocron's `WithLocation`. The contract mirrors `database.timezone`
(ADR-016) one-for-one so the framework has exactly one timezone idiom.

### Scope decisions (locked during brainstorming)

| Decision | Choice | Rationale |
|---|---|---|
| Granularity | **One zone for the whole service** | Maps directly to gocron's scheduler-wide `WithLocation`; matches the user's need. Per-job/per-tenant are non-goals. |
| Configuration surface | **Config key** (`scheduler.timezone`) | 12-factor, env-overridable (`SCHEDULER_TIMEZONE=...`), sits beside existing `scheduler.security.*` / `scheduler.timeout.*`, matches ADR-016. |
| Default when unset | **`UTC`** | Consistent with `database.timezone`; kills cross-environment drift. Accepted as a documented breaking change. |
| Opt-out | **`-` sentinel → host-local** | Same escape hatch as ADR-016; preserves gocron's current `time.Local` behavior for anyone who relied on it. |
| Invalid value | **Fail-fast at startup** | `time.LoadLocation` validation in `config.Validate()`. |

### Behavior contract

| Value | Result |
|---|---|
| Unset (`""`) | Defaulted to `"UTC"` during `config.Validate()` |
| `"UTC"`, `"America/New_York"`, `"Asia/Tokyo"`, … | Validated via `time.LoadLocation`; applied scheduler-wide through `gocron.WithLocation` |
| `"-"` (sentinel) | `WithLocation` omitted → gocron uses `time.Local` (legacy behavior) |
| Anything `time.LoadLocation` rejects (incl. numeric offsets like `+05:00`) | Validation error at startup. For fixed offsets use IANA `Etc/GMT∓N` (note inverted sign), same as ADR-016. |

## Detailed Design

### 1. Config field (`config/types.go`)

Add to `SchedulerConfig`:

```go
// Timezone is the IANA timezone name the scheduler uses to interpret
// wall-clock schedules (DailyAt/WeeklyAt/MonthlyAt/HourlyAt). Validated via
// time.LoadLocation at startup (fail-fast on invalid names).
// Default: "UTC". Set to "-" to use the host's local time (legacy behavior).
Timezone string `koanf:"timezone" json:"timezone" yaml:"timezone" toml:"timezone" mapstructure:"timezone"`
```

No entry is added to `loadDefaults` (`config/config.go`) — defaulting happens in
`Validate()`, exactly as `database.timezone` does (the existing `scheduler.timeout.*`
keys are defaulted in `loadDefaults`, but the timezone semantics need the
empty-vs-`-`-vs-IANA branching, so it belongs in `Validate()`).

### 2. Validation with a shared helper (`config/validation.go`)

`applyDatabaseTimezoneDefault` currently inlines the "empty→UTC, `-`→passthrough, else
`LoadLocation`-or-error" logic. Extract that core into a small shared helper so the DB
and scheduler validators do not diverge (DRY without over-abstraction):

```go
// normalizeIANATimezone applies the UTC default and validates an IANA timezone
// string. The "-" sentinel passes through unchanged (callers decide what
// "disabled" means). field labels the validation error.
func normalizeIANATimezone(field, value string) (string, error) {
    if value == "" {
        value = DefaultTimezone // "UTC" (reuse/rename existing DefaultDatabaseTimezone)
    }
    if value == TimezoneDisabledSentinel { // "-"
        return value, nil
    }
    if _, err := time.LoadLocation(value); err != nil {
        return value, newTimezoneValidationError(field, value, err)
    }
    return value, nil
}
```

- `applyDatabaseTimezoneDefault` becomes a thin caller of the helper for
  `"database.timezone"`.
- A scheduler equivalent (inline in `Validate()` or `applySchedulerTimezoneDefault`)
  calls it for `"scheduler.timezone"`, writing the normalized value back to
  `cfg.Scheduler.Timezone`. Wire this into `Validate()` next to the existing database
  timezone defaulting.
- Constant naming (`DefaultDatabaseTimezone` → shared `DefaultTimezone`, keep
  `TimezoneDisabledSentinel`) is an implementation detail for the plan; both current
  values are `"UTC"` and `"-"`.

### 3. Scheduler wiring (`scheduler/module.go`)

The entire runtime change is in `ensureSchedulerInitialized()`:

```go
opts := []gocron.SchedulerOption{}
tz := config.DefaultTimezone // "UTC" — also the effective default when config is nil
if m.config != nil && m.config.Scheduler.Timezone != "" {
    tz = m.config.Scheduler.Timezone
}
if tz != config.TimezoneDisabledSentinel {
    loc, err := time.LoadLocation(tz) // already validated at startup; defensive
    if err != nil {
        return fmt.Errorf("scheduler: invalid timezone %q: %w", tz, err)
    }
    opts = append(opts, gocron.WithLocation(loc))
}
s, err := gocron.NewScheduler(opts...)
```

**Why no per-schedule-type changes are needed.** gocron threads its scheduler
`location` into `convertAtTimesToDateTime` for Daily/Weekly/Monthly jobs and injects a
`CRON_TZ=<zone>` prefix for the Hourly `CronJob`, all inside
`definition.setup(&j, s.location, …)` (gocron v2.21.2 `job.go`/`scheduler.go`). Setting
the location once at construction makes every existing schedule type honor the zone, so
`scheduleWithGocron` is untouched. `FixedRate` (`DurationJob`) is correctly
timezone-independent — an interval is an interval, unaffected by zone or DST.

**Effective-default edge:** in production `config.Validate()` guarantees
`Scheduler.Timezone` is non-empty (`"UTC"`, an IANA name, or `"-"`). The only path with a
nil/empty config is tests that construct `Module` without a validated config; those
resolve to `UTC`. Existing scheduler tests will be audited and made timezone-explicit
(set `"-"` where they intend host-local).

### 4. Registration API semantics (`scheduler/module.go`, `app/module.go`)

- **No signature changes** to `FixedRate`/`DailyAt`/`WeeklyAt`/`HourlyAt`/`MonthlyAt` or
  the `JobRegistrar` interface. The methods already extract only `hour, minute` via
  `.Clock()`; those wall-clock numbers are now interpreted in the configured zone.
- **Doc-comment updates only:** change "local time" → "wall-clock time in the
  scheduler's configured timezone (default UTC)" on the registration methods and the
  `app.JobRegistrar` interface. Keep the `localTime` parameter name (it remains accurate —
  "local to the scheduler's zone") to avoid signature churn.

### 5. Remove dead code (`scheduler/schedule.go`)

Delete the unused `Timezone *time.Location` field from `ScheduleConfiguration` and its
stale comment. The zone now lives correctly at the scheduler level; a per-schedule field
would mislead readers into thinking per-job timezones are supported.

### 6. Observability & system API

- **Startup log:** add `.Str("timezone", effectiveZone)` to the existing
  "Scheduler initialized" log line in `ensureSchedulerInitialized()`, so operators can
  confirm the active zone. (`effectiveZone` is the resolved string, e.g. `"UTC"`,
  `"America/New_York"`, or `"Local"` for the `-` case.)
- **`/_sys/job` response** (`scheduler/api_handlers.go`): add a top-level service-wide
  `timezone` field to the list response (for debugging — confirmed useful with the
  maintainer). This is **not** per-job.
- **Timestamps unchanged:** `NextExecutionTime` comes from `gocronJob.NextRun()`, which
  already returns the time *in the scheduler's location*; RFC3339 serialization carries
  the offset, so no double conversion. `LastExecutionTime` continues to be stamped in
  `UTC` by `incrementSuccess`/`incrementFailed` — a deliberate, documented distinction
  (machine-readable last-run vs. zone-aware next-run).

## Testing

Mirror ADR-016's regression rigor. camelCase test function names; snake_case
table-case names; append to existing companion test files (no `*_extra_test.go`).

- **Config** (`config/validation_test.go`, table-driven):
  `empty_defaults_to_utc`, `explicit_utc_preserved`, `iana_name_preserved`
  (`America/New_York`), `dash_sentinel_preserved`, `invalid_timezone_rejected`.
- **Scheduler** (`scheduler/module_test.go`):
  - With `scheduler.timezone: "America/New_York"`, register a `DailyAt(...)` and assert
    `gocronJob.NextRun()` equals the expected UTC instant for that NY wall-clock time.
  - `"-"` sentinel → scheduler uses `time.Local` (`WithLocation` omitted).
  - Unset/default → `UTC`.
  - Audit existing scheduler tests for implicit `time.Local` reliance; make explicit.

## Documentation & ADR

- **ADR-023** (`wiki/adr_023_scheduler_timezone.md`) documenting the decision and the
  breaking-change posture. Update the index `wiki/architecture_decisions.md` in the
  **same** change (the index has drifted before — keep it in sync).
- `wiki/scheduler.md`: a "Timezone" section with YAML + env examples and the contract
  table.
- `wiki/migrations.md`: breaking-change entry (scheduler default → `UTC`; opt out with
  `scheduler.timezone: "-"`).
- `CLAUDE.md` scheduler stub + `llms.txt`: one-line mention of `scheduler.timezone`.

## Breaking Change & Migration

Defaulting an unset zone to `UTC` changes runtime behavior for any deployment whose host
`time.Local` is not UTC (jobs shift from host-local wall-clock to UTC wall-clock). This is
the same posture ADR-016 took for DB sessions and is accepted.

```yaml
# OLD (implicit): jobs ran at host-local wall-clock time
scheduler: {}

# NEW (default): jobs run at UTC wall-clock time
scheduler:
  # timezone: UTC  (implicit)

# EXPLICIT zone
scheduler:
  timezone: America/New_York

# OPT-OUT: preserve host-local behavior
scheduler:
  timezone: "-"
```

## Non-Goals (YAGNI)

- Per-job timezones (gocron has no per-job `WithLocation`; would require routing every
  job through `CronJob` with `CRON_TZ` prefixes — explicitly out of scope).
- Per-tenant timezones (the scheduler is process-global, not per-tenant).
- Numeric-offset zones (`+05:00`) — use IANA `Etc/GMT∓N`, same as ADR-016.
- A code-based `NewModule(WithTimezone(...))` override (config is the single source of
  truth).

## Files Touched

| File | Change |
|---|---|
| `config/types.go` | Add `Timezone` to `SchedulerConfig` |
| `config/validation.go` | Extract `normalizeIANATimezone` helper; default+validate `scheduler.timezone` in `Validate()` |
| `scheduler/module.go` | Apply `gocron.WithLocation` in `ensureSchedulerInitialized`; startup-log the zone; doc-comment updates |
| `scheduler/schedule.go` | Remove dead `Timezone *time.Location` field |
| `scheduler/api_handlers.go` | Add top-level `timezone` field to `/_sys/job` response |
| `app/module.go` | Doc-comment update on `JobRegistrar` |
| `config/validation_test.go` | Timezone validation table cases |
| `scheduler/module_test.go` | Zone-application + sentinel + default tests; audit existing tests |
| `wiki/adr_023_scheduler_timezone.md` (new) | ADR |
| `wiki/architecture_decisions.md` | Index entry for ADR-023 |
| `wiki/scheduler.md`, `wiki/migrations.md`, `CLAUDE.md`, `llms.txt` | Docs |
