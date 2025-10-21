package scheduler

import (
	"fmt"
	"time"
)

// ParseTime parses a time string in "HH:MM" format (24-hour) and returns a time.Time.
// This is a convenience function intended for use in application initialization code
// when registering scheduled jobs.
//
// The function panics if the input format is invalid, following the "fail fast" principle
// from the GoBricks constitution - configuration errors should be caught at startup, not runtime.
//
// Example:
//
//	scheduler.DailyAt("cleanup-job", &CleanupJob{}, scheduler.ParseTime("03:00"))  // 3:00 AM
//	scheduler.WeeklyAt("report-job", &ReportJob{}, time.Monday, scheduler.ParseTime("09:30"))  // Monday at 9:30 AM
//
// Format: "HH:MM" where HH is 00-23 and MM is 00-59
//
// Panics on invalid input (wrong format, invalid hour/minute values).
func ParseTime(timeStr string) time.Time {
	t, err := time.Parse("15:04", timeStr)
	if err != nil {
		panic(fmt.Sprintf("scheduler: invalid time format '%s' - expected HH:MM (24-hour), error: %v", timeStr, err))
	}
	return t
}
