package logger

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// ZeroLogger wraps zerolog.Logger to implement the Logger interface.
// It provides structured logging functionality with configurable output formatting.
type ZeroLogger struct {
	zlog         *zerolog.Logger
	filter       *SensitiveDataFilter
	pretty       bool // tracks if logger uses pretty (console) formatting vs JSON
	severityHook func(zerolog.Level)
}

// Ensure ZeroLogger implements the interface
var _ Logger = (*ZeroLogger)(nil)

var callerMarshalOnce sync.Once

// New creates a new ZeroLogger instance with the specified log level and formatting options.
// If pretty is true, output will be formatted for human readability.
func New(level string, pretty bool) *ZeroLogger {
	callerMarshalOnce.Do(func() {
		zerolog.CallerMarshalFunc = func(_ uintptr, file string, line int) string {
			base := filepath.Base(file)
			parent := filepath.Base(filepath.Dir(file))
			if parent != "." && parent != "" {
				return parent + "/" + base + ":" + strconv.Itoa(line)
			}
			return base + ":" + strconv.Itoa(line)
		}
	})

	var l zerolog.Logger

	if pretty {
		l = zerolog.New(zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: time.RFC3339,
		}).With().Timestamp().CallerWithSkipFrameCount(3).Logger()
	} else {
		l = zerolog.New(os.Stdout).With().Timestamp().CallerWithSkipFrameCount(3).Logger()
	}

	zLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		zLevel = zerolog.InfoLevel
	}
	l = l.Level(zLevel)

	// Initialize the sensitive data filter with default configuration
	filter := NewSensitiveDataFilter(DefaultFilterConfig())

	return &ZeroLogger{zlog: &l, filter: filter, pretty: pretty}
}

// NewWithFilter creates a new ZeroLogger instance with custom filter configuration.
// This allows applications to customize which fields are considered sensitive.
func NewWithFilter(level string, pretty bool, filterConfig *FilterConfig) *ZeroLogger {
	callerMarshalOnce.Do(func() {
		zerolog.CallerMarshalFunc = func(_ uintptr, file string, line int) string {
			base := filepath.Base(file)
			parent := filepath.Base(filepath.Dir(file))
			if parent != "." && parent != "" {
				return parent + "/" + base + ":" + strconv.Itoa(line)
			}
			return base + ":" + strconv.Itoa(line)
		}
	})

	var l zerolog.Logger

	if pretty {
		l = zerolog.New(zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: time.RFC3339,
		}).With().Timestamp().CallerWithSkipFrameCount(3).Logger()
	} else {
		l = zerolog.New(os.Stdout).With().Timestamp().CallerWithSkipFrameCount(3).Logger()
	}

	zLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		zLevel = zerolog.InfoLevel
	}
	l = l.Level(zLevel)

	// Initialize the sensitive data filter with provided configuration
	filter := NewSensitiveDataFilter(filterConfig)

	return &ZeroLogger{zlog: &l, filter: filter, pretty: pretty}
}

// WithContext returns a logger with context information attached.
// It follows a two-phase approach for maximum flexibility:
//
// Phase 1 (Explicit): If the context contains an explicit zerolog logger
// (set via zerolog.Ctx), that logger takes precedence. This maintains
// backward compatibility with existing code that uses zerolog's context pattern.
//
// Phase 2 (Automatic): If no explicit logger is found, automatically extracts
// trace_id and span_id from the OpenTelemetry span context and adds them as
// fields. This enables automatic trace correlation without requiring explicit
// logger management in every handler.
//
// This hybrid approach provides:
//   - Deterministic behavior: same context always produces same logger
//   - Backward compatibility: existing zerolog.Ctx usage continues to work
//   - Automatic correlation: trace IDs appear in logs without boilerplate
func (l *ZeroLogger) WithContext(ctx any) Logger {
	if c, ok := ctx.(context.Context); ok {
		hook := l.severityHook
		if ctxHook := severityHookFromContext(c); ctxHook != nil {
			hook = ctxHook
		}

		// OPTION 1: Explicit logger in context (backward compatibility)
		// Check if there's an explicit zerolog logger set in the context
		zl := zerolog.Ctx(c)
		if zl != nil && zl.GetLevel() != zerolog.Disabled {
			return &ZeroLogger{zlog: zl, filter: l.filter, pretty: l.pretty, severityHook: hook}
		}

		// OPTION 2: Extract trace context for automatic correlation
		// If there's an active span in the context, inject trace_id and span_id
		// as structured fields. This enables log-trace correlation in observability
		// backends (e.g., clicking trace_id in Grafana jumps to Jaeger trace).
		span := trace.SpanFromContext(c)
		if span.SpanContext().IsValid() {
			log := l.zlog.With().
				Str("trace_id", span.SpanContext().TraceID().String()).
				Str("span_id", span.SpanContext().SpanID().String()).
				Logger()
			return &ZeroLogger{
				zlog:         &log,
				filter:       l.filter,
				pretty:       l.pretty,
				severityHook: hook,
			}
		}

		return &ZeroLogger{
			zlog:         l.zlog,
			filter:       l.filter,
			pretty:       l.pretty,
			severityHook: hook,
		}
	}
	return l
}

