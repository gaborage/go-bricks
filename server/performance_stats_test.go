package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

func TestPerformanceStats(t *testing.T) {
	tests := []struct {
		name        string
		handler     func(c echo.Context) error
		expectError bool
	}{
		{
			name: "success_case_with_counters",
			handler: func(c echo.Context) error {
				ctx := c.Request().Context()

				// Simulate AMQP operations
				logger.IncrementAMQPCounter(ctx)
				logger.IncrementAMQPCounter(ctx)
				logger.AddAMQPElapsed(ctx, 1000000) // 1ms in nanoseconds
				logger.AddAMQPElapsed(ctx, 500000)  // 0.5ms in nanoseconds

				// Simulate DB operations
				logger.IncrementDBCounter(ctx)
				logger.IncrementDBCounter(ctx)
				logger.IncrementDBCounter(ctx)
				logger.AddDBElapsed(ctx, 2000000) // 2ms in nanoseconds
				logger.AddDBElapsed(ctx, 800000)  // 0.8ms in nanoseconds

				return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
			},
			expectError: false,
		},
		{
			name: "handler_returns_error",
			handler: func(c echo.Context) error {
				ctx := c.Request().Context()
				logger.IncrementAMQPCounter(ctx)
				logger.IncrementDBCounter(ctx)
				return echo.NewHTTPError(http.StatusBadRequest, "test error")
			},
			expectError: true,
		},
		{
			name: "no_operations_performed",
			handler: func(c echo.Context) error {
				return c.JSON(http.StatusOK, map[string]string{"status": "no-ops"})
			},
			expectError: false,
		},
		{
			name: "only_amqp_operations",
			handler: func(c echo.Context) error {
				ctx := c.Request().Context()
				logger.IncrementAMQPCounter(ctx)
				logger.AddAMQPElapsed(ctx, 750000) // 0.75ms in nanoseconds
				return c.JSON(http.StatusOK, map[string]string{"status": "amqp-only"})
			},
			expectError: false,
		},
		{
			name: "only_db_operations",
			handler: func(c echo.Context) error {
				ctx := c.Request().Context()
				logger.IncrementDBCounter(ctx)
				logger.AddDBElapsed(ctx, 1250000) // 1.25ms in nanoseconds
				return c.JSON(http.StatusOK, map[string]string{"status": "db-only"})
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/test", http.NoBody)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Create middleware
			middleware := PerformanceStats()
			handler := middleware(tt.handler)

			// Execute
			err := handler(c)

			// Assert error expectation
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify context has been modified with performance tracking
			ctx := c.Request().Context()
			assert.NotNil(t, ctx)

			// Test that context values can be retrieved (indicating they were set)
			amqpCount := logger.GetAMQPCounter(ctx)
			dbCount := logger.GetDBCounter(ctx)
			amqpElapsed := logger.GetAMQPElapsed(ctx)
			dbElapsed := logger.GetDBElapsed(ctx)

			// Verify counts and elapsed times match expected values from handlers
			switch tt.name {
			case "success_case_with_counters":
				assert.Equal(t, int64(2), amqpCount, "AMQP counter should be 2")
				assert.Equal(t, int64(3), dbCount, "DB counter should be 3")
				assert.Equal(t, int64(1500000), amqpElapsed, "AMQP elapsed should be 1.5ms in nanoseconds")
				assert.Equal(t, int64(2800000), dbElapsed, "DB elapsed should be 2.8ms in nanoseconds")
			case "handler_returns_error":
				assert.Equal(t, int64(1), amqpCount, "AMQP counter should be 1")
				assert.Equal(t, int64(1), dbCount, "DB counter should be 1")
			case "no_operations_performed":
				assert.Equal(t, int64(0), amqpCount, "AMQP counter should be 0")
				assert.Equal(t, int64(0), dbCount, "DB counter should be 0")
				assert.Equal(t, int64(0), amqpElapsed, "AMQP elapsed should be 0")
				assert.Equal(t, int64(0), dbElapsed, "DB elapsed should be 0")
			case "only_amqp_operations":
				assert.Equal(t, int64(1), amqpCount, "AMQP counter should be 1")
				assert.Equal(t, int64(0), dbCount, "DB counter should be 0")
				assert.Equal(t, int64(750000), amqpElapsed, "AMQP elapsed should be 0.75ms in nanoseconds")
				assert.Equal(t, int64(0), dbElapsed, "DB elapsed should be 0")
			case "only_db_operations":
				assert.Equal(t, int64(0), amqpCount, "AMQP counter should be 0")
				assert.Equal(t, int64(1), dbCount, "DB counter should be 1")
				assert.Equal(t, int64(0), amqpElapsed, "AMQP elapsed should be 0")
				assert.Equal(t, int64(1250000), dbElapsed, "DB elapsed should be 1.25ms in nanoseconds")
			}
		})
	}
}

