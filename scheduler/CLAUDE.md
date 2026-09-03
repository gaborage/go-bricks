# scheduler/ — GoBricks package rules

Loaded when work touches `scheduler/`. Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Scheduler

gocron-based job scheduling integrated with the module system. Lazy initialization, overlapping prevention, panic recovery, system APIs at `GET /_sys/job` and `POST /_sys/job/:jobId` (CIDR-restricted), OpenTelemetry instrumentation per job.
Jobs run in **UTC** by default; set `scheduler.timezone` (IANA name; `-` = host-local) to change the zone for all wall-clock schedules.

Jobs implement `Executor` (`Execute(ctx JobContext) error` — JobContext gives JobID, TriggerType, Logger, DB, Messaging, Config) and register in `RegisterJobs(s app.JobRegistrar)` (full example in [llms.txt](../llms.txt)).

**Schedule Methods:** `FixedRate(duration)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`, `HourlyAt(minute)`, `MonthlyAt(dayOfMonth, time)`. See [wiki/scheduler.md](../wiki/scheduler.md).
