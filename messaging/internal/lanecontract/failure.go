package lanecontract

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

// AssertFailure checks what a lane does with a delivery that went wrong: it
// settles exactly once, with the action its declaration names, and it settles
// even when the lane's own tail panicked.
//
// Counting settlements rather than flagging them is deliberate. A boolean cannot
// separate one settlement from two, and a double ack is the defect this
// guarantee exists to prevent — on a real broker it acknowledges a message the
// consumer never finished, and the second ack can acknowledge someone else's.
func AssertFailure(t T, lane *Lane, scenario *Scenario, observed *Observed) {
	t.Helper()

	want := lane.SettleOnSuccess
	if scenario.Outcome != delivery.Succeeded || scenario.PanicIn == PanicInLogOutcome {
		// A panic in the OUTCOME LINE makes the delivery Panicked whatever the
		// handler returned: the lane never saw a complete delivery, so it must
		// not acknowledge one. A panic inside Settle is different — the result
		// was already decided and handed over before Settle ran.
		want = lane.SettleOnFailure
	}

	assert.Equal(t, []string{want}, observed.Settles,
		"the delivery settles exactly once, and on %q", want)
}

// FailureScenarios are the deliveries that must still settle. S1-S3 are the
// three outcomes; S4 and S5 are panics in the lane's own closures, which is
// where an unsettled delivery would otherwise come from.
func FailureScenarios() []Scenario {
	return []Scenario{
		{
			Name:    "handler_returns_nil",
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
		},
		{
			Name:    "handler_returns_an_error",
			Handle:  func(context.Context) error { return errHandlerFailed },
			Outcome: delivery.HandlerError,
		},
		{
			Name:    "handler_panics",
			Handle:  func(context.Context) error { panic("boom") },
			Outcome: delivery.Panicked,
		},
		{
			Name:    "the_lanes_outcome_line_panics",
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
			PanicIn: PanicInLogOutcome,
		},
		{
			Name:    "the_lanes_settle_panics",
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
			PanicIn: PanicInSettle,
		},
	}
}

// RunFailure puts a lane through every failure scenario. The run itself is half
// the assertion: a lane that lets a tail panic escape fails here by taking the
// test down, which no message string could express.
func RunFailure(t *testing.T, lane *Lane) {
	t.Helper()

	for _, scenario := range FailureScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			var observed Observed
			assert.NotPanics(t, func() { observed = lane.Deliver(t, &scenario) },
				"a panic in the delivery tail must not escape into the consume loop")

			if scenario.PanicIn == PanicInSettle {
				// Settle panicked, so what it recorded before panicking is the
				// only evidence it ran; the pipeline must not have retried it.
				assert.Equal(t, []string{lane.SettleOnSuccess}, observed.Settles,
					"a panicking Settle is called once, not retried, and on the result it was handed")
				return
			}
			AssertFailure(t, lane, &scenario, &observed)
		})
	}
}
