# Feature Specification: Job Scheduler

**Feature Branch**: `001-job-scheduler`
**Created**: 2025-10-17
**Status**: Draft
**Input**: User description: "job scheduler: add a job scheduling capability to the framework, we will build on top of github.com/go-co-op/gocron/v2. The expected dev experience is: a Job interface will be declared, it will have an Execute(JobContext context) method and user's will be able to schedule using the provided APIs from the JobRegistrar. JobRegistrar available functions will be like: fixedRate(jobId, job, duration); dailyAt(jobId, job, localTime); weeklyAt(jobId, job, dayOfWeek, localTime); hourlyAt & monthlyAt will follow same idea. Additionally framework will expose 2 sys APIs: /_sys/job -> lists all jobs and their cron style schedule, POST /_sys/job/:job will trigger job by jobId. Jobs are optional, make sure to address startUp and shutdown hooks. Observability is first-class citizen."

## Clarifications

### Session 2025-10-17

- Q: Should the scheduler prevent overlapping executions of the same job or allow concurrent runs? → A: Prevent overlapping - If a job is running, new scheduled/manual triggers are skipped with a logged warning. Jobs complete their execution before the next trigger can fire.
- Q: Should the `/_sys/job*` endpoints be exposed without authentication in the MVP, or should basic security be included from the start? → A: CIDR-based IP allowlist with built-in middleware - Framework provides middleware that checks source IP against configured CIDR list. When CIDR list is non-empty (if empty, only localhost can access it), only matching IPs can access system APIs. Configuration managed via standard app config.
- Q: How should the scheduler handle DST transitions and system clock changes for time-based schedules? → A: Delegate to gocron/v2 - Use the underlying library's DST handling behavior. Document the behavior in framework docs but don't add custom DST logic.
- Q: When a job panics during execution, what specific recovery actions should the system take? → A: Recover, log error with stack trace, record as failed execution - Catch panic with recover(), log error at ERROR level with full stack trace, record execution as failed in metrics, continue scheduler operation.
- Q: When the scheduler is not initialized (no jobs registered), what should the `/_sys/job*` endpoints return? → A: Empty list / 404 - GET `/_sys/job` returns empty list with 200 OK. POST `/_sys/job/:jobId` returns 404 Not Found with message indicating job doesn't exist.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Schedule Recurring Background Tasks (Priority: P1)

As a developer building a GoBricks application, I need to schedule recurring background tasks (like data cleanup, report generation, or cache refresh) so that these operations run automatically at specified intervals without manual intervention.

**Why this priority**: Core functionality - enables the fundamental scheduling capability. Without this, no jobs can be scheduled or executed.

**Independent Test**: Can be fully tested by registering a simple job (e.g., logging "Hello World"), scheduling it to run every 5 seconds, waiting 15 seconds, and verifying the job executed 3 times. Delivers immediate value by automating repetitive tasks.

**Acceptance Scenarios**:

1. **Given** a job that needs to run every 30 minutes, **When** developer registers the job with a fixed-rate schedule, **Then** the job executes automatically every 30 minutes until shutdown
2. **Given** a job scheduled daily at 3:00 AM, **When** the application runs across midnight, **Then** the job executes once at the scheduled time each day
3. **Given** a job scheduled weekly on Monday at 9:00 AM, **When** the week transitions from Sunday to Monday, **Then** the job executes on Monday morning at the specified time
4. **Given** a job scheduled monthly on the 1st at midnight, **When** the month changes, **Then** the job executes on the first day of each month
5. **Given** multiple jobs with different schedules, **When** the application is running, **Then** each job executes according to its independent schedule without interference

---

### User Story 2 - Monitor Scheduled Jobs (Priority: P2)

As an operations engineer or developer, I need to view all scheduled jobs and their execution schedules so that I can verify jobs are configured correctly and understand when they will run next.

**Why this priority**: Essential for operational visibility but depends on P1 (having jobs to monitor). Enables debugging and validation of job configurations.

**Independent Test**: Can be fully tested by registering 3 different jobs with varied schedules, calling the system API to list jobs, and verifying the response shows all jobs with their cron-style schedule expressions and next execution times.

