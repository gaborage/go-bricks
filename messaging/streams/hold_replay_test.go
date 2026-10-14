package streams

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/lanecontract"
	"github.com/gaborage/go-bricks/multitenant"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

// holdManagerOn builds a manager with a ledger and starts the given declarations
// against a fake environment, which is how a holding consumer exists at all.
func holdManagerOn(t *testing.T, ledger HoldLedger, decls *Declarations) *Manager {
	t.Helper()
	m := NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
		Hold:   ledger,
	})
	m.SetTenantStamps(true)
	startOnFake(t, m, newFakeEnvironment(), decls)
	return m
}

// holdDeclsWithHandler is one holding consumer whose handler the test owns.
func holdDeclsWithHandler(handler Handler) *Declarations {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, Handler: handler, Hold: true,
	})
	return decls
}

// TestManagerReplayRunsTheHandlerWithTheHeldTenant pins what a replay is: the
// handler runs again for a parked message, under the tenant the row remembered
// rather than a stamp read off a carrier, and NOTHING is committed — settlement
// belongs to the drain, which deletes the row or defers the tenant.
func TestManagerReplayRunsTheHandlerWithTheHeldTenant(t *testing.T) {
	var sawTenant string
	var sawOffset int64
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
		func(ctx context.Context, msg *Message) error {
			sawTenant, _ = multitenant.TenantID(ctx)
			sawOffset = msg.Offset
			return nil
		}))

	err := m.Replay(context.Background(), testConsumerName, &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: 4, TenantID: "acme",
		Data: []byte("payload"), HeldAt: time.Now(),
	})

	require.NoError(t, err)
	assert.Equal(t, "acme", sawTenant, "the replay carries the tenant the row remembered")
	assert.Equal(t, int64(4), sawOffset)
}

// TestManagerReplayReturnsTheHandlerErrorUntouched pins that the drain decides
// what a failed replay means — the lane hands the error back as it is.
func TestManagerReplayReturnsTheHandlerErrorUntouched(t *testing.T) {
	wantErr := errors.New("still failing")
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
		func(context.Context, *Message) error { return wantErr }))

	err := m.Replay(context.Background(), testConsumerName, &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: 4, TenantID: "acme",
	})

	assert.ErrorIs(t, err, wantErr)
}

// TestManagerReplayConsumeSpanIsARootLinkedToTheCaller pins ADR-068's root rule
// on the hold replay path: the drain's job span is linked, not adopted as the
// parent, and the handler's own spans stay inside the replay's trace.
func TestManagerReplayConsumeSpanIsARootLinkedToTheCaller(t *testing.T) {
	exporter, _ := lanecontract.SetupTelemetry(t)
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
		func(ctx context.Context, _ *Message) error {
			_, child := otel.Tracer("test-handler").Start(ctx, "handler.child")
			child.End()
			return nil
		}))

	parentCtx, parent := otel.Tracer("test-drain").Start(context.Background(), "job.execute")
	err := m.Replay(parentCtx, testConsumerName, &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: 4, TenantID: "acme",
	})
	parent.End()
	require.NoError(t, err)

	consume := obtest.NewSpanCollector(t, exporter).WithName(testStream + " receive").AssertCount(1).First()
	child := obtest.NewSpanCollector(t, exporter).WithName("handler.child").AssertCount(1).First()

	assert.False(t, consume.Parent.IsValid(), "the replay's consume span is a root")
	require.Len(t, consume.Links, 1, "the replay links the caller's span")
	assert.True(t, parent.SpanContext().Equal(consume.Links[0].SpanContext), "the link names the caller's span")

	assert.Equal(t, consume.SpanContext.TraceID(), child.SpanContext.TraceID(), "the handler's span is in the replay's trace")
	assert.Equal(t, consume.SpanContext.SpanID(), child.Parent.SpanID(), "the handler's span nests under the consume span")
	assert.NotEqual(t, parent.SpanContext().TraceID(), child.SpanContext.TraceID(), "the handler's span left the caller's trace")
}

// TestManagerReplayWithoutACallerSpanLinksNothing pins the unguarded link: the
// SDK drops a link to an invalid span context, so a replay with no caller span
// is a root with zero links.
func TestManagerReplayWithoutACallerSpanLinksNothing(t *testing.T) {
	exporter, _ := lanecontract.SetupTelemetry(t)
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(noopHandler))

	err := m.Replay(context.Background(), testConsumerName, &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: 4, TenantID: "acme",
	})
	require.NoError(t, err)

	consume := obtest.NewSpanCollector(t, exporter).WithName(testStream + " receive").AssertCount(1).First()
	assert.False(t, consume.Parent.IsValid(), "the replay's consume span is a root")
	assert.Empty(t, consume.Links, "an invalid caller span context leaves no link")
}

// TestManagerReplayRefusesAnUnknownConsumer pins the case where a ledger row
// outlives the consumer that parked it: the drain must hear about it rather than
// silently drop the row.
func TestManagerReplayRefusesAnUnknownConsumer(t *testing.T) {
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(noopHandler))

	err := m.Replay(context.Background(), "nope", &HeldMessage{TenantID: "acme"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), `no running consumer "nope"`)
}

