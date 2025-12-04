package httpclient

import (
	"context"
	nethttp "net/http"
	"time"

	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

const (
	// HeaderXRequestID is the standard header name for request tracing
	HeaderXRequestID = gobrickstrace.HeaderXRequestID
	// HeaderTraceParent is the W3C trace context header name
	HeaderTraceParent = gobrickstrace.HeaderTraceParent
	// HeaderTraceState is the W3C trace context "tracestate" header name
	HeaderTraceState = gobrickstrace.HeaderTraceState
)

// Client defines the REST client interface for making HTTP requests
type Client interface {
	Get(ctx context.Context, req *Request) (*Response, error)
	Post(ctx context.Context, req *Request) (*Response, error)
	Put(ctx context.Context, req *Request) (*Response, error)
	Patch(ctx context.Context, req *Request) (*Response, error)
	Delete(ctx context.Context, req *Request) (*Response, error)
	Do(ctx context.Context, method string, req *Request) (*Response, error)
}

// Request represents an HTTP request with all necessary data
type Request struct {
	URL     string
	Headers map[string]string
	Body    []byte
	Auth    *BasicAuth
}

// Response represents an HTTP response with tracking information
type Response struct {
	StatusCode int
	Body       []byte
	Headers    nethttp.Header
	Stats      Stats
}

// Stats contains request execution statistics
type Stats struct {
	ElapsedTime time.Duration
	CallCount   int64
}

// BasicAuth contains basic authentication credentials
type BasicAuth struct {
	Username string
	Password string
}

// RequestInterceptor is called before sending the request
type RequestInterceptor func(ctx context.Context, req *nethttp.Request) error

// ResponseInterceptor is called after receiving the response
type ResponseInterceptor func(ctx context.Context, req *nethttp.Request, resp *nethttp.Response) error

// Config holds the REST client configuration
type Config struct {
	Timeout              time.Duration
	MaxRetries           int
	RetryDelay           time.Duration
	RequestInterceptors  []RequestInterceptor
	ResponseInterceptors []ResponseInterceptor
	BasicAuth            *BasicAuth
	DefaultHeaders       map[string]string
	// LogPayloads enables debug-level logging of headers and body payloads
	LogPayloads bool
	// MaxPayloadLogBytes caps the number of body bytes logged when LogPayloads is enabled
	MaxPayloadLogBytes int
	// TraceIDHeader configures the header name used for trace ID propagation (default: X-Request-ID)
	TraceIDHeader string
	// NewTraceID generates a new trace ID when none is present (default: uuid)
	NewTraceID func() string
	// TraceIDExtractor allows advanced extraction of a trace ID from context; return ok=false to fallback to generator
	TraceIDExtractor func(_ context.Context) (traceID string, ok bool)
	// EnableW3CTrace enables W3C Trace Context (traceparent/tracestate) propagation and generation
	EnableW3CTrace bool
}

// Trace ID utility functions

// WithTraceID adds a trace ID to the context for HTTP client propagation
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return gobrickstrace.WithTraceID(ctx, traceID)
}

// TraceIDFromContext returns a trace ID from context if present
func TraceIDFromContext(ctx context.Context) (string, bool) { return gobrickstrace.IDFromContext(ctx) }

// EnsureTraceID returns an existing trace ID from context or generates a new one
func EnsureTraceID(ctx context.Context) string { return gobrickstrace.EnsureTraceID(ctx) }

// GetTraceIDFromContext remains for backward compatibility; it ensures a non-empty value
func GetTraceIDFromContext(ctx context.Context) string { return EnsureTraceID(ctx) }

// WithTraceParent adds a W3C traceparent value to the context
func WithTraceParent(ctx context.Context, traceParent string) context.Context {
	return gobrickstrace.WithTraceParent(ctx, traceParent)
}

// TraceParentFromContext returns a traceparent from context if present
func TraceParentFromContext(ctx context.Context) (string, bool) {
	return gobrickstrace.ParentFromContext(ctx)
}

// WithTraceState adds a W3C tracestate value to the context
func WithTraceState(ctx context.Context, traceState string) context.Context {
	return gobrickstrace.WithTraceState(ctx, traceState)
}

// TraceStateFromContext returns a tracestate from context if present
func TraceStateFromContext(ctx context.Context) (string, bool) {
	return gobrickstrace.StateFromContext(ctx)
}

// GenerateTraceParent creates a minimal W3C traceparent header value.
// Format: version(2)-trace-id(32)-span-id(16)-flags(2), e.g., "00-<32>-<16>-01"
func GenerateTraceParent() string { return gobrickstrace.GenerateTraceParent() }

// allZero moved to trace; kept no-op here to avoid unused removal

// NewTraceIDInterceptor creates a request interceptor that adds trace ID headers
// This provides an alternative approach for users who want explicit control
func NewTraceIDInterceptor() RequestInterceptor {
	return func(ctx context.Context, req *nethttp.Request) error {
		if req.Header.Get(HeaderXRequestID) == "" {
			traceID := GetTraceIDFromContext(ctx)
			req.Header.Set(HeaderXRequestID, traceID)
		}
		return nil
	}
}

// NewTraceIDInterceptorFor creates an interceptor that uses a custom header name
func NewTraceIDInterceptorFor(header string) RequestInterceptor {
	if header == "" {
		header = HeaderXRequestID
	}
	return func(ctx context.Context, req *nethttp.Request) error {
		if req.Header.Get(header) == "" {
			req.Header.Set(header, GetTraceIDFromContext(ctx))
		}
		return nil
	}
}
