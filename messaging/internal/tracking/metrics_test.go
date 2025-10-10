package tracking

import (
	"context"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	obtest "github.com/gaborage/go-bricks/observability/testing"
)

const (
	testExchange   = "test.exchange"
	testRoutingKey = "test.key"
	testQueueName  = "test-queue"
)

// resetMeterForTesting resets the meter state for testing purposes
func resetMeterForTesting() {
	meterOnce = sync.Once{}
	amqpMeter = nil
}

func TestInitAMQPMeter(t *testing.T) {
	// Reset meter for testing
	resetMeterForTesting()

	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Initialize meter
	initAMQPMeter()

	// Verify meter is initialized
	assert.NotNil(t, amqpMeter, "AMQP meter should be initialized")

	// Verify all metric instruments are initialized
	assert.NotNil(t, amqpOperationDuration, "Operation duration histogram should be initialized")
	assert.NotNil(t, amqpMessagesSent, "Messages sent counter should be initialized")
	assert.NotNil(t, amqpMessagesConsumed, "Messages consumed counter should be initialized")
	assert.NotNil(t, amqpPublishRetries, "Publish retries counter should be initialized")
	assert.NotNil(t, amqpConnectionCreate, "Connection create counter should be initialized")
	assert.NotNil(t, amqpConnectionClose, "Connection close counter should be initialized")
	assert.NotNil(t, amqpChannelCreate, "Channel create counter should be initialized")
	assert.NotNil(t, amqpChannelClose, "Channel close counter should be initialized")

	// Verify meter returns same instance on subsequent calls
	meter2 := getAMQPMeter()
	assert.Equal(t, amqpMeter, meter2, "getAMQPMeter should return same instance")
}

func TestRecordAMQPPublishMetricsSuccess(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()
	duration := 150 * time.Millisecond

	// Record successful publish metrics
	RecordAMQPPublishMetrics(ctx, testExchange, testRoutingKey, duration, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert operation duration histogram
	obtest.AssertMetricExists(t, rm, metricOperationDuration)
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)

	// Verify histogram has data
	histData, ok := durationMetric.Data.(metricdata.Histogram[float64])
	require.True(t, ok, "Duration metric should be a histogram")
	require.NotEmpty(t, histData.DataPoints, "Duration histogram should have data points")

	// Verify duration value is in seconds
	dp := histData.DataPoints[0]
	assert.InDelta(t, 0.15, dp.Sum, 0.01, "Duration should be ~0.15 seconds")
	assert.Equal(t, uint64(1), dp.Count, "Should have 1 recorded duration")

	// Verify attributes
	attrs := dp.Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrMessagingSystem, messagingSystemRabbitMQ)
	assertHasAttribute(t, attrs, attrMessagingOperation, operationPublish)
	assertHasAttribute(t, attrs, attrMessagingDestination, "test.exchange:test.key")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQExchange, "test.exchange")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQRoutingKey, "test.key")

	// Assert messages sent counter
	obtest.AssertMetricExists(t, rm, metricMessagesSent)
	obtest.AssertMetricValue(t, rm, metricMessagesSent, int64(1))
}

func TestRecordAMQPPublishMetricsFailure(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()
	duration := 50 * time.Millisecond
	testErr := context.Canceled

	// Record failed publish metrics
	RecordAMQPPublishMetrics(ctx, testExchange, testRoutingKey, duration, testErr)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert operation duration histogram (recorded even on failure)
	obtest.AssertMetricExists(t, rm, metricOperationDuration)
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)

	// Verify error.type attribute is present
	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)
	attrs := histData.DataPoints[0].Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrErrorType, "context.Canceled")

	// Assert messages sent counter is NOT incremented (failure case)
	sentMetric := obtest.FindMetric(rm, metricMessagesSent)
	if sentMetric != nil {
		// If the metric exists, verify it's 0
		sumData := sentMetric.Data.(metricdata.Sum[int64])
		if len(sumData.DataPoints) > 0 {
			assert.Equal(t, int64(0), sumData.DataPoints[0].Value, "Messages sent should be 0 on failure")
		}
	}
}

