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

func (e *fakeLogEvent) Bool(key string, value bool) logger.LogEvent {
	e.fields[key] = value
	return e
}

func (e *fakeLogEvent) Enabled() bool { return true }

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
		req.Header.Set(testContentTypeHeader, testContentType)

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
		assert.Equal(t, "false", debugEvent.fields["body_truncated"])
		// JSON body is parsed and logged via Interface (not raw bytes)
		preview, ok := debugEvent.fields["body_preview"].(map[string]any)
		assert.True(t, ok, "JSON body_preview should be a parsed map")
		assert.Equal(t, "some content for testing", preview["data"])
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
		// Plain text body (no Content-Type) → bytes dropped, not logged
		assert.Equal(t, "", debugEvent.fields["body_content_type"])
		assert.Equal(t, 10, debugEvent.fields["body_preview_dropped"])
		assert.Nil(t, debugEvent.fields["body_preview"])
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
		// Plain text body (no Content-Type) → bytes dropped, not logged; preview size is capped at default 1024
		assert.Equal(t, "", debugEvent.fields["body_content_type"])
		assert.Equal(t, 1024, debugEvent.fields["body_preview_dropped"])
		assert.Nil(t, debugEvent.fields["body_preview"])
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
			Headers:    http.Header{"X-Rate-Limit": []string{"100"}, testContentTypeHeader: []string{testContentType}},
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
		// JSON response body is parsed and logged via Interface (filter can walk keys)
		preview, ok := debugEvent.fields["body_preview"].(map[string]any)
		assert.True(t, ok, "JSON body_preview should be a parsed map")
		assert.Equal(t, float64(123), preview["id"])
		assert.Equal(t, true, preview["created"])
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
		// No Content-Type on response headers → bytes dropped, not logged
		assert.Equal(t, "", debugEvent.fields["body_content_type"])
		assert.Equal(t, 15, debugEvent.fields["body_preview_dropped"])
		assert.Nil(t, debugEvent.fields["body_preview"])
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
		// Plain text body (no Content-Type) → bytes dropped, not logged
		assert.Equal(t, "", debugEvent.fields["body_content_type"])
		assert.Equal(t, len(body), debugEvent.fields["body_preview_dropped"])
		assert.Nil(t, debugEvent.fields["body_preview"])
		assert.Equal(t, "false", debugEvent.fields["body_truncated"])
	})
}

// TestBuilderPayloadLoggingSetters verifies that WithLogPayloads and WithMaxPayloadLogBytes
// propagate correctly into the built client config.
func TestBuilderPayloadLoggingSetters(t *testing.T) {
	t.Run("WithLogPayloads enables payload logging", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		builtClient := NewBuilder(fakeLog).
			WithLogPayloads(true).
			Build()
		c := builtClient.(*client)
		assert.True(t, c.config.LogPayloads)
	})

	t.Run("WithLogPayloads false disables payload logging", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		builtClient := NewBuilder(fakeLog).
			WithLogPayloads(false).
			Build()
		c := builtClient.(*client)
		assert.False(t, c.config.LogPayloads)
	})

	t.Run("WithMaxPayloadLogBytes sets limit", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		builtClient := NewBuilder(fakeLog).
			WithLogPayloads(true).
			WithMaxPayloadLogBytes(2048).
			Build()
		c := builtClient.(*client)
		assert.True(t, c.config.LogPayloads)
		assert.Equal(t, 2048, c.config.MaxPayloadLogBytes)
	})

	t.Run("WithMaxPayloadLogBytes ignores zero or negative", func(t *testing.T) {
		fakeLog := &fakeLogger{}
		builtClient := NewBuilder(fakeLog).
			WithMaxPayloadLogBytes(0).
			Build()
		c := builtClient.(*client)
		// Should retain the default (1024), not override with zero
		assert.Equal(t, 1024, c.config.MaxPayloadLogBytes)
	})
}

