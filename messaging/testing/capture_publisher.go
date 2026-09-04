// Package testing provides test doubles for the messaging package's
// module-facing surface. It is the publish-side complement of testing/mocks:
// since ADR-096 no exported client carries a byte publish method, a module's
// publishes are observed at the typed handle, not at the client.
package testing

import (
	"context"
	"sync"

	"github.com/gaborage/go-bricks/messaging"
)

// CapturePublisher is a messaging.EventPublisher[T] that records every event
// it is asked to publish and returns the configured error. A module stores its
// *messaging.Publisher[T] behind messaging.EventPublisher[T] and a test injects
// this in its place — the typed value is what gets asserted, never a frame of
// bytes the test would have to re-decode.
//
// Safe for concurrent use: handlers publish from many goroutines.
type CapturePublisher[T any] struct {
	mu     sync.Mutex
	events []T
	err    error
}

var _ messaging.EventPublisher[struct{}] = (*CapturePublisher[struct{}])(nil)

// NewCapturePublisher returns an empty capture whose Publish succeeds.
func NewCapturePublisher[T any]() *CapturePublisher[T] {
	return &CapturePublisher[T]{}
}

// Fail makes every later Publish return err (nil restores success). The event
// is still recorded, so a test can assert what the module attempted.
func (c *CapturePublisher[T]) Fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.err = err
}

// Publish records evt and returns the configured error. The client is ignored:
// nothing reaches a broker.
func (c *CapturePublisher[T]) Publish(_ context.Context, _ messaging.AMQPClient, evt T) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, evt)
	return c.err
}

// Events returns every recorded event, oldest first, as a copy the caller owns.
func (c *CapturePublisher[T]) Events() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]T, len(c.events))
	copy(out, c.events)
	return out
}

// Last returns the most recent event and whether there was one.
func (c *CapturePublisher[T]) Last() (evt T, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return evt, false
	}
	return c.events[len(c.events)-1], true
}

// Reset drops the recorded events; the configured error is kept.
func (c *CapturePublisher[T]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = nil
}