**Acceptance Scenarios**:

1. **Given** several jobs are registered with different schedules, **When** operator queries the job listing API, **Then** all jobs are displayed with their unique identifiers, schedule descriptions, and next execution time
2. **Given** a job with a complex schedule (e.g., weekdays at 9 AM), **When** viewing the job list, **Then** the schedule is shown in a human-readable format alongside the cron expression
3. **Given** no jobs are registered (scheduler not initialized), **When** querying the job listing API, **Then** an empty list is returned with HTTP 200 OK

---

### User Story 3 - Manually Trigger Jobs (Priority: P3)

As a developer or operations engineer, I need to manually trigger a scheduled job on demand so that I can test job behavior, recover from failures, or run jobs outside their normal schedule.

**Why this priority**: Valuable for testing and operational flexibility, but not critical for basic scheduling functionality. Depends on P1 (having jobs to trigger).

**Independent Test**: Can be fully tested by registering a job, using the trigger API with the job's identifier, and verifying the job executes immediately regardless of its schedule. Delivers value by enabling ad-hoc job execution for testing and recovery scenarios.

**Acceptance Scenarios**:

1. **Given** a job scheduled to run daily at midnight, **When** operator triggers the job manually at 2 PM, **Then** the job executes immediately and the next scheduled execution remains at midnight
2. **Given** a job identifier that exists and is not currently running, **When** triggering via the system API, **Then** the job executes successfully and returns confirmation
3. **Given** a job identifier that doesn't exist, **When** attempting to trigger via the API, **Then** an appropriate error message is returned
4. **Given** a job is currently executing, **When** attempting to trigger the same job again (scheduled or manual), **Then** the trigger is skipped, a warning is logged, and an appropriate response indicates the job is already running

---

### User Story 4 - Graceful Lifecycle Management (Priority: P2)

As a developer deploying a GoBricks application, I need jobs to start automatically when the application starts and stop gracefully when the application shuts down so that jobs don't leave orphaned processes or incomplete work.

**Why this priority**: Critical for production reliability but builds on P1. Ensures jobs integrate properly with application lifecycle and don't cause deployment issues.

**Independent Test**: Can be fully tested by starting an application with registered jobs, verifying jobs begin executing, initiating a shutdown, and confirming all jobs stop gracefully within a reasonable timeout without leaving hanging goroutines or partial work.

**Acceptance Scenarios**:

1. **Given** jobs are registered during module initialization, **When** the application starts up, **Then** all jobs are scheduled and ready to execute according to their schedules
2. **Given** a job is mid-execution, **When** the application initiates shutdown, **Then** the job is allowed to complete or is cancelled gracefully with appropriate cleanup
3. **Given** the application is shutting down, **When** a job is scheduled to execute during the shutdown window, **Then** the job is either skipped or cancelled appropriately
4. **Given** the scheduler has not been configured (optional jobs), **When** the application starts or stops, **Then** no errors occur and the application behaves normally

---

### User Story 5 - Observability and Tracing (Priority: P2)

As a developer monitoring a production GoBricks application, I need job execution to be fully observable through metrics, traces, and logs so that I can diagnose issues, measure performance, and understand system behavior.

**Why this priority**: Essential for production operations as GoBricks makes observability a first-class citizen. Depends on P1 (having jobs to observe).

**Independent Test**: Can be fully tested by configuring observability, executing a job, and verifying that traces show the job execution span with appropriate context, metrics track execution count/duration, and logs capture execution events at appropriate severity levels.

**Acceptance Scenarios**:

1. **Given** observability is enabled, **When** a job executes, **Then** a trace span is created with job identifier, start time, duration, and outcome (success/failure)
2. **Given** a job execution context, **When** the job executes business logic, **Then** the context propagates to downstream operations (database queries, HTTP calls, etc.) maintaining the trace hierarchy
3. **Given** multiple job executions over time, **When** viewing metrics, **Then** metrics show total execution count, success rate, failure count, and execution duration distribution per job
4. **Given** a job encounters an error, **When** the error occurs, **Then** the error is logged with appropriate severity, included in the trace span, and reflected in failure metrics
5. **Given** observability is disabled, **When** jobs execute, **Then** no errors occur and jobs run normally without instrumentation overhead

