# Scheduler Timezone Configuration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let operators configure the timezone for all scheduled jobs via a single `scheduler.timezone` config key, defaulting to UTC.

**Architecture:** Add `scheduler.timezone` to `SchedulerConfig`, validated at startup with a helper shared with `database.timezone` (default `UTC`, `-` sentinel for host-local, IANA-validated, fail-fast). Apply it scheduler-wide by passing `gocron.WithLocation` when the lazy scheduler is created — gocron threads its location into every wall-clock schedule type, so no per-schedule code changes are needed. Surface the active zone in the startup log and `GET /_sys/job`.

**Tech Stack:** Go 1.26, `github.com/go-co-op/gocron/v2` v2.21.2, Koanf config, testify.

**Spec:** `docs/superpowers/specs/2026-06-02-scheduler-timezone-design.md`

---

## File Structure

| File | Responsibility | Change |
|---|---|---|
| `config/types.go` | Config structs | Add `Timezone` field to `SchedulerConfig` |
| `config/validation.go` | Config defaulting/validation | Add `normalizeIANATimezone` helper; refactor `applyDatabaseTimezoneDefault` to use it; add `validateScheduler`; call it from `Validate` |
| `config/validation_test.go` | Config tests | Append scheduler-timezone table tests |
| `scheduler/module.go` | Scheduler lifecycle | Add `configuredTimezone`/`schedulerLocationOptions`/`timezoneLabel`; apply location + log zone in `ensureSchedulerInitialized`; doc-comment updates |
| `scheduler/schedule.go` | Schedule config | Remove dead `Timezone *time.Location` field |
| `scheduler/api_handlers.go` | System API | Add `meta.timezone` to `/_sys/job` response |
| `scheduler/test_helpers_test.go` | Test helpers | Add `withTimezone` option |
| `scheduler/module_test.go` | Scheduler tests | Append resolution + integration tests |
| `scheduler/api_handlers_test.go` | API tests | Append timezone-exposure test |
| `app/module.go` | `JobRegistrar` interface | Doc-comment update (`localTime` semantics) |
| `wiki/adr_023_scheduler_timezone.md` | ADR | Create |
| `wiki/architecture_decisions.md` | ADR index | Add entry; bump "through ADR-022" |
| `wiki/scheduler.md`, `wiki/migrations.md`, `CLAUDE.md`, `llms.txt` | Docs | Timezone section + breaking-change note |

---

## Task 1: Config field, shared validation helper, and `Validate()` wiring

**Files:**
- Modify: `config/types.go` (`SchedulerConfig`, ~line 508)
- Modify: `config/validation.go` (`Validate` ~line 102, `applyDatabaseTimezoneDefault` ~line 417)
- Test: `config/validation_test.go` (append near the existing `TestApplyDatabaseTimezone*` tests, ~line 1796)

- [ ] **Step 1: Write the failing tests**

Append to `config/validation_test.go`:

```go
func TestValidateSchedulerTimezoneDefault(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		expectedTimezone string
	}{
		{name: "empty_defaults_to_utc", input: "", expectedTimezone: "UTC"},
		{name: "explicit_utc_preserved", input: "UTC", expectedTimezone: "UTC"},
		{name: "iana_name_preserved", input: "America/New_York", expectedTimezone: "America/New_York"},
		{name: "dash_sentinel_preserved", input: "-", expectedTimezone: "-"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Timezone: tt.input}
			err := validateScheduler(cfg)
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedTimezone, cfg.Timezone)
		})
	}
}

func TestValidateSchedulerTimezoneRejectsInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "unknown_iana_name", input: "Not/AZone"},
		{name: "garbage_string", input: "xyz"},
		{name: "numeric_offset_not_iana", input: "+05:30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SchedulerConfig{Timezone: tt.input}
			err := validateScheduler(cfg)
			assertValidationError(t, err, "scheduler.timezone")
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail (compile error)**

Run: `go test ./config/ -run TestValidateSchedulerTimezone -v`
Expected: FAIL — compile error `undefined: validateScheduler` and `SchedulerConfig has no field Timezone`.

- [ ] **Step 3: Add the `Timezone` field to `SchedulerConfig`**

In `config/types.go`, replace the `SchedulerConfig` struct:

```go
// SchedulerConfig holds job scheduler settings.
type SchedulerConfig struct {
	Security SchedulerSecurityConfig `koanf:"security" json:"security" yaml:"security" toml:"security" mapstructure:"security"`
	Timeout  SchedulerTimeoutConfig  `koanf:"timeout" json:"timeout" yaml:"timeout" toml:"timeout" mapstructure:"timeout"`

	// Timezone is the IANA timezone name the scheduler uses to interpret
	// wall-clock schedules (DailyAt/WeeklyAt/MonthlyAt/HourlyAt). Validated via
	// time.LoadLocation at startup (fail-fast on invalid names).
	// Default: "UTC". Set to "-" to use the host's local time (legacy behavior).
	Timezone string `koanf:"timezone" json:"timezone" yaml:"timezone" toml:"timezone" mapstructure:"timezone"`
}
```

- [ ] **Step 4: Add the shared helper and `validateScheduler`; refactor the DB helper**

In `config/validation.go`, replace `applyDatabaseTimezoneDefault` with the refactored version plus the new shared helper and scheduler validator:

```go
// normalizeIANATimezone defaults an empty timezone to UTC and validates it as a
// loadable IANA name. The "-" sentinel passes through unchanged so callers can
// treat it as "disabled". field labels the validation error. Shared by
// database.timezone and scheduler.timezone so the two contracts cannot drift.
func normalizeIANATimezone(field, value string) (string, error) {
	if value == "" {
		value = DefaultDatabaseTimezone
	}
	if value == TimezoneDisabledSentinel {
		return value, nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return value, NewInvalidFieldError(
			field,
			fmt.Sprintf("invalid IANA timezone %q: %v", value, err),
			[]string{`a valid IANA timezone (e.g. "UTC", "America/New_York") or "-" to disable`},
		)
	}
	return value, nil
}

// applyDatabaseTimezoneDefault sets cfg.Timezone to the default ("UTC") when unset
// and validates the configured value as a loadable IANA timezone. The opt-out
// sentinel "-" skips validation and tells the connection layer to leave session
// timezone untouched.
func applyDatabaseTimezoneDefault(cfg *DatabaseConfig) error {
	normalized, err := normalizeIANATimezone("database.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}

// validateScheduler applies defaults and validates scheduler configuration.
// Currently it normalizes scheduler.timezone (default "UTC", "-" opt-out for
// host-local, IANA validation) so an invalid zone fails fast at startup.
func validateScheduler(cfg *SchedulerConfig) error {
	normalized, err := normalizeIANATimezone("scheduler.timezone", cfg.Timezone)
	cfg.Timezone = normalized
	return err
}
```

Then wire `validateScheduler` into `Validate` (`config/validation.go`). Insert directly after the `validateServer` block:

```go
	if err := validateServer(&cfg.Server); err != nil {
		return fmt.Errorf("server config: %w", err)
	}

	if err := validateScheduler(&cfg.Scheduler); err != nil {
		return fmt.Errorf("scheduler config: %w", err)
	}
```

- [ ] **Step 5: Run the new tests to verify they pass**

Run: `go test ./config/ -run TestValidateSchedulerTimezone -v`
Expected: PASS (all sub-tests).

- [ ] **Step 6: Run the full config suite to confirm the DB refactor regressed nothing**

Run: `go test ./config/...`
Expected: PASS. (The existing `TestApplyDatabaseTimezoneDefault` / `TestApplyDatabaseTimezoneRejectsInvalid` are the regression guard for the refactored `applyDatabaseTimezoneDefault`.)

- [ ] **Step 7: Commit**

```bash
git add config/types.go config/validation.go config/validation_test.go
git commit -m "feat(scheduler): add and validate scheduler.timezone config key"
```

---

## Task 2: Apply the timezone to the gocron scheduler

**Files:**
- Modify: `scheduler/module.go` (`ensureSchedulerInitialized`, ~line 225)
- Modify: `scheduler/test_helpers_test.go` (add `withTimezone`)
- Test: `scheduler/module_test.go` (append)

- [ ] **Step 1: Add the `withTimezone` test option**

Append to `scheduler/test_helpers_test.go` (after `withSlowJobThreshold`):

```go
func withTimezone(tz string) testSchedulerOption {
	return func(d *app.ModuleDeps) { d.Config.Scheduler.Timezone = tz }
}
```

- [ ] **Step 2: Write the failing resolution tests**

Append to `scheduler/module_test.go`:

```go
func TestSchedulerConfiguredTimezone(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		expected string
	}{
		{name: "nil_config_defaults_to_utc", cfg: nil, expected: "UTC"},
		{name: "empty_defaults_to_utc", cfg: &config.Config{}, expected: "UTC"},
		{name: "iana_preserved", cfg: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "America/New_York"}}, expected: "America/New_York"},
		{name: "sentinel_preserved", cfg: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "-"}}, expected: "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Module{config: tt.cfg}
			assert.Equal(t, tt.expected, m.configuredTimezone())
		})
	}
}

