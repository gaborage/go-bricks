package httpclient

import (
	"context"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/logger"
)

// Test constants to avoid string duplication
const (
	testContentType        = "application/json"
	testContentTypeHeader  = "Content-Type"
	testRestClientRequest  = "REST client request"
	testRestClientResponse = "REST client response"
)

// fakeLogEvent implements logger.LogEvent for testing
type fakeLogEvent struct {
	logger  *fakeLogger
	level   string
	fields  map[string]any
	message string
}

func (e *fakeLogEvent) Msg(msg string) {
	e.message = msg
	e.logger.events = append(e.logger.events, loggedEvent{
		level:   e.level,
		fields:  copyMap(e.fields),
		message: msg,
	})
}

func (e *fakeLogEvent) Msgf(format string, _ ...any) {
	// For testing, we'll just capture the format as the message
	e.Msg(format)
}

func (e *fakeLogEvent) Err(err error) logger.LogEvent {
	e.fields["error"] = err
	return e
}

func (e *fakeLogEvent) Str(key, value string) logger.LogEvent {
	e.fields[key] = value
	return e
}

func (e *fakeLogEvent) Int(key string, value int) logger.LogEvent {
	e.fields[key] = value
	return e
}

func (e *fakeLogEvent) Int64(key string, value int64) logger.LogEvent {
	e.fields[key] = value
	return e
}

func (e *fakeLogEvent) Uint64(key string, value uint64) logger.LogEvent {
	e.fields[key] = value
	return e
}

func (e *fakeLogEvent) Dur(key string, d time.Duration) logger.LogEvent {
	e.fields[key] = d
	return e
}

func (e *fakeLogEvent) Interface(key string, i any) logger.LogEvent {
	e.fields[key] = i
	return e
}

func (e *fakeLogEvent) Bytes(key string, val []byte) logger.LogEvent {
	e.fields[key] = val
	return e
}

// fakeLogger implements logger.Logger for testing
type fakeLogger struct {
	events []loggedEvent
}

type loggedEvent struct {
	level   string
	fields  map[string]any
	message string
}

func (l *fakeLogger) Info() logger.LogEvent {
	return &fakeLogEvent{
		logger: l,
		level:  "info",
		fields: make(map[string]any),
	}
}

func (l *fakeLogger) Error() logger.LogEvent {
	return &fakeLogEvent{
		logger: l,
		level:  "error",
		fields: make(map[string]any),
	}
}

func (l *fakeLogger) Debug() logger.LogEvent {
	return &fakeLogEvent{
		logger: l,
		level:  "debug",
		fields: make(map[string]any),
	}
}

func (l *fakeLogger) Warn() logger.LogEvent {
	return &fakeLogEvent{
		logger: l,
		level:  "warn",
		fields: make(map[string]any),
	}
}

func (l *fakeLogger) Fatal() logger.LogEvent {
	return &fakeLogEvent{
		logger: l,
		level:  "fatal",
		fields: make(map[string]any),
	}
}

func (l *fakeLogger) WithContext(_ any) logger.Logger {
	// For testing, return the same logger
	return l
}

func (l *fakeLogger) WithFields(_ map[string]any) logger.Logger {
	// For testing, return the same logger
	return l
}

func (l *fakeLogger) eventsByLevel(level string) []loggedEvent {
	var events []loggedEvent
	for _, event := range l.events {
		if event.level == level {
			events = append(events, event)
		}
	}
	return events
}

// Helper function to copy maps for test isolation
func copyMap(original map[string]any) map[string]any {
	return maps.Clone(original)
}

