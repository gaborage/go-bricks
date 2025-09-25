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
	return NewBuilder(log).Build()
}

// Builder provides a fluent interface for configuring the REST client
type Builder struct {
	config     *Config
	logger     logger.Logger
	httpClient *nethttp.Client
	transport  nethttp.RoundTripper
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
			LogPayloads:          false,
			MaxPayloadLogBytes:   1024,
			TraceIDHeader:        HeaderXRequestID,
			NewTraceID:           func() string { return EnsureTraceID(context.Background()) },
			TraceIDExtractor:     TraceIDFromContext,
			EnableW3CTrace:       true,
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

// WithTraceIDHeader sets the header name used for the trace ID (default: X-Request-ID)
func (b *Builder) WithTraceIDHeader(name string) *Builder {
	if name != "" {
		b.config.TraceIDHeader = name
	}
	return b
}

// WithTraceIDGenerator sets the generator used when no trace ID is present
func (b *Builder) WithTraceIDGenerator(gen func() string) *Builder {
	if gen != nil {
		b.config.NewTraceID = gen
	}
	return b
}

// WithTraceIDExtractor sets a function to extract a trace ID from context
func (b *Builder) WithTraceIDExtractor(extractor func(_ context.Context) (string, bool)) *Builder {
	if extractor != nil {
		b.config.TraceIDExtractor = extractor
	}
	return b
}

// WithW3CTrace enables or disables W3C trace context propagation
func (b *Builder) WithW3CTrace(enabled bool) *Builder {
	b.config.EnableW3CTrace = enabled
	return b
}

// WithHTTPClient allows providing a custom *http.Client instance.
// When supplied, the builder uses it directly without modifying its Timeout or Transport.
func (b *Builder) WithHTTPClient(client *nethttp.Client) *Builder {
	b.httpClient = client
	return b
}

// WithTransport sets a custom RoundTripper while still letting the builder manage other client settings.
func (b *Builder) WithTransport(transport nethttp.RoundTripper) *Builder {
	b.transport = transport
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
	// Deep-copy the builder config to avoid sharing mutable state
	cfg := deepCopyConfig(b.config)

	httpClient := b.httpClient
	if httpClient == nil {
		httpClient = &nethttp.Client{Timeout: cfg.Timeout}
	} else if httpClient.Timeout == 0 {
		// Respect existing non-zero timeouts on the provided client.
		// When zero, default to the builder timeout to preserve previous behavior.
		httpClient.Timeout = cfg.Timeout
	}

	if b.transport != nil {
		httpClient.Transport = b.transport
	}

	return &client{
		httpClient:           httpClient,
		logger:               b.logger,
		config:               cfg,
		requestInterceptors:  cfg.RequestInterceptors,
		responseInterceptors: cfg.ResponseInterceptors,
	}
}

// deepCopyConfig creates a deep copy of the provided Config to ensure
// clients do not share mutable state (maps, slices, pointers) with the builder.
func deepCopyConfig(src *Config) *Config {
	if src == nil {
		return nil
	}

	dst := &Config{
		Timeout:            src.Timeout,
		MaxRetries:         src.MaxRetries,
		RetryDelay:         src.RetryDelay,
		LogPayloads:        src.LogPayloads,
		MaxPayloadLogBytes: src.MaxPayloadLogBytes,
		TraceIDHeader:      src.TraceIDHeader,
		EnableW3CTrace:     src.EnableW3CTrace,
	}

	// Copy BasicAuth
	if src.BasicAuth != nil {
		dst.BasicAuth = &BasicAuth{
			Username: src.BasicAuth.Username,
			Password: src.BasicAuth.Password,
		}
	}

	// Copy DefaultHeaders
	if src.DefaultHeaders != nil {
		dst.DefaultHeaders = make(map[string]string, len(src.DefaultHeaders))
		for k, v := range src.DefaultHeaders {
			dst.DefaultHeaders[k] = v
		}
	}

	// Copy interceptors (new slice headers)
	if src.RequestInterceptors != nil {
		dst.RequestInterceptors = make([]RequestInterceptor, len(src.RequestInterceptors))
		copy(dst.RequestInterceptors, src.RequestInterceptors)
	}
	if src.ResponseInterceptors != nil {
		dst.ResponseInterceptors = make([]ResponseInterceptor, len(src.ResponseInterceptors))
		copy(dst.ResponseInterceptors, src.ResponseInterceptors)
	}

	// Copy function fields
	if src.NewTraceID != nil {
		dst.NewTraceID = src.NewTraceID
	}
	if src.TraceIDExtractor != nil {
		dst.TraceIDExtractor = src.TraceIDExtractor
	}

	return dst
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

type attemptResult struct {
	response *Response
	err      error
	retry    bool
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
		result := c.executeAttempt(ctx, method, req, attempt, maxRetries, start, callCount)
		if result.retry {
			continue
		}
		return result.response, result.err
	}
}

func (c *client) executeAttempt(
	ctx context.Context,
	method string,
	req *Request,
	attempt, maxRetries int,
	start time.Time,
	callCount int64,
) attemptResult {
	httpReq, err := c.buildRequest(ctx, method, req)
	if err != nil {
		return attemptResult{err: err}
	}

	traceHeader := c.traceHeaderName()
	traceIDForLog := httpReq.Header.Get(traceHeader)

	c.logRequest(httpReq, req.Body, traceIDForLog)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		if httpResp != nil {
			httpResp.Body.Close()
		}
		return c.handleExecutionError(ctx, err, attempt, maxRetries)
	}

	return c.processHTTPResponse(ctx, httpReq, httpResp, attempt, maxRetries, start, callCount, traceIDForLog)
}