func TestSchedulerLocationOptions(t *testing.T) {
	t.Run("sentinel_yields_no_option", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "-"}}}
		opts, err := m.schedulerLocationOptions()
		require.NoError(t, err)
		assert.Empty(t, opts)
	})
	t.Run("iana_yields_one_option", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "America/New_York"}}}
		opts, err := m.schedulerLocationOptions()
		require.NoError(t, err)
		assert.Len(t, opts, 1)
	})
	t.Run("default_utc_yields_one_option", func(t *testing.T) {
		m := &Module{config: &config.Config{}}
		opts, err := m.schedulerLocationOptions()
		require.NoError(t, err)
		assert.Len(t, opts, 1)
	})
}

func TestSchedulerTimezoneLabel(t *testing.T) {
	t.Run("iana_returns_name", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "Asia/Tokyo"}}}
		assert.Equal(t, "Asia/Tokyo", m.timezoneLabel())
	})
	t.Run("sentinel_returns_local", func(t *testing.T) {
		m := &Module{config: &config.Config{Scheduler: config.SchedulerConfig{Timezone: "-"}}}
		assert.Equal(t, time.Local.String(), m.timezoneLabel())
	})
	t.Run("empty_returns_utc", func(t *testing.T) {
		m := &Module{config: &config.Config{}}
		assert.Equal(t, "UTC", m.timezoneLabel())
	})
}
```

These reference white-box methods on the `scheduler` package's `Module` (the test file is `package scheduler`). The `config` import is already present in `module_test.go`; if the linter flags it as missing, add `"github.com/gaborage/go-bricks/config"` to the test imports.

- [ ] **Step 3: Run to verify failure (compile error)**

Run: `go test ./scheduler/ -run 'TestSchedulerConfiguredTimezone|TestSchedulerLocationOptions|TestSchedulerTimezoneLabel' -v`
Expected: FAIL — `m.configuredTimezone undefined`, `m.schedulerLocationOptions undefined`, `m.timezoneLabel undefined`.

- [ ] **Step 4: Implement the three methods and apply the location**

In `scheduler/module.go`, replace `ensureSchedulerInitialized` with the version below and add the three helper methods immediately after it. (`config`, `time`, `fmt`, and `gocron` are already imported in this file.)

```go
// ensureSchedulerInitialized creates the gocron scheduler on first job registration.
// Per FR-016: Lazy initialization for zero overhead when no jobs are registered.
// Must be called with m.mu write lock held.
func (m *Module) ensureSchedulerInitialized() error {
	if m.scheduler != nil {
		return nil // Already initialized
	}

	// Resolve the configured timezone into a gocron option (default UTC; "-" =
	// host-local). An invalid zone here would already have failed config
	// validation at startup; this is defense in depth.
	opts, err := m.schedulerLocationOptions()
	if err != nil {
		return err
	}

	// Create gocron scheduler
	s, err := gocron.NewScheduler(opts...)
	if err != nil {
		return fmt.Errorf("scheduler: failed to create gocron scheduler: %w", err)
	}

	m.scheduler = s

	// Start the scheduler
	m.scheduler.Start()

	m.logger.Info().
		Str("timezone", m.timezoneLabel()).
		Msg("Scheduler initialized and started")

	return nil
}

