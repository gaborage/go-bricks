package messaging

import (
	"context"
	"slices"
	"testing"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
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
// alone, the failure line adds eight fields plus "error" from the chained
// .Err(res.Err), and the panic line is the same eight without it.
//
// The harness drives valid identifiers, so the four vouched fields are all
// present and identity_rejected is absent — the omission shapes are pinned in
// delivery_identity_test.go instead, since this contract asserts an EXACT key set
// and cannot express "present only sometimes".
func classicLane() lanecontract.Lane {
	failureKeys := []string{
		"message_id", "queue", "event_type", "amqp_correlation_id",
		"consumer_tag", "routing_key", "exchange", "delivery_tag",
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
		// consumeSpanExtras adds these four when the delivery carries them.
		SpanExtraKeys: []string{
			"messaging.rabbitmq.exchange",
			"messaging.rabbitmq.destination.routing_key",
			"messaging.message.id",
			"messaging.message.conversation_id",
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
func deliverClassic(t *testing.T, scenario *lanecontract.Scenario) lanecontract.Observed {
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
	// A scenario can make one of the lane's own closures panic, which is how the
	// failure family proves a tail panic is contained rather than escaping.
	deliverLog := logger.Logger(log)
	if scenario.PanicIn == lanecontract.PanicInLogOutcome {
		deliverLog = &panicOnOutcomeLogger{RecordingLogger: log}
	}
	if scenario.PanicIn == lanecontract.PanicInSettle {
		acker.panicOnSettle = true
	}
	registry.processMessage(context.Background(), consumer, msg, deliverLog)

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
type settleRecorder struct {
	settles       []string
	panicOnSettle bool
}

func (s *settleRecorder) Ack(uint64, bool) error {
	s.settles = append(s.settles, "ack")
	s.maybePanic()
	return nil
}

func (s *settleRecorder) Nack(_ uint64, _, requeue bool) error {
	action := "nack-no-requeue"
	if requeue {
		action = "nack-requeue"
	}
	s.settles = append(s.settles, action)
	s.maybePanic()
	return nil
}

// maybePanic records the settlement first, so a scenario proving "called once
// and not retried" still has the evidence it needs.
func (s *settleRecorder) maybePanic() {
	if s.panicOnSettle {
		panic("the lane's settle blew up")
	}
}

// panicOnOutcomeLogger panics from whichever level the lane's outcome line uses
// — Info on a success, Error on a failure — which is the realistic shape: the
// lane's own logging is what fails.
//
// WithContext must be overridden too. The pipeline binds the logger to the
// per-message context and logs through the RESULT of that call, so returning the
// embedded recorder would hand the lane an unwrapped logger and the panic would
// never fire.
type panicOnOutcomeLogger struct{ *lanecontract.RecordingLogger }

func (l *panicOnOutcomeLogger) WithContext(ctx any) logger.Logger {
	bound, ok := l.RecordingLogger.WithContext(ctx).(*lanecontract.RecordingLogger)
	if !ok {
		return l
	}
	return &panicOnOutcomeLogger{RecordingLogger: bound}
}

func (l *panicOnOutcomeLogger) Info() logger.LogEvent  { panic("the lane's outcome line blew up") }
func (l *panicOnOutcomeLogger) Error() logger.LogEvent { panic("the lane's outcome line blew up") }

func (s *settleRecorder) Reject(uint64, bool) error {
	s.settles = append(s.settles, "reject")
	return nil
}

func TestClassicLaneSatisfiesTheIdentityContract(t *testing.T) {
	lane := classicLane()

	lanecontract.RunIdentity(t, &lane)
}

func TestClassicLaneSatisfiesTheTelemetryContract(t *testing.T) {
	lane := classicLane()

	lanecontract.RunTelemetry(t, &lane)
}

// Each delivery mints its own id, and a handler reading twice within one gets
// the same one — EnsureTraceID mints without writing back, so without the
// pipeline's write-back neither would hold.
func TestClassicLaneMintsAFreshStableTraceIDPerDelivery(t *testing.T) {
	lane := classicLane()
	var secondRead string

	first := lane.Deliver(t, &lanecontract.Scenario{
		Name: "nothing_traveled",
		Handle: func(ctx context.Context) error {
			secondRead, _ = gobrickstrace.IDFromContext(ctx)
			return nil
		},
		Outcome: delivery.Succeeded,
	})
	second := lane.Deliver(t, &lanecontract.Scenario{
		Name:    "nothing_traveled_again",
		Handle:  func(context.Context) error { return nil },
		Outcome: delivery.Succeeded,
	})

	// Assert the precondition both comparisons rest on. If the pipeline stopped
	// planting an id, first.HandlerTraceID and secondRead would both be "" and
	// the Equal below would pass while asserting nothing.
	require.NotEmpty(t, first.HandlerTraceID, "the pipeline plants an id the handler can read")
	assert.Equal(t, first.HandlerTraceID, secondRead, "two reads in one delivery must agree")
	// The sibling half: NotEqual also passes when the SECOND delivery mints
	// nothing, so "each delivery mints its own" would hold vacuously the moment
	// the second stopped planting an id at all.
	require.NotEmpty(t, second.HandlerTraceID, "the second delivery plants an id too")
	assert.NotEqual(t, first.HandlerTraceID, second.HandlerTraceID, "each delivery mints its own")
}

func TestClassicLaneSatisfiesTheFailureContract(t *testing.T) {
	lane := classicLane()

	lanecontract.RunFailure(t, &lane)
}