func (c *client) handleExecutionError(ctx context.Context, err error, attempt, maxRetries int) attemptResult {
	retry, handledErr := c.handleExecError(ctx, err, attempt, maxRetries)
	if handledErr != nil {
		return attemptResult{err: handledErr}
	}
	if retry {
		return attemptResult{retry: true}
	}
	return attemptResult{err: NewNetworkError("request execution failed", err)}
}

func (c *client) processHTTPResponse(
	ctx context.Context,
	httpReq *nethttp.Request,
	httpResp *nethttp.Response,
	attempt, maxRetries int,
	start time.Time,
	callCount int64,
	traceID string,
) attemptResult {
	resp, err := c.buildResponse(ctx, start, callCount, httpReq, httpResp)
	if err != nil {
		return c.handleResponseBuildError(ctx, err, attempt, maxRetries)
	}
	return c.finalizeResponse(ctx, resp, attempt, maxRetries, traceID)
}

func (c *client) handleResponseBuildError(ctx context.Context, err error, attempt, maxRetries int) attemptResult {
	retry, handledErr := c.handleBuildRespError(ctx, err, attempt, maxRetries)
	if handledErr != nil {
		return attemptResult{err: handledErr}
	}
	if retry {
		return attemptResult{retry: true}
	}
	return attemptResult{err: err}
}

func (c *client) finalizeResponse(ctx context.Context, resp *Response, attempt, maxRetries int, traceID string) attemptResult {
	if IsSuccessStatus(resp.StatusCode) {
		c.logResponse(resp, traceID)
		return attemptResult{response: resp}
	}

	retry, err := c.shouldRetryStatus(ctx, resp.StatusCode, attempt, maxRetries)
	if err != nil {
		return attemptResult{response: resp, err: err}
	}
	if retry {
		return attemptResult{retry: true}
	}

	c.logResponse(resp, traceID)
	return attemptResult{
		response: resp,
		err: NewHTTPError(
			fmt.Sprintf("HTTP request failed with status %d", resp.StatusCode),
			resp.StatusCode,
			resp.Body,
		),
	}
}

// handleExecError delegates to shouldRetryOnError and normalizes its results.
func (c *client) handleExecError(ctx context.Context, err error, attempt, maxRetries int) (bool, error) {
	cont, herr := c.shouldRetryOnError(ctx, err, attempt, maxRetries)
	if herr != nil {
		return false, herr
	}
	return cont, nil
}

