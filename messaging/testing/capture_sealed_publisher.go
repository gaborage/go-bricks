package testing

import (
	"bytes"
	"context"
	"strconv"
	"sync"

	"github.com/gaborage/go-bricks/messaging"
)

// CaptureSealedPublisher is a concurrency-safe SealedEventPublisher[T] double that records calls and never seals, verifies or publishes.
type CaptureSealedPublisher[T any] struct {
	mu         sync.Mutex
	sealed     []T
	published  [][]byte
	next       int
	sealErr    error
	publishErr error
}

var _ messaging.SealedEventPublisher[struct{}] = (*CaptureSealedPublisher[struct{}])(nil)

// NewCaptureSealedPublisher returns an empty capture whose Seal and PublishSealed succeed.
func NewCaptureSealedPublisher[T any]() *CaptureSealedPublisher[T] {
	return &CaptureSealedPublisher[T]{}
}

// Seal records evt and returns "sealed-<n>" and "jti-<n>", or the error FailSeal configured.
func (c *CaptureSealedPublisher[T]) Seal(_ context.Context, evt T) (data []byte, jti string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sealed = append(c.sealed, evt)
	if c.sealErr != nil {
		return nil, "", c.sealErr
	}
	c.next++
	n := strconv.Itoa(c.next)
	return []byte("sealed-" + n), "jti-" + n, nil
}

// PublishSealed records a copy of data and returns the error FailPublish configured.
func (c *CaptureSealedPublisher[T]) PublishSealed(_ context.Context, _ messaging.AMQPClient, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.published = append(c.published, bytes.Clone(data))
	return c.publishErr
}

// FailSeal makes every later Seal return err (nil restores success).
func (c *CaptureSealedPublisher[T]) FailSeal(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sealErr = err
}

// FailPublish makes every later PublishSealed return err (nil restores success).
func (c *CaptureSealedPublisher[T]) FailPublish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publishErr = err
}

// Sealed returns every event handed to Seal, oldest first, as a copy the caller owns.
func (c *CaptureSealedPublisher[T]) Sealed() []T {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]T, len(c.sealed))
	copy(out, c.sealed)
	return out
}

// Published returns every body handed to PublishSealed, oldest first, as copies the caller owns.
func (c *CaptureSealedPublisher[T]) Published() [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([][]byte, len(c.published))
	for i, b := range c.published {
		out[i] = bytes.Clone(b)
	}
	return out
}

// Reset drops the records and restarts the placeholder counter; configured errors are kept.
func (c *CaptureSealedPublisher[T]) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sealed, c.published, c.next = nil, nil, 0
}
