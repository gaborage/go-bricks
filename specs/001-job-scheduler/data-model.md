# Phase 1: Data Model

**Feature**: Job Scheduler
**Date**: 2025-10-17
**Status**: Complete

## Overview

This document defines the core entities, interfaces, and data structures for the job scheduler feature. All entities follow GoBricks conventions for type safety, explicit dependencies, and observability integration.

---

## Core Entities

### 1. Job (Interface)

**Purpose**: Contract that application developers implement to define scheduled work.

**Definition**:
```go
package scheduler

// Job represents a unit of work to be executed on a schedule.
// Implementations must be thread-safe as jobs may be triggered concurrently
// (though the scheduler prevents overlapping executions of the SAME job).
type Job interface {
    // Execute performs the job's work using the provided context.
    // Return error to mark execution as failed (will be logged and recorded in metrics).
    // Panic will be recovered, logged with stack trace, and marked as failed.
    Execute(ctx JobContext) error
}
```

**Constraints**:
- Must be thread-safe (different job instances may run concurrently)
- Must respect context cancellation (check `ctx.Done()` for graceful shutdown)
- Must not panic (if it does, scheduler recovers and logs, per FR-021)

**Example Implementation**:
```go
type CleanupJob struct{}

func (j *CleanupJob) Execute(ctx JobContext) error {
    ctx.Logger().Info().Str("jobID", ctx.JobID()).Msg("Starting cleanup")

    // Access framework dependencies
    rows, err := ctx.DB().Query(ctx, "DELETE FROM temp_data WHERE created_at < NOW() - INTERVAL '24 hours'")
    if err != nil {
        return fmt.Errorf("cleanup query failed: %w", err)
    }
    defer rows.Close()

    ctx.Logger().Info().Msg("Cleanup completed")
    return nil
}
```

---

### 2. JobContext (Interface)

**Purpose**: Provides job execution context including framework dependencies, trace context, and metadata.

**Definition**:
```go
package scheduler

import (
    "context"
    "time"

    "github.com/yourusername/gobricks/config"
    "github.com/yourusername/gobricks/database"
    "github.com/yourusername/gobricks/logger"
    "github.com/yourusername/gobricks/messaging"
)

// JobContext provides access to framework dependencies and execution metadata
// during job execution. Embeds context.Context for cancellation and deadlines.
type JobContext interface {
    context.Context // Embed stdlib context for cancellation, deadlines, values

    // JobID returns the unique identifier for this job
    JobID() string

    // TriggerType returns how this job was triggered: "scheduled" or "manual"
    TriggerType() string

    // Logger returns the framework logger with job-specific fields pre-populated
    Logger() logger.Logger

    // DB returns the database interface (may be nil if not configured)
    DB() database.Interface

    // Messaging returns the messaging client (may be nil if not configured)
    Messaging() messaging.Client

    // Config returns the application configuration
    Config() *config.Config
}

// Implementation (internal to scheduler package)
type jobContextImpl struct {
    context.Context
    jobID       string
    triggerType string
    logger      logger.Logger
    db          database.Interface
    messaging   messaging.Client
    config      *config.Config
}

func (ctx *jobContextImpl) JobID() string           { return ctx.jobID }
func (ctx *jobContextImpl) TriggerType() string     { return ctx.triggerType }
func (ctx *jobContextImpl) Logger() logger.Logger   { return ctx.logger }
func (ctx *jobContextImpl) DB() database.Interface  { return ctx.db }
func (ctx *jobContextImpl) Messaging() messaging.Client { return ctx.messaging }
func (ctx *jobContextImpl) Config() *config.Config  { return ctx.config }
```

**Fields**:
- **context.Context**: Embedded for cancellation (shutdown), deadlines, trace context
- **JobID**: Unique identifier for the job (e.g., "cleanup-job", "report-generator")
- **TriggerType**: "scheduled" (automatic) or "manual" (via POST /_sys/job/:jobId)
- **Logger**: Pre-configured with job ID field for consistent logging
- **DB**: Optional database access (nil if not configured in ModuleDeps)
- **Messaging**: Optional messaging client (nil if not configured)
- **Config**: Application configuration for accessing settings

**Usage Notes**:
- JobContext mirrors HTTP handler context pattern (consistent UX per Constitution VII)
- OpenTelemetry context automatically propagated to DB, Messaging calls (per FR-019)
- Logger includes job ID automatically for trace correlation

---

### 3. JobRegistrar (Interface)

**Purpose**: API for registering jobs with various scheduling patterns.