// configuredTimezone returns the raw scheduler timezone string from config,
// defaulting to UTC when config is absent or the field is empty. In production
// config.Validate() has already normalized this value (default UTC, "-" opt-out,
// IANA-validated); the fallback only matters for tests that bypass validation.
func (m *Module) configuredTimezone() string {
	if m.config != nil && m.config.Scheduler.Timezone != "" {
		return m.config.Scheduler.Timezone
	}
	return config.DefaultDatabaseTimezone // "UTC" — shared framework timezone default
}

// schedulerLocationOptions translates the configured timezone into gocron
// scheduler options. The "-" sentinel yields no option so gocron keeps its
// time.Local default (legacy/host-local behavior). Any other value is loaded as
// an IANA location and applied via gocron.WithLocation, which gocron threads into
// every wall-clock schedule type (daily/weekly/monthly via at-times, hourly via a
// CRON_TZ prefix). FixedRate is interval-based and unaffected.
func (m *Module) schedulerLocationOptions() ([]gocron.SchedulerOption, error) {
	tz := m.configuredTimezone()
	if tz == config.TimezoneDisabledSentinel {
		return nil, nil
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("scheduler: invalid timezone %q: %w", tz, err)
	}
	return []gocron.SchedulerOption{gocron.WithLocation(loc)}, nil
}

// timezoneLabel returns an operator-facing label for the effective scheduler
// timezone, used in the startup log and the /_sys/job response. The "-" sentinel
// resolves to the host's local zone name.
func (m *Module) timezoneLabel() string {
	tz := m.configuredTimezone()
	if tz == config.TimezoneDisabledSentinel {
		return time.Local.String()
	}
	return tz
}
```

- [ ] **Step 5: Run the resolution tests to verify they pass**

Run: `go test ./scheduler/ -run 'TestSchedulerConfiguredTimezone|TestSchedulerLocationOptions|TestSchedulerTimezoneLabel' -v`
Expected: PASS.

- [ ] **Step 6: Write the end-to-end integration test**

Append to `scheduler/module_test.go`:

```go
func TestSchedulerAppliesConfiguredTimezoneToJobs(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second, withTimezone("America/New_York"))

	err := registrar.DailyAt("tz-job", &counterJob{}, ParseTime("14:30"))
	require.NoError(t, err)

	module.mu.RLock()
	entry := module.jobs["tz-job"]
	module.mu.RUnlock()
	require.NotNil(t, entry)
	require.NotNil(t, entry.gocronJob)

	nextRun, err := entry.gocronJob.NextRun()
	require.NoError(t, err)
	require.False(t, nextRun.IsZero())

	// The scheduler must interpret 14:30 in the configured zone, not host-local.
	assert.Equal(t, "America/New_York", nextRun.Location().String())
	assert.Equal(t, 14, nextRun.Hour())
	assert.Equal(t, 30, nextRun.Minute())
}
```

- [ ] **Step 7: Run the integration test, then the full scheduler suite**

Run: `go test ./scheduler/ -run TestSchedulerAppliesConfiguredTimezoneToJobs -v`
Expected: PASS (would FAIL before Step 4 because the location was `time.Local`).

Run: `go test ./scheduler/...`
Expected: PASS — no existing test inspects `NextRun()`, so the default flip to UTC does not regress them.

- [ ] **Step 8: Commit**

```bash
git add scheduler/module.go scheduler/test_helpers_test.go scheduler/module_test.go
git commit -m "feat(scheduler): apply configured timezone via gocron WithLocation"
```

---

## Task 3: Remove the dead `ScheduleConfiguration.Timezone` field

**Files:**
- Modify: `scheduler/schedule.go` (~lines 45-48)

- [ ] **Step 1: Confirm the field is unreferenced**

Run: `git grep -n 'schedule.Timezone\|\.schedule\.Timezone\|Timezone:.*time.Location'  -- scheduler/`
Expected: no matches (the field is only declared, never read or written). If any match appears outside the struct declaration, STOP and reconcile before removing.

- [ ] **Step 2: Remove the field**

In `scheduler/schedule.go`, delete these lines from the `ScheduleConfiguration` struct:

```go
	// Timezone (nil = system local time per ASSUME-001)
	// For MVP, this is always nil (local time).
	// Future enhancement: allow explicit timezone specification.
	Timezone *time.Location
