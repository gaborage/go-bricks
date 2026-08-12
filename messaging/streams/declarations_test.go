package streams

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testStream       = "orders"
	testConsumerName = "orders-processor"
)

func noopHandler(context.Context, *Message) error { return nil }

func TestNewDeclarationsIsEmpty(t *testing.T) {
	d := NewDeclarations()

	assert.True(t, d.IsEmpty())
	assert.Equal(t, Stats{}, d.Stats())
	require.NoError(t, d.Validate())
}

func TestDeclareStreamStoresSpec(t *testing.T) {
	d := NewDeclarations()
	spec := &StreamSpec{MaxAge: 90 * time.Second, MaxLengthBytes: 1024, MaxSegmentSizeBytes: 512}

	d.DeclareStream(testStream, spec)

	require.Len(t, d.streams, 1)
	assert.Equal(t, testStream, d.streams[0].Name)
	assert.Equal(t, StreamSpec{MaxAge: 90 * time.Second, MaxLengthBytes: 1024, MaxSegmentSizeBytes: 512}, d.streams[0].Spec)
	assert.False(t, d.IsEmpty())
	assert.Equal(t, Stats{Streams: 1}, d.Stats())
}

func TestDeclareStreamNilSpecLeavesRetentionUnset(t *testing.T) {
	d := NewDeclarations()

	d.DeclareStream(testStream, nil)

	require.Len(t, d.streams, 1)
	assert.Equal(t, StreamSpec{}, d.streams[0].Spec)
}

func TestDeclareStreamCopiesSpec(t *testing.T) {
	d := NewDeclarations()
	spec := &StreamSpec{MaxAge: time.Minute}

	d.DeclareStream(testStream, spec)
	spec.MaxAge = time.Hour
	spec.MaxLengthBytes = 99

	assert.Equal(t, StreamSpec{MaxAge: time.Minute}, d.streams[0].Spec,
		"stored spec must not follow the caller's struct")
}

func TestDeclareStreamIdenticalRedeclarationIsNoop(t *testing.T) {
	d := NewDeclarations()
	spec := &StreamSpec{MaxAge: time.Minute}

	d.DeclareStream(testStream, spec)
	d.DeclareStream(testStream, &StreamSpec{MaxAge: time.Minute})

	assert.Len(t, d.streams, 1)
	require.NoError(t, d.Validate())
}

func TestDeclareStreamConflictingRedeclarationFailsValidation(t *testing.T) {
	d := NewDeclarations()

	d.DeclareStream(testStream, &StreamSpec{MaxAge: time.Minute})
	d.DeclareStream(testStream, &StreamSpec{MaxAge: time.Hour})

	err := d.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), `stream "orders" declared twice with different retention specs`)
}

func TestDeclareConsumerCopiesOptions(t *testing.T) {
	d := NewDeclarations()
	opts := &ConsumerOptions{
		Stream:  testStream,
		Name:    testConsumerName,
		Start:   OffsetFirst(),
		SAC:     true,
		Handler: noopHandler,
	}

	d.DeclareConsumer(opts)
	opts.Name = "mutated"
	opts.SAC = false
	opts.Start = OffsetLast()

	require.Len(t, d.consumers, 1)
	assert.Equal(t, testConsumerName, d.consumers[0].Name)
	assert.True(t, d.consumers[0].SAC)
	assert.Equal(t, OffsetFirst(), d.consumers[0].Start)
}

// TestDeclareConsumerNilPanics is the counterpart to the duplicate-panic case: a
// nil declaration is the same class of wiring error and must be as loud, instead
// of leaving the module with no consumer and no complaint.
func TestDeclareConsumerNilPanics(t *testing.T) {
	d := NewDeclarations()

	assert.PanicsWithValue(t,
		"streams: nil consumer declaration detected\n"+
			"  DeclareConsumer requires a non-nil *ConsumerOptions\n"+
			"  Pass &streams.ConsumerOptions{...} at every DeclareConsumer call within DeclareStreams",
		func() { d.DeclareConsumer(nil) })

	assert.True(t, d.IsEmpty(), "the rejected declaration is not stored")
}

func TestDeclareConsumerDuplicatePanics(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)
	opts := &ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler}
	d.DeclareConsumer(opts)

	assert.PanicsWithValue(t,
		"streams: duplicate consumer declaration detected\n"+
			"  stream=orders name=orders-processor\n"+
			"  Ensure each DeclareConsumer call is unique within DeclareStreams",
		func() {
			d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
		})
}