---

### Edge Cases

- What happens when a job execution exceeds the interval duration (e.g., a job scheduled every 5 minutes takes 10 minutes to complete)? **Resolved:** Overlapping executions are prevented - the long-running job completes before the next trigger can fire; intervening scheduled triggers are skipped with logged warnings.
- How does the system handle clock changes (daylight saving time, system time adjustments) for time-based schedules? **Resolved:** DST handling delegated to gocron/v2 library behavior - framework relies on underlying library's DST transition logic without custom overrides. Behavior documented in framework docs.
- What happens when a job panics during execution? **Resolved:** Panic is recovered, error logged at ERROR level with full stack trace, execution recorded as failed in metrics, scheduler continues operating normally.
- What happens when the job scheduler is not initialized but system APIs are called? **Resolved:** GET `/_sys/job` returns empty list (200 OK), POST `/_sys/job/:jobId` returns 404 Not Found indicating the job doesn't exist.
- How does the system behave when invalid schedule parameters are provided (e.g., 25th hour, invalid day of week)?
- What happens when job context is cancelled mid-execution?
- How are jobs affected during application restarts (are missed executions during downtime replayed)?
- What happens when a request to `/_sys/job*` comes from an IP not in the configured CIDR allowlist? **Resolved:** Request is rejected with HTTP 403 Forbidden, with appropriate error message indicating IP-based access restriction.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST provide a Job interface with an Execute method that accepts a job-specific context
- **FR-002**: System MUST provide a JobRegistrar interface for scheduling jobs with various time-based patterns
- **FR-003**: JobRegistrar MUST support fixed-rate scheduling (execute every N duration)
- **FR-004**: JobRegistrar MUST support daily scheduling at a specific local time
- **FR-005**: JobRegistrar MUST support weekly scheduling on a specific day of the week at a specific local time
- **FR-006**: JobRegistrar MUST support hourly scheduling at a specific minute of the hour
- **FR-007**: JobRegistrar MUST support monthly scheduling on a specific day of the month at a specific local time
- **FR-008**: Each job registration MUST require a unique job identifier
- **FR-009**: System MUST expose a GET endpoint at `/_sys/job` that lists all registered jobs
- **FR-010**: Job listing MUST include job identifier and cron-style schedule expression for each job
- **FR-011**: System MUST expose a POST endpoint at `/_sys/job/:jobId` that triggers a specific job by identifier
- **FR-012**: Job triggering MUST execute the job immediately regardless of its schedule
- **FR-013**: System MUST initialize the job scheduler during application startup if jobs are registered
- **FR-014**: System MUST shut down the job scheduler gracefully during application shutdown
- **FR-015**: Scheduler shutdown MUST allow in-flight jobs to complete or cancel gracefully with timeout
- **FR-016**: Job scheduler MUST be optional (applications without scheduled jobs should not require scheduler initialization)
- **FR-017**: Job execution MUST create trace spans with job identifier and execution metadata
- **FR-018**: System MUST emit metrics for job execution count, success count, failure count, and duration
- **FR-019**: Job execution context MUST propagate OpenTelemetry trace context to downstream operations
- **FR-020**: Job execution errors MUST be logged with appropriate severity levels
- **FR-021**: System MUST recover from job panics using recover(), log the panic at ERROR level with full stack trace, and record the execution as failed in metrics without crashing the scheduler or application
- **FR-022**: System MUST validate job identifiers are unique during registration
- **FR-023**: System MUST validate schedule parameters during registration (valid time ranges, day values, etc.)
- **FR-024**: Job execution MUST respect context cancellation for graceful shutdown
- **FR-025**: System MUST provide job-specific context that includes trace context, job identifier, and trigger type (scheduled vs manual)
- **FR-026**: System MUST prevent overlapping executions of the same job by skipping triggers (scheduled or manual) when a job instance is already running
- **FR-027**: When a job trigger is skipped due to an already-running instance, the system MUST log a warning with job identifier and trigger type
- **FR-028**: System MUST provide CIDR-based IP allowlist middleware for `/_sys/job*` endpoints
- **FR-029**: When a CIDR allowlist is configured (non-empty), system MUST reject requests from IPs not matching any CIDR range with HTTP 403 Forbidden
- **FR-030**: CIDR allowlist configuration MUST be optional and managed via standard application config (empty list allows all IPs for local development)
- **FR-031**: When scheduler is not initialized (no jobs registered), GET `/_sys/job` MUST return empty array with HTTP 200 OK
- **FR-032**: When scheduler is not initialized (no jobs registered), POST `/_sys/job/:jobId` MUST return HTTP 404 Not Found with appropriate error message

