# Tasks: Job Scheduler

**Input**: Design documents from `/specs/001-job-scheduler/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/job-system-api.yaml

**Tests**: Tests are included per Constitution III (Test-First Development) - 80% coverage target is mandatory for framework components.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing of each story.

## Format: `[ID] [P?] [Story] Description`
- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story this task belongs to (e.g., US1, US2, US3)
- Include exact file paths in descriptions

## Path Conventions
- **Framework component**: New `scheduler/` package at repository root
- **Extensions**: `server/` (existing), `config/` (existing)
- **Examples**: `examples/scheduler/`
- **Tests**: Alongside source files (`*_test.go`)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization and package structure

- [ ] T001 Add gocron/v2 dependency to go.mod (go get github.com/go-co-op/gocron/v2)
- [ ] T002 Create scheduler/ package directory structure
- [ ] T003 [P] Create examples/scheduler/ directory for demo application
- [ ] T004 [P] Create specs/001-job-scheduler/checklists/ for validation checklists (if not exists)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core interfaces and types that ALL user stories depend on

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [ ] T005 Define Job interface in scheduler/job.go
- [ ] T006 Define JobContext interface and implementation in scheduler/job.go
- [ ] T007 Define JobRegistrar interface in scheduler/registrar.go
- [ ] T008 [P] Define ScheduleConfiguration types in scheduler/schedule.go
- [ ] T009 [P] Define JobMetadata struct in scheduler/metadata.go
- [ ] T010 [P] Define SchedulerConfig in config/scheduler_config.go
- [ ] T011 Create jobEntry internal struct in scheduler/scheduler.go

**Checkpoint**: Foundation ready - user story implementation can now begin in parallel

---

## Phase 3: User Story 1 - Schedule Recurring Background Tasks (Priority: P1) 🎯 MVP

**Goal**: Enable developers to schedule jobs with 5 scheduling patterns (fixed-rate, daily, weekly, hourly, monthly) integrated with GoBricks module system.

**Independent Test**: Register a simple job (e.g., logging "Hello World"), schedule it to run every 5 seconds, wait 15 seconds, and verify the job executed 3 times. No dependencies on other user stories.

### Tests for User Story 1

**NOTE: Write these tests FIRST, ensure they FAIL before implementation**

- [ ] T012 [P] [US1] Unit test for Job interface and JobContext in scheduler/job_test.go
- [ ] T013 [P] [US1] Unit test for JobRegistrar.FixedRate in scheduler/registrar_test.go
- [ ] T014 [P] [US1] Unit test for JobRegistrar.DailyAt in scheduler/registrar_test.go
- [ ] T015 [P] [US1] Unit test for JobRegistrar.WeeklyAt in scheduler/registrar_test.go
- [ ] T016 [P] [US1] Unit test for JobRegistrar.HourlyAt in scheduler/registrar_test.go
- [ ] T017 [P] [US1] Unit test for JobRegistrar.MonthlyAt in scheduler/registrar_test.go
- [ ] T018 [US1] Integration test for scheduler lifecycle (startup, execute, shutdown) in scheduler/integration_test.go

### Implementation for User Story 1

- [ ] T019 [US1] Implement JobRegistrar with all 5 scheduling methods in scheduler/registrar.go
- [ ] T020 [US1] Implement SchedulerModule struct with Module interface (Init, RegisterRoutes, DeclareMessaging, Shutdown) in scheduler/module.go
- [ ] T021 [US1] Implement lazy scheduler initialization (create gocron scheduler on first job registration) in scheduler/scheduler.go
- [ ] T022 [US1] Implement job execution wrapper with JobContext creation in scheduler/scheduler.go
- [ ] T023 [US1] Implement graceful shutdown with context cancellation and WaitGroup in scheduler/module.go
- [ ] T024 [US1] Add schedule parameter validation (unique job IDs, valid time ranges) in scheduler/registrar.go
- [ ] T025 [US1] Add configuration injection for SchedulerConfig (shutdown timeout) in scheduler/module.go
- [ ] T026 [US1] Implement example cleanup job in examples/scheduler/jobs/cleanup_job.go
- [ ] T027 [US1] Implement example main.go demonstrating all 5 scheduling patterns in examples/scheduler/main.go

**Checkpoint**: At this point, User Story 1 should be fully functional - developers can register and schedule jobs with all 5 patterns, jobs execute automatically, graceful shutdown works

---

## Phase 4: User Story 2 - Monitor Scheduled Jobs (Priority: P2)

**Goal**: Provide operational visibility into scheduled jobs via GET /_sys/job API endpoint showing job metadata, schedules, and execution statistics.

**Independent Test**: Register 3 different jobs with varied schedules, call GET /_sys/job, and verify response shows all jobs with cron expressions, next execution times, and statistics. No dependencies on US3-US5.

### Tests for User Story 2

- [ ] T028 [P] [US2] Contract test for GET /_sys/job endpoint in server/job_handlers_test.go
- [ ] T029 [P] [US2] Unit test for JobMetadata struct and methods in scheduler/metadata_test.go
- [ ] T030 [P] [US2] Integration test for GET /_sys/job with no jobs (empty list) in server/job_handlers_test.go
- [ ] T031 [P] [US2] Integration test for GET /_sys/job with multiple jobs in server/job_handlers_test.go

### Implementation for User Story 2

- [ ] T032 [P] [US2] Implement JobMetadata update methods (incrementSuccess, incrementFailed, incrementSkipped) in scheduler/metadata.go
- [ ] T033 [US2] Implement cron expression generation from ScheduleConfiguration in scheduler/schedule.go
- [ ] T034 [US2] Implement human-readable schedule description generation in scheduler/schedule.go
- [ ] T035 [US2] Implement GET /_sys/job handler (list all jobs) in server/job_handlers.go
- [ ] T036 [US2] Add response envelope formatting (data + meta) per GoBricks convention in server/job_handlers.go
- [ ] T037 [US2] Handle edge case: scheduler not initialized (return empty list with 200 OK) in server/job_handlers.go
- [ ] T038 [US2] Register /_sys/job routes in SchedulerModule.RegisterRoutes in scheduler/module.go

**Checkpoint**: At this point, User Stories 1 AND 2 should both work independently - jobs can be scheduled (US1) and listed via API (US2)

---

## Phase 5: User Story 3 - Manually Trigger Jobs (Priority: P3)

**Goal**: Enable developers and operators to manually trigger jobs on demand via POST /_sys/job/:jobId for testing, recovery, or ad-hoc execution.

**Independent Test**: Register a job, use POST /_sys/job/:jobId to trigger it, and verify job executes immediately regardless of schedule. No dependencies on US4-US5.

### Tests for User Story 3

- [ ] T039 [P] [US3] Contract test for POST /_sys/job/:jobId endpoint in server/job_handlers_test.go
- [ ] T040 [P] [US3] Integration test for manual trigger success (200) in server/job_handlers_test.go
- [ ] T041 [P] [US3] Integration test for manual trigger job not found (404) in server/job_handlers_test.go
- [ ] T042 [P] [US3] Integration test for manual trigger already running (409) in server/job_handlers_test.go

### Implementation for User Story 3

- [ ] T043 [US3] Implement POST /_sys/job/:jobId handler (manual trigger) in server/job_handlers.go
- [ ] T044 [US3] Implement job trigger logic with JobContext creation (triggerType="manual") in scheduler/scheduler.go
- [ ] T045 [US3] Handle edge case: job not found (404 response) in server/job_handlers.go
- [ ] T046 [US3] Handle edge case: job already running (409 response, overlapping prevention check) in server/job_handlers.go
- [ ] T047 [US3] Register POST /_sys/job/:jobId route in SchedulerModule.RegisterRoutes in scheduler/module.go

**Checkpoint**: At this point, User Stories 1-3 should all work independently - jobs can be scheduled (US1), listed (US2), and manually triggered (US3)

---

## Phase 6: User Story 4 - Graceful Lifecycle Management (Priority: P2)

**Goal**: Ensure jobs start automatically on application startup and stop gracefully on shutdown without orphaned processes or incomplete work.

**Independent Test**: Start application with registered jobs, verify jobs begin executing, initiate shutdown, confirm all jobs stop gracefully within timeout without hanging goroutines. Already partially implemented in US1 but needs comprehensive testing.

### Tests for User Story 4

- [ ] T048 [P] [US4] Unit test for graceful shutdown with timeout in scheduler/module_test.go
- [ ] T049 [P] [US4] Integration test for shutdown with in-flight jobs (should complete) in scheduler/integration_test.go
- [ ] T050 [P] [US4] Integration test for shutdown timeout exceeded (should return after timeout) in scheduler/integration_test.go
- [ ] T051 [P] [US4] Integration test for optional scheduler (no jobs registered) in scheduler/integration_test.go
- [ ] T052 [P] [US4] Race detection test for concurrent job execution and shutdown in scheduler/integration_test.go

### Implementation for User Story 4

- [ ] T053 [US4] Enhance shutdown logic to respect configurable timeout from SchedulerConfig in scheduler/module.go
- [ ] T054 [US4] Implement shutdown channel to signal shutdown to job wrappers in scheduler/scheduler.go
- [ ] T055 [US4] Add WaitGroup tracking for all job executions in scheduler/scheduler.go
- [ ] T056 [US4] Implement context cancellation propagation to running jobs via JobContext in scheduler/job.go
- [ ] T057 [US4] Add logging for shutdown events (started, completed, timeout) in scheduler/module.go
- [ ] T058 [US4] Verify no goroutine leaks with pprof or runtime checks in scheduler/integration_test.go

**Checkpoint**: At this point, User Stories 1-4 are complete - jobs have full lifecycle management with graceful startup/shutdown

---

## Phase 7: User Story 5 - Observability and Tracing (Priority: P2)

**Goal**: Full observability for job execution through OpenTelemetry traces, metrics, and logs for production diagnostics and performance monitoring.

**Independent Test**: Configure observability, execute a job, verify traces show job execution span with context, metrics track count/duration, logs capture events. Builds on US1 but independent feature.

### Tests for User Story 5

- [ ] T059 [P] [US5] Unit test for span creation and attributes in scheduler/observability_test.go
- [ ] T060 [P] [US5] Unit test for metrics recording (success, failure, skipped) in scheduler/observability_test.go
- [ ] T061 [P] [US5] Integration test for trace context propagation through JobContext in scheduler/integration_test.go
- [ ] T062 [P] [US5] Integration test for panic recovery and ERROR logging in scheduler/integration_test.go
- [ ] T063 [P] [US5] Integration test for observability disabled (no errors) in scheduler/integration_test.go

### Implementation for User Story 5

- [ ] T064 [P] [US5] Implement OpenTelemetry span creation per job execution in scheduler/observability.go
- [ ] T065 [P] [US5] Implement metrics recording (counters, histograms, gauges) in scheduler/observability.go
- [ ] T066 [P] [US5] Register metrics with OpenTelemetry MeterProvider in scheduler/observability.go
- [ ] T067 [US5] Integrate span creation into job execution wrapper in scheduler/scheduler.go
- [ ] T068 [US5] Integrate metrics recording into job execution wrapper (success, failure, duration) in scheduler/scheduler.go
- [ ] T069 [US5] Implement panic recovery with ERROR logging and stack trace in scheduler/scheduler.go
- [ ] T070 [US5] Embed OpenTelemetry context in JobContext for propagation in scheduler/job.go
- [ ] T071 [US5] Add action logs for job lifecycle events (started, completed, skipped) in scheduler/scheduler.go
- [ ] T072 [US5] Add example observability configuration in examples/scheduler/config.yaml

**Checkpoint**: All 5 user stories complete - full job scheduler feature with observability

---

## Phase 8: Overlapping Execution Prevention (Cross-Cutting - Critical)

**Goal**: Prevent concurrent executions of the same job by skipping triggers when job is already running (per clarification #1).

**Purpose**: This is a cross-cutting concern affecting US1 (scheduling), US3 (manual trigger), and US5 (observability - logged warnings).

### Tests for Overlapping Prevention

- [ ] T073 [P] Unit test for mutex-per-job-ID locking in scheduler/overlap_prevention_test.go
- [ ] T074 [P] Integration test for scheduled trigger skip when job running in scheduler/overlap_prevention_test.go
- [ ] T075 [P] Integration test for manual trigger skip when job running in scheduler/overlap_prevention_test.go
- [ ] T076 [P] Race detection test for concurrent trigger attempts in scheduler/overlap_prevention_test.go

### Implementation for Overlapping Prevention

- [ ] T077 Add mutex field to jobEntry struct in scheduler/scheduler.go
- [ ] T078 Implement TryLock() check in job execution wrapper in scheduler/scheduler.go
- [ ] T079 Add warning log when trigger skipped due to running instance in scheduler/scheduler.go
- [ ] T080 Update JobMetadata.incrementSkipped() call in skip path in scheduler/scheduler.go
- [ ] T081 Verify POST /_sys/job/:jobId returns 409 when job running in server/job_handlers.go

**Checkpoint**: Overlapping prevention complete - framework enforces single-execution guarantee

---

## Phase 9: CIDR-Based Security (Cross-Cutting - Security)

**Goal**: Protect /_sys/job* endpoints with CIDR-based IP allowlist middleware (per clarification #2).

**Purpose**: Security requirement affecting US2 (list jobs) and US3 (trigger jobs).

### Tests for CIDR Middleware

- [ ] T082 [P] Unit test for CIDR parsing and IP matching in server/cidr_middleware_test.go
- [ ] T083 [P] Unit test for empty allowlist (localhost-only access) in server/cidr_middleware_test.go
- [ ] T084 [P] Unit test for non-empty allowlist (IP in range allows) in server/cidr_middleware_test.go
- [ ] T085 [P] Unit test for non-empty allowlist (IP not in range rejects with 403) in server/cidr_middleware_test.go
- [ ] T086 [P] Integration test for GET /_sys/job with CIDR rejection in server/job_handlers_test.go
- [ ] T087 [P] Integration test for POST /_sys/job/:jobId with CIDR rejection in server/job_handlers_test.go

### Implementation for CIDR Middleware

- [ ] T088 [P] Implement CIDR parsing from SchedulerConfig in server/cidr_middleware.go
- [ ] T089 [P] Implement IP extraction from echo.Context request in server/cidr_middleware.go
- [ ] T090 Implement CIDRMiddleware func returning echo.MiddlewareFunc in server/cidr_middleware.go
- [ ] T091 Implement empty allowlist logic (localhost-only: 127.0.0.1, ::1) in server/cidr_middleware.go
- [ ] T092 Implement non-empty allowlist matching logic in server/cidr_middleware.go
- [ ] T093 Implement 403 Forbidden response for IP not in allowlist in server/cidr_middleware.go
- [ ] T094 Apply CIDR middleware to /_sys/job routes in scheduler/module.go
- [ ] T095 Add CIDR allowlist configuration example in examples/scheduler/config.yaml

**Checkpoint**: Security complete - system APIs protected by IP-based access control

---

## Phase 10: Polish & Cross-Cutting Concerns

**Purpose**: Documentation, examples, and final validation

- [ ] T096 [P] Create quickstart validation checklist in specs/001-job-scheduler/checklists/quickstart-validation.md
- [ ] T097 [P] Implement example report generation job in examples/scheduler/jobs/report_job.go
- [ ] T098 [P] Add example config.yaml with all scheduler settings in examples/scheduler/config.yaml
- [ ] T099 [P] Add README for examples/scheduler/ explaining how to run demo in examples/scheduler/README.md
- [ ] T100 Run golangci-lint on scheduler/ package and fix any issues
- [ ] T101 Run race detector on all tests (go test -race ./scheduler/...)
- [ ] T102 Verify 80% code coverage threshold with go test -cover ./scheduler/...
- [ ] T103 Generate SonarCloud coverage report (make test-coverage)
- [ ] T104 Update main framework README.md with job scheduler feature section
- [ ] T105 [P] Add ADR (Architecture Decision Record) for overlapping prevention design in wiki/architecture_decisions.md
- [ ] T106 [P] Add ADR for CIDR security choice in wiki/architecture_decisions.md
- [ ] T107 [P] Document gocron/v2 DST behavior in framework docs or CLAUDE.md
- [ ] T108 Validate quickstart.md examples can be executed (copy-paste test)
- [ ] T109 Run examples/scheduler/main.go and verify all 5 scheduling patterns work
- [ ] T110 Verify system APIs work via curl (GET /_sys/job, POST /_sys/job/:jobId)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies - can start immediately
- **Foundational (Phase 2)**: Depends on Setup (T001-T004) - BLOCKS all user stories
- **User Story 1 (Phase 3)**: Depends on Foundational (T005-T011)
- **User Story 2 (Phase 4)**: Depends on Foundational (T005-T011) - independent from US1
- **User Story 3 (Phase 5)**: Depends on Foundational (T005-T011) - independent from US1-US2
- **User Story 4 (Phase 6)**: Depends on US1 (T019-T027) - enhances lifecycle management
- **User Story 5 (Phase 7)**: Depends on US1 (T019-T027) - adds observability
- **Overlapping Prevention (Phase 8)**: Depends on US1 (T019-T027) - critical cross-cutting concern
- **CIDR Security (Phase 9)**: Depends on US2 (T032-T038) and US3 (T043-T047) - secures APIs
- **Polish (Phase 10)**: Depends on all desired user stories being complete

### User Story Dependencies

- **User Story 1 (P1)**: Can start after Foundational - NO dependencies on other stories ✅ **MVP READY**
- **User Story 2 (P2)**: Can start after Foundational - independent from US1 (but typically done after US1 for logical flow)
- **User Story 3 (P3)**: Can start after Foundational - independent from US1-US2
- **User Story 4 (P2)**: Builds on US1 (enhances lifecycle) - not fully independent
- **User Story 5 (P2)**: Builds on US1 (adds observability) - not fully independent

### Within Each User Story

- Tests MUST be written and FAIL before implementation
- Models/interfaces before services
- Services before handlers/endpoints
- Core implementation before integration
- Story complete before moving to next priority

### Parallel Opportunities

**Setup Phase:**
- T003 and T004 can run in parallel (different directories)

**Foundational Phase:**
- T008, T009, T010 can run in parallel (different files)

**User Story 1 Tests:**
- T012, T013, T014, T015, T016, T017 can all run in parallel (different test files/functions)

**User Story 2 Tests:**
- T028, T029, T030, T031 can all run in parallel (different test files/functions)

**User Story 3 Tests:**
- T039, T040, T041, T042 can all run in parallel (different test files/functions)

**User Story 4 Tests:**
- T048, T049, T050, T051, T052 can all run in parallel (different test files/functions)

**User Story 5 Tests:**
- T059, T060, T061, T062, T063 can all run in parallel (different test files/functions)

**User Story 5 Implementation:**
- T064, T065, T066 can run in parallel (different observability components)

**Overlapping Prevention Tests:**
- T073, T074, T075, T076 can all run in parallel (different test files/functions)

**CIDR Middleware Tests:**
- T082, T083, T084, T085, T086, T087 can all run in parallel (different test files/functions)

**CIDR Middleware Implementation:**
- T088, T089 can run in parallel (independent functions)

**Polish Phase:**
- T096, T097, T098, T099 can all run in parallel (different files)
- T105, T106, T107 can run in parallel (different documentation files)

**After Foundational Phase completes:**
- User Stories 2 and 3 can start in parallel (different files, independent)
- User Story 1 must complete before US4 and US5 can start (dependencies)

---

## Parallel Example: User Story 1

```bash
# Launch all tests for User Story 1 together:
Task: "Unit test for Job interface and JobContext in scheduler/job_test.go"
Task: "Unit test for JobRegistrar.FixedRate in scheduler/registrar_test.go"
Task: "Unit test for JobRegistrar.DailyAt in scheduler/registrar_test.go"
Task: "Unit test for JobRegistrar.WeeklyAt in scheduler/registrar_test.go"
Task: "Unit test for JobRegistrar.HourlyAt in scheduler/registrar_test.go"
Task: "Unit test for JobRegistrar.MonthlyAt in scheduler/registrar_test.go"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1: Setup (T001-T004)
2. Complete Phase 2: Foundational (T005-T011) - CRITICAL
3. Complete Phase 3: User Story 1 (T012-T027)
4. **STOP and VALIDATE**: Test US1 independently - can developers schedule jobs with all 5 patterns?
5. Optionally add Phase 8: Overlapping Prevention (T073-T081) for production safety
6. **Deploy/demo MVP** - basic job scheduling works!