// handleBuildRespError delegates to shouldRetryOnBuildRespError and normalizes its results.
func (c *client) handleBuildRespError(ctx context.Context, err error, attempt, maxRetries int) (bool, error) {
	cont, herr := c.shouldRetryOnBuildRespError(ctx, err, attempt, maxRetries)
	if herr != nil {
		return false, herr
	}
	return cont, nil
}

// shouldRetryOnError determines whether to retry after a request execution error
// and waits with context if appropriate. Returns (true, nil) to retry, or (false, err)
// when no retry should occur and an error should be propagated.
func (c *client) shouldRetryOnError(ctx context.Context, err error, attempt, maxRetries int) (bool, error) {
	if c.isTimeout(err) {
		if attempt < maxRetries {
			if werr := c.backoffWithContext(ctx, attempt); werr != nil {
				return false, werr
			}
			return true, nil
		}
		return false, NewTimeoutError("request timeout", c.config.Timeout)
	}
	if attempt < maxRetries {
		if werr := c.backoffWithContext(ctx, attempt); werr != nil {
			return false, werr
		}
		return true, nil
	}
	return false, NewNetworkError("request execution failed", err)
}

// shouldRetryOnBuildRespError handles errors that occur while building the response
// and decides whether to retry based on error type and attempt count.
func (c *client) shouldRetryOnBuildRespError(ctx context.Context, err error, attempt, maxRetries int) (bool, error) {
	if attempt < maxRetries && IsErrorType(err, NetworkError) {
		if werr := c.backoffWithContext(ctx, attempt); werr != nil {
			return false, werr
		}
		return true, nil
	}
	return false, err
}

