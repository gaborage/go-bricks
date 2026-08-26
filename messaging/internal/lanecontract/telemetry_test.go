package lanecontract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	sdktrace "go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/gaborage/go-bricks/messaging/internal/delivery"
)

// typeOnlyErrorSpan is what a lane must produce for a FAILED delivery under
// ADR-083: one exception event carrying exception.type and nothing else.
func typeOnlyErrorSpan() sdktrace.SpanStub {
	return sdktrace.SpanStub{
		Name: "orders receive",
		Events: []tracesdk.Event{{
			Name:       "exception",
			Attributes: []attribute.KeyValue{attribute.String("exception.type", "*errors.errorString")},
		}},
		Status: tracesdk.Status{Code: codes.Error, Description: "*errors.errorString"},
	}
}

// cleanSpan is what a SUCCEEDED delivery must produce: no exception event.
func cleanSpan() sdktrace.SpanStub {
	return sdktrace.SpanStub{Name: "orders receive"}
}

// runSpanErrorAssertion reports what assertSpanErrorByType complained about,
// instead of failing this test.
func runSpanErrorAssertion(t *testing.T, outcome delivery.Outcome, span *sdktrace.SpanStub) string {
	t.Helper()

	var failures strings.Builder
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, stopped := recovered.(failNow); !stopped {
					panic(recovered)
				}
			}
		}()
		assertSpanErrorByType(&recordingT{out: &failures}, &Scenario{Outcome: outcome}, span)
	}()

	return failures.String()
}

// TestAssertSpanErrorByTypeAttributesTheOutcome pins that the family reads the
// scenario's outcome to decide which span it demands: an exception event is
// REQUIRED on a failure and FORBIDDEN on a success. Both directions are
// asserted, so inverting the outcome test reports a failure for a lane that is
// in fact correct — the mistake a one-directional test cannot see.
func TestAssertSpanErrorByTypeAttributesTheOutcome(t *testing.T) {
	tests := []struct {
		name        string
		outcome     delivery.Outcome
		span        sdktrace.SpanStub
		wantFailure string
	}{
		{
			name:    "failed_delivery_with_a_type_only_event_passes",
			outcome: delivery.HandlerError,
			span:    typeOnlyErrorSpan(),
		},
		{
			name:    "succeeded_delivery_without_an_event_passes",
			outcome: delivery.Succeeded,
			span:    cleanSpan(),
		},
		{
			name:        "failed_delivery_without_an_event_is_reported",
			outcome:     delivery.HandlerError,
			span:        cleanSpan(),
			wantFailure: "expected exactly one exception event",
		},
		{
			name:        "succeeded_delivery_with_an_event_is_reported",
			outcome:     delivery.Succeeded,
			span:        typeOnlyErrorSpan(),
			wantFailure: "span records an exception event it should not have",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := runSpanErrorAssertion(t, tt.outcome, &tt.span)

			if tt.wantFailure == "" {
				assert.Empty(t, got, "the family must accept a lane that behaves correctly")
				return
			}
			assert.Contains(t, got, tt.wantFailure)
		})
	}
}

// TestAssertSpanErrorByTypeRejectsAMessageBearingEvent pins the other half of
// ADR-083: an exception event that carries a second attribute — exception.message
// is the one that matters — is a failure, not a tolerated extra.
func TestAssertSpanErrorByTypeRejectsAMessageBearingEvent(t *testing.T) {
	span := typeOnlyErrorSpan()
	span.Events[0].Attributes = append(span.Events[0].Attributes,
		attribute.String("exception.message", "handler failed: "+HandlerErrorMarker))

	got := runSpanErrorAssertion(t, delivery.HandlerError, &span)

	assert.Contains(t, got, "the exception event carries exception.type and nothing else")
}
