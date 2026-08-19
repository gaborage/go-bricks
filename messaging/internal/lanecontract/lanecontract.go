// Package lanecontract holds the contract every messaging lane driving the
// delivery pipeline must satisfy, and the fixture that drives it.
//
// It must import NEITHER messaging NOR messaging/streams: both lanes' tests are
// in-package, so they import this one, and either import here is a hard cycle.
// Everything lane-specific arrives through Lane's func fields.
package lanecontract

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

// Lane is one lane's self-description and the complete list of what two lanes
// may legitimately differ on: a difference with no field here is drift, and a
// field added here declares a new divergence that must justify itself. Fields
// keyed by delivery.Outcome say the divergence is per-outcome, which is stricter
// than one list across all outcomes, not looser.
//
// AutoAck is deliberately absent — it is classic-only, and a permanently-false
// field for one lane is the creep this struct exists to prevent.
type Lane struct {
	Name string

	// Destination is the queue name on the classic lane, the stream name on the
	// streams lane — the span name's prefix either way.
	Destination string

	// Deliver puts one Scenario through this lane's real delivery path, wrapping
	// the Scenario's handler body in this lane's own handler kind. It must not
	// return until the delivery is complete and every write it made is visible to
	// the caller: the streams lane delivers from one goroutine per partition, so
	// without that edge a family reading Observed races the delivery.
	//
	// It must not call t.Run — the family owns the subtest, so the telemetry
	// fixture's provider restore is scoped to the family's test, not a nested one.
	Deliver func(*testing.T, *Scenario) Observed

	// SpanExtraKeys are the span attribute keys this lane may add on top of the
	// four both lanes share.
	SpanExtraKeys []string

	// OutcomeLines is what this lane's outcome line looks like, per outcome. An
	// outcome absent from the map expects no outcome line at all — the streams
	// lane logs nothing on success — and a family asserts that negative rather
	// than looking for a line. One map, one meaning: there is no second field
	// whose absence means something else.
	OutcomeLines map[delivery.Outcome]OutcomeLine

	// SettleOnSuccess and SettleOnFailure name what settling does here:
	// "ack"/"nack-no-requeue" on the classic lane, "commit"/"skip" on streams.
	SettleOnSuccess string
	SettleOnFailure string
}

// Scenario is one delivery to put through a lane, described without naming
// either lane's message type. Handle is lane-agnostic because the two handler
// kinds differ — a two-method interface versus a bare func type — so each
// adapter wraps this rather than the harness building either.
type Scenario struct {
	Name string

	// Carrier is what traveled with the message; nil is the nothing-traveled
	// case, which every lane must also survive.
	Carrier map[string]any

	// Body is the payload, so a family can assert the shared
	// messaging.message.body.size attribute by value rather than by presence.
	Body []byte

	// Handle returns nil to succeed, an error to fail, or panics.
	Handle func(ctx context.Context) error

	// PanicIn names a LANE-owned closure that must panic for this scenario, so a
	// family can prove a panic in the delivery tail is contained rather than
	// escaping into the consume loop. Empty means neither panics.
	PanicIn string

	// Outcome is what Handle produces. The scenario declares it because it owns
	// the handler body; a family uses it to pick the OutcomeLine the lane
	// declared, and the failure family — not this one — is what checks the lane
	// actually reported it.
	Outcome delivery.Outcome
}

// Observed is everything a lane's delivery produced, in the form the assertion
// families read it.
type Observed struct {
	// Result is captured through the pipeline's LogOutcome hook, so no production
	// signature gains a test-only parameter. A lane driving its real settle path
	// may leave it nil until that path can hand the result back — the classic
	// lane builds its hook inside processMessage, where a test cannot reach it.
	Result *delivery.Result

	// HandlerTraceID is trace.IDFromContext read INSIDE the handler. It is the
	// only place the per-message context is observable, and it is read
	// independently of anything the lane logs, so the two can be compared.
	HandlerTraceID string

	// Settles records each settlement in order, in Lane.SettleOnSuccess's
	// vocabulary. More than one entry means the lane settled twice.
	Settles []string

	Lines   []LogLine
	Spans   tracetest.SpanStubs
	Metrics metricdata.ResourceMetrics
}

