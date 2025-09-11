package main

import (
	"context"
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"time"

	httpClient "github.com/gaborage/go-bricks/http"
	"github.com/gaborage/go-bricks/logger"
)

// Example API response structure
type APIResponse struct {
	Message string `json:"message"`
	Status  string `json:"status"`
}

func main() {
	// Initialize logger
	log := logger.New("info", true)

	// Example 1: Simple client usage
	fmt.Println("=== Simple Client Usage ===")
	simpleExample(log)

	// Example 2: Builder pattern with configuration
	fmt.Println("\n=== Builder Pattern Usage ===")
	builderExample(log)

	// Example 3: With interceptors
	fmt.Println("\n=== Interceptor Usage ===")
	interceptorExample(log)

	// Example 4: Retries with exponential backoff + jitter
	fmt.Println("\n=== Retries + Backoff (with Jitter) ===")
	retryExample(log)
}

func simpleExample(log logger.Logger) {
	// Create a simple client
	client := httpClient.NewClient(log)

	// Make a GET request
	req := &httpClient.Request{
		URL: "https://httpbin.org/get",
		Headers: map[string]string{
			"User-Agent": "GoBricks-Example/1.0",
		},
	}

	ctx := context.Background()
	resp, err := client.Get(ctx, req)
	if err != nil {
		if httpClient.IsErrorType(err, httpClient.NetworkError) {
			fmt.Printf("Network error: %v\n", err)
			return
		}
		fmt.Printf("Request failed: %v\n", err)
		return
	}

	fmt.Printf("Status: %d\n", resp.StatusCode)
	fmt.Printf("Elapsed: %v\n", resp.Stats.ElapsedTime)
	fmt.Printf("Response body length: %d bytes\n", len(resp.Body))
}

func builderExample(log logger.Logger) {
	// Create client using builder pattern
	client := httpClient.NewBuilder(log).
		WithTimeout(10*time.Second).
		WithBasicAuth("user", "password").
		WithDefaultHeader("X-API-Version", "v1").
		WithDefaultHeader("Accept", "application/json").
		Build()

	// POST request with JSON body
	postData := map[string]interface{}{
		"name":  "John Doe",
		"email": "john@example.com",
	}

	jsonBody, err := json.Marshal(postData)
	if err != nil {
		fmt.Printf("Failed to marshal request body: %v\n", err)
		return
	}
	req := &httpClient.Request{
		URL:  "https://httpbin.org/post",
		Body: jsonBody,
		Headers: map[string]string{
			"Content-Type": "application/json",
		},
	}

	ctx := context.Background()
	resp, err := client.Post(ctx, req)
	if err != nil {
		fmt.Printf("POST request failed: %v\n", err)
		return
	}

	fmt.Printf("POST Status: %d\n", resp.StatusCode)
	fmt.Printf("Elapsed: %v\n", resp.Stats.ElapsedTime)

	// Parse response
	var response map[string]interface{}
	if err := json.Unmarshal(resp.Body, &response); err == nil {
		if data, ok := response["json"].(map[string]interface{}); ok {
			fmt.Printf("Posted data: %+v\n", data)
		}
	}
}

func interceptorExample(log logger.Logger) {
	// Request interceptor that adds correlation ID
	requestInterceptor := func(_ context.Context, req *nethttp.Request) error {
		req.Header.Set("X-Correlation-ID", "example-12345")
		fmt.Printf("Request interceptor: Added correlation ID\n")
		return nil
	}

	// Response interceptor that logs response status
	responseInterceptor := func(_ context.Context, _ *nethttp.Request, resp *nethttp.Response) error {
		if resp.StatusCode >= 400 {
			fmt.Printf("Response interceptor: Error status detected (%d)\n", resp.StatusCode)
		} else {
			fmt.Printf("Response interceptor: Success status (%d)\n", resp.StatusCode)
		}
		return nil
	}

	client := httpClient.NewBuilder(log).
		WithTimeout(5 * time.Second).
		WithRequestInterceptor(requestInterceptor).
		WithResponseInterceptor(responseInterceptor).
		Build()

	req := &httpClient.Request{
		URL: "https://httpbin.org/delay/1", // This endpoint adds a 1-second delay
	}

	ctx := context.Background()
	resp, err := client.Get(ctx, req)
	if err != nil {
		fmt.Printf("Intercepted request failed: %v\n", err)
		return
	}

	fmt.Printf("Intercepted request status: %d\n", resp.StatusCode)
	fmt.Printf("Total calls made: %d\n", resp.Stats.CallCount)
}

func retryExample(log logger.Logger) {
	// Demonstrates retries with exponential backoff and full jitter.
	// NOTE: This hits httpbin which may be slow/unavailable in some environments;
	// it is provided as a usage example only.
	client := httpClient.NewBuilder(log).
		WithTimeout(2*time.Second).
		WithRetries(3, 200*time.Millisecond). // base delay; actual sleeps are jittered up to base*2^attempt
		Build()

	// httpbin /status/500 returns 500; the client will retry according to the policy
	req := &httpClient.Request{URL: "https://httpbin.org/status/500"}

	ctx := context.Background()
	resp, err := client.Get(ctx, req)
	if err != nil {
		// Expect an HTTP error after retries are exhausted
		fmt.Printf("Request completed with error after retries: %v\n", err)
		return
	}
	fmt.Printf("Unexpected success: status=%d body=%dB\n", resp.StatusCode, len(resp.Body))
}
