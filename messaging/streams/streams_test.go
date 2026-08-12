package streams

import (
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"github.com/stretchr/testify/assert"
)

func TestOffsetStartZeroValueIsNext(t *testing.T) {
	var zero OffsetStart

	assert.Equal(t, OffsetNext(), zero, "zero value must be the safe Next configuration")
	assert.Equal(t, stream.OffsetSpecification{}.Next(), zero.specification())
}

func TestOffsetStartSpecification(t *testing.T) {
	since := time.Date(2026, 8, 12, 10, 30, 0, 0, time.UTC)

	tests := []struct {
		name  string
		start OffsetStart
		want  stream.OffsetSpecification
	}{
		{name: "first", start: OffsetFirst(), want: stream.OffsetSpecification{}.First()},
		{name: "last", start: OffsetLast(), want: stream.OffsetSpecification{}.Last()},
		{name: "next", start: OffsetNext(), want: stream.OffsetSpecification{}.Next()},
		{name: "absolute_offset", start: OffsetAt(42), want: stream.OffsetSpecification{}.Offset(42)},
		{name: "timestamp", start: OffsetSince(since), want: stream.OffsetSpecification{}.Timestamp(since.UnixMilli())},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.start.specification())
		})
	}
}

func TestOffsetSinceUsesMilliseconds(t *testing.T) {
	ts := time.Date(2026, 8, 12, 10, 30, 0, 500*int(time.Millisecond), time.UTC)

	assert.Equal(t, ts.UnixMilli(), OffsetSince(ts).value)
}
