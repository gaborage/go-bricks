package logger

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

// ZeroLogger wraps zerolog.Logger to implement the Logger interface.
// It provides structured logging functionality with configurable output formatting.
type ZeroLogger struct {
	zlog   *zerolog.Logger
	filter *SensitiveDataFilter
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

	return &ZeroLogger{zlog: &l, filter: filter}
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

	return &ZeroLogger{zlog: &l, filter: filter}
}

// WithContext returns a logger with context information attached.
func (l *ZeroLogger) WithContext(ctx any) Logger {
	if c, ok := ctx.(context.Context); ok {
		zl := zerolog.Ctx(c)
		if zl == nil || zl.GetLevel() == zerolog.Disabled {
			return l
		}
		return &ZeroLogger{zlog: zl, filter: l.filter}
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
	return &ZeroLogger{zlog: &log, filter: l.filter}
}
