package observability

import (
	"context"
	"fmt"
	"time"
)

const (
	// DefaultShutdownTimeout is the default timeout for graceful shutdown.
	DefaultShutdownTimeout = 10 * time.Second
)

// Shutdown is a convenience function for gracefully shutting down an observability provider.
// It creates a context with timeout and calls the provider's Shutdown method.
// Returns an error if shutdown fails or times out.
func Shutdown(provider Provider, timeout time.Duration) error {
	if provider == nil {
		return nil
	}

	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := provider.Shutdown(ctx); err != nil {
		return fmt.Errorf("observability shutdown failed: %w", err)
	}

	return nil
}

// MustShutdown is like Shutdown but panics on error.
// Useful for cleanup in defer statements where error handling is not possible.
func MustShutdown(provider Provider, timeout time.Duration) {
	if err := Shutdown(provider, timeout); err != nil {
		panic(err)
	}
}
