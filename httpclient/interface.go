package httpclient

import (
	"context"
	nethttp "net/http"
	"time"

	gobrickstrace "github.com/gaborage/go-bricks/trace"
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
	// PeerName is a low-cardinality logical service name attached to every metric emitted
	// by this client. Intended for SLO attribution (e.g., "stripe", "visa-vts").
	// When unset, only the high-cardinality server.address is recorded.
	PeerName string
}

// NewTraceIDInterceptor creates a request interceptor that adds trace ID headers
// This provides an alternative approach for users who want explicit control
func NewTraceIDInterceptor() RequestInterceptor {
	return func(ctx context.Context, req *nethttp.Request) error {
		if req.Header.Get(gobrickstrace.HeaderXRequestID) == "" {
			traceID := gobrickstrace.EnsureTraceID(ctx)
			req.Header.Set(gobrickstrace.HeaderXRequestID, traceID)
		}
		return nil
	}
}

// NewTraceIDInterceptorFor creates an interceptor that uses a custom header name
func NewTraceIDInterceptorFor(header string) RequestInterceptor {
	if header == "" {
		header = gobrickstrace.HeaderXRequestID
	}
	return func(ctx context.Context, req *nethttp.Request) error {
		if req.Header.Get(header) == "" {
			req.Header.Set(header, gobrickstrace.EnsureTraceID(ctx))
		}
		return nil
	}
}