// TestClientLogRequestJSONBodyParsedAsMap verifies that JSON request bodies are parsed
// into map[string]any before being passed to Interface() — this is what enables the
// SensitiveDataFilter to walk nested keys and mask sensitive fields. Actual masking
// is tested at the logger layer (logger/adapter_test.go, logger/filter_test.go).
func TestClientLogRequestJSONBodyParsedAsMap(t *testing.T) {
	fakeLog := &fakeLogger{}
	c := &client{
		logger: fakeLog,
		config: &Config{
			LogPayloads:        true,
			MaxPayloadLogBytes: 512,
		},
	}

	req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.example.com/auth", http.NoBody)
	assert.NoError(t, err)
	req.Header.Set(testContentTypeHeader, testContentType)

	body := []byte(`{"username": "alice", "password": "s3cr3t!", "amount": 100}`)
	c.logRequest(req, body, "trace-sensitive")

	debugEvents := fakeLog.eventsByLevel("debug")
	assert.Len(t, debugEvents, 1)

	debugEvent := debugEvents[0]
	// JSON body_preview must be map[string]any so the SensitiveDataFilter can walk its keys.
	// If it were []byte, the filter would see an opaque blob and masking could not occur.
	preview, ok := debugEvent.fields["body_preview"].(map[string]any)
	assert.True(t, ok, "JSON body_preview must be a parsed map[string]any to enable filter walking")
	assert.Equal(t, "alice", preview["username"])
	assert.Equal(t, float64(100), preview["amount"])
	assert.Contains(t, preview, "password", "parsed map must contain all JSON keys")
}

// TestLogBodyPreviewPrimitiveRootDropped verifies that JSON bodies whose root is a
// primitive or array are never emitted as body_preview. SensitiveDataFilter can only
// walk map[string]any keys, so logging a raw string/number/bool/array would bypass
// filtering entirely. The fix gates on map[string]any and drops everything else.
func TestLogBodyPreviewPrimitiveRootDropped(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{name: "json_string", body: []byte(`"secret-token"`)},
		{name: "json_number", body: []byte(`123456789`)},
		{name: "json_bool", body: []byte(`true`)},
		{name: "json_array", body: []byte(`["item1","item2"]`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeLog := &fakeLogger{}
			c := &client{
				logger: fakeLog,
				config: &Config{LogPayloads: true, MaxPayloadLogBytes: 512},
			}

			req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.example.com/x", http.NoBody)
			assert.NoError(t, err)
			req.Header.Set(testContentTypeHeader, testContentType)

			c.logRequest(req, tc.body, "trace-123")

			debugEvents := fakeLog.eventsByLevel("debug")
			assert.Len(t, debugEvents, 1)

			ev := debugEvents[0]
			assert.Nil(t, ev.fields["body_preview"], "primitive/array JSON root must not appear as body_preview")
			assert.Equal(t, len(tc.body), ev.fields["body_preview_dropped"], "byte count must be logged for dropped body")
		})
	}
}

// TestLogBodyPreviewMalformedJSONLogsDroppedCount verifies that when a JSON body
// fails to parse (e.g., truncated mid-token by MaxPayloadLogBytes), the log event
// contains both body_preview_status and body_preview_dropped so operators can
// distinguish truncation-induced errors from server-side malformed JSON.
func TestLogBodyPreviewMalformedJSONLogsDroppedCount(t *testing.T) {
	fakeLog := &fakeLogger{}
	c := &client{
		logger: fakeLog,
		config: &Config{LogPayloads: true, MaxPayloadLogBytes: 10},
	}

	// A 10-byte truncation of valid JSON produces invalid JSON (cut mid-token)
	body := []byte(`{"username": "alice", "password": "s3cr3t"}`)
	req, err := http.NewRequestWithContext(context.Background(), "POST", "https://api.example.com/x", http.NoBody)
	assert.NoError(t, err)
	req.Header.Set(testContentTypeHeader, testContentType)

	c.logRequest(req, body, "trace-123")

	debugEvents := fakeLog.eventsByLevel("debug")
	assert.Len(t, debugEvents, 1)

	ev := debugEvents[0]
	assert.Equal(t, "json_parse_failed", ev.fields["body_preview_status"], "status must indicate parse failure")
	assert.Equal(t, 10, ev.fields["body_preview_dropped"], "byte count must be logged even on parse failure")
	assert.Nil(t, ev.fields["body_preview"], "raw body bytes must never appear in log output")
}

// TestIsJSONContentTypeEdgeCases verifies that isJSONContentType handles
// edge-case Content-Type values that occur with proxies or custom transports.
func TestIsJSONContentTypeEdgeCases(t *testing.T) {
	cases := []struct {
		ct   string
		want bool
	}{
		{ct: "application/json", want: true},
		{ct: "application/json; charset=utf-8", want: true},
		{ct: "  application/json  ", want: true},             // leading/trailing whitespace
		{ct: " application/json; charset=utf-8", want: true}, // leading space before semicolon
		{ct: "application/vnd.api+json", want: true},
		{ct: "APPLICATION/JSON", want: true},
		{ct: "text/plain", want: false},
		{ct: "", want: false},
		{ct: "text/html; charset=utf-8", want: false},
	}
	for _, tc := range cases {
		got := isJSONContentType(tc.ct)
		if got != tc.want {
			t.Errorf("isJSONContentType(%q) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}
