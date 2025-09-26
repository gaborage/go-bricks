package tracking

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// TrackDBOperation logs database operation performance metrics and errors.
// It provides centralized tracking for all database operations including queries,
// statements, and transactions. The function handles slow query detection,
// error logging, and request-scoped performance metrics.
func TrackDBOperation(ctx context.Context, tc *Context, query string, args []any, start time.Time, err error) {
	// Guard against nil tracking context or logger with no-op default
	if tc == nil || tc.Logger == nil {
		return
	}

	elapsed := time.Since(start)

	// Increment database operation counter for request tracking
	logger.IncrementDBCounter(ctx)
	logger.AddDBElapsed(ctx, elapsed.Nanoseconds())

	// Truncate query string to safe max length to avoid unbounded payloads
	truncatedQuery := query
	if tc.Settings.MaxQueryLength() > 0 && len(query) > tc.Settings.MaxQueryLength() {
		truncatedQuery = TruncateString(query, tc.Settings.MaxQueryLength())
	}

	// Log query execution details
	logEvent := tc.Logger.WithContext(ctx).WithFields(map[string]any{
		"vendor":      tc.Vendor,
		"duration_ms": elapsed.Milliseconds(),
		"duration_ns": elapsed.Nanoseconds(),
		"query":       truncatedQuery,
	})

	if tc.Settings.LogQueryParameters() && len(args) > 0 {
		logEvent = logEvent.WithFields(map[string]any{
			"args": SanitizeArgs(args, tc.Settings.MaxQueryLength()),
		})
	}

	if err != nil {
		// Treat sql.ErrNoRows specially - not an actual error, log as debug
		if errors.Is(err, sql.ErrNoRows) {
			logEvent.Debug().Msg("Database operation returned no rows")
		} else {
			logEvent.Error().Err(err).Msg("Database operation error")
		}
	} else if elapsed > tc.Settings.SlowQueryThreshold() {
		logEvent.Warn().Msgf("Slow database operation detected (%s)", elapsed)
	} else {
		logEvent.Debug().Msg("Database operation executed")
	}
}

// TruncateString returns value truncated to at most maxLen characters.
// If maxLen <= 0 or value is already shorter than or equal to maxLen, the
// original string is returned. When maxLen <= 3 the function returns the
// first maxLen characters (no ellipsis); otherwise it returns the first
// maxLen-3 characters followed by "..." to indicate truncation.
func TruncateString(value string, maxLen int) string {
	if maxLen <= 0 {
		return value
	}
	r := []rune(value)
	if len(r) <= maxLen {
		return value
	}
	// Handle multi-byte characters correctly
	if maxLen <= 3 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-3]) + "..."
}

// SanitizeArgs returns a sanitized copy of the provided argument slice suitable for logging.
//
// If args is empty, it returns nil. String values are truncated using TruncateString with
// maxLen; byte slices are replaced with the placeholder "<bytes len=N>"; all other values
// are formatted with "%v" and then truncated using TruncateString. The returned slice has
// the same length and element order as the input (unless args is empty).
func SanitizeArgs(args []any, maxLen int) []any {
	if len(args) == 0 {
		return nil
	}
	sanitized := make([]any, len(args))
	for i, arg := range args {
		switch v := arg.(type) {
		case string:
			sanitized[i] = TruncateString(v, maxLen)
		case []byte:
			sanitized[i] = fmt.Sprintf("<bytes len=%d>", len(v))
		default:
			sanitized[i] = TruncateString(fmt.Sprintf("%v", v), maxLen)
		}
	}
	return sanitized
}
