package streams

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging/internal/tenantstamp"
)

// stampedMessage carries a tenant stamp the way a publisher writes one.
func stampedMessage(tenant string) *amqp.Message {
	msg := amqpMessage("payload")
	msg.ApplicationProperties[tenantstamp.Header] = tenant
	return msg
}

// newHoldRunner builds a runner shaped like one the manager starts for a holding
// consumer: stamps on, a ledger behind it, and its own held set.
func newHoldRunner(t *testing.T, handler Handler, ledger HoldLedger, tracker *offsetTracker) *consumerRunner {
	t.Helper()
	runner := newTestRunner(t, handler, tracker)
	runner.tenantStamps = true
	runner.hold = ledger
	return runner
}

// TestRunnerGatesAHeldTenantBeforeTheHandler pins the gate: a delivery for a
// tenant already held never reaches the handler, is parked behind the one it is
// held behind, and COMMITS — the partition keeps moving for everyone else.
func TestRunnerGatesAHeldTenantBeforeTheHandler(t *testing.T) {
	ledger := &fakeHoldLedger{}
	handled := 0
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		handled++
		return nil
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.held.add("tenant-a")
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	assert.Zero(t, handled, "a held tenant's message never reaches the handler")
	parks := ledger.parked()
	require.Len(t, parks, 1)
	assert.Equal(t, "tenant-a", parks[0].TenantID)
	assert.Equal(t, int64(41), parks[0].Offset)
	assert.Equal(t, []int64{41}, storer.offsets(), "a gated delivery commits: it is durably held")
}

// TestRunnerParksAFailedDeliveryAndHoldsItsTenant pins the park: a handler
// failure for an unheld tenant is written to the ledger, the tenant becomes held
// for this partition, and the offset commits because the message is safe.
func TestRunnerParksAFailedDeliveryAndHoldsItsTenant(t *testing.T) {
	ledger := &fakeHoldLedger{}
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	parks := ledger.parked()
	require.Len(t, parks, 1)
	assert.Equal(t, "tenant-a", parks[0].TenantID)
	assert.Equal(t, testStream, parks[0].Stream)
	assert.True(t, runner.held.has("tenant-a"), "the partition holds the tenant from now on")
	assert.Equal(t, []int64{41}, storer.offsets(), "a parked delivery commits: the ledger owns it now")
}

// TestRunnerSkipsWhenTheStampRefused pins the empty-tenant case, which is the
// same case as a refused stamp: with no tenant there is nothing to key a hold on,
// so the delivery is skipped exactly as an unheld failure was before the hold.
func TestRunnerSkipsWhenTheStampRefused(t *testing.T) {
	ledger := &fakeHoldLedger{}
	handled := 0
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		handled++
		return nil
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	storer := &fakeStorer{}

	// No stamp on the message: seedTenant refuses it before the handler runs.
	runner.deliver(testStream, 41, amqpMessage("payload"), storer)

	assert.Zero(t, handled, "the pipeline refuses the delivery before the handler")
	assert.Empty(t, ledger.parked(), "nothing to park: a hold is keyed by the tenant")
	assert.Empty(t, storer.offsets(), "and nothing is committed")
}

// TestRunnerWithoutAHoldIsUnchanged pins that a consumer that does not hold keeps
// the lane's old behavior exactly: a failure skips, and nothing reaches for a
// ledger that is not there. The settle path must not even TRY to park — the
// pipeline would recover the nil-ledger panic and the offset would still be
// skipped, so only the absence of that panic line tells the two apart.
func TestRunnerWithoutAHoldIsUnchanged(t *testing.T) {
	rec := &recordingLogger{}
	runner := newTestRunnerWithLogger(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, newOffsetTracker(1, time.Hour, newFakeClock().Now), rec)
	runner.tenantStamps = true
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	assert.Empty(t, storer.offsets(), "an unheld failure still skips the offset")
	for _, msg := range rec.messagesAt("error") {
		assert.NotContains(t, msg, "Panic recovered while settling",
			"a runner with no ledger must not reach for one")
	}
}

// TestRunnerStallsUntilTheLedgerAccepts pins the stall: a ledger that refuses the
// write is retried inside the delivery callback rather than dropped, so nothing is
// committed until the hold is durable.
func TestRunnerStallsUntilTheLedgerAccepts(t *testing.T) {
	ledger := &fakeHoldLedger{parkErr: errors.New("ledger unavailable"), failParkTimes: 2}
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.holdBackoff = time.Millisecond
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	assert.Len(t, ledger.parked(), 1, "the park eventually succeeds")
	assert.Equal(t, []int64{41}, storer.offsets())
}

