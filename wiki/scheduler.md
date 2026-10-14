# Scheduler (Deep Dive)

The `scheduler` package provides gocron-based job scheduling integrated with the GoBricks module system. Jobs are registered through `ModuleDeps`, run with overlap protection and panic recovery, and are observable through OpenTelemetry metrics and the built-in `_sys` system APIs.

## Scheduler

The `scheduler` package provides gocron-based job scheduling integrated with the GoBricks module system.

**Key Features:**

- **Lazy initialization**: Scheduler created only when first job is registered
- **Overlapping prevention**: Mutex-based lock per job (skips trigger if already running)
- **Panic recovery**: Automatic recovery with stack trace logging and metrics
- **System APIs**: `GET /_sys/job` (list), `POST /_sys/job/:jobId` (manual trigger), secured via CIDR middleware
- **OpenTelemetry**: Counter, histogram, and panic tracking per job

**Executor Interface:**

```go
type Executor interface {
    Execute(ctx JobContext) error
}

// JobContext provides: JobID(), TriggerType(), Logger(), DB(), Messaging(), Config()
```

**Registration via ModuleDeps:**

```go
// mustParseTime parses "HH:MM" and panics on error — acceptable in init paths
// where a malformed literal is a programmer bug that must crash at startup.
func mustParseTime(s string) time.Time {
    t, err := time.Parse("15:04", s)
    if err != nil {
        panic(err)
    }
    return t
}

func (m *Module) Init(deps *app.ModuleDeps) error {
    return deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, mustParseTime("03:00"))
}
```

**Schedule Types:** `FixedRate(duration)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`, `HourlyAt(minute)`, `MonthlyAt(dayOfMonth, time)`

## Windowed jobs

A `FixedRate` job writes one `log.type=action` line per run, and action lines are never
sampled: at 5s that is 17,280 lines a day per replica. `scheduler.Windowed` is the opt-in door for
such jobs. The run reports typed outcomes through a `Recorder`, and the scheduler writes one line
per window instead, an edge line when the job turns failing, and one metric per declared Counter
([ADR-146](adr_146_windowed_job_summary.md)). Plain jobs are unchanged.

```go
func (m *Module) RegisterJobs(reg app.JobRegistrar) error {
    job := scheduler.Windowed(m.sync,
        scheduler.Counter{Name: "sync.fetched"},
        scheduler.Counter{
            Name:  "sync.written",
            Attrs: []scheduler.AttrSet{{Key: "kind", Values: []string{"order", "refund"}}},
        },
    )
    return reg.FixedRate("ledger-sync", job, 5*time.Second) // only FixedRate accepts it
}

func (m *Module) sync(ctx scheduler.JobContext, rec *scheduler.Recorder) error {
    batch, err := m.source.Fetch(ctx)
    if err != nil {
        return err // fails the run under run.error
    }
    rec.Count("sync.fetched", int64(len(batch)))
    for _, item := range batch {
        kind := attribute.String("kind", item.Kind) // a value not declared fails the run under undeclared
        if err := m.store.Write(ctx, item); err != nil {
            rec.Fail("sync.written", 1, kind) // fails the run under sync.written{kind=<kind>}
            continue
        }
        rec.Count("sync.written", 1, kind)
    }
    return nil
}
```

**Window.** `scheduler.summary.window` (env `SCHEDULER_SUMMARY_WINDOW`) sets the width for every
Windowed job: unset or zero takes **5m**; it must be at least 1m and divide 24h, and a negative
value fails startup. Windows are aligned to the UTC epoch, and a run counts in the window it
**completes** in. The first event past the boundary, a scheduled trigger or a completion, writes
the closed window's line; a window with no run, skip or in-flight run writes nothing. An interval
not shorter than the window logs one startup WARN and registers.

**Window line** (`Job '<id>' window summary`, `log.type=action`, through the module logger):

| Field | Meaning |
| ------- | --------- |
| `job.id` | The job |
| `window.start`, `window.end` | Aligned UTC bounds, RFC3339Nano; `window.end` stays aligned on a partial window |
| `window.partial` | `true` on the window Shutdown sealed |
| `runs`, `runs.failing`, `runs.slow` | Runs completed in the window; those with any failing key; those over `scheduler.timeout.slowjob` |
| `runs.skipped` | Scheduled triggers that found the job still running |
| `runs.inflight` | `1` when a run was still running at close |
| `job.state` | `clean`, `failing` or `recovered` (below) |
| `failing.keys`, `failing.keys.more` | Up to 10 failing keys of the window's runs, reserved keys first; the count of the rest |
| `edges.suppressed` | Edges past the two a window writes |
| `recorder.late` | `Count`/`Fail` calls made after their run returned, dropped |
| `<name>.ok`, `<name>.failing` | Per Counter: the sum of `Count` and of `Fail` |
| `error`, `traceparent`, `correlation_id` | The last completed run's (`error` only when that run failed); with none, no `error`, `traceparent` `""` and a fresh `correlation_id` |

