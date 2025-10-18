package scheduler

import (
	"fmt"
	"time"
)

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

// ScheduleConfiguration holds the configuration details for a scheduled job
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

// Validate checks the ScheduleConfiguration for validity according to its Type
func (c *ScheduleConfiguration) Validate(_ time.Time) error {
	switch c.Type {
	case ScheduleTypeFixedRate:
		if c.Interval <= 0 {
			return &ValidationError{
				Field:   "interval",
				Message: "must be positive",
				Action:  "Choose a duration greater than 0",
				Err:     ErrInvalidInterval,
			}
		}
	case ScheduleTypeDaily:
		return c.validateDaily()
	case ScheduleTypeWeekly:
		return c.validateWeekly()
	case ScheduleTypeHourly:
		return c.validateHourly()
	case ScheduleTypeMonthly:
		return c.validateMonthly()
	default:
		return &ValidationError{
			Field:   "type",
			Message: fmt.Sprintf("unknown schedule type '%s'", c.Type),
			Action:  "Use one of: fixed-rate, daily, weekly, hourly, monthly",
			Err:     ErrInvalidScheduleType,
		}
	}
	return nil
}

// validateDaily checks the validity of daily schedule parameters
func (c *ScheduleConfiguration) validateDaily() error {
	if c.Hour < 0 || c.Hour > 23 {
		return NewRangeError("hour", 0, 23, c.Hour)
	}
	if c.Minute < 0 || c.Minute > 59 {
		return NewRangeError("minute", 0, 59, c.Minute)
	}
	return nil
}

// validateWeekly checks the validity of weekly schedule parameters
func (c *ScheduleConfiguration) validateWeekly() error {
	if c.DayOfWeek < time.Sunday || c.DayOfWeek > time.Saturday {
		return NewInvalidValueError("dayOfWeek", c.DayOfWeek, "Choose a valid weekday (Sunday=0 to Saturday=6)")
	}
	if c.Hour < 0 || c.Hour > 23 {
		return NewRangeError("hour", 0, 23, c.Hour)
	}
	if c.Minute < 0 || c.Minute > 59 {
		return NewRangeError("minute", 0, 59, c.Minute)
	}
	return nil
}

// validateHourly checks the validity of hourly schedule parameters
func (c *ScheduleConfiguration) validateHourly() error {
	if c.Minute < 0 || c.Minute > 59 {
		return NewRangeError("minute", 0, 59, c.Minute)
	}
	return nil
}

// validateMonthly checks the validity of monthly schedule parameters
func (c *ScheduleConfiguration) validateMonthly() error {
	if c.DayOfMonth < 1 || c.DayOfMonth > 31 {
		return NewRangeError("dayOfMonth", 1, 31, c.DayOfMonth)
	}
	if c.Hour < 0 || c.Hour > 23 {
		return NewRangeError("hour", 0, 23, c.Hour)
	}
	if c.Minute < 0 || c.Minute > 59 {
		return NewRangeError("minute", 0, 59, c.Minute)
	}
	return nil
}

// ToCronExpression converts the schedule configuration to a cron-style expression.
// Note: Fixed-rate schedules don't have a true cron equivalent, so we use "@every" notation.
func (c *ScheduleConfiguration) ToCronExpression() string {
	switch c.Type {
	case ScheduleTypeFixedRate:
		return fmt.Sprintf("@every %s", c.Interval.String())
	case ScheduleTypeDaily:
		// Cron format: minute hour day month weekday
		return fmt.Sprintf("%d %d * * *", c.Minute, c.Hour)
	case ScheduleTypeWeekly:
		// Cron format: minute hour day month weekday (0=Sunday, 6=Saturday)
		return fmt.Sprintf("%d %d * * %d", c.Minute, c.Hour, c.DayOfWeek)
	case ScheduleTypeHourly:
		// Cron format: minute hour day month weekday
		return fmt.Sprintf("%d * * * *", c.Minute)
	case ScheduleTypeMonthly:
		// Cron format: minute hour day month weekday
		return fmt.Sprintf("%d %d %d * *", c.Minute, c.Hour, c.DayOfMonth)
	default:
		return ""
	}
}

// ToHumanReadable converts the schedule configuration to a user-friendly description.
func (c *ScheduleConfiguration) ToHumanReadable() string {
	switch c.Type {
	case ScheduleTypeFixedRate:
		return fmt.Sprintf("Every %s", formatDuration(c.Interval))
	case ScheduleTypeDaily:
		return fmt.Sprintf("Daily at %s", formatTime(c.Hour, c.Minute))
	case ScheduleTypeWeekly:
		return fmt.Sprintf("Every %s at %s", c.DayOfWeek.String(), formatTime(c.Hour, c.Minute))
	case ScheduleTypeHourly:
		return fmt.Sprintf("Hourly at :%02d", c.Minute)
	case ScheduleTypeMonthly:
		return fmt.Sprintf("Monthly on day %d at %s", c.DayOfMonth, formatTime(c.Hour, c.Minute))
	default:
		return ""
	}
}

// formatTime formats hour and minute as 12-hour time with AM/PM
func formatTime(hour, minute int) string {
	period := "AM"
	displayHour := hour

	if hour >= 12 {
		period = "PM"
		if hour > 12 {
			displayHour = hour - 12
		}
	}
	if hour == 0 {
		displayHour = 12
	}

	return fmt.Sprintf("%d:%02d %s", displayHour, minute, period)
}

// formatDuration formats a duration in a human-readable way
func formatDuration(d time.Duration) string {
	// Handle common cases for cleaner output
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	if d < time.Hour {
		minutes := int(d.Minutes())
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	}

	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day"
	}
	return fmt.Sprintf("%d days", days)
}
