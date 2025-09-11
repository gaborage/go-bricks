// Package http provides a small, composable HTTP client with
// request/response interceptors, default headers, basic auth,
// and a retry mechanism with exponential backoff and jitter.
//
// Retries
//   - Controlled via Builder.WithRetries(maxRetries, retryDelay).
//   - Retries occur on:
//   - Transport errors (network failures)
//   - Timeouts (context deadline exceeded or net.Error timeout)
//   - HTTP 5xx responses
//   - 4xx responses are not retried.
//
// Backoff Strategy
//   - Exponential backoff based on retryDelay: delay = retryDelay * 2^attempt
//   - Full jitter is applied: actual sleep is random in [0, delay).
//   - Delay is capped at 30 seconds to avoid excessive waits.
//
// Notes
//   - Request bodies are re-sent by rebuilding the http.Request on each attempt.
//   - Interceptor errors are not retried and are surfaced immediately.
package http