// TestClientLogRequest tests the logRequest method
func TestClientLogRequest(t *testing.T) {
	t.Run("basic request logging", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        false,
				MaxPayloadLogBytes: 1024,
			},
		}

		req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.example.com/users", http.NoBody)
		assert.NoError(t, err)
		req.Header.Set("Authorization", "Bearer token")
		req.Header.Set(testContentTypeHeader, testContentType)

		body := []byte(`{"name": "test user"}`)
		traceID := "test-trace-123"

		c.logRequest(req, body, traceID)

		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		infoEvent := infoEvents[0]
		assert.Equal(t, testRestClientRequest, infoEvent.message)
		assert.Equal(t, "outbound", infoEvent.fields["direction"])
		assert.Equal(t, "POST", infoEvent.fields["method"])
		assert.Equal(t, "https://api.example.com/users", infoEvent.fields["url"])
		assert.Equal(t, "test-trace-123", infoEvent.fields["request_id"])
		assert.Equal(t, 2, infoEvent.fields["header_count"])
		assert.Equal(t, len(body), infoEvent.fields["body_size"])

		// Should not have debug events when LogPayloads is false
		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 0)
	})

	t.Run("request with empty body", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{LogPayloads: false},
		}

		req, err := http.NewRequestWithContext(context.Background(), "GET", "https://api.example.com/status", http.NoBody)
		assert.NoError(t, err)

		c.logRequest(req, nil, "trace-456")

		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		infoEvent := infoEvents[0]
		assert.Equal(t, "outbound", infoEvent.fields["direction"])
		assert.Equal(t, "GET", infoEvent.fields["method"])
		assert.Equal(t, "trace-456", infoEvent.fields["request_id"])
		// Should not have body_size field when body is empty
		_, hasBodySize := infoEvent.fields["body_size"]
		assert.False(t, hasBodySize)
		// Should not have header_count field when no headers
		_, hasHeaderCount := infoEvent.fields["header_count"]
		assert.False(t, hasHeaderCount)
	})

	t.Run("request with payload logging enabled", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        true,
				MaxPayloadLogBytes: 50,
			},
		}

		req, err := http.NewRequestWithContext(context.Background(), "PUT", "https://api.example.com/resource", http.NoBody)
		assert.NoError(t, err)
		req.Header.Set("X-API-Key", "secret")

		body := []byte(`{"data": "some content for testing"}`)
		c.logRequest(req, body, "trace-789")

		// Should have both info and debug events
		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 1)

		debugEvent := debugEvents[0]
		assert.Equal(t, testRestClientRequest, debugEvent.message)
		assert.Equal(t, "outbound", debugEvent.fields["direction"])
		assert.Equal(t, "PUT", debugEvent.fields["method"])
		assert.Equal(t, "trace-789", debugEvent.fields["request_id"])
		assert.NotNil(t, debugEvent.fields["headers"])
		assert.Equal(t, len(body), debugEvent.fields["body_size"])
		assert.Equal(t, "false", debugEvent.fields["body_truncated"]) // Body is small, not truncated
		assert.Equal(t, body, debugEvent.fields["body_preview"])
	})

	t.Run("request with large body truncation", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        true,
				MaxPayloadLogBytes: 10, // Very small limit
			},
		}

		req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.example.com/upload", http.NoBody)
		assert.NoError(t, err)

		largeBody := []byte("This is a very long body that should be truncated for logging purposes")
		c.logRequest(req, largeBody, "trace-truncate")

		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 1)

		debugEvent := debugEvents[0]
		assert.Equal(t, len(largeBody), debugEvent.fields["body_size"])
		assert.Equal(t, "true", debugEvent.fields["body_truncated"])
		assert.Equal(t, largeBody[:10], debugEvent.fields["body_preview"])
	})

	t.Run("request with zero MaxPayloadLogBytes uses default", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        true,
				MaxPayloadLogBytes: 0, // Should default to 1024
			},
		}

		req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.example.com/test", http.NoBody)
		assert.NoError(t, err)

		// Create a body larger than 1024 bytes
		largeBody := make([]byte, 1500)
		for i := range largeBody {
			largeBody[i] = byte('A' + (i % 26))
		}

		c.logRequest(req, largeBody, "trace-default")

		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 1)

		debugEvent := debugEvents[0]
		assert.Equal(t, len(largeBody), debugEvent.fields["body_size"])
		assert.Equal(t, "true", debugEvent.fields["body_truncated"])
		assert.Equal(t, largeBody[:1024], debugEvent.fields["body_preview"]) // Should use 1024 default
	})
}

