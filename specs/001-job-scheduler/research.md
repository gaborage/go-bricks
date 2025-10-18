# Phase 0: Research & Design Decisions

**Feature**: Job Scheduler
**Date**: 2025-10-17
**Status**: Complete

## Overview

This document captures research findings and design decisions for integrating job scheduling into the GoBricks framework. All NEEDS CLARIFICATION items from Technical Context have been resolved.

---

## 1. gocron/v2 Integration Patterns

### Decision
Use **gocron/v2.Scheduler** wrapped in GoBricks Module interface with lazy initialization pattern.

### Rationale
- **Proven library**: gocron/v2 is actively maintained, handles DST transitions correctly, supports all required scheduling patterns
- **Lazy initialization**: Scheduler created only when first job is registered, supporting FR-016 (optional jobs with zero overhead)
- **Module integration**: Scheduler implements Module interface (Init, Shutdown) for consistent lifecycle management

### Implementation Pattern

```go
type SchedulerModule struct {
    scheduler *gocron.Scheduler
    jobs      map[string]*jobEntry // jobID -> job + metadata
    mu        sync.RWMutex
    deps      *app.ModuleDeps
}

func (m *SchedulerModule) Init(deps *app.ModuleDeps) error {
    m.deps = deps
    m.jobs = make(map[string]*jobEntry)
    // Scheduler created lazily on first job registration
    return nil
}

func (m *SchedulerModule) ensureScheduler() *gocron.Scheduler {
    if m.scheduler == nil {
        m.scheduler = gocron.NewScheduler()
    }
    return m.scheduler
}
```

### Alternatives Considered
- **Eager initialization**: Create scheduler in Init() - Rejected due to FR-016 requirement for zero overhead when no jobs registered
- **Direct gocron exposure**: Expose gocron.Scheduler directly to users - Rejected due to lack of GoBricks integration (observability, logging, framework dependencies)

---

## 2. Overlapping Execution Prevention

### Decision
Implement **mutex-per-job-ID** pattern with job execution wrapper.

### Rationale
- **Clarification #1 requirement**: Prevent overlapping executions, skip triggers with logged warnings
- **Granular locking**: Per-job mutex allows different jobs to run concurrently while preventing same-job overlap
- **gocron limitation**: gocron/v2 does not provide built-in overlapping prevention - must be implemented in wrapper
- **Performance**: Mutex overhead minimal (<100ns per lock/unlock), acceptable for job execution frequency

### Implementation Pattern

```go
type jobEntry struct {
    job      Job
    schedule ScheduleConfiguration
    metadata *JobMetadata
    mu       sync.Mutex // Prevents overlapping execution
}

func (m *SchedulerModule) wrapJobExecution(jobID string) func() {
    return func() {
        entry := m.jobs[jobID]

        // Try to acquire lock (non-blocking)
        if !entry.mu.TryLock() {
            m.deps.Logger.Warn().
                Str("jobID", jobID).
                Msg("Job execution skipped - already running")
            entry.metadata.incrementSkipped()
            return
        }
        defer entry.mu.Unlock()

        // Execute job with observability wrapper
        m.executeWithObservability(jobID, entry)
    }
}
```

### Alternatives Considered
- **gocron.SingletonMode()**: Use gocron's built-in singleton mode - Rejected because gocron/v2 removed this feature (was in v1)
- **Channel-based queueing**: Queue subsequent triggers - Rejected per clarification #1 (skip, don't queue)
- **Global mutex**: Single lock for all jobs - Rejected due to poor concurrency (different jobs should run in parallel)

---

## 3. CIDR Middleware Implementation

### Decision
Use **Go stdlib `net` package** with Echo middleware pattern.

### Rationale
- **Stdlib sufficiency**: `net.ParseCIDR()` and `net.IPNet.Contains()` provide all required functionality
- **Zero dependencies**: No third-party libraries needed for CIDR matching
- **Performance**: Stdlib implementation optimized, O(1) IP containment check per CIDR range
- **Echo integration**: Middleware pattern consistent with existing GoBricks server package

### Implementation Pattern