```

If `time` becomes an unused import after removal, leave it — `schedule.go` still uses `time.Duration` and `time.Weekday`, so the import stays. Verify in the next step.

- [ ] **Step 3: Build and test**

Run: `go build ./scheduler/... && go test ./scheduler/...`
Expected: PASS (no compile error; `time` is still used by other fields).

- [ ] **Step 4: Commit**

```bash
git add scheduler/schedule.go
git commit -m "refactor(scheduler): remove dead ScheduleConfiguration.Timezone field"
```

---

## Task 4: Expose the active timezone in `GET /_sys/job`

**Files:**
- Modify: `scheduler/api_handlers.go` (`listJobsHandler`, ~line 62)
- Test: `scheduler/api_handlers_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `scheduler/api_handlers_test.go`:

```go
// TestListJobsHandlerExposesTimezone verifies GET /_sys/job reports the active zone
func TestListJobsHandlerExposesTimezone(t *testing.T) {
	module, registrar := newTestScheduler(t, 5*time.Second, withTimezone("America/New_York"))

	err := registrar.FixedRate(testJobID, &counterJob{}, 10*time.Second)
	require.NoError(t, err)

	result, apiErr := module.listJobsHandler(EmptyRequest{}, server.HandlerContext{Config: &config.Config{}})
	require.Nil(t, apiErr)
	assert.Equal(t, "America/New_York", result.Data.Meta["timezone"])
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./scheduler/ -run TestListJobsHandlerExposesTimezone -v`
Expected: FAIL — `result.Data.Meta["timezone"]` is `nil`, not `"America/New_York"`.

- [ ] **Step 3: Add the `timezone` meta field**

In `scheduler/api_handlers.go`, update the `Meta` map in `listJobsHandler`:

```go
	// Return with standard GoBricks envelope
	response := JobListResponse{
		Data: jobs,
		Meta: map[string]interface{}{
			"total":    len(jobs),
			"timezone": m.timezoneLabel(),
		},
	}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./scheduler/ -run TestListJobsHandlerExposesTimezone -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add scheduler/api_handlers.go scheduler/api_handlers_test.go
git commit -m "feat(scheduler): expose active timezone in /_sys/job response"
```

---

## Task 5: Update `localTime` doc comments

No behavior change — clarify that the `localTime` parameters are interpreted in the scheduler's configured zone.

**Files:**
- Modify: `app/module.go` (`JobRegistrar` interface, ~lines 47-62)
- Modify: `scheduler/module.go` (`DailyAt`/`WeeklyAt`/`MonthlyAt` doc comments)

- [ ] **Step 1: Update the `JobRegistrar` interface comments**

In `app/module.go`, replace the three "local time" comments:

```go
	// DailyAt schedules a job to run daily at the given wall-clock time,
	// interpreted in the scheduler's configured timezone (scheduler.timezone,
	// default UTC).
	DailyAt(jobID string, job any, localTime time.Time) error

	// WeeklyAt schedules a job to run weekly on the given day and wall-clock
	// time, interpreted in the scheduler's configured timezone.
	WeeklyAt(jobID string, job any, dayOfWeek time.Weekday, localTime time.Time) error
```

and

```go
	// MonthlyAt schedules a job to run monthly on the given day and wall-clock
	// time, interpreted in the scheduler's configured timezone.
	MonthlyAt(jobID string, job any, dayOfMonth int, localTime time.Time) error
```

- [ ] **Step 2: Update the concrete method comments**

