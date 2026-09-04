// Package publishdoor is the one link-time seam through which framework
// internals outside package messaging — the outbox relay — hand raw bytes to
// an AMQP client. ADR-096 removed the byte publish methods from every exported
// messaging type; the bytes door is an unexported interface inside messaging,
// so a sibling package cannot name it. messaging registers a dispatcher here at
// init, exactly as messaging/streams registers its runtime (ADR-091), and the
// relay publishes through Publish. Nothing under internal/ is importable by a
// consumer, so no module can reach this door.
package publishdoor

import (
	"context"
	"errors"
	"sync/atomic"
)

// Options is the destination of one byte publish: the fields messaging's own
// publish options carry, restated here because this package cannot import
// messaging (messaging imports it).
type Options struct {
	Exchange   string
	RoutingKey string
	Headers    map[string]any
	Mandatory  bool
	Immediate  bool
}

// Func publishes data to opts through client, which must be a messaging
// client the framework built (messaging asserts its unexported bytes door on
// it and returns its typed error otherwise).
type Func func(ctx context.Context, client any, opts Options, data []byte) error

// ErrNotRegistered is returned by Publish when no dispatcher is registered —
// package messaging is not linked, which no framework build allows.
var ErrNotRegistered = errors.New("publishdoor: no byte publish dispatcher registered (package messaging not linked)")

var registered atomic.Pointer[Func]

// Register installs the dispatcher. messaging calls it from init; a second
// registration replaces the first, which only a test does (see Swap).
func Register(fn Func) {
	if fn == nil {
		panic("publishdoor: Register requires a non-nil dispatcher")
	}
	registered.Store(&fn)
}

// Swap replaces the dispatcher and returns the previous one, for a test that
// captures the relay's publishes; restore it in t.Cleanup. A nil fn unregisters.
func Swap(fn Func) Func {
	var prev Func
	if p := registered.Swap(ptrOrNil(fn)); p != nil {
		prev = *p
	}
	return prev
}

func ptrOrNil(fn Func) *Func {
	if fn == nil {
		return nil
	}
	return &fn
}

// Publish hands data to the registered dispatcher.
func Publish(ctx context.Context, client any, opts Options, data []byte) error {
	p := registered.Load()
	if p == nil {
		return ErrNotRegistered
	}
	return (*p)(ctx, client, opts, data)
}
