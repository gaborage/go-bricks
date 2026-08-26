package lanecontract

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
	gobrickstrace "github.com/gaborage/go-bricks/trace"
)

// T is require.TestingT plus Helper. It is an interface so the harness can prove
// a family BITES by running it against a recorder: a family reports by calling
// Errorf, so the live *testing.T would fail the suite doing the proving.
type T interface {
	require.TestingT
	Helper()
}

// The spine delivery.AppendOutcome stamps on every lane's outcome line. A lane
// declares only what it adds beyond these.
const (
	fieldCorrelationID  = "correlation_id"
	fieldProcessingTime = "processing_time"
	fieldPanicType      = "panic_type"
	fieldStack          = "stack"
)

// spineKeys is the whole spine, in one place: extraKeys strips it, and the
// no-declared-line branch asserts a lane emits none of it.
var spineKeys = []string{fieldCorrelationID, fieldProcessingTime, fieldPanicType, fieldStack}

// errHandlerFailed is the handler error the scenarios raise. Its message embeds
// HandlerErrorMarker so a family can prove the message reaches no span sink.
var errHandlerFailed = errors.New("handler failed: " + HandlerErrorMarker)

// AssertIdentity checks the trace-identity contract for one delivery: the id that
// traveled on the carrier is the id the handler reads and the id the lane logs,
// on an outcome line whose shape matches what the lane declared.
func AssertIdentity(t T, lane *Lane, scenario *Scenario, observed *Observed) {
	t.Helper()

	require.NoError(t, lane.Validate(), "a lane whose declaration is unusable cannot be asserted against")

	handlerID := observed.HandlerTraceID
	assert.NotEmpty(t, handlerID,
		"trace.IDFromContext must return an id inside the handler; the pipeline writes one back even when nothing traveled")

	if carried, declared := carriedTraceID(scenario.Carrier); declared {
		assert.Equal(t, carried, handlerID,
			"the id that traveled on the carrier must be the id the handler reads")
	}

	line := assertOutcomeLine(t, lane, scenario, observed)
	if line == nil {
		return
	}

	assert.Equal(t, []string{handlerID}, line.Values(fieldCorrelationID),
		"the outcome line must carry the handler's id under correlation_id, exactly once")
	assertSpine(t, scenario.Outcome, line)
}

// assertOutcomeLine finds the line this lane declared for the scenario's outcome
// and checks its level and its exact extra-key set. Returns nil when there is no
// line to assert against, so the caller stops rather than reading a zero value.
func assertOutcomeLine(t T, lane *Lane, scenario *Scenario, observed *Observed) *LogLine {
	t.Helper()

	declared, ok := lane.OutcomeLines[scenario.Outcome]
	if !ok {
		// The lane declares no line for this outcome — the streams lane logs
		// nothing on success. Assert the negative its declaration promises, or a
		// lane exempts itself from every log assertion by declaring nothing.
		for i := range observed.Lines {
			for _, key := range spineKeys {
				assert.Empty(t, observed.Lines[i].Values(key),
					"the lane declares no outcome line here, so no line may carry the spine field %q", key)
			}
		}
		return nil
	}

	var found *LogLine
	for i := range observed.Lines {
		if observed.Lines[i].Msg == declared.Msg {
			require.Nil(t, found, "message %q was emitted more than once, so it cannot locate a line", declared.Msg)
			found = &observed.Lines[i]
		}
	}
	if !assert.NotNil(t, found, "no line carries the declared message %q", declared.Msg) {
		return nil
	}

	assert.Equal(t, declared.Level, found.Level, "the outcome line's level must be the one the lane declared")
	assert.ElementsMatch(t, declared.ExtraKeys, extraKeys(*found),
		"the outcome line must carry EXACTLY the extra keys the lane declared: a key it stopped emitting is drift as much as one it added")

	return found
}

