# Trace ID Propagation Example

This example demonstrates the HTTP client's automatic trace ID propagation functionality for improved observability in distributed systems.

## Features Demonstrated

1. **Automatic Trace ID Generation**: When no trace ID is provided, the client automatically generates a UUID
2. **Context-Based Trace IDs**: Pass trace IDs through context for seamless propagation
3. **Header Precedence**: Request headers take precedence over context values
4. **Request Interceptor**: Alternative approach using interceptors for explicit control

## Running the Example

```bash
cd examples/trace-propagation
go run main.go
```

## Expected Output

The example will show how trace IDs are automatically added to outbound HTTP requests:

```
=== Example 1: Automatic Trace ID ===
Server received trace ID: 550e8400-e29b-41d4-a716-446655440000
Response: {"message": "Hello", "traceId": "550e8400-e29b-41d4-a716-446655440000"}

=== Example 2: Context Trace ID ===
Server received trace ID: my-custom-trace-123
Response: {"message": "Hello", "traceId": "my-custom-trace-123"}

=== Example 3: Header Trace ID (precedence) ===
Server received trace ID: header-trace-priority
Response: {"message": "Hello", "traceId": "header-trace-priority"}

=== Example 4: Trace ID Interceptor ===
Server received trace ID: interceptor-trace-456
Response: {"message": "Hello", "traceId": "interceptor-trace-456"}
```

## Key Concepts

- **X-Request-ID Header**: Standard header used for request tracing
- **Context Propagation**: Trace IDs flow through Go contexts
- **Header Precedence**: Explicit headers override context values
- **Zero Configuration**: Works automatically without setup
- **Backward Compatible**: No breaking changes to existing code

## Integration Patterns

1. **Default Behavior**: Automatic trace ID propagation
2. **Context Utilities**: `http.WithTraceID()` and `http.GetTraceIDFromContext()`
3. **Request Interceptors**: `http.NewTraceIDInterceptor()` for explicit control
4. **Header Override**: Set `X-Request-ID` header directly in request