In `scheduler/module.go`, update the doc comment on each of `DailyAt`, `WeeklyAt`, and `MonthlyAt` to note the time is interpreted in the scheduler's configured timezone (default UTC). For example, `DailyAt`:

```go
// DailyAt implements JobRegistrar per FR-004. localTime is interpreted in the
// scheduler's configured timezone (scheduler.timezone, default UTC).
func (m *Module) DailyAt(jobID string, job any, localTime time.Time) error {
```

Apply the analogous one-line clarification to `WeeklyAt` and `MonthlyAt`.

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: success (comments only).

- [ ] **Step 4: Commit**

```bash
git add app/module.go scheduler/module.go
git commit -m "docs(scheduler): clarify localTime is in the configured timezone"
```

---

## Task 6: Documentation and ADR-023

**Files:**
- Create: `wiki/adr_023_scheduler_timezone.md`
- Modify: `wiki/architecture_decisions.md`, `wiki/scheduler.md`, `wiki/migrations.md`, `CLAUDE.md`, `llms.txt`

- [ ] **Step 1: Create the ADR**

Create `wiki/adr_023_scheduler_timezone.md`:

```markdown
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
  and `scheduler.timezone`, so the two contracts cannot drift.
- **Observability.** The active zone appears in the "Scheduler initialized" log
  and in `meta.timezone` of `GET /_sys/job`.

### Rejected alternatives

| Alternative | Why rejected |
|-------------|--------------|
| Per-job timezones | gocron has no per-job `WithLocation`; would force raw `CRON_TZ` cron strings. Out of scope for the stated need (YAGNI). |
| Code-based `NewModule(WithTimezone(...))` | Not 12-factor; can't change per environment without recompile; diverges from the `database.timezone` config precedent. |
| Default to host-local | Perpetuates the cross-environment drift this change exists to kill. The `"-"` sentinel preserves the escape hatch without making it the default. |
| Numeric offsets (`+05:00`) | `time.LoadLocation` rejects them; use IANA `Etc/GMT∓N` (inverted sign), same as ADR-016. |
```

- [ ] **Step 2: Add the ADR index entry**

In `wiki/architecture_decisions.md`, read lines ~225-244 to match the existing entry format, then add after the ADR-022 entry block:

```markdown
### [ADR-023: Scheduler Timezone Configuration](adr_023_scheduler_timezone.md)

Adds `scheduler.timezone`, a single config field applied scheduler-wide via
gocron's `WithLocation`, mirroring the `database.timezone` contract from ADR-016
(default UTC, `"-"` opt-out for host-local, IANA-validated, fail-fast). Resolves
the absence of any timezone knob for scheduled jobs and removes the vestigial
`ScheduleConfiguration.Timezone` field. Breaking: an unset zone now means UTC
instead of host-local.
```

Also update the closing note in the same file: change `ADR numbers (ADR-001 through ADR-022)` to `ADR numbers (ADR-001 through ADR-023)`.

- [ ] **Step 3: Add the Timezone section to `wiki/scheduler.md`**

Append to `wiki/scheduler.md`:

```markdown
## Timezone

By default the scheduler interprets all wall-clock schedules
(`DailyAt`/`WeeklyAt`/`MonthlyAt`/`HourlyAt`) in **UTC**. Configure a different
zone with `scheduler.timezone` (IANA name). `FixedRate` is interval-based and
unaffected by timezone.

```yaml
scheduler:
  timezone: America/New_York   # jobs fire at this zone's wall-clock time
```

| Value | Behavior |
|-------|----------|
| unset | UTC (default) |
| IANA name (`UTC`, `Europe/Madrid`, …) | Jobs fire at that zone's wall-clock time; DST handled by the zone |
| `"-"` | Host-local time (the process `time.Local`) — legacy behavior |
| numeric offset (`+05:00`) | Rejected at startup; use `Etc/GMT∓N` (inverted sign) |

Invalid IANA names fail fast at startup. The active zone appears in the
"Scheduler initialized" startup log and in the `meta.timezone` field of
`GET /_sys/job`. This mirrors the `database.timezone` contract — see
[ADR-016](adr_016_database_session_timezone.md) and
[ADR-023](adr_023_scheduler_timezone.md).
```

