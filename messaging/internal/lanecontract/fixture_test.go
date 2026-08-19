package lanecontract

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

func TestSetupTelemetryInstallsProvidersAndRestoresThemOnCleanup(t *testing.T) {
	beforeTracer := otel.GetTracerProvider()
	beforeMeter := otel.GetMeterProvider()

	t.Run("installed_for_the_test", func(t *testing.T) {
		exporter, meter := SetupTelemetry(t)

		require.NotNil(t, exporter)
		require.NotNil(t, meter)
		assert.NotSame(t, beforeTracer, otel.GetTracerProvider(), "the tracer provider must be swapped")
		assert.NotSame(t, beforeMeter, otel.GetMeterProvider(), "the meter provider must be swapped")
	})

	// The subtest's cleanup has run by now. Both globals are process-wide, so a
	// fixture that failed to restore them would silently corrupt every later test.
	assert.Same(t, beforeTracer, otel.GetTracerProvider(), "cleanup must restore the tracer provider")
	assert.Same(t, beforeMeter, otel.GetMeterProvider(), "cleanup must restore the meter provider")
}

// The streams lane runs one goroutine per partition, so this is what the mutex
// is for: without it, -race fails here.
func TestOutcomesRecordsConcurrentDeliveriesWithoutRacing(t *testing.T) {
	const deliveries = 50
	outcomes := &Outcomes{}

	var wg sync.WaitGroup
	wg.Add(deliveries)
	for range deliveries {
		go func() {
			defer wg.Done()
			outcomes.Log(&delivery.Result{})
		}()
	}
	wg.Wait()

	assert.Len(t, outcomes.Seen(), deliveries)
}

func TestRecordingLoggerCapturesEveryFieldTypeInEmissionOrder(t *testing.T) {
	log := NewRecordingLogger()

	log.Error().
		Str("correlation_id", "req-1").
		Int("body_size", 12).
		Int64("offset", 7).
		Uint64("delivery_tag", 123).
		Dur("processing_time", 3*time.Millisecond).
		Interface("panic", "boom").
		Bytes("stack", []byte("goroutine 1")).
		Bool("redelivered", true).
		Err(errors.New("handler failed")).
		Msg("outcome")

	lines := log.Lines()
	require.Len(t, lines, 1)
	assert.Equal(t, "outcome", lines[0].Msg)
	assert.Equal(t, [][2]string{
		{"correlation_id", "req-1"},
		{"body_size", "12"},
		{"offset", "7"},
		{"delivery_tag", "123"},
		{"processing_time", "3ms"},
		{"panic", "boom"},
		{"stack", "goroutine 1"},
		{"redelivered", "true"},
		{"error", "handler failed"},
	}, lines[0].Fields)
}

func TestRecordingLoggerDropsANilError(t *testing.T) {
	log := NewRecordingLogger()

	log.Info().Err(nil).Msg("outcome")

	assert.Empty(t, log.Lines()[0].Fields)
}

func TestRecordingLoggerDerivedLoggersShareTheBuffer(t *testing.T) {
	log := NewRecordingLogger()
	ctx := context.Background()

	bound := log.WithContext(ctx)
	bound.Info().Msg("from the bound logger")
	log.WithFields(map[string]any{"tenant": "acme", "consumer": "orders-worker"}).Info().Msg("from the fielded logger")

	lines := log.Lines()
	require.Len(t, lines, 2, "a derived logger must record into its parent's buffer")
	assert.Equal(t, "from the bound logger", lines[0].Msg)
	// Map iteration order is random, so WithFields sorts its keys.
	assert.Equal(t, [][2]string{{"consumer", "orders-worker"}, {"tenant", "acme"}}, lines[1].Fields)

	require.IsType(t, &RecordingLogger{}, bound)
	assert.Equal(t, ctx, bound.(*RecordingLogger).BoundTo, "the bound context must be recorded")
}

// The two lanes differ on level today, so a family can only catch that if every
// level method stamps its own.
func TestRecordingLoggerStampsTheLevelOnEveryLine(t *testing.T) {
	tests := []struct {
		name  string
		emit  func(logger.Logger) logger.LogEvent
		level string
	}{
		{name: "info", emit: logger.Logger.Info, level: LevelInfo},
		{name: "error", emit: logger.Logger.Error, level: LevelError},
		{name: "debug", emit: logger.Logger.Debug, level: LevelDebug},
		{name: "warn", emit: logger.Logger.Warn, level: LevelWarn},
		{name: "fatal", emit: logger.Logger.Fatal, level: LevelFatal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := NewRecordingLogger()

			event := tt.emit(log)
			assert.True(t, event.Enabled(), "the recorder never drops an event")
			event.Msgf("emitted at %s", tt.level)

			lines := log.Lines()
			require.Len(t, lines, 1)
			assert.Equal(t, tt.level, lines[0].Level)
			assert.Equal(t, "emitted at "+tt.level, lines[0].Msg)
		})
	}
}
