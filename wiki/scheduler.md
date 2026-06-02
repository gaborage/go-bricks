# Scheduler (Deep Dive)

The `scheduler` package provides gocron-based job scheduling integrated with the GoBricks module system. Jobs are registered through `ModuleDeps`, run with overlap protection and panic recovery, and are observable through OpenTelemetry metrics and the built-in `_sys` system APIs.

## Scheduler

The `scheduler` package provides gocron-based job scheduling integrated with the GoBricks module system.

**Key Features:**
- **Lazy initialization**: Scheduler created only when first job is registered
- **Overlapping prevention**: Mutex-based lock per job (skips trigger if already running)
- **Panic recovery**: Automatic recovery with stack trace logging and metrics
- **System APIs**: `GET /_sys/jobs` (list), `POST /_sys/job/:jobId` (manual trigger), secured via CIDR middleware
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