The line is WARN when `runs.failing > 0` or the window only skipped (`runs == 0`,
`runs.skipped > 0`), otherwise INFO. Its keys are fixed by the declaration: at most 82 fields at
32 Counters, so the OTel SDK's 128-attribute limit never truncates it, and no runtime string ever
becomes a key.

**Failing keys and edges.** A run fails under `run.error` (it returned an error), `run.panic`,
`run.slow`, `undeclared` (an unknown name or value, a wrong attribute key, or `n < 0`; never a
panic), or a `Fail` with `n > 0`, keyed `name{k=v,...}` with attributes sorted. Each scheduled
run adds its keys to the job's failing set, and a window's close resets the set to its last
completed run's keys. A run that moves the set from empty writes an edge line with
`edge.reason=new`; one that adds a key writes `grew`. A window writes at most two edges and counts
the rest in `edges.suppressed`. The edge line (`Job '<id>' failing edge: <reason>`) carries
`job.id`, `edge.reason`, the run's `failing.keys`/`failing.keys.more`, its `<name>.ok`/`.failing`,
`error`, `traceparent` and `correlation_id`; it is ERROR on `run.error` or `run.panic`, otherwise
WARN. `job.state` reads `failing` when the set is non-empty at close, `recovered` when it was
non-empty at open and is empty at close, and `clean` otherwise. It follows the last completed run,
not `failing.keys`: a window whose last run is clean reads `clean` or `recovered` even when
`failing.keys` lists an earlier run's failures.

**What else changes for a Windowed job.** Its per-run summary line drops to DEBUG without
`log.type`. A scheduled trigger that finds the job running logs DEBUG and counts in
`runs.skipped`; `/_sys/job` still counts the skip. The panic stack line is ERROR only on a run that
writes an edge, DEBUG otherwise. Counts recorded before a panic survive, and the panic value never
reaches a line.

**Manual triggers** (`POST /_sys/job/:jobId`) keep today's action line and add to the metrics, but
never touch the window, the failing set or edges.

**Shutdown** seals every window on every exit, writing the open one with `window.partial=true`. A
run that completes after the seal writes its own action line: INFO when clean, ERROR on an error
or panic, WARN otherwise.

**Metrics.** Each Counter is an `Int64Counter` named `Counter.Name` on the scheduler's meter,
created at registration, with the declared attributes plus `job.id`. `Count` and `Fail` both add
`n`; undeclared and late calls add nothing.

**Validation** runs at registration and returns a `ValidationError` naming the field:

- at most 32 Counters, each with at most 2,000 attribute combinations;
- names match `^[a-z][a-z0-9_.]{0,62}$`, are unique, and are neither equal to nor under
  (`<word>.`) `job`, `run`, `runs`, `window`, `failing`, `edges`, `log`, `recorder` or `undeclared`;
- names hold no needle of `logger.DefaultFilterConfig()`, case-insensitively, so `x.token.count`
  or `authors.synced` (`auth`) is refused: the filter would mask its own fields;
- attribute keys match `^[a-z][a-z0-9_]{0,31}$`; values match `^[a-z0-9_-]{1,64}$`, unique and
  non-empty, and every call passes each declared key once, as a string;
- a zero `scheduler.summary.window` on a config that skipped `config.Validate` fails the
  registration. Calling the job's `Execute` directly returns an error without running it.

**Limits:**

- A needle you add through `log.sensitivefields` (or `app.Options.LoggerFilterConfig`) is not
  checked: a Counter name holding one has its `.ok`/`.failing` fields masked on every line.
- A window with no event writes no line, so with an interval at or above the window a hung run's
  middle windows go unreported.
- A run that only calls `Fail`, or records `undeclared`, still returns nil: `/_sys/job` and
  `job.execution.total` count it as a success. Alert on the window and edge lines or the Counter
  metric.

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
| ------- | ---------- |
| unset | UTC (default) |
| IANA name (`UTC`, `Europe/Madrid`, …) | Jobs fire at that zone's wall-clock time; DST handled by the zone |
| `"-"` | Host-local time (the process `time.Local`) — legacy behavior |
| `Local` | Rejected at startup; `"-"` is the only host-local spelling ([ADR-093](adr_093_reject_literal_local_timezone.md)) |
| numeric offset (`+05:00`) | Rejected at startup; use `Etc/GMT∓N` (inverted sign) |

