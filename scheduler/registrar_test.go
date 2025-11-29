package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	rejectDuplicateJobIDErrorMsg = "rejects duplicate job ID"
	duplicatedJobID              = "duplicate-job"
)

// TestJobRegistrarFixedRate tests fixed-rate job scheduling
func TestJobRegistrarFixedRate(t *testing.T) {
	t.Run("registers job with valid interval", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.FixedRate(testJobID, job, 30*time.Second)
		assert.NoError(t, err)
	})

	t.Run(rejectDuplicateJobIDErrorMsg, func(t *testing.T) {
		registrar := newTestRegistrar()
		job1 := &testJob{}
		job2 := &testJob{}

		err := registrar.FixedRate(duplicatedJobID, job1, 30*time.Second)
		require.NoError(t, err)

		err = registrar.FixedRate(duplicatedJobID, job2, 60*time.Second)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), duplicatedJobID)
		assert.Contains(t, err.Error(), "already registered")
	})

	t.Run("rejects zero interval", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.FixedRate(testJobID, job, 0)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "interval")
	})

	t.Run("rejects negative interval", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.FixedRate(testJobID, job, -10*time.Second)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "interval")
	})
}

// TestJobRegistrarDailyAt tests daily job scheduling
func TestJobRegistrarDailyAt(t *testing.T) {
	t.Run("registers job with valid time", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("03:00")

		err := registrar.DailyAt(testJobID, job, localTime)
		assert.NoError(t, err)
	})

	t.Run(rejectDuplicateJobIDErrorMsg, func(t *testing.T) {
		registrar := newTestRegistrar()
		job1 := &testJob{}
		job2 := &testJob{}
		localTime := mustParseTime("03:00")

		err := registrar.DailyAt(duplicatedJobID, job1, localTime)
		require.NoError(t, err)

		err = registrar.DailyAt(duplicatedJobID, job2, localTime)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), duplicatedJobID)
	})

	t.Run("accepts hour 0 (midnight)", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.DailyAt(testJobID, job, localTime)
		assert.NoError(t, err)
	})

	t.Run("accepts hour 23", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("23:59")

		err := registrar.DailyAt(testJobID, job, localTime)
		assert.NoError(t, err)
	})
}

// TestJobRegistrarWeeklyAt tests weekly job scheduling
func TestJobRegistrarWeeklyAt(t *testing.T) {
	t.Run("registers job with valid day and time", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("09:00")

		err := registrar.WeeklyAt(testJobID, job, time.Monday, localTime)
		assert.NoError(t, err)
	})

	t.Run(rejectDuplicateJobIDErrorMsg, func(t *testing.T) {
		registrar := newTestRegistrar()
		job1 := &testJob{}
		job2 := &testJob{}
		localTime := mustParseTime("09:00")

		err := registrar.WeeklyAt(duplicatedJobID, job1, time.Monday, localTime)
		require.NoError(t, err)

		err = registrar.WeeklyAt(duplicatedJobID, job2, time.Tuesday, localTime)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), duplicatedJobID)
	})

	t.Run("accepts all weekdays", func(t *testing.T) {
		registrar := newTestRegistrar()
		localTime := mustParseTime("09:00")

		weekdays := []time.Weekday{
			time.Sunday, time.Monday, time.Tuesday, time.Wednesday,
			time.Thursday, time.Friday, time.Saturday,
		}

		for _, day := range weekdays {
			job := &testJob{}
			err := registrar.WeeklyAt(day.String()+"-job", job, day, localTime)
			assert.NoError(t, err, "Should accept day: %s", day)
		}
	})
}

// TestJobRegistrarHourlyAt tests hourly job scheduling
func TestJobRegistrarHourlyAt(t *testing.T) {
	t.Run("registers job with valid minute", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.HourlyAt(testJobID, job, 15)
		assert.NoError(t, err)
	})

	t.Run(rejectDuplicateJobIDErrorMsg, func(t *testing.T) {
		registrar := newTestRegistrar()
		job1 := &testJob{}
		job2 := &testJob{}

		err := registrar.HourlyAt(duplicatedJobID, job1, 15)
		require.NoError(t, err)

		err = registrar.HourlyAt(duplicatedJobID, job2, 30)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), duplicatedJobID)
	})

	t.Run("accepts minute 0", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.HourlyAt(testJobID, job, 0)
		assert.NoError(t, err)
	})

	t.Run("accepts minute 59", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.HourlyAt(testJobID, job, 59)
		assert.NoError(t, err)
	})

	t.Run("rejects minute 60", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.HourlyAt(testJobID, job, 60)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "minute")
	})

	t.Run("rejects negative minute", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}

		err := registrar.HourlyAt(testJobID, job, -1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "minute")
	})
}

// TestJobRegistrarMonthlyAt tests monthly job scheduling
func TestJobRegistrarMonthlyAt(t *testing.T) {
	t.Run("registers job with valid day and time", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.MonthlyAt(testJobID, job, 1, localTime)
		assert.NoError(t, err)
	})

	t.Run(rejectDuplicateJobIDErrorMsg, func(t *testing.T) {
		registrar := newTestRegistrar()
		job1 := &testJob{}
		job2 := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.MonthlyAt(duplicatedJobID, job1, 1, localTime)
		require.NoError(t, err)

		err = registrar.MonthlyAt(duplicatedJobID, job2, 15, localTime)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), duplicatedJobID)
	})

	t.Run("accepts day 1", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.MonthlyAt(testJobID, job, 1, localTime)
		assert.NoError(t, err)
	})

	t.Run("accepts day 31", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.MonthlyAt(testJobID, job, 31, localTime)
		assert.NoError(t, err)
	})

	t.Run("rejects day 0", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.MonthlyAt(testJobID, job, 0, localTime)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "day")
	})

	t.Run("rejects day 32", func(t *testing.T) {
		registrar := newTestRegistrar()
		job := &testJob{}
		localTime := mustParseTime("00:00")

		err := registrar.MonthlyAt(testJobID, job, 32, localTime)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "day")
	})
}

// Test helpers

// newTestRegistrar creates a test JobRegistrar using the real SchedulerModule
func newTestRegistrar() app.JobRegistrar {
	module := NewSchedulerModule()

	// Initialize with minimal dependencies
	deps := &app.ModuleDeps{
		Logger:        logger.New("info", false),
		Config:        &config.Config{},
		Tracer:        nil,
		MeterProvider: nil,
		GetDB: func(_ context.Context) (types.Interface, error) {
			return nil, nil
		},
		GetMessaging: func(_ context.Context) (messaging.AMQPClient, error) {
			return nil, nil
		},
	}

	if err := module.Init(deps); err != nil {
		panic(err) //nolint:S8148 // NOSONAR: Test helper - panic on setup failure is intentional
	}

	return module
}

// mustParseTime parses time in "HH:MM" format
func mustParseTime(s string) time.Time {
	t, err := time.Parse("15:04", s)
	if err != nil {
		panic(err) //nolint:S8148 // NOSONAR: Test helper - panic on invalid time format is intentional
	}
	return t
}
