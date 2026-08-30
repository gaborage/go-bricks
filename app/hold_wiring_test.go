package app

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/streams"
)

// fakeHoldModule is a module offering a hold ledger, and receiving the replayer
// source — the two duck-types the framework detects at registration.
type fakeHoldModule struct {
	name     string
	ledger   streams.HoldLedger
	replayer func() streams.HoldReplayer
}

func (m *fakeHoldModule) Name() string {
	if m.name == "" {
		return "fake-hold"
	}
	return m.name
}
func (*fakeHoldModule) Init(*ModuleDeps) error                            { return nil }
func (*fakeHoldModule) Shutdown() error                                   { return nil }
func (m *fakeHoldModule) HoldLedger() streams.HoldLedger                  { return m.ledger }
func (m *fakeHoldModule) SetHoldReplayer(src func() streams.HoldReplayer) { m.replayer = src }

// stubHoldLedger is a ledger that records nothing: these tests are about the
// wiring, not the parking.
type stubHoldLedger struct{}

func (*stubHoldLedger) Park(context.Context, *streams.HeldMessage) error { return nil }
func (*stubHoldLedger) HeldTenants(context.Context, string) ([]string, error) {
	return nil, nil
}

// newHoldApp is the smallest App the wiring needs: RegisterModule and holdLedger
// touch neither config nor resources.
func newHoldApp(t *testing.T) *App {
	t.Helper()
	log := logger.New("error", false)
	return &App{
		logger:   log,
		registry: NewModuleRegistry(&ModuleDeps{Logger: log}),
	}
}

// TestRegisterModuleCapturesTheHoldLedger pins the detection: a module offering a
// ledger is remembered, and the stream setup finds exactly it.
func TestRegisterModuleCapturesTheHoldLedger(t *testing.T) {
	a := newHoldApp(t)
	ledger := &stubHoldLedger{}

	require.NoError(t, a.RegisterModule(&fakeHoldModule{ledger: ledger}))

	found, err := a.holdLedger()
	require.NoError(t, err)
	assert.Same(t, ledger, found)
}

// TestRegisterModuleIgnoresAModuleOfferingNoLedger pins the disabled case: a
// module whose hold is off offers nil, and the lane sees no ledger at all.
func TestRegisterModuleIgnoresAModuleOfferingNoLedger(t *testing.T) {
	a := newHoldApp(t)

	require.NoError(t, a.RegisterModule(&fakeHoldModule{ledger: nil}))

	found, err := a.holdLedger()
	require.NoError(t, err)
	assert.Nil(t, found, "a nil ledger is not a ledger")
}

// TestTwoHoldLedgersFailStartup pins the refusal: one consumer's held set cannot
// be split across two ledgers, so the ambiguity is a startup error.
func TestTwoHoldLedgersFailStartup(t *testing.T) {
	a := newHoldApp(t)
	require.NoError(t, a.RegisterModule(&fakeHoldModule{name: "hold-one", ledger: &stubHoldLedger{}}))
	require.NoError(t, a.RegisterModule(&fakeHoldModule{name: "hold-two", ledger: &stubHoldLedger{}}))

	_, err := a.holdLedger()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "two modules provide a hold ledger")
}

// TestRegisterModuleInjectsTheReplayerSource pins that the module receives a
// source it can call safely before the streams manager exists.
func TestRegisterModuleInjectsTheReplayerSource(t *testing.T) {
	a := newHoldApp(t)
	module := &fakeHoldModule{}

	require.NoError(t, a.RegisterModule(module))

	require.NotNil(t, module.replayer, "the module is handed a source at registration")
	assert.Nil(t, module.replayer(), "which answers nil until a manager can drain")
}