```go
// config/scheduler_config.go
type SchedulerConfig struct {
    CIDRAllowlist []string `config:"scheduler.cidr_allowlist"`
    ShutdownTimeout time.Duration `config:"scheduler.shutdown_timeout" default:"30s"`
}

// server/cidr_middleware.go
func CIDRMiddleware(allowlist []string) echo.MiddlewareFunc {
    // Parse CIDRs once at initialization
    networks := parseCIDRs(allowlist)

    return func(next echo.HandlerFunc) echo.HandlerFunc {
        return func(c echo.Context) error {
            if len(networks) == 0 {
                // Empty allowlist = allow all (local dev)
                return next(c)
            }

            clientIP := extractIP(c.Request())
            if !isAllowed(clientIP, networks) {
                return c.JSON(403, map[string]interface{}{
                    "error": "Access denied - IP not in allowlist",
                })
            }

            return next(c)
        }
    }
}
```

### Alternatives Considered
- **github.com/yl2chen/cidranger**: Third-party CIDR library - Rejected as stdlib sufficient for requirement
- **IP-based allowlist (no CIDR)**: Simple IP matching - Rejected as CIDR ranges more flexible for production deployments
- **Application-level auth**: OAuth2/JWT - Deferred per clarification #2 (CIDR sufficient for MVP, more comprehensive auth can be added later)

---

## 4. Observability Integration

### Decision
Wrap every job execution in **OpenTelemetry span + metrics recording + panic recovery**.

### Rationale
- **Constitution V requirement**: Observability as first-class citizen
- **FR-017 to FR-021**: Explicit requirements for spans, metrics, logging
- **Dual-mode logging**: Action logs for job lifecycle (100% sampling), ERROR logs for panics
- **Context propagation**: JobContext embeds OpenTelemetry context, automatically propagates to downstream operations (database, HTTP, messaging)

### Implementation Pattern

```go
func (m *SchedulerModule) executeWithObservability(jobID string, entry *jobEntry) {
    ctx := context.Background()

    // Start OpenTelemetry span
    tracer := m.deps.TraceProvider.Tracer("scheduler")
    ctx, span := tracer.Start(ctx, "job.execute",
        trace.WithAttributes(
            attribute.String("job.id", jobID),
            attribute.String("job.trigger", "scheduled"), // or "manual"
        ))
    defer span.End()

    // Create JobContext
    jobCtx := &JobContextImpl{
        Context:      ctx,
        JobID:        jobID,
        TriggerType:  "scheduled",
        Logger:       m.deps.Logger,
        DB:           m.deps.DB,
        Messaging:    m.deps.Messaging,
        Config:       m.deps.Config,
    }

    // Record start time for metrics
    start := time.Now()

    // Panic recovery
    defer func() {
        if r := recover(); r != nil {
            stack := debug.Stack()
            m.deps.Logger.Error().
                Str("jobID", jobID).
                Interface("panic", r).
                Bytes("stack", stack).
                Msg("Job panicked")

            span.RecordError(fmt.Errorf("panic: %v", r))
            span.SetStatus(codes.Error, "panic")
            entry.metadata.incrementFailed()

            // Record metric
            m.recordMetric(jobID, "failure", time.Since(start))
        }
    }()

    // Execute job
    err := entry.job.Execute(jobCtx)

    // Record outcome
    if err != nil {
        span.RecordError(err)
        span.SetStatus(codes.Error, err.Error())
        entry.metadata.incrementFailed()
        m.recordMetric(jobID, "failure", time.Since(start))
    } else {
        span.SetStatus(codes.Ok, "success")
        entry.metadata.incrementSuccess()
        m.recordMetric(jobID, "success", time.Since(start))
    }
}
```

### Metrics Schema

```go
// Counters
job_executions_total{job_id, status} // status: success|failure|skipped
job_panics_total{job_id}

// Histograms
job_execution_duration_seconds{job_id} // Duration distribution

// Gauges
job_running{job_id} // 0 or 1 (currently executing)
jobs_registered_total // Total jobs in scheduler
```