func TestDeclareConsumerSameNameOnDifferentStreamsIsAllowed(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)
	d.DeclareStream("shipments", nil)

	d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
	d.DeclareConsumer(&ConsumerOptions{Stream: "shipments", Name: testConsumerName, Handler: noopHandler})

	assert.Equal(t, Stats{Streams: 2, Consumers: 2}, d.Stats())
	require.NoError(t, d.Validate())
}

func TestValidateStreamDeclarations(t *testing.T) {
	tests := []struct {
		name    string
		build   func(d *Declarations)
		wantErr string
	}{
		{
			name: "valid_declarations",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, nil)
				d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
			},
		},
		{
			name: "empty_stream_name",
			build: func(d *Declarations) {
				d.DeclareStream("", nil)
			},
			wantErr: "stream declaration has an empty name",
		},
		{
			name: "consumer_without_name",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, nil)
				d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Handler: noopHandler})
			},
			wantErr: `consumer on stream "orders" has an empty name; a name is required for offset tracking`,
		},
		{
			name: "consumer_without_handler",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, nil)
				d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName})
			},
			wantErr: `consumer "orders-processor" on stream "orders" has a nil handler`,
		},
		{
			name: "consumer_on_undeclared_stream",
			build: func(d *Declarations) {
				d.DeclareConsumer(&ConsumerOptions{Stream: "ghost", Name: testConsumerName, Handler: noopHandler})
			},
			wantErr: `consumer "orders-processor" references undeclared stream "ghost"`,
		},
		{
			name: "negative_max_age",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, &StreamSpec{MaxAge: -time.Hour})
			},
			wantErr: `stream "orders" has a negative MaxAge (-1h0m0s); zero leaves retention to the broker`,
		},
		{
			name: "negative_max_length_bytes",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, &StreamSpec{MaxLengthBytes: -1})
			},
			wantErr: `stream "orders" has a negative MaxLengthBytes (-1); zero leaves retention to the broker`,
		},
		{
			name: "negative_max_segment_size_bytes",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, &StreamSpec{MaxSegmentSizeBytes: -512})
			},
			wantErr: `stream "orders" has a negative MaxSegmentSizeBytes (-512); zero leaves retention to the broker`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			tt.build(d)

			err := d.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestValidateAcceptsZeroRetentionAsBrokerDefault is the regression risk carried
// by the negative-retention rule: zero is how a caller asks for the broker's own
// retention, so it must stay valid and must stay stored as zero — that is what
// keeps the manager's positive-only guards from sending anything at all.
func TestValidateAcceptsZeroRetentionAsBrokerDefault(t *testing.T) {
	tests := []struct {
		name string
		spec *StreamSpec
		want StreamSpec
	}{
		{name: "nil_spec"},
		{name: "every_field_zero", spec: &StreamSpec{}},
		{
			name: "zero_fields_beside_a_populated_sibling",
			spec: &StreamSpec{MaxLengthBytes: 1024},
			want: StreamSpec{MaxLengthBytes: 1024},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			d.DeclareStream(testStream, tt.spec)

			require.NoError(t, d.Validate())
			assert.Equal(t, tt.want, d.streams[0].Spec, "a zero retention field stays zero")
		})
	}
}

// TestValidateReportsEveryNegativeRetentionField proves the three rules aggregate
// rather than the first one masking the rest.
func TestValidateReportsEveryNegativeRetentionField(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, &StreamSpec{MaxAge: -time.Second, MaxLengthBytes: -2, MaxSegmentSizeBytes: -3})

	err := d.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative MaxAge")
	assert.Contains(t, err.Error(), "negative MaxLengthBytes")
	assert.Contains(t, err.Error(), "negative MaxSegmentSizeBytes")
}

func TestValidateAggregatesEveryProblem(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream("", nil)
	d.DeclareConsumer(&ConsumerOptions{Stream: "ghost"})

	err := d.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stream declaration has an empty name")
	assert.Contains(t, err.Error(), "has an empty name; a name is required for offset tracking")
	assert.Contains(t, err.Error(), "has a nil handler")
	assert.Contains(t, err.Error(), `references undeclared stream "ghost"`)
}