func TestRecordAMQPPublishMetricsDefaultExchange(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()
	exchange := "" // Default exchange
	routingKey := "my-queue"
	duration := 10 * time.Millisecond

	// Record publish metrics
	RecordAMQPPublishMetrics(ctx, exchange, routingKey, duration, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert destination name format for default exchange
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)

	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)
	attrs := histData.DataPoints[0].Attributes.ToSlice()

	assertHasAttribute(t, attrs, attrMessagingDestination, ":my-queue")
}

func TestRecordAMQPConsumeMetricsSuccess(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()
	delivery := &amqp.Delivery{
		Exchange:   "events",
		RoutingKey: testRoutingKey,
	}
	queueName := testQueueName
	duration := 25 * time.Millisecond

	// Record consume metrics
	RecordAMQPConsumeMetrics(ctx, delivery, queueName, duration, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert operation duration histogram
	obtest.AssertMetricExists(t, rm, metricOperationDuration)
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)

	// Verify attributes
	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)
	attrs := histData.DataPoints[0].Attributes.ToSlice()

	assertHasAttribute(t, attrs, attrMessagingSystem, messagingSystemRabbitMQ)
	assertHasAttribute(t, attrs, attrMessagingOperation, operationReceive)
	assertHasAttribute(t, attrs, attrMessagingDestination, "events:test.key:test-queue")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQExchange, "events")
	assertHasAttribute(t, attrs, attrMessagingRabbitMQRoutingKey, testRoutingKey)
	assertHasAttribute(t, attrs, attrMessagingRabbitMQQueue, testQueueName)

	// Assert messages consumed counter
	obtest.AssertMetricExists(t, rm, metricMessagesConsumed)
	obtest.AssertMetricValue(t, rm, metricMessagesConsumed, int64(1))
}

