# Job Scheduler Quickstart Guide

## GoBricks Framework - Job Scheduler Feature**

This guide shows you how to add scheduled background tasks to your GoBricks application in under 10 lines of code.

---

## Overview

The GoBricks job scheduler enables you to run recurring background tasks with five scheduling patterns:

- **Fixed-rate**: Execute every N duration (e.g., every 30 minutes)
- **Daily**: Execute once per day at a specific time (e.g., daily at 3:00 AM)
- **Weekly**: Execute once per week on a specific day and time (e.g., Mondays at 9:00 AM)
- **Hourly**: Execute once per hour at a specific minute (e.g., every hour at :15)
- **Monthly**: Execute once per month on a specific day and time (e.g., 1st of month at midnight)

Jobs integrate seamlessly with GoBricks' observability (OpenTelemetry traces, metrics, logs), configuration management, and module system.

---

## Quick Example

```go
package main

import (
    "fmt"
    "time"

    "github.com/gaborage/go-bricks/app"
    "github.com/gaborage/go-bricks/scheduler"
)

// 1. Define a job by implementing the scheduler.Job interface
type CleanupJob struct{}

func (j *CleanupJob) Execute(ctx scheduler.JobContext) error {
    ctx.Logger().Info().Str("jobID", ctx.JobID()).Msg("Starting cleanup")

    // Access framework dependencies through JobContext
    rows, err := ctx.DB().Query(ctx, "DELETE FROM temp_data WHERE created_at < NOW() - INTERVAL '24 hours'")
    if err != nil {
        return fmt.Errorf("cleanup failed: %w", err)
    }
    defer rows.Close()

    ctx.Logger().Info().Msg("Cleanup completed")
    return nil
}

// 2. Implement JobProvider interface in your module
type MyModule struct {}

func (m *MyModule) Init(deps *app.ModuleDeps) error {
    // Module initialization logic here
    return nil
}

func (m *MyModule) RegisterJobs(reg app.JobRegistrar) error {
    // Register cleanup job to run daily at 3:00 AM
    localTime, _ := time.Parse("15:04", "03:00")
    return reg.DailyAt("cleanup-job", &CleanupJob{}, localTime)
}
```

**That's it!** Your job will now execute automatically at 3:00 AM every day with full observability (traces, metrics, logs).

---

## Step-by-Step Guide

### Step 1: Create a Job

Implement the `scheduler.Job` interface:

```go
type ReportGeneratorJob struct {
    // You can inject dependencies here
    reportService *ReportService
}

func (j *ReportGeneratorJob) Execute(ctx scheduler.JobContext) error {
    // Access job metadata
    ctx.Logger().Info().
        Str("jobID", ctx.JobID()).
        Str("trigger", ctx.TriggerType()). // "scheduled" or "manual"
        Msg("Generating daily report")

    // Use framework dependencies
    report, err := j.reportService.Generate(ctx)
    if err != nil {
        // Errors are automatically logged and recorded in metrics
        return fmt.Errorf("report generation failed: %w", err)
    }

    // Access config
    recipients := ctx.Config().String("report.recipients")

    // Use messaging
    if ctx.Messaging() != nil {
        ctx.Messaging().Publish(ctx, "reports", report)
    }

    ctx.Logger().Info().Msg("Report generated successfully")
    return nil
}
```

### Step 2: Register the Job

Implement the `JobProvider` interface in your module to register jobs. The framework calls `RegisterJobs()` automatically after all modules are initialized:

```go
func (m *MyModule) Init(deps *app.ModuleDeps) error {
    // Module initialization logic here
    return nil
}

// RegisterJobs is called automatically after all modules are initialized
func (m *MyModule) RegisterJobs(reg app.JobRegistrar) error {
    // Fixed-rate: every 30 minutes
    err := reg.FixedRate("sync-job", &SyncJob{}, 30*time.Minute)
    if err != nil {
        return err
    }

    // Daily: at 3:00 AM local time
    err = reg.DailyAt("cleanup-job", &CleanupJob{}, scheduler.ParseTime("03:00"))
    if err != nil {
        return err
    }

    // Weekly: Mondays at 9:00 AM
    err = reg.WeeklyAt("report-job", &ReportJob{}, time.Monday, scheduler.ParseTime("09:00"))
    if err != nil {
        return err
    }

    // Hourly: at 15 minutes past every hour
    err = reg.HourlyAt("health-check", &HealthCheckJob{}, 15)
    if err != nil {
        return err
    }

    // Monthly: 1st of month at midnight
    return reg.MonthlyAt("billing-job", &BillingJob{}, 1, scheduler.ParseTime("00:00"))
}
```