**Definition**:
```go
package scheduler

import (
    "time"
)

// JobRegistrar provides methods to schedule jobs with various time-based patterns.
type JobRegistrar interface {
    // FixedRate schedules a job to execute every 'interval' duration.
    // Example: FixedRate("cleanup", job, 30*time.Minute) -> every 30 minutes
    FixedRate(jobID string, job Job, interval time.Duration) error

    // DailyAt schedules a job to execute once per day at the specified local time.
    // Example: DailyAt("report", job, time.Parse("15:04", "03:00")) -> daily at 3:00 AM
    DailyAt(jobID string, job Job, localTime time.Time) error

    // WeeklyAt schedules a job to execute once per week on the specified day and local time.
    // Example: WeeklyAt("backup", job, time.Monday, time.Parse("15:04", "02:00")) -> Mondays at 2:00 AM
    WeeklyAt(jobID string, job Job, dayOfWeek time.Weekday, localTime time.Time) error

    // HourlyAt schedules a job to execute once per hour at the specified minute.
    // Example: HourlyAt("sync", job, 15) -> every hour at HH:15
    HourlyAt(jobID string, job Job, minute int) error

    // MonthlyAt schedules a job to execute once per month on the specified day and local time.
    // Example: MonthlyAt("billing", job, 1, time.Parse("15:04", "00:00")) -> 1st of every month at midnight
    MonthlyAt(jobID string, job Job, dayOfMonth int, localTime time.Time) error
}
```

**Validation Rules** (per FR-022, FR-023):
- **jobID**: Must be unique across all registered jobs, alphanumeric + hyphens/underscores, case-sensitive
- **interval** (FixedRate): Must be > 0
- **minute** (HourlyAt): Must be 0-59
- **dayOfMonth** (MonthlyAt): Must be 1-31
- **dayOfWeek** (WeeklyAt): Must be valid time.Weekday (Sunday-Saturday)
- **localTime**: Hour 0-23, Minute 0-59

**Error Handling**:
- Returns error on validation failure (jobID duplicate, invalid parameters)
- Error message format (per Constitution VII): `scheduler: <field> <message> <action>`
- Example: `scheduler: jobID "cleanup" already registered. Choose a unique identifier.`

---

### 4. ScheduleConfiguration (Struct)

**Purpose**: Internal representation of job schedule (used by metadata, system APIs).

**Definition**:
```go
package scheduler

import "time"

// ScheduleType identifies the scheduling pattern
type ScheduleType string

const (
    ScheduleTypeFixedRate ScheduleType = "fixed-rate"
    ScheduleTypeDaily     ScheduleType = "daily"
    ScheduleTypeWeekly    ScheduleType = "weekly"
    ScheduleTypeHourly    ScheduleType = "hourly"
    ScheduleTypeMonthly   ScheduleType = "monthly"
)

// ScheduleConfiguration describes when a job should execute.
type ScheduleConfiguration struct {
    Type ScheduleType

    // FixedRate fields
    Interval time.Duration // Used when Type == ScheduleTypeFixedRate

    // Time-based fields (daily, weekly, monthly)
    Hour   int // 0-23
    Minute int // 0-59

    // WeeklyAt field
    DayOfWeek time.Weekday // Sunday-Saturday

    // MonthlyAt field
    DayOfMonth int // 1-31

    // Timezone (nil = system local time per ASSUME-001)
    Timezone *time.Location
}
```

**Usage**:
- Constructed internally when jobs are registered via JobRegistrar
- Converted to cron expression for system API responses (GET /_sys/job)
- Stored in jobEntry alongside Job instance

---

### 5. JobMetadata (Struct)

**Purpose**: Job information returned by system APIs (GET /_sys/job).

**Definition**:
```go
package scheduler

import "time"

// JobMetadata contains information about a registered job for system API responses.
type JobMetadata struct {
    JobID        string     `json:"jobId"`
    ScheduleType string     `json:"scheduleType"` // "fixed-rate", "daily", etc.
    CronExpression string   `json:"cronExpression"` // Cron-style representation
    HumanReadable string    `json:"humanReadable"` // e.g., "Every 30 minutes", "Daily at 3:00 AM"
    NextExecutionTime *time.Time `json:"nextExecutionTime,omitempty"` // nil if job not scheduled yet
    LastExecutionTime *time.Time `json:"lastExecutionTime,omitempty"` // nil if never executed
    LastExecutionStatus string   `json:"lastExecutionStatus,omitempty"` // "success", "failure", "skipped"
    TotalExecutions int64      `json:"totalExecutions"`
    SuccessCount    int64      `json:"successCount"`
    FailureCount    int64      `json:"failureCount"`
    SkippedCount    int64      `json:"skippedCount"` // Overlapping execution prevention
}
```

**Field Descriptions**:
- **JobID**: Unique identifier provided during registration
- **ScheduleType**: One of: fixed-rate, daily, weekly, hourly, monthly
- **CronExpression**: Cron-style representation (e.g., "0 3 * * *" for daily at 3 AM)
- **HumanReadable**: User-friendly description (e.g., "Every Monday at 2:00 AM")
- **NextExecutionTime**: When job will execute next (nil if not yet scheduled)
- **LastExecutionTime**: When job last executed (nil if never run)
- **LastExecutionStatus**: "success" (no error), "failure" (error returned or panic), "skipped" (overlapping prevention)
- **TotalExecutions**: Total times job has been triggered (scheduled + manual)
- **SuccessCount**: Executions that completed without error
- **FailureCount**: Executions that returned error or panicked
- **SkippedCount**: Triggers skipped due to already-running instance

**Lifecycle**:
- Created when job is registered
- Updated after each execution (increment counters, update last execution time/status)
- Thread-safe access (protected by jobEntry mutex)

