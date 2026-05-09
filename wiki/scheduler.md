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
func (m *Module) Init(deps *app.ModuleDeps) error {
    return deps.Scheduler.DailyAt("cleanup-job", &CleanupJob{}, mustParseTime("03:00"))
}
```

**Schedule Types:** `Every(duration)`, `Cron(expression)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`
