# scheduler/ — GoBricks package rules

Repo-wide rules stay in the root [CLAUDE.md](../CLAUDE.md).

## Scheduler

gocron-based job scheduling integrated with the module system. Lazy initialization, overlapping prevention, panic recovery, system APIs at `GET /_sys/job` and `POST /_sys/job/:jobId` (CIDR-restricted), OpenTelemetry instrumentation per job.
Jobs run in **UTC** by default; set `scheduler.timezone` (IANA name; `-` = host-local) to change the zone for all wall-clock schedules.

Jobs implement `Executor` (`Execute(ctx JobContext) error` — JobContext gives JobID, TriggerType, Logger, DB, Messaging, Config) and register in `RegisterJobs(s app.JobRegistrar)` (full example in [llms.txt](../llms.txt)).

**Schedule Methods:** `FixedRate(duration)`, `DailyAt(time)`, `WeeklyAt(weekday, time)`, `HourlyAt(minute)`, `MonthlyAt(dayOfMonth, time)`. See [wiki/scheduler.md](../wiki/scheduler.md).

**Windowed jobs:** `scheduler.Windowed(run, counters...)` is a `FixedRate`-only door: the run reports declared Counters through a `Recorder` (`Count`/`Fail`), and the scheduler writes one fixed-key action line per UTC-aligned `scheduler.summary.window` (default 5m), edge lines on failing-set changes, and one `Int64Counter` per Counter; its per-run line, skips and unedged panic stacks drop to DEBUG. No runtime string may become a log field name. See [wiki/scheduler.md#windowed-jobs](../wiki/scheduler.md#windowed-jobs) and [ADR-146](../wiki/adr_146_windowed_job_summary.md).
