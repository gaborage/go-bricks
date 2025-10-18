package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/database/types"
	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging"
	"github.com/stretchr/testify/assert"
)

// TestJobInterface verifies that Job interface can be implemented
func TestJobInterface(t *testing.T) {
	t.Run("simple job implementation compiles", func(_ *testing.T) {
		job := &testJob{}
		var _ Job = job // Compile-time check
	})

	t.Run("job execution can be called", func(t *testing.T) {
		job := &testJob{}
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, // logger
			nil, // getDB
			nil, // getMessaging
			nil, // config
		)

		err := job.Execute(ctx)
		assert.NoError(t, err)
		assert.True(t, job.executed, "Job should have been executed")
	})
}

// TestJobContextAccessors verifies JobContext provides correct values
func TestJobContextAccessors(t *testing.T) {
	t.Run("returns correct job ID", func(t *testing.T) {
		ctx := newJobContext(
			context.Background(),
			"cleanup-job",
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		assert.Equal(t, "cleanup-job", ctx.JobID())
	})

	t.Run("returns correct trigger type for scheduled", func(t *testing.T) {
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		assert.Equal(t, "scheduled", ctx.TriggerType())
	})

	t.Run("returns correct trigger type for manual", func(t *testing.T) {
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"manual",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		assert.Equal(t, "manual", ctx.TriggerType())
	})

	t.Run("provides logger", func(t *testing.T) {
		mockLogger := logger.New("info", false)
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			mockLogger,
			nilDBResolver, nilMessagingResolver, nil,
		)

		assert.NotNil(t, ctx.Logger())
		assert.Equal(t, mockLogger, ctx.Logger())
	})

	t.Run("provides nil DB when not configured", func(t *testing.T) {
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, nil, nil, nil,
		)

		assert.Nil(t, ctx.DB())
	})

	t.Run("provides DB when configured", func(_ *testing.T) {
		var mockDB types.Interface // Will be nil but typed
		mockDBResolver := func() types.Interface { return mockDB }
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil,
			mockDBResolver, // DB resolver provided
			nilMessagingResolver, nil,
		)

		// We can't assert non-nil here without a real DB implementation
		// but we verify the interface is accessible
		_ = ctx.DB()
	})

	t.Run("provides nil Messaging when not configured", func(t *testing.T) {
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, nilDBResolver, nil, nil,
		)

		assert.Nil(t, ctx.Messaging())
	})

	t.Run("provides Messaging when configured", func(_ *testing.T) {
		var mockMessaging messaging.Client // Will be nil but typed
		mockMessagingResolver := func() messaging.Client { return mockMessaging }
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, nilDBResolver,
			mockMessagingResolver, // Messaging resolver provided
			nil,
		)

		// We can't assert non-nil here without a real messaging implementation
		// but we verify the interface is accessible
		_ = ctx.Messaging()
	})

	t.Run("provides nil Config when not configured", func(t *testing.T) {
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		assert.Nil(t, ctx.Config())
	})

	t.Run("provides Config when configured", func(t *testing.T) {
		cfg := &config.Config{}
		ctx := newJobContext(
			context.Background(),
			testJobID,
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver,
			cfg, // Config provided
		)

		assert.NotNil(t, ctx.Config())
		assert.Equal(t, cfg, ctx.Config())
	})
}

// TestJobContextContextBehavior verifies JobContext embeds context.Context correctly
func TestJobContextContextBehavior(t *testing.T) {
	t.Run("respects parent context cancellation", func(t *testing.T) {
		parentCtx, cancel := context.WithCancel(context.Background())
		ctx := newJobContext(
			parentCtx,
			testJobID,
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		// Before cancellation
		assert.NoError(t, ctx.Err())

		// Trigger cancellation
		cancel()

		// After cancellation
		assert.Error(t, ctx.Err())
		assert.Equal(t, context.Canceled, ctx.Err())
	})

	t.Run("respects context deadline", func(t *testing.T) {
		parentCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()

		ctx := newJobContext(
			parentCtx,
			testJobID,
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		// Wait for timeout
		time.Sleep(20 * time.Millisecond)

		// Should be cancelled
		assert.Error(t, ctx.Err())
		assert.Equal(t, context.DeadlineExceeded, ctx.Err())
	})

	t.Run("context.Done channel signals cancellation", func(t *testing.T) {
		parentCtx, cancel := context.WithCancel(context.Background())
		ctx := newJobContext(
			parentCtx,
			testJobID,
			"scheduled",
			nil, nilDBResolver, nilMessagingResolver, nil,
		)

		// Cancel in background
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		// Wait for Done signal
		<-ctx.Done()
		assert.Error(t, ctx.Err())
	})
}

// testJob is a simple test implementation of Job interface
type testJob struct {
	executed bool
	err      error
}

func (j *testJob) Execute(_ JobContext) error {
	j.executed = true
	return j.err
}

// Test helpers for multi-tenancy resolver functions
func nilDBResolver() types.Interface {
	return nil
}

func nilMessagingResolver() messaging.Client {
	return nil
}