// TestRunnerStopsStallingWhenTheConsumerStops pins the other end of the stall: a
// canceled consume context ends it, and nothing is committed — a restart
// redelivers from the last committed offset, which is the at-least-once the lane
// promises.
func TestRunnerStopsStallingWhenTheConsumerStops(t *testing.T) {
	ledger := &fakeHoldLedger{parkErr: errors.New("ledger unavailable"), failParkTimes: 1 << 30}
	ctx, cancel := context.WithCancel(context.Background())
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.baseCtx = ctx
	runner.holdBackoff = time.Millisecond
	storer := &fakeStorer{}
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	assert.Empty(t, ledger.parked(), "the ledger never accepted it")
	assert.Empty(t, storer.offsets(), "so the offset is not committed and the message is redelivered")
}

// TestRunnerParkSurvivesTheRetryPolicy pins the interaction between the hold and
// C1's in-place retry: a holding consumer carries DefaultHoldRetry, so a park
// failing inside the handler step is re-invoked by the retry loop. The stall's own
// context check ends it, and the delivery still parks exactly once.
func TestRunnerParkSurvivesTheRetryPolicy(t *testing.T) {
	ledger := &fakeHoldLedger{}
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.retry = toDeliveryRetry(&DefaultHoldRetry)
	runner.holdBackoff = time.Millisecond
	runner.held.add("tenant-a")
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)

	assert.Len(t, ledger.parked(), 1, "the gate parks once however many attempts the policy allows")
	assert.Equal(t, []int64{41}, storer.offsets())
}

// TestRunnerLogsWhatItParked pins the operator's line for a parked delivery: the
// tenant it holds, and the handler error's TYPE beside its bounded text.
func TestRunnerLogsWhatItParked(t *testing.T) {
	rec := &recordingLogger{}
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		return errHandlerFailed
	}, &fakeHoldLedger{}, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.log = rec

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), &fakeStorer{})

	assert.Contains(t, rec.warnMessages(), holdParkedMsg)
	assert.Equal(t, "tenant-a", rec.fieldAt("warn", holdParkedMsg, "tenant"))
	assert.Equal(t, testStream, rec.fieldAt("warn", holdParkedMsg, logFieldStream))
	assert.Equal(t, "*errors.errorString", rec.fieldAt("warn", holdParkedMsg, "error_type"))
	assert.Equal(t, errHandlerFailed.Error(), rec.fieldAt("warn", holdParkedMsg, "error"))
}

// TestRunnerDoesNotGateAnotherTenant pins what the hold is FOR: one tenant held
// must not stop the partition it shares. The second tenant's delivery runs and
// commits while the first is parked.
func TestRunnerDoesNotGateAnotherTenant(t *testing.T) {
	ledger := &fakeHoldLedger{}
	handled := 0
	runner := newHoldRunner(t, func(context.Context, *Message) error {
		handled++
		return nil
	}, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.held.add("tenant-a")
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedMessage("tenant-a"), storer)
	runner.deliver(testStream, 42, stampedMessage("tenant-b"), storer)

	assert.Equal(t, 1, handled, "only the unheld tenant's message reaches the handler")
	parks := ledger.parked()
	require.Len(t, parks, 1, "and only the held tenant's message is parked")
	assert.Equal(t, "tenant-a", parks[0].TenantID)
	assert.False(t, runner.held.has("tenant-b"), "a held neighbor does not hold this tenant")
	assert.Equal(t, []int64{41, 42}, storer.offsets(), "the partition keeps moving")
}

// newTypedHoldRunner builds a holding runner the way the manager builds one for a
// TYPED declaration: the typed handler and the poison screen that belongs to it.
func newTypedHoldRunner[T any](t *testing.T, ledger HoldLedger, fn func(context.Context, T, *Message) error) *consumerRunner {
	t.Helper()

	typed := newTypedConsumer(testConsumerName, fn)
	runner := newHoldRunner(t, typed.handle, ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	runner.screen = typed.screen

	return runner
}

// TestRunnerNeverParksAPayloadFailure pins the poison bypass on the ordinary
// path: a body that cannot decode fails identically on every replay, so parking
// it would hold the tenant behind a message the drain can never delete. It is
// skipped exactly as a failure on a consumer that does not hold (ADR-092).
func TestRunnerNeverParksAPayloadFailure(t *testing.T) {
	ledger := &fakeHoldLedger{}
	runner := newTypedHoldRunner(t, ledger, func(context.Context, streamOrder, *Message) error {
		return nil
	})
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedJSONMessage("tenant-a", `{"amount":"notanint"}`), storer)

	assert.Empty(t, ledger.parked(), "poison is never written to the ledger")
	assert.False(t, runner.held.has("tenant-a"), "and the tenant is never held behind it")
	assert.Empty(t, storer.offsets(), "the offset is skipped, not committed")
}