// assertSpine checks the fields the pipeline stamps rather than the lane. Without
// it the exact-set check above is exact only OUTSIDE the spine, so dropping
// processing_time — or stamping a panic on a success — would pass.
func assertSpine(t T, outcome delivery.Outcome, line *LogLine) {
	t.Helper()

	assert.NotEmpty(t, line.Values(fieldProcessingTime),
		"the shared spine stamps processing_time on every outcome line")

	if outcome == delivery.Panicked {
		assert.NotEmpty(t, line.Values(fieldPanicType),
			"a panicked delivery must log the recovered value's TYPE — never the value (ADR-081)")
		assert.NotEmpty(t, line.Values(fieldStack), "a panicked delivery must log its stack")
		return
	}
	assert.Empty(t, line.Values(fieldPanicType), "panic_type belongs to the Panicked outcome only")
	assert.Empty(t, line.Values(fieldStack), "stack belongs to the Panicked outcome only")
}

// extraKeys is the line's keys with the shared spine removed, so what is left is
// what the lane itself contributed.
func extraKeys(line LogLine) []string {
	out := make([]string, 0, len(line.Fields))
	for _, field := range line.Fields {
		if !slices.Contains(spineKeys, field[0]) {
			out = append(out, field[0])
		}
	}
	return out
}

// carriedTraceID derives, independently of the pipeline, the id a carrier should
// yield. declared reports whether the carrier names an id at all, so a carrier
// the oracle cannot read fails rather than skipping the comparison it exists for.
// Deliberately not trace.ExtractFromHeaders: asserting the pipeline against
// itself would pass however wrong that function became.
func carriedTraceID(carrier map[string]any) (id string, declared bool) {
	if raw, ok := carrier[gobrickstrace.HeaderXRequestID]; ok {
		return headerText(raw), true
	}
	if raw, ok := carrier[gobrickstrace.HeaderTraceParent]; ok {
		return traceIDFromParent(headerText(raw)), true
	}
	return "", false
}

// headerText mirrors the production accessor: AMQP tables carry []byte in the
// wild, and an oracle accepting only string would skip the comparison.
func headerText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

// traceIDFromParent takes the trace-id field of a W3C traceparent:
// version-traceid-spanid-flags, where the trace-id is 32 hex characters.
func traceIDFromParent(parent string) string {
	if parts := strings.Split(parent, "-"); len(parts) >= 4 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}

// IdentityScenarios is the set of deliveries every lane is put through. It lives
// here rather than in an adapter because scenarios are not a Lane field: two
// lanes facing different scenarios is drift the contract could not see.
func IdentityScenarios() []Scenario {
	const carriedID = "req-2026"
	const carriedParent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	withID := func() map[string]any {
		return map[string]any{gobrickstrace.HeaderXRequestID: carriedID}
	}

	return []Scenario{
		{
			Name:    "handler_returns_nil",
			Carrier: withID(),
			Body:    []byte(`{"id":1}`),
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
		},
		{
			Name:    "handler_returns_an_error",
			Carrier: withID(),
			Handle:  func(context.Context) error { return errHandlerFailed },
			Outcome: delivery.HandlerError,
		},
		{
			Name:    "handler_panics",
			Carrier: withID(),
			Handle:  func(context.Context) error { panic("boom") },
			Outcome: delivery.Panicked,
		},
		{
			// The id must come from the traceparent's trace-id field rather than a
			// freshly minted UUID.
			Name:    "carrier_carries_traceparent_only",
			Carrier: map[string]any{gobrickstrace.HeaderTraceParent: carriedParent},
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
		},
		{
			// AMQP tables carry []byte, so a lane must read one as readily as a string.
			Name:    "carrier_carries_x_request_id_as_bytes",
			Carrier: map[string]any{gobrickstrace.HeaderXRequestID: []byte(carriedID)},
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
		},
		{
			// Nothing traveled: the pipeline mints an id and must write it back, or
			// the handler reads none.
			Name:    "carrier_is_nil",
			Handle:  func(context.Context) error { return nil },
			Outcome: delivery.Succeeded,
		},
	}
}

// RunIdentity puts a lane through every identity scenario. A lane's own test
// calls this and nothing else, so both lanes provably face the same set.
func RunIdentity(t *testing.T, lane *Lane) {
	t.Helper()

	for _, scenario := range IdentityScenarios() {
		t.Run(scenario.Name, func(t *testing.T) {
			observed := lane.Deliver(t, &scenario)

			AssertIdentity(t, lane, &scenario, &observed)
		})
	}
}
