package lanecontract

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/leasescope"
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// runIdentityAgainstBroken drives the broken lane through one scenario and
// returns every assertion message AssertIdentity produced, without failing this
// test.
func runIdentityAgainstBroken(t *testing.T, scenario Scenario) string {
	t.Helper()
	return runAgainstBroken(t, scenario, AssertIdentity)
}

// runTelemetryAgainstBroken is the same for the telemetry family.
func runTelemetryAgainstBroken(t *testing.T, scenario Scenario) string {
	t.Helper()
	return runAgainstBroken(t, scenario, AssertTelemetry)
}

func runAgainstBroken(t *testing.T, scenario Scenario, family func(T, *Lane, *Scenario, *Observed)) string {
	t.Helper()

	lane := BrokenLane()
	observed := lane.Deliver(t, scenario)

	var failures strings.Builder
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, stopped := recovered.(failNow); !stopped {
					panic(recovered)
				}
			}
		}()
		family(&recordingT{out: &failures}, &lane, &scenario, &observed)
	}()

	return failures.String()
}

// recordingT captures what a family reported instead of failing the test.
// FailNow panics with a sentinel the caller recovers, so require keeps its
// contract of stopping the flow — a no-op would let the next require whose
// failure leaves something nil panic here and nowhere else.
type recordingT struct{ out *strings.Builder }

type failNow struct{}

func (r *recordingT) Errorf(format string, args ...any) {
	fmt.Fprintf(r.out, format+"\n", args...)
}
func (r *recordingT) FailNow() { panic(failNow{}) }
func (r *recordingT) Helper()  {}

var _ T = (*recordingT)(nil)

func TestBrokenLaneFailsTheTraceIdentityAssertions(t *testing.T) {
	scenario := Scenario{
		Name:    "succeeding_handler_with_a_carried_trace_id",
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: BrokenCarrierTraceID},
		Handle:  func(context.Context) error { return nil },
		Outcome: delivery.Succeeded,
	}

	failures := runIdentityAgainstBroken(t, scenario)

	// Each of these is one identity assertion proving it bites. If a line stops
	// appearing, either the assertion was weakened or the broken lane was
	// "fixed" — both turn the family into a rubber stamp.
	require.NotEmpty(t, failures, "the identity family must fail against the broken lane")
	assert.Contains(t, failures, "trace.IDFromContext must return an id inside the handler",
		"drops the carrier and writes nothing back")
	assert.Contains(t, failures, "the id that traveled on the carrier must be the id the handler reads",
		"drops the carrier")
	assert.Contains(t, failures, "no line carries the declared message",
		"logs the failure text on a success, so the declared line cannot be found")
}

// The success scenario cannot reach the level and key assertions, because the
// line it declared is never emitted. The handler-error scenario can: the broken
// lane's one line does carry the failure text, so the family gets far enough to
// compare its level and its exact key set.
func TestBrokenLaneFailsTheOutcomeLineShapeAssertions(t *testing.T) {
	scenario := Scenario{
		Name:    "failing_handler",
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: BrokenCarrierTraceID},
		Handle:  func(context.Context) error { return assert.AnError },
		Outcome: delivery.HandlerError,
	}

	failures := runIdentityAgainstBroken(t, scenario)

	require.NotEmpty(t, failures)
	assert.Contains(t, failures, "the outcome line must carry EXACTLY the extra keys the lane declared",
		"emits an undeclared key and omits the one it declared")
	assert.Contains(t, failures, "the outcome line must carry the handler's id under correlation_id",
		"writes no correlation_id")
	assert.Contains(t, failures, "the outcome line's level must be the one the lane declared",
		"logs at WARN where every outcome it declares says ERROR")
	assert.Contains(t, failures, "the shared spine stamps processing_time",
		"writes none of the spine")
}

// The panic scenario reaches the spine's panic/stack assertions, which no other
// scenario can: they are asserted present only for Panicked.
func TestBrokenLaneFailsThePanicSpineAssertions(t *testing.T) {
	failures := runIdentityAgainstBroken(t, Scenario{
		Name:    "panicking_handler",
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: BrokenCarrierTraceID},
		Handle:  func(context.Context) error { panic("boom") },
		Outcome: delivery.Panicked,
	})

	require.NotEmpty(t, failures)
	assert.Contains(t, failures, "a panicked delivery must log the recovered value")
	assert.Contains(t, failures, "a panicked delivery must log its stack")
}

// The lease and settle defects are not identity's to catch — the failure family
// owns them — but they must still be present, or the lane stops being a
// counter-example for the families that land later.
func TestBrokenLaneStillCarriesTheDefectsOtherFamiliesNeed(t *testing.T) {
	lane := BrokenLane()
	leaseReleased := false
	releasedBeforeHandlerReturned := false

	observed := lane.Deliver(t, Scenario{
		Name: "borrows_a_handle",
		Handle: func(ctx context.Context) error {
			leasescope.Register(ctx, func() { leaseReleased = true })
			releasedBeforeHandlerReturned = leaseReleased
			return nil
		},
		Outcome: delivery.Succeeded,
	})

	assert.True(t, releasedBeforeHandlerReturned,
		"installs no lease scope: leasescope.Register releases inline when none is present")
	assert.Equal(t, []string{lane.SettleOnSuccess, lane.SettleOnSuccess}, observed.Settles,
		"settles twice")
}

func TestBrokenLaneFailsEveryTelemetryAssertion(t *testing.T) {
	failures := runTelemetryAgainstBroken(t, Scenario{
		Name:    "failing_handler",
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: BrokenCarrierTraceID},
		Body:    []byte("payload"),
		Handle:  func(context.Context) error { return assert.AnError },
		Outcome: delivery.HandlerError,
	})

	require.NotEmpty(t, failures, "the telemetry family must fail against the broken lane")
	assert.Contains(t, failures, "the span is named <destination> receive on both lanes")
	assert.Contains(t, failures, "a consume span is Consumer-kind")
	assert.Contains(t, failures, "the consume span is a root")
	assert.Contains(t, failures, "span attribute messaging.system has the wrong value")
	assert.Contains(t, failures, "not-rabbitmq", "the failure names the value the lane actually set")
	// The broken lane reports 9999 bytes for a seven-byte body. Without this the
	// body-size comparison could be removed from the family entirely and every
	// assertion here would still pass: no other line mentions that attribute.
	assert.Contains(t, failures, "span attribute messaging.message.body.size has the wrong value")
	// The broken lane sets neither of these, so a correct assertAttr reports them
	// ABSENT. Matching only a wrong-value message would pass even if assertAttr
	// matched the wrong key and compared some other attribute's value.
	assert.Contains(t, failures, "span attribute messaging.operation.name is absent")
	assert.Contains(t, failures, "span attribute messaging.destination.name is absent")
	assert.Contains(t, failures, "carries the per-message trace ID")
	assert.Contains(t, failures, "which the lane did not declare in SpanExtraKeys")
	assert.Contains(t, failures, "the record counts the delivery once")
	assert.Contains(t, failures, "error.type is present exactly when the delivery failed")
	assert.Contains(t, failures, "the exemplar names the delivery's own span, not a sibling in the same trace")
	assert.Contains(t, failures, "the exemplar names the delivery's own trace")
}

// The broken lane's DECLARATION is deliberately valid: it is broken by what it
// emits, not by declaring something a family would reject up front. A lane that
// failed Validate would short-circuit every family before they asserted anything.
func TestBrokenLaneDeclarationItselfValidates(t *testing.T) {
	lane := BrokenLane()

	require.NoError(t, lane.Validate())
}
