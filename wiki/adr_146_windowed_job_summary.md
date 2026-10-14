# ADR-146: A Windowed Job Writes One Summary Line per Window

**Status:** Accepted
**Date:** 2026-10-14

## Context

A `FixedRate` job writes one `log.type=action` line per run. Action lines export at every
severity, so nothing samples them: a job at 5s writes 17,280 lines a day per replica. A hung run
adds an `already running` WARN on every trigger, and a panicking job writes an ERROR stack line on
every run. None of these lines carry the job's domain outcomes (rows relayed, messages parked), so
the volume buys no signal. gaborage/go-bricks#2098.

## Decision

**An opt-in door.** `scheduler.Windowed(run func(JobContext, *Recorder) error, counters ...Counter) Executor`
wraps a job that reports typed outcomes through a per-run `Recorder`:
`Count`/`Fail(name string, n int64, attrs ...attribute.KeyValue)` against the declared
`Counter{Name, Attrs []AttrSet{Key, Values}}`. Only `FixedRate` accepts it; every other schedule
returns a `ValidationError`, and its exported `Execute` returns an error without calling `run`.
Plain jobs are unchanged.

**Windows.** `[k·w, (k+1)·w)` on the UTC epoch, `w` = `scheduler.summary.window` (default 5m,
at least 1m, dividing 24h; zero takes the default, negative fails). A run belongs to the window it
**completes** in. Under a per-job mutex, the first event past the boundary closes the window: a
scheduled trigger (checked before the run lock) or a completion. A completion that closes window
k counts in k's `runs.inflight`, then in k+1's `runs`. A window with no run, skip or in-flight run
writes nothing. An interval not shorter than the window logs one startup WARN and registers.

**The window line** (`log.type=action`, through the module logger) has fixed keys: `job.id`,
`window.start`, `window.end`, `window.partial`, `runs`, `runs.failing`, `runs.slow`,
`runs.skipped`, `runs.inflight`, `job.state`, `failing.keys` (at most 10), `failing.keys.more`,
`edges.suppressed`, `recorder.late`, `<name>.ok`/`<name>.failing` per Counter, and the last
completed run's `error`, `traceparent` and `correlation_id` under #2095's rule (`traceparent` is
`""` without a valid span context; with no completed run, `correlation_id` is freshly minted). No
runtime string ever becomes a key. Budget: 17 fixed keys, `log.type` and 2 per Counter, at most 82
at the 32-Counter cap, under the OTel SDK's 128-attribute record limit without relying on #2096
writing `log.type` first. WARN when `runs.failing > 0` or the window only skipped; otherwise INFO.

**Failing keys.** A completed run fails under the reserved `run.error`, `run.panic`, `run.slow`
(over `scheduler.timeout.slowjob`) and `undeclared` (an unknown name or value, an attribute-key
mismatch or `n < 0`; never a panic), plus every `Fail` with `n > 0`, keyed `name{k=v,...}` with
attributes sorted. Reserved keys list first in `failing.keys`. `run.slow` is failing: the per-run
slow WARN becomes at most one WARN window line per window.

**Edges** (scheduled completions only). Each run adds its keys to the job's failing set; a
window's close resets the set to its last completed run's keys. Clean to failing writes an edge
line with `edge.reason=new`; a key the set did not hold writes `grew`. At most two edges per
window; more only increment `edges.suppressed`. ERROR on `run.error`/`run.panic`, otherwise WARN.
The edge line carries `job.id`, `edge.reason`, the run's `failing.keys`, `failing.keys.more`,
counts, `error`, `traceparent` and `correlation_id`. `job.state` reads `failing` when the set is
non-empty at close, `recovered` when it was non-empty at open and is empty at close, else `clean`.

**Demotions, Windowed jobs only.** The per-run summary line becomes DEBUG without `log.type`. A
scheduled trigger's run-lock miss becomes DEBUG and counts in `runs.skipped` (`/_sys/job` still
counts it). The panic stack line is ERROR only on a run that writes an edge, DEBUG otherwise.
Manual triggers keep today's action line and add to metrics, but never touch windows or edges.

