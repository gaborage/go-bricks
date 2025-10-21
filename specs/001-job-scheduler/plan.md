# Implementation Plan: Job Scheduler

**Branch**: `001-job-scheduler` | **Date**: 2025-10-17 | **Spec**: [spec.md](./spec.md)

**Input**: Feature specification from `/specs/001-job-scheduler/spec.md`

## Summary

Add job scheduling capability to GoBricks framework using github.com/go-co-op/gocron/v2 as the underlying scheduler. The feature enables developers to schedule recurring background tasks (data cleanup, report generation, cache refresh) with five scheduling patterns: fixed-rate, daily, weekly, hourly, and monthly. Jobs integrate with GoBricks' module system, observability infrastructure (OpenTelemetry traces/metrics/logs), and configuration management. System APIs (`/_sys/job*`) provide operational visibility (list jobs, trigger manually) with CIDR-based IP allowlist security. Scheduler is optional - applications without scheduled jobs run with zero overhead.

**Technical approach**: Wrap gocron/v2 scheduler in GoBricks patterns (Module interface, typed JobContext with framework dependencies, prevent overlapping execution, graceful shutdown with configurable timeout). Observability via span creation per job execution, metrics for count/duration/success/failure, dual-mode logging. CIDR middleware protects system APIs.

## Technical Context

**Language/Version**: Go 1.24+ (framework supports Go 1.24 and 1.25 per CI matrix)

**Primary Dependencies**:
- **github.com/go-co-op/gocron/v2** - Underlying scheduler library (handles DST transitions, cron expressions, schedule execution)
- **github.com/labstack/echo/v4** - HTTP framework (existing GoBricks dependency for system APIs)
- **go.opentelemetry.io/otel** - Observability (traces, metrics, logs - existing GoBricks dependency)
- **github.com/rs/zerolog** - Structured logging (existing GoBricks dependency)
- **github.com/knadh/koanf/v2** - Configuration management (existing GoBricks dependency)

**Storage**: N/A (scheduler state is in-memory; job execution statistics are ephemeral per application lifecycle)

**Testing**:
- **Unit tests**: testify/assert, httptest for system APIs, time-based testing with fixed clocks
- **Integration tests**: testcontainers not required (scheduler is in-memory), but full lifecycle tests with real goroutines/timers
- **Race detection**: All tests run with `-race` flag (multi-platform CI: Ubuntu/Windows × Go 1.24/1.25)
- **Coverage target**: 80% (SonarCloud enforced per constitution)

**Target Platform**: Linux/Windows servers (framework is platform-agnostic, tested on Ubuntu and Windows in CI)

**Project Type**: Framework component (extends existing GoBricks microservices framework)

**Performance Goals**:
- Job execution scheduling overhead: <1ms (context creation, span initialization, metrics recording)
- System API response time: <100ms for up to 100 registered jobs (per SC-004)
- Graceful shutdown: <30s even with long-running jobs (per SC-005)
- Support 1000 concurrently scheduled jobs without degradation (per SC-007)

