package observability_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/gaborage/go-bricks/observability"
	obtest "github.com/gaborage/go-bricks/observability/testing"
)

type spanProbeError struct{ msg string }

func (e *spanProbeError) Error() string { return e.msg }

// recordSpanError runs RecordErrorByType against a real SDK span and returns the
// exported stub.
func recordSpanError(t *testing.T, err error) tracetest.SpanStub {
	t.Helper()

	tp := obtest.NewTestTraceProvider()
	t.Cleanup(func() { require.NoError(t, tp.Shutdown(context.Background())) })

	_, span := tp.TestTracer().Start(context.Background(), "op")
	observability.RecordErrorByType(span, err)
	span.End()

	spans := tp.Exporter.GetSpans()
	require.Len(t, spans, 1)
	return spans[0]
}

func TestRecordErrorByTypeNilErrorRecordsNothing(t *testing.T) {
	stub := recordSpanError(t, nil)

	assert.Empty(t, stub.Events)
	assert.Equal(t, codes.Unset, stub.Status.Code)
	assert.Empty(t, stub.Status.Description)
}

func TestRecordErrorByTypeNilSpanIsNoOp(t *testing.T) {
	assert.NotPanics(t, func() {
		observability.RecordErrorByType(nil, errors.New("boom"))
	})
	assert.NotPanics(t, func() {
		observability.RecordErrorByType(nil, nil)
	})
}

// TestRecordErrorByTypeNonRecordingSpanIsNoOp covers the IsRecording guard: a
// sampled-out or tracing-disabled span records nothing, and the helper does not
// pay to render a type that no exporter will ever see.
func TestRecordErrorByTypeNonRecordingSpanIsNoOp(t *testing.T) {
	_, span := tracenoop.NewTracerProvider().Tracer("noop").Start(context.Background(), "op")
	defer span.End()
	require.False(t, span.IsRecording())

	assert.NotPanics(t, func() {
		observability.RecordErrorByType(span, errors.New(obtest.LeakCanary))
	})
}

func TestRecordErrorByTypeRecordsOuterTypeOnly(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantType string
	}{
		{
			name:     "plain_error",
			err:      errors.New(obtest.LeakCanary),
			wantType: "*errors.errorString",
		},
		{
			name:     "custom_error_type",
			err:      &spanProbeError{msg: obtest.LeakCanary},
			wantType: "*observability_test.spanProbeError",
		},
		{
			name:     "wrapped_error_renders_outer_type",
			err:      fmt.Errorf("wrapped: %w", &spanProbeError{msg: obtest.LeakCanary}),
			wantType: "*fmt.wrapError",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := recordSpanError(t, tt.err)

			obtest.AssertExceptionTypeOnly(t, &stub, tt.wantType)
			assert.Equal(t, codes.Error, stub.Status.Code)
			assert.Equal(t, tt.wantType, stub.Status.Description)

			obtest.AssertNoSpanMarkers(t, &stub, obtest.LeakCanary)
		})
	}
}

// TestRecordErrorByTypeEmitsNoExceptionMessage pins the deliberate omission of
// the OTel exception.message attribute.
func TestRecordErrorByTypeEmitsNoExceptionMessage(t *testing.T) {
	stub := recordSpanError(t, errors.New(obtest.LeakCanary))

	require.Len(t, stub.Events, 1)
	for _, attr := range stub.Events[0].Attributes {
		assert.False(t, strings.HasSuffix(string(attr.Key), ".message"), "unexpected message attribute %s", attr.Key)
	}
}