func TestPerformanceStatsContextInitialization(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Verify original context doesn't have performance tracking
	originalCtx := c.Request().Context()
	assert.Equal(t, int64(0), logger.GetAMQPCounter(originalCtx))
	assert.Equal(t, int64(0), logger.GetDBCounter(originalCtx))
	assert.Equal(t, int64(0), logger.GetAMQPElapsed(originalCtx))
	assert.Equal(t, int64(0), logger.GetDBElapsed(originalCtx))

	middleware := PerformanceStats()
	handler := middleware(func(c echo.Context) error {
		// After middleware, context should have performance tracking initialized
		ctx := c.Request().Context()

		// Initial values should be 0, but context should be set up for tracking
		assert.Equal(t, int64(0), logger.GetAMQPCounter(ctx))
		assert.Equal(t, int64(0), logger.GetDBCounter(ctx))
		assert.Equal(t, int64(0), logger.GetAMQPElapsed(ctx))
		assert.Equal(t, int64(0), logger.GetDBElapsed(ctx))

		// Verify that incrementing works after initialization
		logger.IncrementAMQPCounter(ctx)
		logger.IncrementDBCounter(ctx)
		logger.AddAMQPElapsed(ctx, 100)
		logger.AddDBElapsed(ctx, 200)

		assert.Equal(t, int64(1), logger.GetAMQPCounter(ctx))
		assert.Equal(t, int64(1), logger.GetDBCounter(ctx))
		assert.Equal(t, int64(100), logger.GetAMQPElapsed(ctx))
		assert.Equal(t, int64(200), logger.GetDBElapsed(ctx))

		return nil
	})

	err := handler(c)
	require.NoError(t, err)
}

func TestPerformanceStatsConcurrentAccess(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	middleware := PerformanceStats()

	// Test concurrent increments to verify atomic operations work correctly
	handler := middleware(func(c echo.Context) error {
		ctx := c.Request().Context()

		// Use goroutines to simulate concurrent access
		done := make(chan bool, 100)

		// Start 50 goroutines for AMQP operations
		for i := 0; i < 50; i++ {
			go func() {
				logger.IncrementAMQPCounter(ctx)
				logger.AddAMQPElapsed(ctx, 1000) // Add 1000ns each time
				done <- true
			}()
		}

		// Start 50 goroutines for DB operations
		for i := 0; i < 50; i++ {
			go func() {
				logger.IncrementDBCounter(ctx)
				logger.AddDBElapsed(ctx, 2000) // Add 2000ns each time
				done <- true
			}()
		}

		// Wait for all goroutines to complete
		for i := 0; i < 100; i++ {
			<-done
		}

		// Verify final counts are correct (atomic operations preserved consistency)
		amqpCount := logger.GetAMQPCounter(ctx)
		dbCount := logger.GetDBCounter(ctx)
		amqpElapsed := logger.GetAMQPElapsed(ctx)
		dbElapsed := logger.GetDBElapsed(ctx)

		assert.Equal(t, int64(50), amqpCount, "AMQP counter should be 50")
		assert.Equal(t, int64(50), dbCount, "DB counter should be 50")
		assert.Equal(t, int64(50000), amqpElapsed, "AMQP elapsed should be 50000ns")
		assert.Equal(t, int64(100000), dbElapsed, "DB elapsed should be 100000ns")

		return nil
	})

	err := handler(c)
	require.NoError(t, err)
}

func TestPerformanceStatsMiddlewareChaining(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Test that performance stats middleware works correctly when chained with other middleware
	performanceMiddleware := PerformanceStats()

	// Simple middleware that modifies request context
	type customContextKey string
	customKey := customContextKey("custom_key")

	customMiddleware := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ctx := context.WithValue(c.Request().Context(), customKey, "custom_value")
			c.SetRequest(c.Request().WithContext(ctx))
			return next(c)
		}
	}

	// Chain middlewares: custom -> performance -> handler
	chainedHandler := customMiddleware(performanceMiddleware(func(c echo.Context) error {
		ctx := c.Request().Context()

		// Verify custom middleware value is still present
		assert.Equal(t, "custom_value", ctx.Value(customKey))

		// Verify performance tracking is available
		logger.IncrementAMQPCounter(ctx)
		logger.IncrementDBCounter(ctx)

		assert.Equal(t, int64(1), logger.GetAMQPCounter(ctx))
		assert.Equal(t, int64(1), logger.GetDBCounter(ctx))

		return nil
	}))

	err := chainedHandler(c)
	require.NoError(t, err)
}
