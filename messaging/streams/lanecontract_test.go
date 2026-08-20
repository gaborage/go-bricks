package streams

import (
	"context"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/amqp"

	"github.com/gaborage/go-bricks/logger"
	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	"github.com/gaborage/go-bricks/messaging/internal/lanecontract"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// This file is in package streams on purpose: deliver and consumerRunner are
// unexported, so driving the REAL lane is only possible from inside it.

const laneStream = "orders-stream"

// streamsLane describes this lane to the harness. It logs NOTHING on success —
// a stream is a high-throughput ordered log consumed inline — so Succeeded is
// absent from OutcomeLines, and the family asserts that negative rather than
// looking for a line.
func streamsLane() lanecontract.Lane {
	extras := []string{logFieldStream, logFieldConsumer, logFieldOffset}
	return lanecontract.Lane{
		Name:        "streams",
		Destination: laneStream,
		Deliver:     deliverStreams,
		SpanExtraKeys: []string{
			attrConsumerName,
			attrStreamOffset,
		},
		OutcomeLines: map[delivery.Outcome]lanecontract.OutcomeLine{
			delivery.HandlerError: {
				Level: lanecontract.LevelError,
				Msg:   "Stream message handling failed - offset not committed",
				// The lane chains .Err(res.Err), which the recorder files as "error".
				ExtraKeys: append(append([]string{}, extras...), "error"),
			},
			delivery.Panicked: {
				Level:     lanecontract.LevelError,
				Msg:       "Panic recovered in stream handler - offset not committed",
				ExtraKeys: extras,
			},
		},
		SettleOnSuccess: "commit",
		SettleOnFailure: "skip",
	}
}

// settleWatcher records what the offset tracker actually did, in the harness's
// vocabulary: a stored offset is a commit, and no store is a skip.
type settleWatcher struct{ stored []int64 }

func (s *settleWatcher) StoreCustomOffset(offset int64) error {
	s.stored = append(s.stored, offset)
	return nil
}

func deliverStreams(t *testing.T, scenario *lanecontract.Scenario) lanecontract.Observed {
	t.Helper()

	exporter, meter := lanecontract.SetupTelemetry(t)
	log := lanecontract.NewRecordingLogger()

	var handlerTraceID string
	handler := func(ctx context.Context, _ *Message) error {
		handlerTraceID = traceIDOf(ctx)
		return scenario.Handle(ctx)
	}

	deliverLog := loggerFor(log, scenario)
	watcher := &settleWatcher{}
	// A tracker that commits on every message, so one delivery produces one
	// settlement rather than waiting for a batch to fill.
	runner := &consumerRunner{
		name:    "orders-worker",
		handler: handler,
		offsets: bookOf(newOffsetTracker(1, time.Hour, newFakeClock().Now)),
		log:     deliverLog,
		baseCtx: context.Background(),
	}

	message := &amqp.Message{
		Data:                  [][]byte{scenario.Body},
		ApplicationProperties: scenario.Carrier,
	}

	runner.deliver(laneStream, 55, message, watcher)

	settles := make([]string, 0, len(watcher.stored))
	for range watcher.stored {
		settles = append(settles, "commit")
	}
	if scenario.Outcome != delivery.Succeeded && len(settles) == 0 {
		settles = append(settles, "skip")
	}

	return lanecontract.Observed{
		HandlerTraceID: handlerTraceID,
		Settles:        settles,
		Lines:          log.Lines(),
		Spans:          exporter.GetSpans(),
		Metrics:        meter.Collect(t),
	}
}

// traceIDOf reads what the handler saw of the per-message context — the only
// place that context is observable, and read independently of anything logged.
func traceIDOf(ctx context.Context) string {
	id, _ := gobrickstrace.IDFromContext(ctx)
	return id
}

// loggerFor wires a scenario's panic injection into this lane's outcome line,
// which it writes through Error().
func loggerFor(log *lanecontract.RecordingLogger, scenario *lanecontract.Scenario) logger.Logger {
	if scenario.PanicIn == lanecontract.PanicInLogOutcome {
		return &panicOnOutcomeLogger{RecordingLogger: log}
	}
	return log
}

// panicOnOutcomeLogger panics from the level this lane's outcome line uses.
// WithContext is overridden too: the pipeline logs through the RESULT of that
// call, so returning the embedded recorder would never fire the panic.
type panicOnOutcomeLogger struct{ *lanecontract.RecordingLogger }

func (l *panicOnOutcomeLogger) WithContext(ctx any) logger.Logger {
	bound, ok := l.RecordingLogger.WithContext(ctx).(*lanecontract.RecordingLogger)
	if !ok {
		return l
	}
	return &panicOnOutcomeLogger{RecordingLogger: bound}
}

func (l *panicOnOutcomeLogger) Error() logger.LogEvent { panic("the lane's outcome line blew up") }

func TestStreamsLaneSatisfiesTheIdentityContract(t *testing.T) {
	lane := streamsLane()

	lanecontract.RunIdentity(t, &lane)
}

func TestStreamsLaneSatisfiesTheTelemetryContract(t *testing.T) {
	lane := streamsLane()

	lanecontract.RunTelemetry(t, &lane)
}

func TestStreamsLaneSatisfiesTheFailureContract(t *testing.T) {
	lane := streamsLane()

	lanecontract.RunFailure(t, &lane)
}
