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

// ContentTypeJSON and ContentTypeJOSE are the encodings the framework's own
// doors claim when they know one — a marshaled payload, or an event sealed
// into a compact JWS (ADR-097). They live here because messaging and the outbox
// must agree on the literal and both already import this package.
const (
	ContentTypeJSON = "application/json"
	ContentTypeJOSE = "application/jose"
)

// MessageProps are the properties of the MESSAGE — as opposed to Options' fields,
// which name its destination — known only to the door that encoded or read it,
// for the AMQP properties of the same names (ADR-105). A nil *MessageProps means
// nothing is known here: messaging falls back to octet-stream, leaves type
// unset, and mints its own message id.
type MessageProps struct {
	ContentType string
	EventType   string
	MessageID   string
}

// Options is the destination of one byte publish: the fields messaging's own
// publish options carry, restated here because this package cannot import
// messaging (messaging imports it).
type Options struct {
	Exchange   string
	RoutingKey string
	Headers    map[string]any
	Mandatory  bool
	Immediate  bool
	Props      *MessageProps
}

// Func publishes data to opts through client, which must be a messaging
// client the framework built (messaging asserts its unexported bytes door on
// it and returns its typed error otherwise).
type Func func(ctx context.Context, client any, opts Options, data []byte) error

// ErrNotRegistered is returned by Publish when no dispatcher is registered —
// package messaging is not linked, which no framework build allows.
var ErrNotRegistered = errors.New("publishdoor: no byte publish dispatcher registered (package messaging not linked)")

var registered atomic.Pointer[Func]

// Register installs the dispatcher. messaging calls it from init, exactly once
// per process; a second registration panics, as the streams seam does, so a
// stray registrant cannot silently win by link order. A test that needs to
// replace the dispatcher uses Swap.
func Register(fn Func) {
	if fn == nil {
		panic("publishdoor: Register requires a non-nil dispatcher")
	}
	if !registered.CompareAndSwap(nil, &fn) {
		panic("publishdoor: byte publish dispatcher already registered")
	}
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
