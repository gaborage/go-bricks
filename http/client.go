package http

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	nethttp "net/http"
	"sync/atomic"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

const (
	// DefaultTimeout is the default request timeout duration
	DefaultTimeout = 30 * time.Second

	// DefaultMaxRetries is the default maximum number of retries for failed requests
	DefaultMaxRetries = 0

	// DefaultRetryDelay is the default delay between retries
	DefaultRetryDelay = 1 * time.Second
)

// client implements the Client interface
type client struct {
	httpClient           *nethttp.Client
	logger               logger.Logger
	config               *Config
	requestInterceptors  []RequestInterceptor
	responseInterceptors []ResponseInterceptor
	callCount            int64
}

// NewClient creates a new REST client with default configuration
func NewClient(log logger.Logger) Client {
	return &client{
		httpClient: &nethttp.Client{
			Timeout: DefaultTimeout,
		},
		logger: log,
		config: &Config{
			Timeout:              DefaultTimeout,
			MaxRetries:           DefaultMaxRetries,
			RetryDelay:           DefaultRetryDelay,
			RequestInterceptors:  []RequestInterceptor{},
			ResponseInterceptors: []ResponseInterceptor{},
			DefaultHeaders:       make(map[string]string),
		},
	}
}

// Builder provides a fluent interface for configuring the REST client
type Builder struct {
	config *Config
	logger logger.Logger
}

// NewBuilder creates a new client builder
func NewBuilder(log logger.Logger) *Builder {
	return &Builder{
		config: &Config{
			Timeout:              DefaultTimeout,
			MaxRetries:           DefaultMaxRetries,
			RetryDelay:           DefaultRetryDelay,
			RequestInterceptors:  []RequestInterceptor{},
			ResponseInterceptors: []ResponseInterceptor{},
			DefaultHeaders:       make(map[string]string),
		},
		logger: log,
	}
}

// WithTimeout sets the request timeout
func (b *Builder) WithTimeout(timeout time.Duration) *Builder {
	b.config.Timeout = timeout
	return b
}

// WithRetries sets the retry configuration
func (b *Builder) WithRetries(maxRetries int, retryDelay time.Duration) *Builder {
	b.config.MaxRetries = maxRetries
	b.config.RetryDelay = retryDelay
	return b
}

// WithBasicAuth sets basic authentication credentials
func (b *Builder) WithBasicAuth(username, password string) *Builder {
	b.config.BasicAuth = &BasicAuth{
		Username: username,
		Password: password,
	}
	return b
}

// WithDefaultHeader adds a default header that will be sent with all requests
func (b *Builder) WithDefaultHeader(key, value string) *Builder {
	b.config.DefaultHeaders[key] = value
	return b
}

// WithRequestInterceptor adds a request interceptor
func (b *Builder) WithRequestInterceptor(interceptor RequestInterceptor) *Builder {
	b.config.RequestInterceptors = append(b.config.RequestInterceptors, interceptor)
	return b
}

// WithResponseInterceptor adds a response interceptor
func (b *Builder) WithResponseInterceptor(interceptor ResponseInterceptor) *Builder {
	b.config.ResponseInterceptors = append(b.config.ResponseInterceptors, interceptor)
	return b
}

// Build creates the REST client with the configured options
func (b *Builder) Build() Client {
	return &client{
		httpClient: &nethttp.Client{
			Timeout: b.config.Timeout,
		},
		logger:               b.logger,
		config:               b.config,
		requestInterceptors:  b.config.RequestInterceptors,
		responseInterceptors: b.config.ResponseInterceptors,
	}
}

// Get performs a GET request
func (c *client) Get(ctx context.Context, req *Request) (*Response, error) {
	return c.Do(ctx, nethttp.MethodGet, req)
}

// Post performs a POST request
func (c *client) Post(ctx context.Context, req *Request) (*Response, error) {
	return c.Do(ctx, nethttp.MethodPost, req)
}

// Put performs a PUT request
func (c *client) Put(ctx context.Context, req *Request) (*Response, error) {
	return c.Do(ctx, nethttp.MethodPut, req)
}

// Patch performs a PATCH request
func (c *client) Patch(ctx context.Context, req *Request) (*Response, error) {
	return c.Do(ctx, nethttp.MethodPatch, req)
}

// Delete performs a DELETE request
func (c *client) Delete(ctx context.Context, req *Request) (*Response, error) {
	return c.Do(ctx, nethttp.MethodDelete, req)
}

// Do performs an HTTP request with the specified method
func (c *client) Do(ctx context.Context, method string, req *Request) (*Response, error) {
	if err := c.validateRequest(req); err != nil {
		return nil, err
	}

	start := time.Now()
	callCount := atomic.AddInt64(&c.callCount, 1)
	maxRetries := c.config.MaxRetries

	for attempt := 0; ; attempt++ {
		c.logRequest(method, req)

		httpReq, err := c.buildRequest(ctx, method, req)
		if err != nil {
			return nil, err
		}

		httpResp, err := c.httpClient.Do(httpReq)
		if err != nil {
			if c.isTimeout(err) {
				if attempt < maxRetries {
					time.Sleep(c.backoffDelay(attempt))
					continue
				}
				return nil, NewTimeoutError("request timeout", c.config.Timeout)
			}
			if attempt < maxRetries {
				time.Sleep(c.backoffDelay(attempt))
				continue
			}
			return nil, NewNetworkError("request execution failed", err)
		}

		resp, err := c.buildResponse(ctx, start, callCount, httpReq, httpResp)
		if err != nil {
			if attempt < maxRetries && IsErrorType(err, NetworkError) {
				time.Sleep(c.backoffDelay(attempt))
				continue
			}
			return nil, err
		}

		if IsSuccessStatus(resp.StatusCode) {
			c.logResponse(resp)
			return resp, nil
		}

		if c.isRetryableStatus(resp.StatusCode) && attempt < maxRetries {
			time.Sleep(c.backoffDelay(attempt))
			continue
		}

		c.logResponse(resp)
		return resp, NewHTTPError(
			fmt.Sprintf("HTTP request failed with status %d", resp.StatusCode),
			resp.StatusCode,
			resp.Body,
		)
	}
}

