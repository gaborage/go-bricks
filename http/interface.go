package http

import (
	"context"
	nethttp "net/http"
	"time"
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
}
