package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	validConfigurationMsg = "valid configuration"
)

// TestScheduleConfigurationValidateFixedRate verifies validation of fixed-rate schedule configurations
func TestScheduleConfigurationValidateFixedRate(t *testing.T) {
	t.Run("valid interval", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 5 * time.Minute,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("zero interval", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 0,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		// Test error type
		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "interval", valErr.Field)
		assert.Contains(t, valErr.Message, "must be positive")
		assert.Contains(t, valErr.Action, "Choose a duration greater than 0")

		// Test sentinel error
		assert.True(t, errors.Is(err, ErrInvalidInterval))
	})

	t.Run("negative interval", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: -5 * time.Minute,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "interval", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidInterval))
	})
}

// TestScheduleConfigurationValidateDaily verifies validation of daily schedule configurations
func TestScheduleConfigurationValidateDaily(t *testing.T) {
	t.Run(validConfigurationMsg, func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   14,
			Minute: 30,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid boundary values - hour 0", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   0,
			Minute: 0,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid boundary values - hour 23, minute 59", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   23,
			Minute: 59,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("hour too low", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   -1,
			Minute: 30,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "hour", valErr.Field)
		assert.Contains(t, valErr.Message, "0-23")
		assert.Contains(t, valErr.Message, "-1")
		assert.Contains(t, valErr.Action, "Choose a value between 0 and 23")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("hour too high", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   24,
			Minute: 30,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "hour", valErr.Field)
		assert.Contains(t, valErr.Message, "24")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("minute too low", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   14,
			Minute: -1,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "minute", valErr.Field)
		assert.Contains(t, valErr.Message, "0-59")
		assert.Contains(t, valErr.Message, "-1")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("minute too high", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   14,
			Minute: 60,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "minute", valErr.Field)
		assert.Contains(t, valErr.Message, "60")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})
}

// TestScheduleConfigurationValidateWeekly verifies validation of weekly schedule configurations
func TestScheduleConfigurationValidateWeekly(t *testing.T) {
	t.Run(validConfigurationMsg, func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Monday,
			Hour:      10,
			Minute:    15,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid Sunday", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Sunday,
			Hour:      10,
			Minute:    15,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid Saturday", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Saturday,
			Hour:      10,
			Minute:    15,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("invalid day of week - too low", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Weekday(-1),
			Hour:      10,
			Minute:    15,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "dayOfWeek", valErr.Field)
		assert.Contains(t, valErr.Message, "invalid value")
		assert.Contains(t, valErr.Action, "Sunday=0 to Saturday=6")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("invalid day of week - too high", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Weekday(7),
			Hour:      10,
			Minute:    15,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "dayOfWeek", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("invalid hour", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Monday,
			Hour:      -1,
			Minute:    15,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "hour", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("invalid minute", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Monday,
			Hour:      10,
			Minute:    60,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "minute", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})
}

// TestScheduleConfigurationValidateHourly verifies validation of hourly schedule configurations
func TestScheduleConfigurationValidateHourly(t *testing.T) {
	t.Run(validConfigurationMsg, func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: 15,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid boundary - minute 0", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: 0,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid boundary - minute 59", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: 59,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("minute too low", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: -1,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "minute", valErr.Field)
		assert.Contains(t, valErr.Message, "0-59")
		assert.Contains(t, valErr.Message, "-1")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("minute too high", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: 60,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "minute", valErr.Field)
		assert.Contains(t, valErr.Message, "60")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})
}

// TestScheduleConfigurationValidateMonthly verifies validation of monthly schedule configurations
func TestScheduleConfigurationValidateMonthly(t *testing.T) {
	t.Run(validConfigurationMsg, func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 15,
			Hour:       10,
			Minute:     30,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid boundary - day 1", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 1,
			Hour:       0,
			Minute:     0,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("valid boundary - day 31", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 31,
			Hour:       23,
			Minute:     59,
		}
		err := config.Validate(time.Now())
		assert.NoError(t, err)
	})

	t.Run("day of month too low", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 0,
			Hour:       10,
			Minute:     30,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "dayOfMonth", valErr.Field)
		assert.Contains(t, valErr.Message, "1-31")
		assert.Contains(t, valErr.Message, "0")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("day of month too high", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 32,
			Hour:       10,
			Minute:     30,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "dayOfMonth", valErr.Field)
		assert.Contains(t, valErr.Message, "32")
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("invalid hour", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 15,
			Hour:       24,
			Minute:     30,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "hour", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})

	t.Run("invalid minute", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 15,
			Hour:       10,
			Minute:     -1,
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "minute", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidTimeRange))
	})
}

// TestScheduleConfigurationValidateUnknownType verifies validation of unknown schedule types
func TestScheduleConfigurationValidateUnknownType(t *testing.T) {
	t.Run("invalid schedule type", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type: ScheduleType("invalid-type"),
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "type", valErr.Field)
		assert.Contains(t, valErr.Message, "unknown schedule type")
		assert.Contains(t, valErr.Message, "invalid-type")
		assert.Contains(t, valErr.Action, "fixed-rate, daily, weekly, hourly, monthly")
		assert.True(t, errors.Is(err, ErrInvalidScheduleType))
	})

	t.Run("empty schedule type", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type: ScheduleType(""),
		}
		err := config.Validate(time.Now())

		require.Error(t, err)

		var valErr *ValidationError
		assert.True(t, errors.As(err, &valErr))
		assert.Equal(t, "type", valErr.Field)
		assert.True(t, errors.Is(err, ErrInvalidScheduleType))
	})
}