// backoffDelay returns the exponential backoff delay for the given attempt,
// using RetryDelay as the base and capping to a reasonable maximum.
func (c *client) backoffDelay(attempt int) time.Duration {
	base := c.config.RetryDelay
	if base <= 0 {
		base = 50 * time.Millisecond
	}
	// Cap attempt to avoid overflow when computing multiplier
	if attempt > 20 { // 2^20 = 1,048,576
		attempt = 20
	}
	// Exponential backoff: base * 2^attempt
	mult := 1 << attempt
	d := base * time.Duration(mult)
	// Cap to 30 seconds to avoid excessive sleeps
	const maxBackoff = 30 * time.Second
	if d > maxBackoff {
		d = maxBackoff
	}
	// Full jitter: random duration in [0, d)
	if d <= 0 {
		return base
	}
	maxN := big.NewInt(int64(d))
	n, err := crand.Int(crand.Reader, maxN)
	if err != nil {
		// On RNG failure, fall back to the full delay
		return d
	}
	return time.Duration(n.Int64())
}

// validateRequest validates the request before sending
func (c *client) validateRequest(req *Request) error {
	if req == nil {
		return NewValidationError("request cannot be nil", "request")
	}
	if req.URL == "" {
		return NewValidationError("URL cannot be empty", "url")
	}
	return nil
}

// applyHeaders applies headers to the HTTP request
func (c *client) applyHeaders(httpReq *nethttp.Request, req *Request) {
	// Apply default headers first
	for key, value := range c.config.DefaultHeaders {
		httpReq.Header.Set(key, value)
	}

	// Apply request-specific headers (these override defaults)
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	// Set Content-Type if not already set and body is present
	if httpReq.Header.Get("Content-Type") == "" && req.Body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
}

// applyAuth applies authentication to the HTTP request
func (c *client) applyAuth(httpReq *nethttp.Request, req *Request) {
	// Request-specific auth takes precedence
	auth := req.Auth
	if auth == nil {
		auth = c.config.BasicAuth
	}

	if auth != nil {
		httpReq.SetBasicAuth(auth.Username, auth.Password)
	}
}

// buildRequest constructs an *http.Request, applies headers/auth, and runs request interceptors.
func (c *client) buildRequest(ctx context.Context, method string, req *Request) (*nethttp.Request, error) {
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := nethttp.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, NewNetworkError("failed to create HTTP request", err)
	}

	c.applyHeaders(httpReq, req)
	c.applyAuth(httpReq, req)

	if err := c.runRequestInterceptors(ctx, httpReq); err != nil {
		return nil, NewInterceptorError("request interceptor failed", "request", err)
	}
	return httpReq, nil
}

// buildResponse runs response interceptors, reads body, and builds a Response.
func (c *client) buildResponse(ctx context.Context, start time.Time, callCount int64, httpReq *nethttp.Request, httpResp *nethttp.Response) (*Response, error) {
	defer httpResp.Body.Close()

	if err := c.runResponseInterceptors(ctx, httpReq, httpResp); err != nil {
		return nil, NewInterceptorError("response interceptor failed", "response", err)
	}

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, NewNetworkError("failed to read response body", err)
	}

	elapsed := time.Since(start)
	return &Response{
		StatusCode: httpResp.StatusCode,
		Body:       respBody,
		Headers:    httpResp.Header,
		Stats: Stats{
			ElapsedTime: elapsed,
			CallCount:   callCount,
		},
	}, nil
}

func (c *client) isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (c *client) isRetryableStatus(code int) bool {
	return code >= 500 && code < 600
}

// runRequestInterceptors executes all request interceptors
func (c *client) runRequestInterceptors(ctx context.Context, req *nethttp.Request) error {
	for _, interceptor := range c.requestInterceptors {
		if err := interceptor(ctx, req); err != nil {
			return err
		}
	}
	return nil
}

// runResponseInterceptors executes all response interceptors
func (c *client) runResponseInterceptors(ctx context.Context, req *nethttp.Request, resp *nethttp.Response) error {
	for _, interceptor := range c.responseInterceptors {
		if err := interceptor(ctx, req, resp); err != nil {
			return err
		}
	}
	return nil
}

// logRequest logs the outgoing request
func (c *client) logRequest(method string, req *Request) {
	logEvent := c.logger.Info().
		Str("direction", "outbound").
		Str("method", method).
		Str("url", req.URL)

	if len(req.Headers) > 0 {
		logEvent.Interface("headers", req.Headers)
	}

	if len(req.Body) > 0 {
		logEvent.Bytes("body", req.Body)
	}

	logEvent.Msg("REST client request")
}

// logResponse logs the incoming response
func (c *client) logResponse(resp *Response) {
	logEvent := c.logger.Info().
		Str("direction", "inbound").
		Int("status", resp.StatusCode).
		Dur("elapsed", resp.Stats.ElapsedTime).
		Int64("call_count", resp.Stats.CallCount)

	if len(resp.Body) > 0 {
		logEvent.Bytes("body", resp.Body)
	}

	logEvent.Msg("REST client response")
}
