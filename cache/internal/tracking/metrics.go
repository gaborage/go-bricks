package tracking

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const (
	// Meter name for cache metrics instrumentation
	cacheMeterName = "go-bricks/cache"

	// Metric names following OpenTelemetry semantic conventions
	// Using db.client.operation.duration for Redis (as Redis is a database)
	metricCacheOperationDuration = "db.client.operation.duration" // Histogram in seconds

	// Cache-specific metrics
	metricCacheHit  = "cache.hit"  // Counter for cache hits
	metricCacheMiss = "cache.miss" // Counter for cache misses

	// Manager metrics (GoBricks-specific)
	metricCacheManagerActiveCaches = "cache.manager.active_caches" // UpDownCounter
	metricCacheManagerEvictions    = "cache.manager.evictions"     // Counter
	metricCacheManagerIdleCleanups = "cache.manager.idle_cleanups" // Counter
	metricCacheManagerTotalCreated = "cache.manager.total_created" // Counter
	metricCacheManagerErrors       = "cache.manager.errors"        // Counter

	// Attribute keys per OTel semantic conventions
	attrDBSystem       = "db.system.name"
	attrDBOperation    = "db.operation.name"
	attrDBNamespace    = "db.namespace"
	attrErrorType      = "error.type"
	attrCacheHitStatus = "cache.hit" // Boolean attribute for hit/miss
	attrPoolName       = "pool.name" // For multi-tenant cache identification
)

// Cache operation names
const (
	OpGet           = "get"
	OpSet           = "set"
	OpDelete        = "delete"
	OpGetOrSet      = "getorset"
	OpCompareAndSet = "cas"
	OpHealth        = "ping"
)

// isLookupOperation returns true if the operation is a cache lookup (get or getorset).
// These operations track hit/miss statistics.
func isLookupOperation(operation string) bool {
	return operation == OpGet || operation == OpGetOrSet
}

var (
	// Singleton meter initialization
	cacheMeter    metric.Meter
	meterOnce     sync.Once
	meterInitMu   sync.Mutex
	metricsInited bool

	// Metric instruments
	cacheOperationDuration metric.Float64Histogram
	cacheHitCounter        metric.Int64Counter
	cacheMissCounter       metric.Int64Counter
)

// logMetricError logs a metric initialization error to stderr.
func logMetricError(metricName string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: Failed to initialize cache metric %s: %v\n", metricName, err)
	}
}

// initCacheMeter initializes the OpenTelemetry meter and cache metric instruments.
func initCacheMeter() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	if cacheMeter != nil {
		return
	}

	cacheMeter = otel.Meter(cacheMeterName)

	var err error

	// Initialize histogram for operation duration (in seconds per OTel spec)
	cacheOperationDuration, err = cacheMeter.Float64Histogram(
		metricCacheOperationDuration,
		metric.WithDescription("Duration of cache/Redis operations"),
		metric.WithUnit("s"),
	)
	logMetricError(metricCacheOperationDuration, err)

	// Initialize cache hit counter
	cacheHitCounter, err = cacheMeter.Int64Counter(
		metricCacheHit,
		metric.WithDescription("Number of cache hits"),
		metric.WithUnit("{hit}"),
	)
	logMetricError(metricCacheHit, err)

	// Initialize cache miss counter
	cacheMissCounter, err = cacheMeter.Int64Counter(
		metricCacheMiss,
		metric.WithDescription("Number of cache misses"),
		metric.WithUnit("{miss}"),
	)
	logMetricError(metricCacheMiss, err)

	metricsInited = true
}

// ensureCacheMeterInitialized ensures the cache meter is initialized.
func ensureCacheMeterInitialized() {
	meterOnce.Do(initCacheMeter)
}

