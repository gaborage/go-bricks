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

const brokenCarrierTraceID = "req-broken"

// runIdentityAgainstBroken drives the broken lane through one scenario and
// returns every assertion message AssertIdentity produced, without failing this
// test.
func runIdentityAgainstBroken(t *testing.T, scenario Scenario) string {
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
		AssertIdentity(&recordingT{out: &failures}, &lane, &scenario, &observed)
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
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: brokenCarrierTraceID},
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
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: brokenCarrierTraceID},
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
		Carrier: map[string]any{gobrickstrace.HeaderXRequestID: brokenCarrierTraceID},
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
	assert.Empty(t, observed.Spans, "opens no span")
	assert.Empty(t, observed.Metrics.ScopeMetrics, "records no consume")
}

// The broken lane's DECLARATION is deliberately valid: it is broken by what it
// emits, not by declaring something a family would reject up front. A lane that
// failed Validate would short-circuit every family before they asserted anything.
func TestBrokenLaneDeclarationItselfValidates(t *testing.T) {
	lane := BrokenLane()

	require.NoError(t, lane.Validate())
}