// WithFields returns a logger with additional fields attached to all log entries.
func (l *ZeroLogger) WithFields(fields map[string]any) Logger {
	// Filter sensitive data from fields before adding them
	if l.filter != nil {
		fields = l.filter.FilterFields(fields)
	}
	log := l.zlog.With().Fields(fields).Logger()
	return &ZeroLogger{zlog: &log, filter: l.filter, pretty: l.pretty, severityHook: l.severityHook}
}

// OTelProvider is a minimal interface for accessing OpenTelemetry logger provider
// and configuration. This interface allows the logger package to integrate with
// observability without creating circular dependencies.
type OTelProvider interface {
	// LoggerProvider returns the configured logger provider.
	// Returns nil if logging is disabled.
	LoggerProvider() *sdklog.LoggerProvider

	// ShouldDisableStdout returns true if stdout should be disabled when OTLP is enabled.
	// This method is implemented via type assertion to avoid exposing internal config.
	ShouldDisableStdout() bool
}

// WithOTelProvider attaches an OpenTelemetry logger provider for OTLP log export.
// Returns the same logger if provider is nil/disabled, or creates a new logger
// with dual output (stdout + OTLP) or OTLP-only based on the provider's configuration.
//
// IMPORTANT: OTLP export requires JSON mode (pretty=false). This method fails fast
// with a panic if pretty mode is active, ensuring configuration errors are caught
// during initialization rather than silently degrading observability.
//
// Configuration conflict detection:
//   - If logger is created with pretty=true AND OTLP export is enabled, panics with
//     clear error message directing user to fix their configuration.
//
// Output modes:
//   - DisableStdout=false (default): logs go to both stdout and OTLP (useful for dev)
//   - DisableStdout=true: logs only go to OTLP (production efficiency)
func (l *ZeroLogger) WithOTelProvider(provider OTelProvider) *ZeroLogger {
	// Nil provider check - return original logger
	if provider == nil || provider.LoggerProvider() == nil {
		return l
	}

	// Fail-fast: pretty mode incompatible with OTLP bridge
	// The OTel bridge requires JSON output to parse log entries, but pretty mode
	// outputs human-readable console format. Rather than silently skip OTLP export,
	// we fail fast to ensure users are aware of the configuration conflict.
	if l.pretty {
		panic("OTLP log export requires JSON mode (pretty=false). " +
			"Configuration conflict detected: logger.pretty=true AND observability.logs.enabled=true. " +
			"Fix: Set logger.pretty=false in config or disable observability.logs.enabled")
	}

	// Create OTel bridge to convert zerolog JSON to OTel log records
	bridge := NewOTelBridge(provider.LoggerProvider())
	if bridge == nil {
		// Bridge creation failed gracefully (e.g., nil provider)
		return l
	}

	// Determine output destination based on DisableStdout configuration
	var output io.Writer
	if provider.ShouldDisableStdout() {
		// OTLP only - reduces disk I/O in production
		output = bridge
	} else {
		// Both stdout and OTLP - useful for local debugging
		output = io.MultiWriter(os.Stdout, bridge)
	}

	// Swap the writer while preserving all existing context, fields, and hooks
	// Using Output() instead of zerolog.New() maintains the logger's accumulated state
	newLog := l.zlog.Output(output)

	return &ZeroLogger{
		zlog:         &newLog,
		filter:       l.filter,
		pretty:       false, // Always false - validated above
		severityHook: l.severityHook,
	}
}
