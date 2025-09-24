// Package main demonstrates the HTTP client's automatic trace ID propagation
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	gobrickshttp "github.com/gaborage/go-bricks/http"
	"github.com/gaborage/go-bricks/logger"
)

func main() {
	// Create a test server that logs received headers
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Request-ID")
		traceParent := r.Header.Get("traceparent")
		fmt.Printf("Server received trace ID: %s\n", traceID)
		if traceParent != "" {
			fmt.Printf("Server received traceparent: %s\n", traceParent)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"message": "Hello", "traceId": "%s"}`, traceID)
	}))
	defer server.Close()

	// Create HTTP client
	log := logger.New("info", false)
	client := gobrickshttp.NewClient(log)

	// Example 1: Automatic trace ID generation
	fmt.Println("=== Example 1: Automatic Trace ID ===")
	req1 := &gobrickshttp.Request{URL: server.URL}
	resp1, err := client.Get(context.Background(), req1)
	if err != nil {
		log.Error().Err(err).Msg("Request 1 failed")
		return
	}
	fmt.Printf("Response: %s\n\n", string(resp1.Body))

	// Example 2: Context-provided trace ID
	fmt.Println("=== Example 2: Context Trace ID ===")
	ctx := gobrickshttp.WithTraceID(context.Background(), "my-custom-trace-123")
	req2 := &gobrickshttp.Request{URL: server.URL}
	resp2, err := client.Get(ctx, req2)
	if err != nil {
		log.Error().Err(err).Msg("Request 2 failed")
		return
	}
	fmt.Printf("Response: %s\n\n", string(resp2.Body))

	// Example 3: Header-provided trace ID (takes precedence)
	fmt.Println("=== Example 3: Header Trace ID (precedence) ===")
	ctxWithTrace := gobrickshttp.WithTraceID(context.Background(), "context-trace")
	req3 := &gobrickshttp.Request{
		URL: server.URL,
		Headers: map[string]string{
			"X-Request-ID": "header-trace-priority",
		},
	}
	resp3, err := client.Get(ctxWithTrace, req3)
	if err != nil {
		log.Error().Err(err).Msg("Request 3 failed")
		return
	}
	fmt.Printf("Response: %s\n\n", string(resp3.Body))

	// Example 4: Using trace ID interceptor (alternative approach)
	fmt.Println("=== Example 4: Trace ID Interceptor ===")
	clientWithInterceptor := gobrickshttp.NewBuilder(log).
		WithRequestInterceptor(gobrickshttp.NewTraceIDInterceptor()).
		Build()

	ctx4 := gobrickshttp.WithTraceID(context.Background(), "interceptor-trace-456")
	req4 := &gobrickshttp.Request{URL: server.URL}
	resp4, err := clientWithInterceptor.Get(ctx4, req4)
	if err != nil {
		log.Error().Err(err).Msg("Request 4 failed")
		return
	}
	fmt.Printf("Response: %s\n", string(resp4.Body))
}