Invalid IANA names fail fast at startup: `config.Validate` refuses them, and
`scheduler.Module.Init` loads the zone once more as its own precondition, so a
config handed to `Init` without validation (an empty or unloadable value) fails
there rather than at first job registration ([ADR-075](adr_075_scheduler_timeout_single_default.md)).
The `Local` refusal above lives in `config.Validate` only, so a config handed
straight to `Module.Init` with `Local` still resolves host-local; that path is
outside the validated-configuration contract.
The active zone appears in the
"Scheduler initialized" startup log and in the `meta.timezone` field of
`GET /_sys/job`. This mirrors the `database.timezone` contract — see
[ADR-016](adr_016_database_session_timezone.md) and
[ADR-023](adr_023_scheduler_timezone.md).

## Deadlines

Job contexts are cancel-only: they are cancelled on graceful shutdown
(`scheduler.timeout.shutdown`) and never carry a deadline.
`scheduler.timeout.slowjob` (25s) logs WARN after the fact; it does not
cancel.

Apply your own deadline if a job step has a known SLO. When that step
publishes, size the deadline against the real AMQP call bound —
`readytimeout + maxpublishattempts × connectiontimeout` (**150s warm /
155s ceiling** at default backoffs) — not the 30s per-attempt confirmation
wait. A raised `reconnect.resenddelay` can stretch the publish-error path;
see [context_deadlines.md](context_deadlines.md) and
[messaging.md](messaging.md#bounded-publish-retries-reconnectmaxpublishattempts).

## Multi-tenant jobs

A job runs with no tenant in its context. Under per-tenant tenancy,
`JobContext.DB()` and `JobContext.Messaging()` therefore resolve no tenant:
they return nil and log an ERROR. A job that works on tenants names each one.

The scheduler installs one lease scope per job run ([ADR-032](adr_032_lease_refcount_tenant_handles.md)).
A sweep that borrows on the job context, `deps.DB(multitenant.SetTenant(jobCtx, id))`
(or `deps.Cache`/`deps.Messaging`), registers every tenant's lease in that one
scope, and none is released until the job returns. Releasing a lease does not
close a cached handle, so this matters only when the sweep covers more tenants
than the manager's max size (`database.manager.maxsize`, `cache.manager.maxsize`,
`messaging.publisher.maxcached`): the managers never refuse a borrow, so every
leased handle the LRU displaces stays open, with its connections, until the job
returns.

`multitenant.ForEachTenant` runs a callback once per tenant, each inside that
tenant's own lease scope, drained when the callback returns or panics. The job
then holds about one tenant's handles at a time. Take the tenant list from
`Config.PerTenantJobKeys()` for static tenants (`[""]` in single-tenant mode, so
the callback runs once with no tenant), or supply your own for a dynamic source:

```go
type SweepJob struct {
    db func(context.Context) (database.Interface, error) // deps.DB, captured in Init
}

func (j *SweepJob) Execute(jobCtx scheduler.JobContext) error {
    tenants := jobCtx.Config().PerTenantJobKeys()
    return multitenant.ForEachTenant(jobCtx, tenants, func(ctx context.Context, tenantID string) error {
        db, err := j.db(ctx) // borrow on ctx, never on jobCtx
        if err != nil {
            return fmt.Errorf("tenant %q: %w", tenantID, err)
        }
        if _, err := db.Exec(ctx, "DELETE FROM sessions WHERE expires_at < now()"); err != nil {
            return fmt.Errorf("tenant %q: %w", tenantID, err)
        }
        return nil
    })
}
```

Tenants run in order. Errors from the callback are joined unwrapped and the
sweep continues; once the context is done no further tenant starts and
`ctx.Err()` joins the result. A panic is not recovered: it propagates after that
tenant's scope has drained, and the scheduler's job-level recovery reports it.

Escape rules:

- Borrow inside the callback only through its `ctx` or a context derived from
  it. A borrow on the outer job context lands in the job scope and is not
  bounded.
- Do not use a handle, `database.Session` or transaction obtained inside the
  callback after it returns.
- The bound matters only when the sweep covers more tenants than the manager's
  max size; at or below it every handle stays cached either way.
- The caller supplies the tenant list: `Config.PerTenantJobKeys()` for static
  tenants, or its own list for dynamic sources.