// RecordCacheOperation records cache operation metrics.
// This should be called after each cache operation.
//
// Parameters:
//   - ctx: context for metric recording
//   - operation: operation name (get, set, delete, cas, etc.)
//   - duration: operation duration
//   - hit: whether this was a cache hit (for Get operations)
//   - err: error if operation failed
//   - namespace: optional tenant identifier
func RecordCacheOperation(ctx context.Context, operation string, duration time.Duration, hit bool, err error, namespace string) {
	ensureCacheMeterInitialized()

	// Build base attributes
	attrs := []attribute.KeyValue{
		attribute.String(attrDBSystem, "redis"),
		attribute.String(attrDBOperation, operation),
	}

	// Add optional namespace
	if namespace != "" {
		attrs = append(attrs, attribute.String(attrDBNamespace, namespace))
	}

	// Add hit/miss attribute for cache lookup operations
	if isLookupOperation(operation) {
		attrs = append(attrs, attribute.Bool(attrCacheHitStatus, hit))
	}

	// Add error type if present
	if err != nil {
		attrs = append(attrs, attribute.String(attrErrorType, classifyError(err)))
	}

	// Record duration histogram
	if cacheOperationDuration != nil {
		durationSec := float64(duration.Nanoseconds()) / 1e9
		cacheOperationDuration.Record(ctx, durationSec, metric.WithAttributes(attrs...))
	}

	// Record hit/miss counters for lookup operations
	if isLookupOperation(operation) {
		recordHitMissCounters(ctx, hit, attrs)
	}
}