// TestRunnerNeverParksAValidationFailure is the other half: validation is as
// deterministic as decoding, so the same bypass has to cover it.
func TestRunnerNeverParksAValidationFailure(t *testing.T) {
	ledger := &fakeHoldLedger{}
	runner := newTypedHoldRunner(t, ledger, func(context.Context, streamOrder, *Message) error {
		return nil
	})

	runner.deliver(testStream, 41, stampedJSONMessage("tenant-a", `{"reference":"waytoolong","amount":0}`), &fakeStorer{})

	assert.Empty(t, ledger.parked())
	assert.False(t, runner.held.has("tenant-a"))
}

// TestRunnerStillParksABusinessFailureOnATypedConsumer is the premise the two
// bypass tests need: the same typed runner DOES park when the handler itself
// fails, so the assertions above pin the payload branch rather than a hold that
// stopped working.
func TestRunnerStillParksABusinessFailureOnATypedConsumer(t *testing.T) {
	ledger := &fakeHoldLedger{}
	runner := newTypedHoldRunner(t, ledger, func(context.Context, streamOrder, *Message) error {
		return errHandlerFailed
	})
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedJSONMessage("tenant-a", `{"reference":"abc","amount":7}`), storer)

	require.Len(t, ledger.parked(), 1)
	assert.True(t, runner.held.has("tenant-a"))
	assert.Equal(t, []int64{41}, storer.offsets())
}

// TestRunnerScreensPoisonBeforeTheGate pins the harder half: a HELD tenant's
// delivery is parked without running, so only the screen can tell that this one
// would never have decoded. Parking it would leave the tenant held forever behind
// a row no replay can drain.
func TestRunnerScreensPoisonBeforeTheGate(t *testing.T) {
	ledger := &fakeHoldLedger{}
	handled := 0
	runner := newTypedHoldRunner(t, ledger, func(context.Context, streamOrder, *Message) error {
		handled++
		return nil
	})
	runner.held.add("tenant-a")
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedJSONMessage("tenant-a", `{"amount":"notanint"}`), storer)

	assert.Zero(t, handled, "a held tenant's message still never reaches the handler")
	assert.Empty(t, ledger.parked(), "and poison is not parked behind the tenant either")
	assert.True(t, runner.held.has("tenant-a"), "the tenant stays held: the messages before it are still parked")
	assert.Empty(t, storer.offsets(), "the offset is skipped")
}

// TestRunnerStillGatesADecodableMessage is the premise for the screen: the same
// held tenant's DECODABLE message is still parked in order, so the test above
// pins the screen rather than a gate that stopped parking.
func TestRunnerStillGatesADecodableMessage(t *testing.T) {
	ledger := &fakeHoldLedger{}
	handled := 0
	runner := newTypedHoldRunner(t, ledger, func(context.Context, streamOrder, *Message) error {
		handled++
		return nil
	})
	runner.held.add("tenant-a")
	storer := &fakeStorer{}

	runner.deliver(testStream, 41, stampedJSONMessage("tenant-a", `{"reference":"abc","amount":7}`), storer)

	assert.Zero(t, handled)
	require.Len(t, ledger.parked(), 1)
	assert.Equal(t, int64(41), ledger.parked()[0].Offset)
	assert.Equal(t, []int64{41}, storer.offsets(), "a gated delivery commits: it is durably held")
}

// A consumer with no screen — every hand-written Handler — keeps the gate it had
// before typed consumers existed.
func TestRunnerWithoutAScreenGatesEverything(t *testing.T) {
	ledger := &fakeHoldLedger{}
	runner := newHoldRunner(t, func(context.Context, *Message) error { return nil },
		ledger, newOffsetTracker(1, time.Hour, newFakeClock().Now))
	require.Nil(t, runner.screen, "premise: a hand-written handler carries no screen")
	runner.held.add("tenant-a")

	runner.deliver(testStream, 41, stampedJSONMessage("tenant-a", `not json at all`), &fakeStorer{})

	assert.Len(t, ledger.parked(), 1, "with no screen there is nothing to ask, so the gate parks")
}
