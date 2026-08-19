package messaging

import (
	"context"
	"slices"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/lanecontract"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// This file is in package messaging on purpose: processMessage is unexported, so
// driving the REAL lane is only possible from inside it.

const (
	laneQueue    = "orders"
	laneExchange = "events"
)

// classicLane describes the AMQP lane to the harness. The declared shapes are
// read off logOutcome and buildFailureLogEvent: the success line adds message_id
// alone, the failure line adds seven fields plus "error" from the chained
// .Err(res.Err), and the panic line is the same seven without it.
func classicLane() lanecontract.Lane {
	failureKeys := []string{
		"message_id", "queue", "event_type", "amqp_correlation_id",
		"consumer_tag", "routing_key", "exchange",
	}
	return lanecontract.Lane{
		Name:        "classic",
		Destination: laneQueue,
		Deliver:     deliverClassic,
		OutcomeLines: map[delivery.Outcome]lanecontract.OutcomeLine{
			delivery.Succeeded: {
				Level: lanecontract.LevelInfo, Msg: "Message processed successfully",
				ExtraKeys: []string{"message_id"},
			},
			delivery.HandlerError: {
				Level: lanecontract.LevelError, Msg: "Message processing failed - discarding without requeue",
				ExtraKeys: slices.Concat(failureKeys, []string{"error"}),
			},
			delivery.Panicked: {
				Level: lanecontract.LevelError, Msg: "Panic recovered in message handler - discarding without requeue",
				ExtraKeys: failureKeys,
			},
		},
		SettleOnSuccess: "ack",
		SettleOnFailure: "nack-no-requeue",
	}
}

// laneHandler adapts the harness's lane-agnostic handler body to this lane's
// two-method MessageHandler interface, and records what the handler saw of the
// per-message context — the only place that context is observable.
type laneHandler struct {
	body    func(context.Context) error
	traceID string
}

func (h *laneHandler) Handle(ctx context.Context, _ *amqp.Delivery) error {
	h.traceID, _ = gobrickstrace.IDFromContext(ctx)
	return h.body(ctx)
}

func (h *laneHandler) EventType() string { return "orders.created" }

// deliverClassic drives the real processMessage. It does not rebuild the
// pipeline request the lane builds, because a copy here would drift from the
// lane silently — which is the failure this harness exists to catch.
//
// Observed.Result is left nil — see that field's doc for why the classic lane
// cannot fill it yet.
func deliverClassic(t *testing.T, scenario lanecontract.Scenario) lanecontract.Observed {
	t.Helper()

	exporter, meter := lanecontract.SetupTelemetry(t)
	log := lanecontract.NewRecordingLogger()

	handler := &laneHandler{body: scenario.Handle}
	consumer := &ConsumerDeclaration{
		Queue:     laneQueue,
		EventType: "orders.created",
		Handler:   handler,
	}
	acker := &settleRecorder{}
	msg := &amqp.Delivery{
		MessageId:     "msg-1",
		CorrelationId: "amqp-corr-1",
		RoutingKey:    "orders.created",
		Exchange:      laneExchange,
		DeliveryTag:   1,
		Body:          scenario.Body,
		Headers:       amqp.Table(scenario.Carrier),
		Acknowledger:  acker,
	}

	registry := NewRegistry(&simpleMockAMQPClient{}, &stubLogger{})
	registry.processMessage(context.Background(), consumer, msg, log)

	return lanecontract.Observed{
		HandlerTraceID: handler.traceID,
		Settles:        acker.settles,
		Lines:          log.Lines(),
		Spans:          exporter.GetSpans(),
		Metrics:        meter.Collect(t),
	}
}

// settleRecorder records settlements in the harness's vocabulary, so "settled
// exactly once" is observable rather than inferred from a pair of booleans.
type settleRecorder struct{ settles []string }

func (s *settleRecorder) Ack(uint64, bool) error {
	s.settles = append(s.settles, "ack")
	return nil
}

func (s *settleRecorder) Nack(_ uint64, _, requeue bool) error {
	action := "nack-no-requeue"
	if requeue {
		action = "nack-requeue"
	}
	s.settles = append(s.settles, action)
	return nil
}

func (s *settleRecorder) Reject(uint64, bool) error {
	s.settles = append(s.settles, "reject")
	return nil
}

func TestClassicLaneSatisfiesTheIdentityContract(t *testing.T) {
	lane := classicLane()

	lanecontract.RunIdentity(t, &lane)
}

// Each delivery mints its own id, and a handler reading twice within one gets
// the same one — EnsureTraceID mints without writing back, so without the
// pipeline's write-back neither would hold.
func TestClassicLaneMintsAFreshStableTraceIDPerDelivery(t *testing.T) {
	lane := classicLane()
	var secondRead string

	first := lane.Deliver(t, lanecontract.Scenario{
		Name: "nothing_traveled",
		Handle: func(ctx context.Context) error {
			secondRead, _ = gobrickstrace.IDFromContext(ctx)
			return nil
		},
		Outcome: delivery.Succeeded,
	})
	second := lane.Deliver(t, lanecontract.Scenario{
		Name:    "nothing_traveled_again",
		Handle:  func(context.Context) error { return nil },
		Outcome: delivery.Succeeded,
	})

	assert.Equal(t, first.HandlerTraceID, secondRead, "two reads in one delivery must agree")
	assert.NotEqual(t, first.HandlerTraceID, second.HandlerTraceID, "each delivery mints its own")
}