// classifyError returns an error classification string for metrics.
func classifyError(err error) string {
	if err == nil {
		return ""
	}

	// Extract error type name
	errStr := err.Error()

	// Common cache error patterns
	switch {
	case contains(errStr, "connection"):
		return "connection_error"
	case contains(errStr, "timeout"):
		return "timeout"
	case contains(errStr, "closed"):
		return "closed"
	case contains(errStr, "not found"):
		return "not_found"
	default:
		return "error"
	}
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	if substr == "" {
		return true
	}
	if s == "" {
		return false
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// recordHitMissCounters records cache hit or miss counters for lookup operations.
// This helper is extracted to reduce cognitive complexity of RecordCacheOperation.
func recordHitMissCounters(ctx context.Context, hit bool, attrs []attribute.KeyValue) {
	if hit {
		if cacheHitCounter != nil {
			cacheHitCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
		}
	} else {
		if cacheMissCounter != nil {
			cacheMissCounter.Add(ctx, 1, metric.WithAttributes(attrs...))
		}
	}
}

// ManagerMetricsStats holds the stats needed for manager metrics.
type ManagerMetricsStats struct {
	ActiveCaches int
	TotalCreated int
	Evictions    int
	IdleCleanups int
	Errors       int
}

// managerMetricsRegistration encapsulates manager metrics state.
type managerMetricsRegistration struct {
	statsProvider func() ManagerMetricsStats
	poolName      string

	activeCachesCounter metric.Int64ObservableUpDownCounter
	totalCreatedCounter metric.Int64ObservableCounter
	evictionsCounter    metric.Int64ObservableCounter
	idleCleanupsCounter metric.Int64ObservableCounter
	errorsCounter       metric.Int64ObservableCounter

	baseAttrs []attribute.KeyValue
}

// observeManagerStats is called during metrics collection.
func (r *managerMetricsRegistration) observeManagerStats(_ context.Context, observer metric.Observer) error {
	stats := r.statsProvider()

	// Record current active caches (absolute value)
	if r.activeCachesCounter != nil {
		observer.ObserveInt64(r.activeCachesCounter, int64(stats.ActiveCaches), metric.WithAttributes(r.baseAttrs...))
	}

	// For observable counters, we need to report the cumulative value
	if r.totalCreatedCounter != nil {
		observer.ObserveInt64(r.totalCreatedCounter, int64(stats.TotalCreated), metric.WithAttributes(r.baseAttrs...))
	}

	if r.evictionsCounter != nil {
		observer.ObserveInt64(r.evictionsCounter, int64(stats.Evictions), metric.WithAttributes(r.baseAttrs...))
	}

	if r.idleCleanupsCounter != nil {
		observer.ObserveInt64(r.idleCleanupsCounter, int64(stats.IdleCleanups), metric.WithAttributes(r.baseAttrs...))
	}

	if r.errorsCounter != nil {
		observer.ObserveInt64(r.errorsCounter, int64(stats.Errors), metric.WithAttributes(r.baseAttrs...))
	}

	return nil
}

// createObservableUpDownCounter creates an observable up-down counter.
func createObservableUpDownCounter(meter metric.Meter, name, description string) metric.Int64ObservableUpDownCounter {
	counter, err := meter.Int64ObservableUpDownCounter(name, metric.WithDescription(description))
	logMetricError(name, err)
	return counter
}

// createObservableCounter creates an observable counter.
func createObservableCounter(meter metric.Meter, name, description string) metric.Int64ObservableCounter {
	counter, err := meter.Int64ObservableCounter(name, metric.WithDescription(description))
	logMetricError(name, err)
	return counter
}

// collectObservables collects non-nil observable instruments.
func collectObservables(instruments ...metric.Observable) []metric.Observable {
	var result []metric.Observable
	for _, inst := range instruments {
		if inst != nil {
			result = append(result, inst)
		}
	}
	return result
}

// noOpCleanup returns a no-op cleanup function.
func noOpCleanup() func() {
	return func() { /** no-op **/ }
}

// RegisterManagerMetrics registers observable metrics for cache manager.
// The statsProvider function is called during each metrics collection cycle.
// Returns a cleanup function to unregister the metrics.
//
// Metrics registered:
//   - cache.manager.active_caches: Current number of active cache instances
//   - cache.manager.total_created: Total caches created since manager start
//   - cache.manager.evictions: Total evictions due to LRU policy
//   - cache.manager.idle_cleanups: Total cleanups due to idle timeout
//   - cache.manager.errors: Total initialization and close errors
func RegisterManagerMetrics(statsProvider func() ManagerMetricsStats, poolName string) func() {
	ensureCacheMeterInitialized()

	if cacheMeter == nil {
		return noOpCleanup()
	}

	baseAttrs := []attribute.KeyValue{}
	if poolName != "" {
		baseAttrs = append(baseAttrs, attribute.String(attrPoolName, poolName))
	}

	reg := &managerMetricsRegistration{
		statsProvider: statsProvider,
		poolName:      poolName,
		baseAttrs:     baseAttrs,
	}

	// Create observable counters
	reg.activeCachesCounter = createObservableUpDownCounter(cacheMeter, metricCacheManagerActiveCaches,
		"Current number of active cache instances")
	reg.totalCreatedCounter = createObservableCounter(cacheMeter, metricCacheManagerTotalCreated,
		"Total cache instances created since manager start")
	reg.evictionsCounter = createObservableCounter(cacheMeter, metricCacheManagerEvictions,
		"Total evictions due to LRU policy")
	reg.idleCleanupsCounter = createObservableCounter(cacheMeter, metricCacheManagerIdleCleanups,
		"Total cleanups due to idle timeout")
	reg.errorsCounter = createObservableCounter(cacheMeter, metricCacheManagerErrors,
		"Total cache initialization and close errors")

	// Collect non-nil instruments
	instruments := collectObservables(
		reg.activeCachesCounter,
		reg.totalCreatedCounter,
		reg.evictionsCounter,
		reg.idleCleanupsCounter,
		reg.errorsCounter,
	)

	if len(instruments) == 0 {
		return noOpCleanup()
	}

	// Register callback
	registration, err := cacheMeter.RegisterCallback(reg.observeManagerStats, instruments...)
	if err != nil {
		logMetricError("manager_metrics_callback", err)
		return noOpCleanup()
	}

	return func() {
		if err := registration.Unregister(); err != nil {
			logMetricError("manager_metrics_unregister", err)
		}
	}
}

// IsInitialized returns true if cache metrics have been initialized.
func IsInitialized() bool {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()
	return metricsInited
}

// ResetForTesting resets the metric state for testing purposes.
func ResetForTesting() {
	meterInitMu.Lock()
	defer meterInitMu.Unlock()

	cacheMeter = nil
	cacheOperationDuration = nil
	cacheHitCounter = nil
	cacheMissCounter = nil
	metricsInited = false
	meterOnce = sync.Once{}
}
