package logger

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestIdentityFieldKeysAreStable pins the wire names: every stamping site writes through
// these constants, so a changed literal here would rename a field on every log line.
func TestIdentityFieldKeysAreStable(t *testing.T) {
	assert.Equal(t, "correlation_id", FieldCorrelationID)
	assert.Equal(t, "trace_id", FieldTraceID)
	assert.Equal(t, "span_id", FieldSpanID)
	assert.NotEqual(t, FieldCorrelationID, FieldTraceID, "the two identifier spaces must not share a key")
}