- [ ] **Step 4: Add the breaking-change entry to `wiki/migrations.md`**

Read the existing section structure of `wiki/migrations.md`, then add an entry matching its format. Content:

```markdown
### Scheduler default timezone → UTC (ADR-023)

Previously the scheduler ran jobs in the host's local time (`time.Local`). It now
defaults to **UTC**. Deployments that relied on host-local job times must set
`scheduler.timezone: "-"` to preserve the old behavior, or set an explicit IANA
zone.

```yaml
scheduler:
  timezone: "-"   # preserve pre-upgrade host-local behavior
```
```

- [ ] **Step 5: Update `CLAUDE.md` and `llms.txt`**

In `CLAUDE.md`, in the `### Scheduler` section, add this sentence after the first paragraph (the one ending "OpenTelemetry instrumentation per job."):

```markdown
Jobs run in **UTC** by default; set `scheduler.timezone` (IANA name; `-` = host-local) to change the zone for all wall-clock schedules.
```

In `llms.txt`: if a YAML example containing a `scheduler:` block exists, add `timezone: America/New_York` under it; if no such block exists, skip this file (no fabricated anchor).

- [ ] **Step 6: Commit**

```bash
git add wiki/adr_023_scheduler_timezone.md wiki/architecture_decisions.md wiki/scheduler.md wiki/migrations.md CLAUDE.md llms.txt
git commit -m "docs(scheduler): document scheduler.timezone + ADR-023"
```

---

## Task 7: Full verification and pre-push review

**Files:** none (verification + review)

- [ ] **Step 1: Run the full pre-commit check**

Run: `make check`
Expected: PASS — fmt clean, lint clean, all tests pass with `-race`. Fix any import-ordering / trailing-newline / type-narrowing issues and re-run until green.

- [ ] **Step 2: Run the mandatory pre-push reviews**

Per `CLAUDE.md` Workflow Rules, run both skills on the staged diff before pushing:
- `/code-review` (reuse / quality / efficiency / correctness)
- `/security-audit` (boundary validation, etc.)

Apply `/code-review` findings first, then `/security-audit`. Re-run `make check` after any fix.

- [ ] **Step 3: Finish the branch**

Use the `superpowers:finishing-a-development-branch` skill to decide merge/PR. Confirm the current branch is `worktree-scheduler-timezone-config` (NOT `main`) before pushing. When opening a PR, address findings from every automated reviewer (CodeRabbit + SonarCloud per-PR issues).

---

## Self-Review

**Spec coverage** — every spec section maps to a task:
- Config field + contract → Task 1 ✓
- Shared validation helper (reuse `DefaultDatabaseTimezone`, no rename — confirmed cross-package referenced) → Task 1 ✓
- Scheduler wiring via `WithLocation`, no per-schedule changes, sentinel/nil edges → Task 2 ✓
- No registration signature change; doc clarification → Task 5 ✓
- Remove dead `Timezone` field → Task 3 ✓
- Observability (startup log + `/_sys/job`) → Task 2 (log) + Task 4 (API) ✓
- Testing (config table tests; scheduler resolution + integration) → Tasks 1, 2, 4 ✓
- Docs + ADR-023 + index + migrations + CLAUDE.md → Task 6 ✓
- Non-goals untouched (no per-job/per-tenant/code-option) ✓

**Placeholder scan** — no TBD/TODO; every code step has complete code; commands have expected output. The only soft instruction is `llms.txt` (Task 6 Step 5), bounded by an explicit "skip if no anchor" rule to avoid fabrication.

**Type consistency** — method names used consistently across tasks: `configuredTimezone()`, `schedulerLocationOptions()` (returns `([]gocron.SchedulerOption, error)`), `timezoneLabel()` (defined Task 2; consumed Task 2 log + Task 4 handler). `validateScheduler(*SchedulerConfig) error` and `normalizeIANATimezone(field, value string) (string, error)` consistent in Task 1. `meta.timezone` key consistent between Task 4 implementation and test.
