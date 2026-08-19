package lanecontract

import (
	"context"
	"fmt"
	"testing"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// The broken lane's vocabulary. Its destination and settle words are
// deliberately real-looking: a family must fail it on what it DOES, never on an
// obviously fake string.
const (
	BrokenLaneName        = "broken"
	brokenDestination     = "broken.queue"
	brokenSettleOnSuccess = "ack"
	brokenSettleOnFailure = "nack-no-requeue"

	// What the lane DECLARES it emits. Each is contradicted by what it actually
	// emits, and each contradiction is one assertion's proof that it bites.
	brokenSuccessMsg = "Message processed successfully"
	brokenFailureMsg = "Message processing failed - discarding without requeue"
	brokenPanicMsg   = "Panic recovered in message handler - discarding without requeue"
	brokenOutcomeKey = "queue"
)

// BrokenLane returns a Lane whose delivery breaks every rule the contract
// enforces while its DECLARATION stays honest about what a conforming lane would
// do — each gap between the two is what an assertion catches, and the DEFECT
// markers below enumerate them.
//
// Every assertion family must FAIL against it. That is also the half of the proof
// that survives: once both lanes conform, the red run against an un-migrated lane
// can no longer be reproduced, but this lane stays broken forever.
func BrokenLane() Lane {
	return Lane{
		Name:        BrokenLaneName,
		Destination: brokenDestination,
		Deliver:     deliverBroken,
		OutcomeLines: map[delivery.Outcome]OutcomeLine{
			delivery.Succeeded: {
				Level: LevelInfo, Msg: brokenSuccessMsg,
				ExtraKeys: []string{brokenOutcomeKey},
			},
			delivery.HandlerError: {
				Level: LevelError, Msg: brokenFailureMsg,
				ExtraKeys: []string{brokenOutcomeKey},
			},
			delivery.Panicked: {
				Level: LevelError, Msg: brokenPanicMsg,
				ExtraKeys: []string{brokenOutcomeKey},
			},
		},
		SettleOnSuccess: brokenSettleOnSuccess,
		SettleOnFailure: brokenSettleOnFailure,
	}
}

// deliverBroken hand-rolls a non-conforming delivery. Each defect is marked so a
// later reader does not "fix" one and quietly turn the proof into a tautology.
func deliverBroken(t *testing.T, scenario Scenario) Observed {
	t.Helper()

	exporter, meter := SetupTelemetry(t)
	log := NewRecordingLogger()

	// DEFECT 1: the carrier is dropped — no trace extraction, so nothing that
	// traveled with the message reaches the context or the Result.
	// DEFECT 2: no lease scope, so a handle the handler borrows is released the
	// moment it registers. No span and no consume either: there is no pipeline.
	res, handlerTraceID := invokeBroken(context.Background(), scenario.Handle)
	// DEFECT 3: the Result hands back the root logger, not one bound to the
	// per-message context, so its BoundTo is nil.
	res.Log = log

	// DEFECT 4: none of the shared spine — no correlation_id, no processing_time,
	// and no panic or stack even when the handler panicked.
	// DEFECT 5: the failure text on a success too, so a success scenario cannot
	// find the line it declared. The panic text IS emitted, deliberately: an
	// unlocatable line short-circuits every later assertion, so the panic outcome
	// is what carries the family past location to the spine.
	// DEFECT 6: at WARN, where every outcome it declares says ERROR.
	// DEFECT 7: an undeclared key, and not the one it did declare.
	emitted := brokenFailureMsg
	if res.Outcome == delivery.Panicked {
		emitted = brokenPanicMsg
	}
	log.Warn().Str("exchange", brokenDestination).Msg(emitted)

	// DEFECT 8: settled twice. On a real broker that is a double ack.
	settles := []string{brokenSettleOnSuccess, brokenSettleOnSuccess}
	if res.Outcome != delivery.Succeeded {
		settles = []string{brokenSettleOnFailure, brokenSettleOnFailure}
	}

	return Observed{
		Result:         res,
		HandlerTraceID: handlerTraceID,
		Settles:        settles,
		Lines:          log.Lines(),
		Spans:          exporter.GetSpans(),
		Metrics:        meter.Collect(t),
	}
}

// invokeBroken runs the handler body, classifies the outcome, and reports what
// the handler saw of the context. It recovers a panic only because letting one
// escape would take the test process down.
func invokeBroken(ctx context.Context, handle func(context.Context) error) (res *delivery.Result, handlerTraceID string) {
	res = &delivery.Result{}

	defer func() {
		if recovered := recover(); recovered != nil {
			res.Outcome = delivery.Panicked
			res.Err = fmt.Errorf("panic in message handler: %v", recovered)
			// Panic and Stack are deliberately left unset — DEFECT 4.
		}
	}()

	handlerTraceID, _ = gobrickstrace.IDFromContext(ctx)

	if err := handle(ctx); err != nil {
		res.Outcome = delivery.HandlerError
		res.Err = err
	}
	return res, handlerTraceID
}