### Alternatives Considered
- **Minimal observability**: Basic logging only - Rejected due to Constitution V requirement
- **Sampling**: Sample traces for job executions - Rejected as job execution frequency low enough for 100% sampling
- **No panic recovery**: Let jobs crash scheduler - Rejected per FR-021 and clarification #4

---

## 5. Graceful Shutdown Patterns

### Decision
Use **context cancellation with timeout** + **WaitGroup tracking**.

### Rationale
- **FR-015 requirement**: Allow in-flight jobs to complete or cancel gracefully with timeout
- **ASSUME-010**: Default 30s shutdown timeout, configurable via `scheduler.shutdown_timeout`
- **No goroutine leaks**: WaitGroup ensures all job goroutines complete before shutdown returns
- **Respect context**: Jobs receive cancelled context, can check and cleanup gracefully

### Implementation Pattern

```go
type SchedulerModule struct {
    scheduler  *gocron.Scheduler
    jobs       map[string]*jobEntry
    wg         sync.WaitGroup
    shutdownCh chan struct{}
    mu         sync.RWMutex
    deps       *app.ModuleDeps
}

func (m *SchedulerModule) Shutdown() error {
    close(m.shutdownCh) // Signal shutdown

    if m.scheduler != nil {
        m.scheduler.Shutdown() // Stop accepting new executions
    }

    // Wait for in-flight jobs with timeout
    shutdownTimeout := m.getShutdownTimeout() // From config, default 30s
    done := make(chan struct{})

    go func() {
        m.wg.Wait() // Wait for all job executions
        close(done)
    }()

    select {
    case <-done:
        m.deps.Logger.Info().Msg("All jobs completed gracefully")
        return nil
    case <-time.After(shutdownTimeout):
        m.deps.Logger.Warn().
            Dur("timeout", shutdownTimeout).
            Msg("Shutdown timeout reached, some jobs may not have completed")
        return fmt.Errorf("shutdown timeout after %v", shutdownTimeout)
    }
}

func (m *SchedulerModule) wrapJobExecution(jobID string) func() {
    return func() {
        select {
        case <-m.shutdownCh:
            // Shutdown in progress, skip execution
            return
        default:
        }

        m.wg.Add(1)
        defer m.wg.Done()

        // ... existing execution logic with context cancellation support
    }
}
```

### Alternatives Considered
- **Immediate shutdown**: Kill all running jobs - Rejected per FR-015 (graceful completion required)
- **Unbounded wait**: Wait indefinitely for jobs - Rejected due to deployment risk (hangs on misbehaving jobs)
- **No WaitGroup**: Rely on gocron shutdown only - Rejected as doesn't guarantee goroutine cleanup

---

## Summary of Design Decisions

| Area | Decision | Key Rationale |
|------|----------|---------------|
| **Scheduler Library** | gocron/v2 | Proven, handles DST, supports all required patterns |
| **Initialization** | Lazy (on first job) | Zero overhead when no jobs (FR-016) |
| **Overlap Prevention** | Mutex per job ID | Granular locking, skip with warning per clarification #1 |
| **CIDR Matching** | Go stdlib `net` package | Sufficient, zero dependencies, Echo middleware pattern |
| **Observability** | Span + metrics + panic recovery wrapper | Constitution V, FR-017 to FR-021 |
| **Shutdown** | Context cancellation + WaitGroup + timeout | Graceful with configurable timeout (FR-015, ASSUME-010) |
| **JobContext** | Mirrors HTTP handler pattern | Consistent UX, access to framework dependencies |
| **Configuration** | Standard `Config.InjectInto()` pattern | Follows GoBricks conventions |

---

## Resolved Unknowns

All NEEDS CLARIFICATION items from plan.md Technical Context have been resolved:

✅ **Primary Dependencies**: gocron/v2, Echo, OpenTelemetry, zerolog, koanf (all identified)
✅ **Testing**: testify, httptest, time-based testing with fixed clocks, race detection
✅ **Performance Goals**: <1ms job overhead, <100ms API response, 1000 jobs supported, <30s shutdown
✅ **Constraints**: Overlap prevention, optional init, CIDR security, DST delegation, panic recovery

**Status**: Ready for Phase 1 (Data Model & Contracts)