**MVP Value**: Developers can schedule background tasks with 5 flexible patterns, jobs execute automatically, graceful shutdown works.

### Incremental Delivery

1. **Foundation** (Phases 1-2): Setup + Foundational → Framework interfaces ready
2. **MVP** (Phase 3): User Story 1 → Basic job scheduling → **Deploy/Demo**
3. **Operational Visibility** (Phase 4): User Story 2 → List jobs API → **Deploy/Demo**
4. **Operational Control** (Phase 5): User Story 3 → Manual trigger API → **Deploy/Demo**
5. **Production Hardening** (Phases 6-9): Lifecycle (US4) + Observability (US5) + Overlapping Prevention + Security → **Deploy/Demo**
6. **Polish** (Phase 10): Documentation, examples, validation → **Final Release**

Each increment adds value without breaking previous stories.

### Parallel Team Strategy

With multiple developers (after Foundational phase completes):

1. **Team completes Setup + Foundational together** (critical path)
2. **Once Foundational is done:**
   - Developer A: User Story 1 (T012-T027)
   - Developer B: User Story 2 (T028-T038) - starts in parallel with A
   - Developer C: User Story 3 (T039-T047) - starts in parallel with A and B
3. **After US1 completes:**
   - Developer A continues: User Story 4 (T048-T058)
   - Developer D (new): User Story 5 (T059-T072) - parallel with A