**Shutdown** seals every window in one `defer` on all exits (gocron's stop error, all jobs done,
timeout), writing `window.partial=true`; `window.end` stays the aligned end. A completion after the
seal writes one per-run action line: INFO when clean, ERROR on an error or panic, WARN otherwise.

**Metrics.** One `Int64Counter` per Counter, named `Counter.Name` on the scheduler's meter, with
the declared attributes plus `job.id`, created at registration. `Count` and `Fail` both add `n`;
failing is run state, not a metric attribute. The reserved `job` word keeps a Counter clear of
`job.execution.*` and `job.panic.total`.

**Validation fails fast** with a `ValidationError` naming the field: at most 32 Counters; names
`^[a-z][a-z0-9_.]{0,62}$`, unique, neither equal to nor under (`<word>.`) `job run runs window
failing edges log recorder undeclared`, and holding no needle of `logger.DefaultFilterConfig()`
(case-insensitive substring, as the filter matches); keys `^[a-z][a-z0-9_]{0,31}$`; values
`^[a-z0-9_-]{1,64}$`, unique and non-empty; at most 2,000 series per Counter; and a zero
`Summary.Window` (a config that skipped `config.Validate`) fails the registration, naming
`scheduler.summary.window`. `Init` gains no check.

## Alternatives considered

- **`Tally[T]`.** Rejected: fails the deletion test; its sums are the Recorder calls.
- **`Scope(key)`.** Rejected: puts runtime strings in field names.
- **Refusing `interval >= window`.** Rejected: breaks valid configs; a WARN suffices.
- **Per-job window keys.** Rejected: one `scheduler.summary.window` puts every Windowed job on the
  same boundaries, and a key per job is config surface no adopter needs.
- **Ambient `Count(ctx)`.** Rejected: silently does nothing outside a run.
- **A zero-window check in `Init`** ([ADR-075](adr_075_scheduler_timeout_single_default.md)'s pattern). Rejected: fails every hand-built config,
  plain jobs included, which is a break.
- **Start-time attribution.** Rejected: a run spanning a boundary would lose its tally.

## Consequences

- **Not breaking.** Additive: a new door, a new config key with a default, and plain jobs and every
  other schedule unchanged.
- Window and edge lines take `traceparent` from `jobTraceparent` and `correlation_id` from the ID
  `executeJob` mints once per run (#2095); rewiring the plain per-run line stays with #2095.

**Residual risks:**

1. Needles added through `log.sensitivefields`, or a code-built `app.Options.LoggerFilterConfig`,
   are **not** checked at registration. A Counter name holding one has its `.ok`/`.failing`
   fields masked on every window and edge line.
2. A window with no event in it writes no line (empty-window back-fill is out of scope), so with
   an interval at or above the window a hung run's middle windows go unreported.
3. An `undeclared` or `Fail`-only run returns nil, so `/_sys/job` and `job.execution.total` count it
   as a success. The failing signal is the window and edge lines and the Counter metric.
4. The window and edge writes are guarded, so a consumer logger that panics drops that line
   instead of unwinding window accounting.

**Out of scope:** empty-window back-fill, histograms, the relay and drain adopters
(#2099, #2100), the trace-identity and bridge-ordering fixes (#2095, #2096), and trace
selection (#2101).

## References

- `scheduler/windowed.go` — `Windowed`, `Recorder`, `compileWindowed`
- `scheduler/window.go` — `windowState`, `logWindow`, `logEdge`
- `scheduler/module.go` — `registerJob`, `newJobWindow`, `acquire`, `executeJob`, `settleRun`,
  `sealWindows`
- `config/scheduler_section.go` — `normalizeScheduler`, `checkScheduler`
- `scheduler/window_test.go`, `scheduler/windowed_test.go`
- [scheduler.md](scheduler.md#windowed-jobs)
- gaborage/go-bricks#2098, #2095, #2096
- See [migrations.md](migrations.md) `[C72.28]`.