// TestValidationErrorFormat verifies the Error() method of ValidationError
func TestValidationErrorFormat(t *testing.T) {
	t.Run("with action", func(t *testing.T) {
		err := &ValidationError{
			Field:   "testField",
			Message: "is invalid",
			Action:  "Choose a valid value",
		}
		assert.Equal(t, "scheduler: testField is invalid. Choose a valid value", err.Error())
	})

	t.Run("without action", func(t *testing.T) {
		err := &ValidationError{
			Field:   "testField",
			Message: "is invalid",
		}
		assert.Equal(t, "scheduler: testField is invalid", err.Error())
	})
}

// TestNewRangeError verifies creation of ValidationError for range errors
func TestNewRangeError(t *testing.T) {
	err := NewRangeError("hour", 0, 23, 25)

	var valErr *ValidationError
	require.True(t, errors.As(err, &valErr))
	assert.Equal(t, "hour", valErr.Field)
	assert.Contains(t, valErr.Message, "must be 0-23")
	assert.Contains(t, valErr.Message, "(got 25)")
	assert.Contains(t, valErr.Action, "Choose a value between 0 and 23")
	assert.True(t, errors.Is(err, ErrInvalidTimeRange))
}

func TestNewInvalidValueError(t *testing.T) {
	err := NewInvalidValueError("dayOfWeek", 99, "Choose a valid weekday")

	var valErr *ValidationError
	require.True(t, errors.As(err, &valErr))
	assert.Equal(t, "dayOfWeek", valErr.Field)
	assert.Contains(t, valErr.Message, "invalid value 99")
	assert.Equal(t, "Choose a valid weekday", valErr.Action)
	assert.True(t, errors.Is(err, ErrInvalidTimeRange))
}

// TestScheduleConfigurationToCronExpression verifies conversion of schedule configurations to cron expressions
func TestScheduleConfigurationToCronExpression(t *testing.T) {
	t.Run("fixed-rate", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 30 * time.Minute,
		}
		assert.Equal(t, "@every 30m0s", config.ToCronExpression())
	})

	t.Run("daily", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   14,
			Minute: 30,
		}
		// Cron format: minute hour day month weekday
		assert.Equal(t, "30 14 * * *", config.ToCronExpression())
	})

	t.Run("weekly", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Monday,
			Hour:      10,
			Minute:    15,
		}
		// Cron format: minute hour day month weekday (Monday=1)
		assert.Equal(t, "15 10 * * 1", config.ToCronExpression())
	})

	t.Run("hourly", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: 45,
		}
		// Cron format: minute hour day month weekday
		assert.Equal(t, "45 * * * *", config.ToCronExpression())
	})

	t.Run("monthly", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 15,
			Hour:       9,
			Minute:     0,
		}
		// Cron format: minute hour day month weekday
		assert.Equal(t, "0 9 15 * *", config.ToCronExpression())
	})

	t.Run("unknown type", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type: ScheduleType("unknown"),
		}
		assert.Equal(t, "", config.ToCronExpression())
	})
}

// TestScheduleConfigurationToHumanReadable verifies conversion of schedule configurations to human-readable strings
func TestScheduleConfigurationToHumanReadable(t *testing.T) {
	t.Run("fixed-rate - minutes", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 30 * time.Minute,
		}
		assert.Equal(t, "Every 30 minutes", config.ToHumanReadable())
	})

	t.Run("fixed-rate - 1 minute", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 1 * time.Minute,
		}
		assert.Equal(t, "Every 1 minute", config.ToHumanReadable())
	})

	t.Run("fixed-rate - hours", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 2 * time.Hour,
		}
		assert.Equal(t, "Every 2 hours", config.ToHumanReadable())
	})

	t.Run("fixed-rate - 1 hour", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 1 * time.Hour,
		}
		assert.Equal(t, "Every 1 hour", config.ToHumanReadable())
	})

	t.Run("fixed-rate - days", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 24 * time.Hour,
		}
		assert.Equal(t, "Every 1 day", config.ToHumanReadable())
	})

	t.Run("fixed-rate - seconds", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:     ScheduleTypeFixedRate,
			Interval: 45 * time.Second,
		}
		assert.Equal(t, "Every 45 seconds", config.ToHumanReadable())
	})

	t.Run("daily - AM", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   3,
			Minute: 0,
		}
		assert.Equal(t, "Daily at 3:00 AM", config.ToHumanReadable())
	})

	t.Run("daily - PM", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   14,
			Minute: 30,
		}
		assert.Equal(t, "Daily at 2:30 PM", config.ToHumanReadable())
	})

	t.Run("daily - noon", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   12,
			Minute: 0,
		}
		assert.Equal(t, "Daily at 12:00 PM", config.ToHumanReadable())
	})

	t.Run("daily - midnight", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeDaily,
			Hour:   0,
			Minute: 0,
		}
		assert.Equal(t, "Daily at 12:00 AM", config.ToHumanReadable())
	})

	t.Run("weekly", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:      ScheduleTypeWeekly,
			DayOfWeek: time.Monday,
			Hour:      10,
			Minute:    15,
		}
		assert.Equal(t, "Every Monday at 10:15 AM", config.ToHumanReadable())
	})

	t.Run("hourly", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:   ScheduleTypeHourly,
			Minute: 45,
		}
		assert.Equal(t, "Hourly at :45", config.ToHumanReadable())
	})

	t.Run("monthly", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type:       ScheduleTypeMonthly,
			DayOfMonth: 15,
			Hour:       9,
			Minute:     0,
		}
		assert.Equal(t, "Monthly on day 15 at 9:00 AM", config.ToHumanReadable())
	})

	t.Run("unknown type", func(t *testing.T) {
		config := ScheduleConfiguration{
			Type: ScheduleType("unknown"),
		}
		assert.Equal(t, "", config.ToHumanReadable())
	})
}
