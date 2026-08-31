package app

import "github.com/gaborage/go-bricks/internal/streamruntime"

// ErrStreamsNotLinked is returned at startup when messaging.streams.uri is set
// but messaging/streams was never imported into the build. The lane is opt-in
// at the build graph (ADR-091); a leftover URI must not boot as a silent no-op.
var ErrStreamsNotLinked = streamruntime.ErrNotLinked

// StreamRuntime is the registered streams implementation. messaging/streams
// registers one from init. This is a link-time factory, not a second Manager
// (ADR-045): the concrete manager still lives in messaging/streams.
type StreamRuntime = streamruntime.Runtime

// StreamDeclarations is the collected, validated declaration set.
type StreamDeclarations = streamruntime.Declarations

// StreamDeclStats is the operator-facing count the unconfigured-URI error names.
type StreamDeclStats = streamruntime.DeclStats

// StreamManagerOptions is the subset of manager construction app owns.
type StreamManagerOptions = streamruntime.ManagerOptions

// StreamHandle is the started (or about-to-start) stream manager as app sees it.
type StreamHandle = streamruntime.Handle

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

// RegisterStreamRuntime installs the streams lane implementation. A blank
// import of messaging/streams does this from init; an explicit call is the
// same seam. A second registration panics.
func RegisterStreamRuntime(r StreamRuntime) {
	streamruntime.Register(r)
}

func registeredStreamRuntime() StreamRuntime {
	return streamruntime.Registered()
}

func swapStreamRuntime(r StreamRuntime) StreamRuntime {
	return streamruntime.SwapRegistered(r)
}