// shouldRetryStatus decides whether to retry based on HTTP status code and waits
// for the backoff delay while honoring context cancellation.
func (c *client) shouldRetryStatus(ctx context.Context, statusCode, attempt, maxRetries int) (bool, error) {
	if c.isRetryableStatus(statusCode) && attempt < maxRetries {
		if werr := c.backoffWithContext(ctx, attempt); werr != nil {
			return false, werr
		}
		return true, nil
	}
	return false, nil
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

// backoffWithContext waits for the backoff delay or returns early if the context is done.
func (c *client) backoffWithContext(ctx context.Context, attempt int) error {
	d := c.backoffDelay(attempt)
	if d <= 0 {
		// Yield to scheduler but remain cancellable
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

// extractTraceID extracts a trace ID from the context using configured strategy, or generates a new one
func (c *client) extractTraceID(ctx context.Context) string {
	if c.config != nil && c.config.TraceIDExtractor != nil {
		if traceID, ok := c.config.TraceIDExtractor(ctx); ok && traceID != "" {
			return traceID
		}
	}
	if c.config != nil && c.config.NewTraceID != nil {
		return c.config.NewTraceID()
	}
	// Fallback in unlikely case config is nil or functions are nil
	return EnsureTraceID(ctx)
}

func (c *client) traceHeaderName() string {
	if c.config != nil && c.config.TraceIDHeader != "" {
		return c.config.TraceIDHeader
	}
	return HeaderXRequestID
}

// applyHeaders applies headers to the HTTP request
func (c *client) applyHeaders(httpReq *nethttp.Request, req *Request) {
	applyHeaderMap(httpReq.Header, c.config.DefaultHeaders)
	applyHeaderMap(httpReq.Header, req.Headers)
	c.ensureContentTypeHeader(httpReq, req.Body)
	c.ensureTraceIDHeader(httpReq)
	if c.config.EnableW3CTrace {
		c.ensureTraceContextHeaders(httpReq)
	}
}

func applyHeaderMap(dst nethttp.Header, headers map[string]string) {
	if len(headers) == 0 {
		return
	}
	for key, value := range headers {
		dst.Set(key, value)
	}
}

func (c *client) ensureContentTypeHeader(httpReq *nethttp.Request, body []byte) {
	if body == nil {
		return
	}
	if httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
}

func (c *client) ensureTraceIDHeader(httpReq *nethttp.Request) {
	headerName := c.traceHeaderName()
	if httpReq.Header.Get(headerName) != "" {
		return
	}
	httpReq.Header.Set(headerName, c.extractTraceID(httpReq.Context()))
}

func (c *client) ensureTraceContextHeaders(httpReq *nethttp.Request) {
	if httpReq.Header.Get(HeaderTraceParent) == "" {
		if tp, ok := TraceParentFromContext(httpReq.Context()); ok {
			httpReq.Header.Set(HeaderTraceParent, tp)
		} else {
			httpReq.Header.Set(HeaderTraceParent, GenerateTraceParent())
		}
	}
	if httpReq.Header.Get(HeaderTraceState) == "" {
		if ts, ok := TraceStateFromContext(httpReq.Context()); ok {
			httpReq.Header.Set(HeaderTraceState, ts)
		}
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
func (c *client) logRequest(httpReq *nethttp.Request, body []byte, traceID string) {
	// Info-level: only metadata to avoid leaking PII/secrets
	infoEvent := c.logger.Info().
		Str("direction", "outbound").
		Str("method", httpReq.Method).
		Str("url", httpReq.URL.String()).
		Str("request_id", traceID)
	if headerCount := len(httpReq.Header); headerCount > 0 {
		infoEvent.Int("header_count", headerCount)
	}
	if len(body) > 0 {
		infoEvent.Int("body_size", len(body))
	}
	infoEvent.Msg("REST client request")

	// Optional debug payload logging, gated by config
	if c.config != nil && c.config.LogPayloads {
		dbg := c.logger.Debug().
			Str("direction", "outbound").
			Str("method", httpReq.Method).
			Str("url", httpReq.URL.String()).
			Str("request_id", traceID)
		if len(httpReq.Header) > 0 {
			// Headers go through logger filter to mask sensitive keys/values
			dbg = dbg.Interface("headers", httpReq.Header)
		}
		if len(body) > 0 {
			limit := c.config.MaxPayloadLogBytes
			if limit <= 0 {
				limit = 1024
			}
			truncated := false
			preview := body
			if len(preview) > limit {
				preview = preview[:limit]
				truncated = true
			}
			dbg = dbg.Int("body_size", len(body)).
				Str("body_truncated", map[bool]string{true: "true", false: "false"}[truncated]).
				Bytes("body_preview", preview)
		}
		dbg.Msg("REST client request")
	}
}

// logResponse logs the incoming response
func (c *client) logResponse(resp *Response, traceID string) {
	// Info-level: only metadata to avoid leaking PII/secrets
	infoEvent := c.logger.Info().
		Str("direction", "inbound").
		Int("status", resp.StatusCode).
		Dur("elapsed", resp.Stats.ElapsedTime).
		Int64("call_count", resp.Stats.CallCount).
		Str("request_id", traceID)
	if len(resp.Body) > 0 {
		infoEvent.Int("body_size", len(resp.Body))
	}
	// Headers are not logged at info level to minimize risk
	infoEvent.Msg("REST client response")

	// Optional debug payload logging, gated by config
	if c.config != nil && c.config.LogPayloads {
		dbg := c.logger.Debug().
			Str("direction", "inbound").
			Int("status", resp.StatusCode).
			Dur("elapsed", resp.Stats.ElapsedTime).
			Int64("call_count", resp.Stats.CallCount).
			Str("request_id", traceID)
		if len(resp.Body) > 0 {
			limit := c.config.MaxPayloadLogBytes
			if limit <= 0 {
				limit = 1024
			}
			truncated := false
			preview := resp.Body
			if len(preview) > limit {
				preview = preview[:limit]
				truncated = true
			}
			dbg = dbg.Int("body_size", len(resp.Body)).
				Str("body_truncated", map[bool]string{true: "true", false: "false"}[truncated]).
				Bytes("body_preview", preview)
		}
		// Response headers go through logger filter to mask sensitive keys/values
		if len(resp.Headers) > 0 {
			dbg = dbg.Interface("headers", resp.Headers)
		}
		dbg.Msg("REST client response")
	}
}

// generator moved to http/interface.go for reuse by server