func TestRecordAMQPConsumeMetricsZeroDuration(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()
	delivery := &amqp.Delivery{
		Exchange:   "events",
		RoutingKey: testRoutingKey,
	}
	queueName := testQueueName
	duration := 0 * time.Millisecond // Zero duration (initial receive)

	// Record consume metrics
	RecordAMQPConsumeMetrics(ctx, delivery, queueName, duration, nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Duration histogram should NOT be recorded when duration is 0
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	if durationMetric != nil {
		histData := durationMetric.Data.(metricdata.Histogram[float64])
		assert.Empty(t, histData.DataPoints, "Duration should not be recorded when duration is 0")
	}

	// Messages consumed counter should still be incremented
	obtest.AssertMetricValue(t, rm, metricMessagesConsumed, int64(1))
}

func TestRecordPublishRetry(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()

	// Record different retry reasons
	RecordPublishRetry(ctx, testExchange, testRoutingKey, "nack")
	RecordPublishRetry(ctx, testExchange, testRoutingKey, "timeout")
	RecordPublishRetry(ctx, testExchange, testRoutingKey, "publish_error")

	// Collect metrics
	rm := mp.Collect(t)

	// Assert retry counter exists
	obtest.AssertMetricExists(t, rm, metricPublishRetries)
	retryMetric := obtest.FindMetric(rm, metricPublishRetries)
	require.NotNil(t, retryMetric)

	// Verify total retry count
	sumData := retryMetric.Data.(metricdata.Sum[int64])
	totalRetries := int64(0)
	for _, dp := range sumData.DataPoints {
		totalRetries += dp.Value
	}
	assert.Equal(t, int64(3), totalRetries, "Should have 3 total retries")

	// Verify retry reasons are recorded as attributes
	reasonsFound := make(map[string]bool)
	for _, dp := range sumData.DataPoints {
		attrs := dp.Attributes.ToSlice()
		for _, attr := range attrs {
			if attr.Key == attrRetryReason {
				reasonsFound[attr.Value.AsString()] = true
			}
		}
	}
	assert.True(t, reasonsFound["nack"], "Should have nack retry reason")
	assert.True(t, reasonsFound["timeout"], "Should have timeout retry reason")
	assert.True(t, reasonsFound["publish_error"], "Should have publish_error retry reason")
}

func TestRecordConnectionEvent(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	// Record connection create
	RecordConnectionEvent("create", nil)

	// Record connection close (with error)
	RecordConnectionEvent("close", context.Canceled)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert connection create counter
	obtest.AssertMetricExists(t, rm, metricConnectionCreate)
	obtest.AssertMetricValue(t, rm, metricConnectionCreate, int64(1))

	// Assert connection close counter
	obtest.AssertMetricExists(t, rm, metricConnectionClose)
	obtest.AssertMetricValue(t, rm, metricConnectionClose, int64(1))

	// Verify error.type attribute on close event
	closeMetric := obtest.FindMetric(rm, metricConnectionClose)
	require.NotNil(t, closeMetric)
	sumData := closeMetric.Data.(metricdata.Sum[int64])
	require.NotEmpty(t, sumData.DataPoints)
	attrs := sumData.DataPoints[0].Attributes.ToSlice()
	assertHasAttribute(t, attrs, attrErrorType, "context.Canceled")
}

func TestRecordChannelEvent(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	// Record channel create
	RecordChannelEvent("create", nil)

	// Record channel close (no error)
	RecordChannelEvent("close", nil)

	// Collect metrics
	rm := mp.Collect(t)

	// Assert channel create counter
	obtest.AssertMetricExists(t, rm, metricChannelCreate)
	obtest.AssertMetricValue(t, rm, metricChannelCreate, int64(1))

	// Assert channel close counter
	obtest.AssertMetricExists(t, rm, metricChannelClose)
	obtest.AssertMetricValue(t, rm, metricChannelClose, int64(1))
}

func TestHistogramBucketBoundaries(t *testing.T) {
	// Setup test meter provider
	mp := obtest.NewTestMeterProvider()
	defer func() {
		require.NoError(t, mp.Shutdown(context.Background()))
	}()
	otel.SetMeterProvider(mp)

	// Reset and reinitialize meter
	resetMeterForTesting()
	initAMQPMeter()

	ctx := context.Background()
	exchange := "test"
	routingKey := "key"

	// Record various durations to test bucket boundaries
	testDurations := []time.Duration{
		1 * time.Millisecond,   // 0.001s - should fall in first bucket
		50 * time.Millisecond,  // 0.05s
		500 * time.Millisecond, // 0.5s
		2 * time.Second,        // 2s
		5 * time.Second,        // 5s
	}

	for _, d := range testDurations {
		RecordAMQPPublishMetrics(ctx, exchange, routingKey, d, nil)
	}

	// Collect metrics
	rm := mp.Collect(t)

	// Verify histogram exists and has correct number of recordings
	durationMetric := obtest.FindMetric(rm, metricOperationDuration)
	require.NotNil(t, durationMetric)

	histData := durationMetric.Data.(metricdata.Histogram[float64])
	require.NotEmpty(t, histData.DataPoints)

	// Verify bucket boundaries match OTel semconv
	expectedBounds := []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}
	assert.Equal(t, expectedBounds, histData.DataPoints[0].Bounds, "Histogram bucket boundaries should match OTel semconv")

	// Verify count of recorded durations
	assert.Equal(t, uint64(len(testDurations)), histData.DataPoints[0].Count, "Should have recorded all durations")
}

// Helper function to assert attribute value in attribute slice
func assertHasAttribute(t *testing.T, attrs []attribute.KeyValue, key, expectedValue string) {
	t.Helper()
	for _, attr := range attrs {
		if string(attr.Key) == key {
			assert.Equal(t, expectedValue, attr.Value.AsString(), "Attribute %s should have value %s", key, expectedValue)
			return
		}
	}
	t.Errorf("Attribute %s not found", key)
}