// OutcomeLine is the shape of one lane's outcome line for one outcome: the
// level it logs at, the exact text it logs, and the fields it adds beyond the
// spine delivery.AppendOutcome stamps.
type OutcomeLine struct {
	// Level is one of the Level constants below. It is declared rather than
	// inferred because the lanes genuinely differ here, and a level the harness
	// cannot compare against is a divergence it cannot catch.
	Level string

	// Msg is the exact message text. It is also the locator: a family finds this
	// line among the lane's others by its text, since the streams lane emits a
	// store-failure WARN and a panic line alongside its outcome line, and
	// selecting by "the line carrying correlation_id" cannot tell a spine-less
	// outcome line from a missing one.
	Msg string

	// ExtraKeys is the EXACT set of fields this line carries beyond the shared
	// spine — not a permission list. A lane that quietly stops emitting a
	// declared key has drifted exactly as much as one that adds an undeclared
	// key, and only an exact-set comparison catches both. Per-outcome keying is
	// what makes it viable: the classic lane's success line adds one field where
	// its failure line adds eight.
	ExtraKeys []string
}

// The levels a recorded line can carry. The two lanes differ here today — the
// classic lane logs a successful delivery at Info, the streams lane logs no
// success line at all — so a level-blind harness would hide exactly the kind of
// divergence it exists to catch.
const (
	LevelInfo  = "info"
	LevelError = "error"
	LevelDebug = "debug"
	LevelWarn  = "warn"
	LevelFatal = "fatal"
)

// The lane-owned closures a scenario can make panic.
const (
	PanicInLogOutcome = "log-outcome"
	PanicInSettle     = "settle"
)

// LogLine is one emitted log line: its level, its message, and every field write
// in emission order. Duplicate keys are preserved — a lane stamping one key twice
// is exactly the drift this harness catches.
type LogLine struct {
	Level  string
	Msg    string
	Fields [][2]string
}

// Values returns every value written under key, in order; nil when absent.
func (l LogLine) Values(key string) []string {
	var out []string
	for _, field := range l.Fields {
		if field[0] == key {
			out = append(out, field[1])
		}
	}
	return out
}

// outcomes is every delivery outcome in a fixed order, so Validate reports
// deterministically instead of in map order.
var outcomes = []delivery.Outcome{delivery.Succeeded, delivery.HandlerError, delivery.Panicked}

// outcomeName renders an outcome for an error message; delivery.Outcome is a
// bare int, and "Panicked" beats "2" in a failure a human has to read.
func outcomeName(outcome delivery.Outcome) string {
	switch outcome {
	case delivery.Succeeded:
		return "Succeeded"
	case delivery.HandlerError:
		return "HandlerError"
	case delivery.Panicked:
		return "Panicked"
	default:
		return fmt.Sprintf("Outcome(%d)", int(outcome))
	}
}

// isLevel reports whether level is one of the levels the recorder stamps.
func isLevel(level string) bool {
	switch level {
	case LevelInfo, LevelError, LevelDebug, LevelWarn, LevelFatal:
		return true
	default:
		return false
	}
}

// Validate reports every way this lane's declaration is unusable, joined, so a
// lane author fixes them in one pass. The receiver is a pointer because Lane is
// wide enough that copying it per call is wasteful, not because it mutates. A family calls it before asserting
// anything: a declaration the harness cannot act on would otherwise pass every
// assertion silently, which is the rubber stamp this package exists to prevent.
func (l *Lane) Validate() error {
	var problems []error
	declaredBy := make(map[string]delivery.Outcome, len(l.OutcomeLines))

	for _, outcome := range outcomes {
		line, declared := l.OutcomeLines[outcome]
		if !declared {
			continue
		}

		if !isLevel(line.Level) {
			problems = append(problems, fmt.Errorf(
				"lane %q outcome %s: level %q is not a level the recorder stamps",
				l.Name, outcomeName(outcome), line.Level))
		}

		if line.Msg == "" {
			problems = append(problems, fmt.Errorf(
				"lane %q outcome %s: declares %d extra keys but no message, so a family cannot locate its line",
				l.Name, outcomeName(outcome), len(line.ExtraKeys)))
			continue
		}

		if first, duplicate := declaredBy[line.Msg]; duplicate {
			problems = append(problems, fmt.Errorf(
				"lane %q outcome %s: message %q is already declared by %s, so a family cannot tell the two lines apart",
				l.Name, outcomeName(outcome), line.Msg, outcomeName(first)))
			continue
		}
		declaredBy[line.Msg] = outcome
	}

	return errors.Join(problems...)
}