### Step 3: Register Modules (Order Doesn't Matter!)

With the two-phase registration pattern, you can register modules in any order:

```go
func main() {
    app, log, err := app.New()
    if err != nil {
        log.Fatal().Err(err).Msg("Failed to create app")
    }

    // Register modules in any order - framework handles the rest
    app.RegisterModule(&products.Module{})  // Implements JobProvider
    app.RegisterModule(&reports.Module{})   // Implements JobProvider

    if err := app.Run(); err != nil {
        log.Fatal().Err(err).Msg("Application error")
    }
}
```

**How it works**: The framework uses a two-phase initialization:
1. **Phase 1 (Init)**: All modules initialize via `Init(deps)` - setup dependencies, validate config
2. **Phase 2 (RegisterJobs)**: Modules implementing `JobProvider` register jobs via `RegisterJobs(scheduler)`

This design eliminates registration order dependencies - jobs are registered after all modules are initialized.

### Step 4: Handle Job Lifecycle

Jobs automatically respect application lifecycle:

```go
// Startup: Jobs are scheduled automatically when app starts
// (if you registered them in Init)

// Shutdown: In-flight jobs complete or are cancelled gracefully
// (configurable timeout, default 30s)

// No additional code required!
```

---

## Configuration

### Scheduler Configuration

Add to `config.yaml`:

```yaml
scheduler:
  security:
    # CIDR allowlist for /_sys/job* system APIs
    # Empty list = localhost-only (127.0.0.1, ::1) - safe default
    cidrallowlist:
      - "10.0.0.0/8"      # Private network
      - "192.168.1.0/24"  # Specific subnet

    # Trust reverse proxies for X-Forwarded-For extraction
    trustedproxies:
      - "10.0.0.0/8"      # Trust reverse proxies in private network

  timeout:
    # Graceful shutdown timeout for in-flight jobs
    shutdown: 30s

    # Slow job execution threshold for logging
    # Jobs exceeding this duration get result_code="WARN"
    slowjob: 30s
```

### Environment Variable Override

```bash
# Security
SCHEDULER_SECURITY_CIDRALLOWLIST="10.0.0.0/8,192.168.1.0/24"
SCHEDULER_SECURITY_TRUSTEDPROXIES="10.0.0.0/8"

# Timeouts
SCHEDULER_TIMEOUT_SHUTDOWN=45s
SCHEDULER_TIMEOUT_SLOWJOB=60s
```

---

## System APIs

The scheduler exposes two operational endpoints:

### List All Jobs

```bash
curl http://localhost:8080/_sys/job
```

**Response**:
```json
{
  "data": [
    {
      "jobId": "cleanup-job",
      "scheduleType": "daily",
      "cronExpression": "0 3 * * *",
      "humanReadable": "Daily at 3:00 AM",
      "nextExecutionTime": "2025-10-18T03:00:00Z",
      "lastExecutionTime": "2025-10-17T03:00:00Z",
      "lastExecutionStatus": "success",
      "totalExecutions": 42,
      "successCount": 41,
      "failureCount": 1,
      "skippedCount": 0
    }
  ],
  "meta": {
    "total": 1
  }
}
```

### Manually Trigger a Job

```bash
curl -X POST http://localhost:8080/_sys/job/cleanup-job
```

**Response (Success)**:
```json
{
  "data": {
    "jobId": "cleanup-job",
    "trigger": "manual",
    "message": "Request accepted: job will run unless an instance is already running"
  },
  "meta": {}
}
```

**Note**: The response is returned immediately (HTTP 202 Accepted). The job executes asynchronously. If already running, it will be skipped with a logged warning.

---

## Observability

### Traces

Every job execution creates an OpenTelemetry span:

```text
job.execute
├── span.attributes
│   ├── job.id = "cleanup-job"
│   ├── job.trigger = "scheduled"  (or "manual")
│   └── job.status = "success"  (or "failure")
├── span.duration = 1.234s
└── child spans (DB queries, HTTP calls, etc.)
```

### Metrics

The scheduler emits these metrics:

```prometheus
# Total job executions by status
job_executions_total{job_id="cleanup-job",status="success"} 41
job_executions_total{job_id="cleanup-job",status="failure"} 1
job_executions_total{job_id="cleanup-job",status="skipped"} 0

# Job execution duration (histogram)
job_execution_duration_seconds{job_id="cleanup-job"} 1.234

# Panic recovery counter
job_panics_total{job_id="cleanup-job"} 0

# Currently running jobs (gauge)
job_running{job_id="cleanup-job"} 0

# Total registered jobs
jobs_registered_total 3
```