// TestManagerReloadHeldSwapsTheGate pins the drain's other direction: what the
// ledger holds becomes what the partition gates on.
func TestManagerReloadHeldSwapsTheGate(t *testing.T) {
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"globex"}}}
	handled := map[string]int{}
	m := holdManagerOn(t, ledger, holdDeclsWithHandler(
		func(ctx context.Context, _ *Message) error {
			tenant, _ := multitenant.TenantID(ctx)
			handled[tenant]++
			return nil
		}))
	runner := m.consumers[0].runner
	runner.held.add("acme")

	require.NoError(t, m.ReloadHeld(t.Context(), testConsumerName))

	storer := &fakeStorer{}
	runner.deliver(testStream, 1, stampedMessage("acme"), storer)
	runner.deliver(testStream, 2, stampedMessage("globex"), storer)

	assert.Equal(t, 1, handled["acme"], "acme was released by the reload")
	assert.Zero(t, handled["globex"], "and globex is held by it")
}

// TestManagerReloadHeldIgnoresAnUnknownConsumer pins that a drain pass naming a
// consumer this replica does not run is a no-op, not a panic: consumers differ
// per deployment and the ledger is shared.
func TestManagerReloadHeldIgnoresAnUnknownConsumer(t *testing.T) {
	m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(noopHandler))

	assert.NotPanics(t, func() {
		assert.NoError(t, m.ReloadHeld(t.Context(), "nope"))
	})
}

// TestManagerHoldConsumersListsOnlyHoldingOnes pins what the drain iterates: a
// consumer that does not hold has nothing to drain.
func TestManagerHoldConsumersListsOnlyHoldingOnes(t *testing.T) {
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)
	decls.DeclareStream("plain", nil)
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: testStream, Name: testConsumerName, Handler: noopHandler, Hold: true,
	})
	decls.DeclareConsumer(&ConsumerOptions{
		Stream: "plain", Name: "plain-worker", Handler: noopHandler,
	})
	m := holdManagerOn(t, &fakeHoldLedger{}, decls)

	assert.Equal(t, []string{testConsumerName}, m.HoldConsumers())
}

// TestManagerReloadHeldKeepsAParkThatRacesTheRead pins the generation guard on
// the drain's reload: a park landing while the ledger listing is in flight must
// survive it. The listing predates that park, so applying it would un-hold a
// tenant whose message is already parked — and the next message for that tenant
// would be delivered ahead of it, which is the ordering guarantee gone.
func TestManagerReloadHeldKeepsAParkThatRacesTheRead(t *testing.T) {
	ledger := &fakeHoldLedger{held: map[string][]string{testConsumerName: {"globex"}}}
	m := holdManagerOn(t, ledger, holdDeclsWithHandler(noopHandler))
	runner := m.consumers[0].runner

	// Read 1 answers the listing the park is NOT in, and the park lands while that
	// read is in flight. Read 2 — the retry the refused replace forces — is the one
	// that sees it. A reload that applies read 1 loses the park.
	reads := 0
	ledger.duringHeldRead = func() {
		reads++
		switch reads {
		case 1:
			runner.held.add("acme")
		case 2:
			ledger.held[testConsumerName] = []string{"globex", "acme"}
		}
	}

	require.NoError(t, m.ReloadHeld(t.Context(), testConsumerName))

	assert.True(t, runner.held.has("acme"), "the park that raced the read is still held")
	assert.True(t, runner.held.has("globex"), "and what the ledger reported is held too")
}

// heldMessage is one parked row as the drain hands it back.
func heldMessage(tenant string, offset int64) *HeldMessage {
	return &HeldMessage{
		Consumer: testConsumerName, Stream: testStream, Offset: offset, TenantID: tenant,
		Data: []byte("payload"), HeldAt: time.Now(),
	}
}

// captureReplayLogs returns what the lane wrote while fn ran. The framework
// logger writes to os.Stdout directly, so the logger under test has to be built
// inside the capture.
func captureReplayLogs(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	defer func() { os.Stdout = original }()
	defer r.Close()
	os.Stdout = w

	fn()

	require.NoError(t, w.Close())
	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	require.NoError(t, err)
	return buf.String()
}

// TestReplayLogsOnlyFailures pins what a replay writes. A succeeded replay is
// silent — one line per replayed row would drown the failures an operator is
// actually watching for — and a failed one is marked as a replay, so a retry of a
// held message is not read as a message failing for the first time.
func TestReplayLogsOnlyFailures(t *testing.T) {
	t.Run("a_succeeded_replay_says_nothing", func(t *testing.T) {
		out := captureReplayLogs(t, func() {
			m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(noopHandler))
			require.NoError(t, m.Replay(t.Context(), testConsumerName, heldMessage("acme", 7)))
		})
		assert.NotContains(t, out, "Hold replay failed", "a replay that worked is not news")
	})

	t.Run("a_failed_replay_is_logged_as_a_replay", func(t *testing.T) {
		out := captureReplayLogs(t, func() {
			m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
				func(context.Context, *Message) error { return errors.New("handler said no") }))
			require.Error(t, m.Replay(t.Context(), testConsumerName, heldMessage("acme", 7)))
		})
		assert.Contains(t, out, "Hold replay failed")
		assert.Contains(t, out, `"hold_replay":true`, "marked as a replay, not a first failure")
		assert.Contains(t, out, "handler said no", "and carries the handler's own error")
	})

	t.Run("a_panicked_replay_is_logged_without_its_value", func(t *testing.T) {
		out := captureReplayLogs(t, func() {
			m := holdManagerOn(t, &fakeHoldLedger{}, holdDeclsWithHandler(
				func(context.Context, *Message) error { panic("secret in the panic value") }))
			require.Error(t, m.Replay(t.Context(), testConsumerName, heldMessage("acme", 7)))
		})
		assert.Contains(t, out, "Hold replay failed")
		assert.NotContains(t, out, "secret in the panic value", "ADR-081: by type, never by value")
	})
}