// TestClientLogResponse tests the logResponse method
func TestClientLogResponse(t *testing.T) {
	t.Run("basic response logging", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        false,
				MaxPayloadLogBytes: 1024,
			},
		}

		response := &Response{
			StatusCode: 200,
			Body:       []byte(`{"success": true}`),
			Headers:    http.Header{testContentTypeHeader: []string{testContentType}},
			Stats: Stats{
				ElapsedTime: 250 * time.Millisecond,
				CallCount:   5,
			},
		}

		c.logResponse(response, "trace-response-123")

		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		infoEvent := infoEvents[0]
		assert.Equal(t, testRestClientResponse, infoEvent.message)
		assert.Equal(t, "inbound", infoEvent.fields["direction"])
		assert.Equal(t, 200, infoEvent.fields["status"])
		assert.Equal(t, 250*time.Millisecond, infoEvent.fields["elapsed"])
		assert.Equal(t, int64(5), infoEvent.fields["call_count"])
		assert.Equal(t, "trace-response-123", infoEvent.fields["request_id"])
		assert.Equal(t, len(response.Body), infoEvent.fields["body_size"])

		// Should not have debug events when LogPayloads is false
		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 0)
	})

	t.Run("response with empty body", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{LogPayloads: false},
		}

		response := &Response{
			StatusCode: 204,
			Body:       nil,
			Headers:    http.Header{},
			Stats: Stats{
				ElapsedTime: 100 * time.Millisecond,
				CallCount:   1,
			},
		}

		c.logResponse(response, "trace-empty")

		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		infoEvent := infoEvents[0]
		assert.Equal(t, 204, infoEvent.fields["status"])
		assert.Equal(t, "trace-empty", infoEvent.fields["request_id"])
		// Should not have body_size field when body is empty
		_, hasBodySize := infoEvent.fields["body_size"]
		assert.False(t, hasBodySize)
	})

	t.Run("response with payload logging enabled", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        true,
				MaxPayloadLogBytes: 100,
			},
		}

		response := &Response{
			StatusCode: 201,
			Body:       []byte(`{"id": 123, "created": true}`),
			Headers:    http.Header{"X-Rate-Limit": []string{"100"}},
			Stats: Stats{
				ElapsedTime: 300 * time.Millisecond,
				CallCount:   2,
			},
		}

		c.logResponse(response, "trace-debug")

		// Should have both info and debug events
		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 1)

		debugEvent := debugEvents[0]
		assert.Equal(t, testRestClientResponse, debugEvent.message)
		assert.Equal(t, "inbound", debugEvent.fields["direction"])
		assert.Equal(t, 201, debugEvent.fields["status"])
		assert.Equal(t, "trace-debug", debugEvent.fields["request_id"])
		assert.NotNil(t, debugEvent.fields["headers"])
		assert.Equal(t, len(response.Body), debugEvent.fields["body_size"])
		assert.Equal(t, "false", debugEvent.fields["body_truncated"])
		assert.Equal(t, response.Body, debugEvent.fields["body_preview"])
	})

	t.Run("response with large body truncation", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        true,
				MaxPayloadLogBytes: 15,
			},
		}

		largeResponseBody := []byte(`{"data": "this is a very long response that should be truncated"}`)
		response := &Response{
			StatusCode: 200,
			Body:       largeResponseBody,
			Headers:    http.Header{},
			Stats: Stats{
				ElapsedTime: 500 * time.Millisecond,
				CallCount:   3,
			},
		}

		c.logResponse(response, "trace-large")

		debugEvents := fakeLog.eventsByLevel("debug")
		assert.Len(t, debugEvents, 1)

		debugEvent := debugEvents[0]
		assert.Equal(t, len(largeResponseBody), debugEvent.fields["body_size"])
		assert.Equal(t, "true", debugEvent.fields["body_truncated"])
		assert.Equal(t, largeResponseBody[:15], debugEvent.fields["body_preview"])
	})

	t.Run("response error scenarios", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		c := &client{
			logger: fakeLog,
			config: &Config{LogPayloads: true},
		}

		response := &Response{
			StatusCode: 500,
			Body:       []byte(`{"error": "internal server error"}`),
			Headers:    http.Header{testContentTypeHeader: []string{"application/json"}},
			Stats: Stats{
				ElapsedTime: 5 * time.Second,
				CallCount:   1,
			},
		}

		c.logResponse(response, "trace-error")

		infoEvents := fakeLog.eventsByLevel("info")
		assert.Len(t, infoEvents, 1)

		infoEvent := infoEvents[0]
		assert.Equal(t, 500, infoEvent.fields["status"])
		assert.Equal(t, 5*time.Second, infoEvent.fields["elapsed"])
	})
}

// TestLoggingIntegration tests end-to-end logging scenarios
func TestLoggingIntegration(t *testing.T) {
	t.Run("logging configuration inheritance", func(t *testing.T) {
		fakeLog := &fakeLogger{}

		// Create client with logging configuration
		builtClient := NewBuilder(fakeLog).
			WithTimeout(5 * time.Second).
			Build()

		// Access internal client to test logging methods
		clientImpl := builtClient.(*client)

		// Verify config defaults
		assert.False(t, clientImpl.config.LogPayloads)
		assert.Equal(t, 1024, clientImpl.config.MaxPayloadLogBytes)

		// Test that logging methods work
		req, err := http.NewRequestWithContext(context.Background(), "GET", "http://test.com", http.NoBody)

		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}

		clientImpl.logRequest(req, []byte("test"), "test-integration")

		events := fakeLog.eventsByLevel("info")
		assert.Len(t, events, 1)
		assert.Equal(t, "REST client request", events[0].message)
	})

	t.Run("payload logging configuration", func(t *testing.T) {
		fakeLog := &fakeLogger{}

		// Create a client with custom config that enables payload logging
		clientImpl := &client{
			logger: fakeLog,
			config: &Config{
				LogPayloads:        true,
				MaxPayloadLogBytes: 50,
			},
		}

		req, err := http.NewRequestWithContext(context.Background(), "POST", "http://test.com", http.NoBody)

		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}

		body := []byte("short body")
		clientImpl.logRequest(req, body, "test-payload")

		// Should have both info and debug logs
		assert.Len(t, fakeLog.eventsByLevel("info"), 1)
		assert.Len(t, fakeLog.eventsByLevel("debug"), 1)

		debugEvent := fakeLog.eventsByLevel("debug")[0]
		assert.Contains(t, debugEvent.fields, "body_preview")
		assert.Equal(t, "false", debugEvent.fields["body_truncated"])
	})
}