### Logs

**Action Logs** (100% sampled, structured):
```json
{
  "level": "info",
  "log.type": "action",
  "time": "2025-10-17T03:00:00Z",
  "job.id": "cleanup-job",
  "job.schedule_type": "daily",
  "job.trigger": "scheduled",
  "job.execution.duration": 1234000000,
  "job.status": "success",
  "result_code": "INFO",
  "correlation_id": "abc123",
  "traceparent": "00-abc123-def456-01",
  "tenant": "acme-corp",
  "db_queries": 3,
  "db_elapsed": 450000000,
  "amqp_published": 1,
  "amqp_elapsed": 120000000,
  "message": "Job 'cleanup-job' completed successfully in 1.234s"
}
```

**Slow Job Detection** (exceeds 30s threshold):
```json
{
  "level": "info",
  "log.type": "action",
  "result_code": "WARN",
  "job.execution.duration": 45000000000,
  "message": "Job 'heavy-report' completed successfully in 45s"
}
```

**Panic Logs** (ERROR level with stack trace):
```json
{
  "level": "error",
  "time": "2025-10-17T03:00:05Z",
  "jobID": "cleanup-job",
  "panic": "runtime error: index out of range",
  "stack": "goroutine 123 [running]:\n...",
  "message": "Job panicked"
}
```

---

## Advanced Patterns

### Respecting Context Cancellation

Jobs should check `ctx.Done()` for graceful shutdown:

```go
func (j *LongRunningJob) Execute(ctx scheduler.JobContext) error {
    for i := 0; i < 1000; i++ {
        // Check for shutdown signal
        select {
        case <-ctx.Done():
            ctx.Logger().Warn().Msg("Job cancelled during execution")
            return ctx.Err() // Returns context.Canceled
        default:
        }

        // Do work
        processItem(i)
    }
    return nil
}
```

### Conditional Job Registration

Register jobs conditionally based on config:

```go
func (m *MyModule) Init(deps *app.ModuleDeps) error {
    if deps.Config().Bool("features.cleanup.enabled") {
        return deps.Scheduler.DailyAt("cleanup", &CleanupJob{}, mustParseTime("03:00"))
    }
    return nil // No jobs registered - zero overhead
}
```

### Job Dependencies

Inject dependencies into your job struct:

```go
type NotificationJob struct {
    emailService *EmailService
    smsService   *SMSService
}

func (m *MyModule) Init(deps *app.ModuleDeps) error {
    job := &NotificationJob{
        emailService: m.emailService,
        smsService:   m.smsService,
    }
    return deps.Scheduler.DailyAt("notifications", job, scheduler.ParseTime("08:00"))
}
```

### Multi-Tenant Job Contexts

When running GoBricks in multi-tenant mode, jobs automatically support tenant-specific resources:

```go
type TenantReportJob struct {
    tenantID string
}

func (j *TenantReportJob) Execute(ctx scheduler.JobContext) error {
    // DB() and Messaging() resolve dynamically based on tenant context
    // The context carries tenant information from job registration

    db := ctx.DB()  // Returns tenant-specific database connection
    if db == nil {
        return fmt.Errorf("database not available")
    }

    // Query tenant-specific data
    rows, err := db.Query(ctx, "SELECT * FROM reports WHERE tenant_id = ?", j.tenantID)
    if err != nil {
        return err
    }
    defer rows.Close()

    // Send to tenant-specific messaging queue
    if msg := ctx.Messaging(); msg != nil {
        msg.Publish(ctx, "tenant.reports", reportData)
    }

    return nil
}

func (m *MyModule) Init(deps *app.ModuleDeps) error {
    // Register jobs for each tenant
    for _, tenantID := range m.tenantIDs {
        job := &TenantReportJob{tenantID: tenantID}
        jobID := fmt.Sprintf("report-%s", tenantID)

        err := deps.Scheduler.DailyAt(jobID, job, scheduler.ParseTime("03:00"))
        if err != nil {
            return err
        }
    }
    return nil
}
```

**Key points**:
- `JobContext.DB()` and `JobContext.Messaging()` are resolver functions
- In single-tenant mode: Return global DB/messaging instances
- In multi-tenant mode: Resolve from context based on tenant ID
- Jobs don't need conditional logic - framework handles tenant resolution

---

## Securing System APIs

### CIDR Allowlist

Protect `/_sys/job*` endpoints with IP-based access control:

```yaml
scheduler:
  security:
    cidrallowlist:
      - "10.0.0.0/8"         # Private network
      - "172.16.0.0/12"      # Docker internal
      - "203.0.113.42/32"    # Specific monitoring server
```

**Behavior**:
- **Empty list** (default): Localhost-only (127.0.0.1, ::1) - prevents accidental exposure
- **Non-empty list**: Only matching IPs allowed, others get 403 Forbidden

### Trusted Proxies (X-Forwarded-For Validation)

When running behind reverse proxies (nginx, HAProxy, AWS ALB), configure trusted proxy ranges to properly extract client IPs:

```yaml
scheduler:
  security:
    cidrallowlist:
      - "10.0.0.0/8"         # Allow private network clients
    trustedproxies:
      - "10.0.0.0/8"         # Trust reverse proxies in private network
```

**How it works (RFC 7239 compliant)**:
1. Request arrives from reverse proxy at `10.0.0.5`
2. Request has `X-Forwarded-For: 203.0.113.1, 10.0.0.5`
3. Middleware checks: Is `10.0.0.5` (immediate peer) trusted? → Yes
4. Walk XFF right-to-left: `10.0.0.5` (trusted), `203.0.113.1` (untrusted) → Real client is `203.0.113.1`
5. Check if `203.0.113.1` is in CIDR allowlist

**Security notes**:
- **Empty `trustedproxies`**: All proxy headers ignored (prevents IP spoofing)
- **Non-empty `trustedproxies`**: Headers honored only from trusted proxies
- **Without this**: Attackers can bypass CIDR allowlist by setting fake `X-Forwarded-For` headers

### Reverse Proxy

For production, consider additional security:

```nginx
location /_sys/ {
    # Require authentication
    auth_basic "System APIs";
    auth_basic_user_file /etc/nginx/.htpasswd;

    # Or use OAuth2 proxy
    auth_request /oauth2/auth;

    proxy_pass http://app:8080;
}
```

---

## Troubleshooting

### Job Not Executing

**Check scheduler logs**:
```bash
grep "jobID" logs/app.log
```

**Verify job is registered**:
```bash
curl http://localhost:8080/_sys/job | jq '.data[] | .jobId'
```

**Check for validation errors**:
- Job ID must be unique (duplicate registration fails)
- Schedule parameters must be valid (e.g., hour 0-23, minute 0-59)

### Job Executions Skipped

**Check overlapping prevention**:
```bash
curl http://localhost:8080/_sys/job | jq '.data[] | select(.jobId == "cleanup-job") | .skippedCount'
```

If `skippedCount` is increasing, the job is taking longer than its interval. Either:
- Optimize the job to run faster
- Increase the interval (e.g., every 60 minutes instead of 30)

### Slow Shutdown

**Check in-flight jobs**:
```text
WARN Shutdown timeout reached, some jobs may not have completed timeout=30s
```

**Solutions**:
- Increase `shutdown_timeout` in config
- Ensure jobs respect `ctx.Done()` for graceful cancellation
- Optimize long-running jobs

---

## Next Steps

- **See examples**: Check `examples/scheduler/` for complete demo application
- **Read contracts**: See `contracts/job-system-api.yaml` for OpenAPI spec
- **Explore data model**: See `data-model.md` for detailed entity definitions
- **Review architecture**: See `research.md` for design decisions

---

## FAQ

**Q: Can multiple instances of my app run the same job?**

A: No built-in distributed locking in MVP. Each app instance runs its own scheduler independently. For distributed scheduling, consider external solutions (e.g., database-based locking, distributed cron systems).

**Q: What happens if a job panics?**

A: Panic is recovered, logged at ERROR level with full-stack trace, execution marked as failed in metrics, scheduler continues operating normally. See FR-021 and clarification #4.

**Q: How do I test scheduled jobs?**

A: Manually trigger via POST `/_sys/job/:jobId` or write unit tests that call `job.Execute(mockContext)` directly.

**Q: Can I schedule jobs at sub-second intervals?**

A: Yes, use `FixedRate` with durations like `100*time.Millisecond`. However, consider performance implications for very high-frequency jobs.

**Q: How do I see job execution history?**

A: Use GET `/_sys/job` for current statistics (total/success/failure/skipped counts, last execution time/status). For historical data, export metrics to a monitoring backend (Prometheus, Grafana).

**Q: Do jobs run during deployments?**

A: Depends on deployment strategy. Blue-green deployments allow old instance jobs to complete before shutdown (respecting `shutdown_timeout`). Rolling deployments may interrupt long-running jobs - ensure jobs are idempotent and handle restarts gracefully.
