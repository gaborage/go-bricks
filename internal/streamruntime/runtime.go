// Package streamruntime is the one link-time seam for the native streams lane.
//
// messaging/streams registers an implementation at init. app starts the manager
// through that registration. inbox implements the hold port without importing
// messaging/streams, so a core-only consumer never pulls the vendor client
// (ADR-091).
package streamruntime

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gaborage/go-bricks/logger"
)

// ErrNotLinked is returned at startup when messaging.streams.uri is set but
// messaging/streams was never imported into the build.
var ErrNotLinked = errors.New(
	`messaging.streams.uri is set but the streams lane is not linked; import _ "github.com/gaborage/go-bricks/messaging/streams"`)

// Runtime is the registered streams implementation.
type Runtime interface {
	CollectDeclarations(modules []ModuleNamer, log logger.Logger) (Declarations, error)
	NewManager(opts *ManagerOptions) Handle
	CanDrainHold() bool
}

// ModuleNamer is the subset of app.Module the runtime needs to walk declarers.
type ModuleNamer interface {
	Name() string
}

// Declarations is the collected, validated declaration set. The concrete type
// is produced by the registered runtime so app never names streams types.
type Declarations interface {
	IsEmpty() bool
	Stats() DeclStats
}

// DeclStats is the operator-facing count the unconfigured-URI error names.
type DeclStats struct {
	Streams    int
	Consumers  int
	Publishers int
}

// ManagerOptions is the subset of manager construction the framework owns.
type ManagerOptions struct {
	URI                 string
	AddressResolverHost string
	AddressResolverPort int
	OffsetStoreCount    int
	OffsetStoreInterval time.Duration
	Logger              logger.Logger
	Hold                HoldLedger
}

// Handle is the started (or about-to-start) stream manager as the framework sees it.
type Handle interface {
	Start(ctx context.Context, decls Declarations) error
	Close() error
	StopConsumers()
	SetTenantStamps(enabled bool)
	Ready() bool
	Stats() map[string]any
}

// HeldMessage is one parked stream delivery as the hold ledger sees it.
type HeldMessage struct {
	Consumer   string
	Stream     string
	Offset     int64
	TenantID   string
	Data       []byte
	Properties map[string]any
	HeldAt     time.Time
}

// HoldLedger is the port stream consumers park through.
//
// Park is idempotent on (Consumer, Stream, Offset) and marks the tenant held in
// the same write: a row whose tenant is not held would be replayed by nothing.
type HoldLedger interface {
	Park(ctx context.Context, msg *HeldMessage) error
	HeldTenants(ctx context.Context, consumer string) ([]string, error)
}

// HoldReplayer is what the hold drain drives to put a held message back through
// the lane, and how it tells a runner which tenants the ledger still holds.
type HoldReplayer interface {
	HoldConsumers() []string
	Replay(ctx context.Context, consumer string, msg *HeldMessage) error
	// ReloadHeld refreshes one consumer's held set from the ledger. It takes no
	// listing: the generation that guards the set has to be read BEFORE the ledger
	// is, and only the streams package holds it, so the read belongs on that side
	// of the port.
	ReloadHeld(ctx context.Context, consumer string) error
}

var (
	mu      sync.RWMutex
	runtime Runtime
)

// Register installs the streams lane implementation. messaging/streams calls it
// from init. A second registration panics: two factories would silently drop one lane.
func Register(r Runtime) {
	if r == nil {
		panic("streamruntime: Register called with nil")
	}
	mu.Lock()
	defer mu.Unlock()
	if runtime != nil {
		panic("streamruntime: streams runtime already registered")
	}
	runtime = r
}

// Registered returns the installed runtime, or nil when the lane is not linked.
func Registered() Runtime {
	mu.RLock()
	defer mu.RUnlock()
	return runtime
}

// SwapRegistered replaces the installed runtime and returns the previous one.
// Tests use it to exercise the unlinked path in a package that also imports
// messaging/streams.
func SwapRegistered(r Runtime) Runtime {
	mu.Lock()
	defer mu.Unlock()
	prev := runtime
	runtime = r
	return prev
}
