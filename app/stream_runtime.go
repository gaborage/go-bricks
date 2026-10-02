package app

import "github.com/gaborage/go-bricks/internal/streamruntime"

// ErrStreamsNotLinked is returned at startup when messaging.streams.uri is set
// but messaging/streams was never imported into the build. The lane is opt-in
// at the build graph (ADR-091); a leftover URI must not boot as a silent no-op.
var ErrStreamsNotLinked = streamruntime.ErrNotLinked

// HeldMessage is one parked stream delivery as the hold ledger sees it. It
// lives on this seam so inbox can implement the hold port without importing
// messaging/streams (and therefore without pulling the vendor client).
type HeldMessage = streamruntime.HeldMessage

// HoldLedger is the port stream consumers park through. Inbox implements it
// when inbox.hold.enabled is set.
type HoldLedger = streamruntime.HoldLedger

// HoldReplayer is what the hold drain drives to put a held message back through
// the lane.
type HoldReplayer = streamruntime.HoldReplayer

// streamHandle is the field type stored on App: the methods every lifecycle
// walk needs, without Start, so tests can still assign a concrete *streams.Manager.
type streamHandle interface {
	Close() error
	StopConsumers()
	Ready() bool
	Stats() map[string]any
}

func registeredStreamRuntime() streamruntime.Runtime {
	return streamruntime.Registered()
}

func swapStreamRuntime(r streamruntime.Runtime) streamruntime.Runtime {
	return streamruntime.SwapRegistered(r)
}
