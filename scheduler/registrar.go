package scheduler

import "time"

// JobRegistrar provides methods to schedule jobs with various time-based patterns.
// Each method registers a job with a unique identifier and schedule configuration.
//
// All methods validate parameters per FR-022 (unique job IDs) and FR-023 (valid schedule parameters).
// Validation errors are returned immediately at registration time (fail-fast per Constitution).
//
// Example:
//
//	registrar := deps.Scheduler
//	err := registrar.FixedRate("sync-job", &SyncJob{}, 30*time.Minute)
//	err = registrar.DailyAt("cleanup-job", &CleanupJob{}, mustParseTime("03:00"))
//	err = registrar.WeeklyAt("report-job", &ReportJob{}, time.Monday, mustParseTime("09:00"))
type JobRegistrar interface {
	// FixedRate schedules a job to execute every 'interval' duration.
	// Validation: interval must be > 0, jobID must be unique.
	//
	// Example: FixedRate("cleanup", job, 30*time.Minute) -> every 30 minutes
	FixedRate(jobID string, job Job, interval time.Duration) error

	// DailyAt schedules a job to execute once per day at the specified local time.
	// Validation: localTime hour 0-23, minute 0-59, jobID must be unique.
	//
	// Example: DailyAt("report", job, time.Parse("15:04", "03:00")) -> daily at 3:00 AM
	// Note: Timezone defaults to system local time per ASSUME-001, DST handled by gocron/v2 per ASSUME-001a.
	DailyAt(jobID string, job Job, localTime time.Time) error

	// WeeklyAt schedules a job to execute once per week on the specified day and local time.
	// Validation: dayOfWeek (time.Sunday-time.Saturday), localTime hour 0-23, minute 0-59, jobID must be unique.
	//
	// Example: WeeklyAt("backup", job, time.Monday, time.Parse("15:04", "02:00")) -> Mondays at 2:00 AM
	WeeklyAt(jobID string, job Job, dayOfWeek time.Weekday, localTime time.Time) error

	// HourlyAt schedules a job to execute once per hour at the specified minute.
	// Validation: minute 0-59, jobID must be unique.
	//
	// Example: HourlyAt("sync", job, 15) -> every hour at HH:15
	HourlyAt(jobID string, job Job, minute int) error

	// MonthlyAt schedules a job to execute once per month on the specified day and local time.
	// Validation: dayOfMonth 1-31, localTime hour 0-23, minute 0-59, jobID must be unique.
	//
	// Example: MonthlyAt("billing", job, 1, time.Parse("15:04", "00:00")) -> 1st of every month at midnight
	// Note: For months with fewer days (e.g., February), gocron/v2 handles edge cases per library behavior.
	MonthlyAt(jobID string, job Job, dayOfMonth int, localTime time.Time) error
}