**Constraints**:
- **Overlapping execution prevention**: Default behavior prevents concurrent runs of the same job (per clarification #1)
- **Optional initialization**: Applications without jobs must run with zero overhead (per FR-016, SC-009)
- **CIDR-based security**: System APIs protected by IP allowlist middleware (per clarification #2)
- **DST delegation**: Framework relies on gocron/v2's DST handling, no custom logic (per clarification #3)
- **Panic recovery**: Jobs that panic are recovered, logged with stack trace, marked as failed (per clarification #4)
- **Graceful degradation**: System APIs return empty list/404 when scheduler not initialized (per clarification #5)

**Scale/Scope**:
- 5 scheduling patterns (fixed-rate, daily, weekly, hourly, monthly)
- 2 system HTTP endpoints (GET /_sys/job, POST /_sys/job/:jobId)
- 1 new framework module (scheduler package)
- ~1500-2000 LOC estimated (core scheduler wrapper, JobContext, observability integration, HTTP handlers, tests)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### I. Explicit Over Implicit
✅ **PASS**
- JobContext explicitly includes trace context, job ID, trigger type (FR-025)
- Configuration via standard app config with documented defaults (FR-030: empty CIDR list behavior)
- No hidden scheduler initialization (FR-016: optional, explicit module registration)
- Error messages specify job ID and failure reason (FR-027: skip logging)

### II. Type Safety Over Dynamic Hacks
✅ **PASS**
- Job interface uses typed JobContext parameter (FR-001)
- JobRegistrar methods use typed scheduling parameters (duration, time, day-of-week) (FR-003-FR-007)
- Schedule validation at registration time, not runtime (FR-023)
- No use of `interface{}` for job parameters - strong typing throughout

### III. Test-First Development
✅ **PASS** *(Design phase verification)*
- Unit tests required for all scheduling patterns, overlapping prevention, panic recovery
- Integration tests required for full lifecycle (startup, execution, shutdown), system APIs
- HTTP handler contract tests for /_sys/job* endpoints
- Race detection enabled for all tests (goroutine safety critical for scheduler)
- **Commitment**: 80% coverage target, SonarCloud quality gate enforced

### IV. Security First
✅ **PASS**
- CIDR-based IP allowlist for system APIs (FR-028, FR-029)
- Input validation at job registration (FR-022: unique IDs, FR-023: schedule parameters)
- No secrets in job metadata or logs
- Audit trail via structured logging (job executions, failures, manual triggers)

### V. Observability as First-Class Citizen
✅ **PASS**
- OpenTelemetry spans for every job execution with job ID, duration, outcome (FR-017)
- Metrics for count, success/failure rate, duration distribution (FR-018, SC-006)
- W3C traceparent propagation through JobContext to downstream operations (FR-019)
- Dual-mode logging: action logs for job lifecycle events, trace logs at ERROR for panics (FR-020, FR-021)
- Slow job execution escalation possible via observability patterns

### VI. Performance Standards
✅ **PASS**
- Minimal overhead: JobContext creation <1ms, no allocations in hot path
- Scheduler supports 1000 jobs without degradation (SC-007)
- System APIs respond <100ms for 100 jobs (SC-004)
- Graceful shutdown <30s (SC-005)
- gocron/v2 chosen for proven performance characteristics

### VII. User Experience Consistency
✅ **PASS**
- Integrates with existing Module interface pattern (Init, Shutdown hooks)
- JobContext mirrors HTTP handler context pattern (access to logger, database, messaging, config)
- Configuration follows standard priority: env vars > config.<env>.yaml > config.yaml > defaults
- Error responses follow GoBricks' `{data, meta}` envelope structure for system APIs
- Deterministic behavior: overlapping prevention, panic recovery, CIDR rejection

**Gate Result**: ✅ **ALL CHECKS PASSED** - Proceed to Phase 0 research

## Project Structure

### Documentation (this feature)

```
specs/001-job-scheduler/
├── spec.md              # Feature specification with clarifications
├── plan.md              # This file (implementation plan)
├── research.md          # Phase 0: gocron/v2 integration patterns, CIDR middleware research
├── data-model.md        # Phase 1: Job, JobContext, JobRegistrar, ScheduleConfiguration, JobMetadata entities
├── quickstart.md        # Phase 1: Developer guide for scheduling jobs
├── contracts/           # Phase 1: OpenAPI specs for /_sys/job* endpoints
│   └── job-system-api.yaml
└── tasks.md             # Phase 2: Actionable tasks (generated by /speckit.tasks)
```

### Source Code (repository root)

```
scheduler/                      # New package
├── scheduler.go                # Core scheduler wrapper around gocron/v2
├── job.go                      # Job interface, JobContext implementation
├── registrar.go                # JobRegistrar interface and implementation
├── schedule.go                 # ScheduleConfiguration types
├── metadata.go                 # JobMetadata for system API responses
├── errors.go                   # ValidationError and other custom error types
├── module.go                   # GoBricks Module interface implementation
├── api_handlers.go             # GET /_sys/job, POST /_sys/job/:jobId handlers
├── cidr_middleware.go          # CIDR-based IP allowlist middleware
├── job_test.go                 # Unit tests for JobContext, execution flow
├── registrar_test.go           # Unit tests for registration, validation
├── module_test.go              # Unit tests for module lifecycle
├── api_handlers_test.go        # Contract tests for system API endpoints
├── cidr_middleware_test.go     # Unit tests for CIDR matching, rejection
└── integration_test.go         # Full lifecycle integration tests

config/                         # Existing package - extend for scheduler config
└── scheduler_config.go         # NEW: SchedulerConfig struct with CIDR allowlist, shutdown timeout

examples/                       # Existing directory
└── scheduler/                  # NEW: Example application
    ├── main.go                 # Demo app with sample scheduled jobs
    ├── jobs/
    │   ├── cleanup_job.go      # Example: data cleanup job
    │   └── report_job.go       # Example: report generation job
    └── config.yaml             # Example configuration with scheduler settings
```

**Structure Decision**: Single project structure (framework component). New `scheduler/` package follows existing GoBricks package organization (`app/`, `config/`, `database/`, `logger/`, `messaging/`, `server/`, `observability/`). System API handlers (`api_handlers.go`) and CIDR middleware (`cidr_middleware.go`) are colocated in the `scheduler/` package for tight coupling with scheduler state. Configuration types in `config/` package maintain consistency with `Config.InjectInto()` pattern.

## Complexity Tracking

*No constitution violations - table empty*

---

## Phase 0: Research & Design Decisions

See [research.md](./research.md) for detailed findings.

### Key Research Areas

1. **gocron/v2 Integration Patterns**
   - How to wrap gocron scheduler in GoBricks Module interface
   - Best practices for job registration API design
   - Lifecycle management (startup, shutdown, graceful stop)
   - How gocron handles DST transitions (delegate behavior per clarification #3)

2. **Overlapping Execution Prevention**
   - Strategies for preventing concurrent runs of the same job (mutex per job ID vs. gocron built-in features)
   - Integration with gocron's execution model
   - Lock granularity and performance implications

3. **CIDR Middleware Implementation**
   - Go libraries for CIDR parsing and IP matching (stdlib `net` package vs. third-party)
   - Echo middleware patterns for request interception
   - Configuration injection for CIDR list
   - Performance characteristics of IP range matching

4. **Observability Integration**
   - OpenTelemetry span lifecycle for job execution (start before job, end after, capture panic)
   - Metrics registration (counter for executions, histogram for duration, gauge for running jobs)
   - Dual-mode logging integration (action vs. trace logs)
   - Context propagation through gocron's job execution callback

5. **Graceful Shutdown Patterns**
   - Go context cancellation propagation to running jobs
   - Timeout-based shutdown (wait for in-flight jobs with deadline)
   - Goroutine cleanup verification (no leaks)

---

## Phase 1: Data Model & Contracts

See [data-model.md](./data-model.md) for detailed entity definitions.

### Core Entities

1. **Job** (interface)
   - Execute(ctx JobContext) error
   - Implemented by application developers

2. **JobContext** (struct/interface)
   - context.Context (embedded for cancellation, deadlines)
   - JobID string
   - TriggerType (scheduled | manual)
   - Logger logger.Logger
   - DB database.Interface (optional)
   - Messaging messaging.Client (optional)
   - Config *config.Config
   - TraceProvider (OpenTelemetry)

3. **JobRegistrar** (interface)
   - FixedRate(jobID string, job Job, duration time.Duration) error
   - DailyAt(jobID string, job Job, localTime time.Time) error
   - WeeklyAt(jobID string, job Job, dayOfWeek time.Weekday, localTime time.Time) error
   - HourlyAt(jobID string, job Job, minute int) error
   - MonthlyAt(jobID string, job Job, dayOfMonth int, localTime time.Time) error

4. **ScheduleConfiguration** (struct)
   - Type: ScheduleType (fixed-rate | daily | weekly | hourly | monthly)
   - Interval: time.Duration (for fixed-rate)
   - Hour: int (0-23, for daily/weekly/monthly)
   - Minute: int (0-59, for all time-based schedules)
   - DayOfWeek: time.Weekday (for weekly)
   - DayOfMonth: int (1-31, for monthly)
   - Timezone: *time.Location (nil = system local time)

5. **JobMetadata** (struct - for system API responses)
   - JobID string
   - ScheduleType string
   - CronExpression string
   - HumanReadable string
   - NextExecutionTime *time.Time
   - LastExecutionTime *time.Time
   - LastExecutionStatus (success | failure | skipped)
   - TotalExecutions int64
   - SuccessCount int64
   - FailureCount int64

### API Contracts

See [contracts/job-system-api.yaml](./contracts/job-system-api.yaml) for OpenAPI 3.0 specification.

**Endpoints**:

1. **GET /_sys/job**
   - **Purpose**: List all registered jobs with metadata
   - **Request**: None
   - **Response 200**: `{"data": [JobMetadata], "meta": {"total": int}}`
   - **Response 200** (no jobs): `{"data": [], "meta": {"total": 0}}`
   - **Response 403**: CIDR allowlist rejection (when configured)

2. **POST /_sys/job/:jobId**
   - **Purpose**: Manually trigger job execution
   - **Request**: Path parameter `jobId`
   - **Response 200**: `{"data": {"jobId": string, "triggered": true, "scheduledNextAt": timestamp}}`
   - **Response 404**: Job not found (invalid jobId or scheduler not initialized)
   - **Response 409**: Job already running (overlapping prevention)
   - **Response 403**: CIDR allowlist rejection (when configured)

---

## Phase 2: Task Breakdown

Task generation will be handled by `/speckit.tasks` command. Expected task categories:

1. **Foundation Tasks** (P1)
   - Create scheduler package structure
   - Define Job interface and JobContext
   - Implement JobRegistrar with all 5 scheduling methods
   - Wrap gocron/v2 scheduler with GoBricks Module interface

2. **Observability Tasks** (P2)
   - Integrate OpenTelemetry spans for job execution
   - Implement metrics recording (count, duration, success/failure)
   - Add dual-mode logging (action logs for lifecycle, ERROR logs for panics)
   - Test trace propagation through JobContext

3. **Security Tasks** (P2)
   - Implement CIDR middleware for /_sys/job* endpoints
   - Add configuration injection for CIDR allowlist
   - Validate IP matching logic with unit tests
   - Document security configuration in quickstart

4. **System API Tasks** (P3)
   - Implement GET /_sys/job handler (list jobs)
   - Implement POST /_sys/job/:jobId handler (trigger job)
   - Add contract tests for both endpoints
   - Handle edge cases (scheduler not initialized, job not found)

5. **Reliability Tasks** (P2)
   - Implement overlapping execution prevention (mutex per job ID)
   - Add panic recovery with stack trace logging
   - Implement graceful shutdown with timeout
   - Test goroutine cleanup (no leaks)

6. **Integration & Testing Tasks** (P1)
   - Write unit tests for all scheduling patterns
   - Write integration tests for full lifecycle (startup, execute, shutdown)
   - Add race detection tests for concurrent job execution
   - Validate 80% coverage threshold

7. **Documentation Tasks** (P3)
   - Create quickstart.md with code examples
   - Document CIDR configuration patterns
   - Add examples/ directory with sample scheduled jobs
   - Update main framework README with scheduler feature

---

## Next Steps

1. **Review this plan** for accuracy and completeness
2. **Run `/speckit.tasks`** to generate detailed, dependency-ordered tasks in `tasks.md`
3. **Run `/speckit.analyze`** to validate consistency across spec.md, plan.md, tasks.md
4. **Proceed to implementation** using `/speckit.implement` or manual task execution