---

## Relationships

```
JobRegistrar
    └─> registers Job instances with ScheduleConfiguration
            └─> stored in jobEntry{Job, ScheduleConfiguration, JobMetadata, mutex}
                    └─> JobMetadata exposed via GET /_sys/job

Job.Execute()
    └─> receives JobContext with framework dependencies
            └─> JobContext embeds OpenTelemetry context
                    └─> propagates to DB, Messaging, downstream operations
```

---

## Configuration Schema

```go
package config

type SchedulerConfig struct {
    // CIDR allowlist for /_sys/job* endpoints
    // Empty list = localhost-only (127.0.0.1, ::1) - safer default prevents accidental exposure
    // Non-empty = restrict to matching IP ranges only
    // Example: []string{"10.0.0.0/8", "192.168.1.0/24"}
    CIDRAllowlist []string `config:"scheduler.cidr_allowlist"`

    // Trusted proxy CIDR ranges for X-Forwarded-For/X-Real-IP validation
    // Empty list = do not trust any proxy headers (prevents header spoofing)
    // Non-empty = only trust headers when immediate peer (RemoteAddr) matches these ranges
    // Uses RFC 7239 right-to-left XFF resolution to find real client IP
    // Example: []string{"10.0.0.0/8"} - trust reverse proxies in 10.x network
    TrustedProxies []string `config:"scheduler.trusted_proxies"`

    // Graceful shutdown timeout for in-flight jobs
    // Default: 30s (per ASSUME-010)
    ShutdownTimeout time.Duration `config:"scheduler.shutdown_timeout" default:"30s"`
}
```

**Configuration Example** (config.yaml):
```yaml
scheduler:
  cidr_allowlist:
    - "10.0.0.0/8"      # Private network
    - "192.168.1.0/24"  # Local network
  trusted_proxies:
    - "10.0.0.0/8"      # Trust reverse proxies in private network
  shutdown_timeout: 45s
```

**Environment Variable Override**:
```bash
SCHEDULER_CIDR_ALLOWLIST="10.0.0.0/8,192.168.1.0/24"
SCHEDULER_TRUSTED_PROXIES="10.0.0.0/8"
SCHEDULER_SHUTDOWN_TIMEOUT=45s
```

**Security Notes**:
- **Localhost-Only Default**: Empty `cidr_allowlist` restricts access to 127.0.0.1 and ::1 only. This prevents accidental exposure of system endpoints when running in production without explicit configuration.
- **Header Spoofing Prevention**: Empty `trusted_proxies` ignores all X-Forwarded-For and X-Real-IP headers. Only configure trusted proxies when running behind known reverse proxies (e.g., nginx, HAProxy) to prevent attackers from bypassing IP allowlisting with forged headers.
- **RFC 7239 Compliance**: When trusted proxies are configured, the middleware walks X-Forwarded-For right-to-left to find the first untrusted IP, preventing proxy chain injection attacks.

---

## Internal Data Structures

### jobEntry (Internal)

```go
// jobEntry represents a registered job with metadata and execution control
type jobEntry struct {
    job      Job
    schedule ScheduleConfiguration
    metadata *JobMetadata
    mu       sync.Mutex // Prevents overlapping execution

    // gocron job handle (for cancellation, metadata extraction)
    gocronJob gocron.Job
}

// Thread-safe metadata updates
func (e *jobEntry) incrementSuccess() {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.metadata.SuccessCount++
    e.metadata.TotalExecutions++
    now := time.Now()
    e.metadata.LastExecutionTime = &now
    e.metadata.LastExecutionStatus = "success"
}

func (e *jobEntry) incrementFailed() {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.metadata.FailureCount++
    e.metadata.TotalExecutions++
    now := time.Now()
    e.metadata.LastExecutionTime = &now
    e.metadata.LastExecutionStatus = "failure"
}

func (e *jobEntry) incrementSkipped() {
    e.mu.Lock()
    defer e.mu.Unlock()
    e.metadata.SkippedCount++
    // Note: skipped executions don't update LastExecutionTime or LastExecutionStatus
}
```

---

## Type Safety Guarantees

| Entity | Type Safety Mechanism | Benefit |
|--------|----------------------|---------|
| **Job** | Interface with typed Execute(JobContext) | Compile-time contract enforcement |
| **JobContext** | Typed accessors (no `interface{}`) | IDE autocomplete, refactor-safe |
| **JobRegistrar** | Typed parameters (time.Duration, time.Weekday) | Invalid values caught at registration |
| **ScheduleConfiguration** | Typed ScheduleType enum | Exhaustive switch checking |
| **JobMetadata** | JSON struct tags | OpenAPI schema generation |

---

## Summary

All entities defined with:
- ✅ **Explicit typing** (no `interface{}`, per Constitution II)
- ✅ **Thread safety** (mutexes for shared state)
- ✅ **Validation** (registration-time checks, per FR-022, FR-023)
- ✅ **Observability** (JobContext propagates trace context)
- ✅ **Consistency** (mirrors HTTP handler patterns, per Constitution VII)

**Status**: Ready for contract definition (OpenAPI specs for system APIs)
