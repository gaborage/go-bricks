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
