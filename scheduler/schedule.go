package scheduler

import "time"

// ScheduleType identifies the scheduling pattern
type ScheduleType string

const (
	// ScheduleTypeFixedRate represents jobs that execute every N duration
	ScheduleTypeFixedRate ScheduleType = "fixed-rate"

	// ScheduleTypeDaily represents jobs that execute once per day at a specific time
	ScheduleTypeDaily ScheduleType = "daily"

	// ScheduleTypeWeekly represents jobs that execute once per week on a specific day and time
	ScheduleTypeWeekly ScheduleType = "weekly"

	// ScheduleTypeHourly represents jobs that execute once per hour at a specific minute
	ScheduleTypeHourly ScheduleType = "hourly"

	// ScheduleTypeMonthly represents jobs that execute once per month on a specific day and time
	ScheduleTypeMonthly ScheduleType = "monthly"
)

// ScheduleConfiguration describes when a job should execute.
// This is an internal representation used by jobEntry and metadata.
// Constructed when jobs are registered via JobRegistrar methods.
type ScheduleConfiguration struct {
	Type ScheduleType

	// FixedRate fields
	Interval time.Duration // Used when Type == ScheduleTypeFixedRate

	// Time-based fields (daily, weekly, monthly, hourly)
	Hour   int // 0-23 (used for daily, weekly, monthly)
	Minute int // 0-59 (used for all time-based schedules)

	// WeeklyAt field
	DayOfWeek time.Weekday // Sunday-Saturday

	// MonthlyAt field
	DayOfMonth int // 1-31

	// Timezone (nil = system local time per ASSUME-001)
	// For MVP, this is always nil (local time).
	// Future enhancement: allow explicit timezone specification.
	Timezone *time.Location
}