4. **Cross-cutting concerns** (Phases 8-9) after US1-US5 complete
5. **Polish** (Phase 10) - all developers contribute

---

## Notes

- **[P] tasks** = different files, no dependencies - safe to run in parallel
- **[Story] label** maps task to specific user story for traceability
- **Each user story should be independently completable and testable** (US1, US2, US3 are fully independent; US4-US5 build on US1)
- **Constitution III mandate**: 80% coverage target for framework components - tests are NOT optional
- **Verify tests fail before implementing** (TDD approach per constitution)
- **Race detection** enabled for all tests (critical for scheduler goroutine safety)
- **Commit after each task or logical group** for incremental progress
- **Stop at any checkpoint to validate story independently** before moving to next
- **Avoid**: vague tasks, same file conflicts without [P] marker, cross-story dependencies that break independence
- **CIDR security note**: Empty allowlist now means localhost-only (updated per spec.md clarification #2)

---

## Total Task Count: 110 tasks

### Breakdown by Phase:
- **Setup**: 4 tasks
- **Foundational**: 7 tasks (blocking all stories)
- **User Story 1 (P1)**: 16 tasks (7 tests + 9 implementation) - **MVP**
- **User Story 2 (P2)**: 11 tasks (4 tests + 7 implementation)
- **User Story 3 (P3)**: 9 tasks (4 tests + 5 implementation)
- **User Story 4 (P2)**: 11 tasks (5 tests + 6 implementation)
- **User Story 5 (P2)**: 14 tasks (5 tests + 9 implementation)
- **Overlapping Prevention**: 9 tasks (4 tests + 5 implementation) - critical cross-cutting
- **CIDR Security**: 14 tasks (6 tests + 8 implementation) - security requirement
- **Polish**: 15 tasks

### Parallel Opportunities Identified:
- **61 tasks marked [P]** can run in parallel (different files, no dependencies)
- **User Stories 2 and 3** can start in parallel after Foundational phase
- **Within each story**: Multiple tests can run in parallel, multiple implementation tasks can run in parallel

### Independent Test Criteria per Story:
- **US1**: Register job, schedule every 5s, wait 15s, verify 3 executions
- **US2**: Register 3 jobs, call GET /_sys/job, verify all listed with metadata
- **US3**: Register job, POST /_sys/job/:jobId, verify immediate execution
- **US4**: Start app with jobs, initiate shutdown, verify graceful stop within timeout
- **US5**: Enable observability, execute job, verify traces/metrics/logs

### Suggested MVP Scope:
**Phases 1-3 only** (Setup + Foundational + User Story 1) = 27 tasks
- Delivers: Basic job scheduling with all 5 patterns, automatic execution, graceful shutdown
- Can be deployed and demoed independently
- Provides immediate value to framework users