### Key Entities

- **Job**: Represents a unit of work to be executed on a schedule; contains execute logic that operates on a provided context; identified by a unique job identifier
- **JobContext**: Execution context passed to jobs; includes OpenTelemetry trace context, job identifier, trigger metadata (scheduled vs manual), cancellation capability, and access to framework dependencies (logger, database, etc.)
- **JobRegistrar**: Registry interface for scheduling jobs; associates job identifiers with Job instances and schedule configurations; validates uniqueness and schedule parameters
- **ScheduleConfiguration**: Describes when a job should execute; includes schedule type (fixed-rate, daily, weekly, hourly, monthly), timing parameters (duration, time-of-day, day-of-week, day-of-month), and timezone information for time-based schedules
- **JobMetadata**: Information about a registered job; includes job identifier, schedule description (human-readable and cron expression), next execution time, last execution time and status, execution statistics (total runs, success count, failure count)

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Developers can register and schedule a simple background job in under 10 lines of code
- **SC-002**: All five scheduling patterns (fixed-rate, daily, weekly, hourly, monthly) execute jobs within 1 second of their scheduled time under normal system load
- **SC-003**: Job execution creates complete OpenTelemetry traces that connect to downstream operations (database, HTTP, messaging) with 100% trace context propagation
- **SC-004**: System APIs for listing and triggering jobs respond within 100 milliseconds for applications with up to 100 registered jobs
- **SC-005**: Application shutdown completes gracefully within 30 seconds even with long-running jobs in progress
- **SC-006**: Job execution metrics (count, duration, success/failure rate) are exported to observability backend for all job executions with 100% coverage
- **SC-007**: Job scheduler handles up to 1000 concurrently scheduled jobs without noticeable performance degradation
- **SC-008**: Job panics are recovered and logged without crashing the scheduler, with recovery success rate of 100%
- **SC-009**: Applications without scheduled jobs can run without initializing the scheduler with zero overhead or errors

## Assumptions

- **ASSUME-001**: Timezone for time-based schedules (daily, weekly, hourly, monthly) defaults to system local time unless specified otherwise
- **ASSUME-001a**: DST transitions and clock changes are handled by gocron/v2 library's built-in behavior - framework does not implement custom DST logic
- **ASSUME-002**: Overlapping executions are prevented system-wide: when a job is running, all subsequent triggers (scheduled or manual) are skipped with logged warnings until the current execution completes
- **ASSUME-003**: Missed executions during application downtime are not replayed on restart (jobs resume their schedule from restart time forward)
- **ASSUME-004**: Job identifiers follow standard identifier conventions (alphanumeric, hyphens, underscores; case-sensitive)
- **ASSUME-005**: Manual job triggers via POST `/_sys/job/:jobId` do not affect the next scheduled execution time
- **ASSUME-006**: System APIs (`/_sys/job*`) use CIDR-based IP allowlist for access control; empty allowlist (default) permits all IPs for local development, non-empty list restricts access to matching IP ranges
- **ASSUME-007**: Job scheduler integrates with existing GoBricks module system and uses standard ModuleDeps pattern for dependency injection
- **ASSUME-008**: Jobs have access to framework dependencies (database, logger, messaging, config) through JobContext similar to HTTP handlers
- **ASSUME-009**: Observability instrumentation follows existing GoBricks patterns (OpenTelemetry SDK, W3C traceparent propagation)
- **ASSUME-010**: Graceful shutdown timeout for in-flight jobs defaults to 30 seconds but can be configured
