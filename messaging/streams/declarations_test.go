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
	testSuperStream  = "orders-partitioned"
	testPartitions   = 3
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

func TestDeclareSuperStreamStoresDefinition(t *testing.T) {
	d := NewDeclarations()
	spec := &StreamSpec{MaxAge: 90 * time.Second, MaxLengthBytes: 1024, MaxSegmentSizeBytes: 512}

	d.DeclareSuperStream(testSuperStream, testPartitions, spec)

	require.Len(t, d.superStreams, 1)
	assert.Equal(t, testSuperStream, d.superStreams[0].Name)
	assert.Equal(t, testPartitions, d.superStreams[0].Partitions)
	assert.Equal(t, StreamSpec{MaxAge: 90 * time.Second, MaxLengthBytes: 1024, MaxSegmentSizeBytes: 512}, d.superStreams[0].Spec)
	assert.False(t, d.IsEmpty(), "a super stream alone is a declaration")
	assert.Empty(t, d.streams, "a super stream is not stored as a plain stream")
}

func TestDeclareSuperStreamNilSpecLeavesRetentionUnset(t *testing.T) {
	d := NewDeclarations()

	d.DeclareSuperStream(testSuperStream, testPartitions, nil)

	require.Len(t, d.superStreams, 1)
	assert.Equal(t, StreamSpec{}, d.superStreams[0].Spec)
}

func TestDeclareSuperStreamCopiesSpec(t *testing.T) {
	d := NewDeclarations()
	spec := &StreamSpec{MaxAge: time.Minute}

	d.DeclareSuperStream(testSuperStream, testPartitions, spec)
	spec.MaxAge = time.Hour
	spec.MaxLengthBytes = 99

	assert.Equal(t, StreamSpec{MaxAge: time.Minute}, d.superStreams[0].Spec,
		"stored spec must not follow the caller's struct")
}

func TestDeclareSuperStreamIdenticalRedeclarationIsNoop(t *testing.T) {
	d := NewDeclarations()

	d.DeclareSuperStream(testSuperStream, testPartitions, &StreamSpec{MaxAge: time.Minute})
	d.DeclareSuperStream(testSuperStream, testPartitions, &StreamSpec{MaxAge: time.Minute})

	assert.Len(t, d.superStreams, 1)
	require.NoError(t, d.Validate())
}

// TestDeclareSuperStreamConflictingRedeclarationFailsValidation covers both halves
// of a super stream's identity: the partition count is as much part of it as the
// retention spec, so a mismatch in either must be reported rather than silently
// keeping whichever declaration ran first.
func TestDeclareSuperStreamConflictingRedeclarationFailsValidation(t *testing.T) {
	tests := []struct {
		name       string
		partitions int
		spec       *StreamSpec
	}{
		{name: "different_partition_count", partitions: 5, spec: &StreamSpec{MaxAge: time.Minute}},
		{name: "different_retention_spec", partitions: testPartitions, spec: &StreamSpec{MaxAge: time.Hour}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			d.DeclareSuperStream(testSuperStream, testPartitions, &StreamSpec{MaxAge: time.Minute})

			d.DeclareSuperStream(testSuperStream, tt.partitions, tt.spec)

			err := d.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), `super stream "orders-partitioned" declared twice with different definitions`)
			assert.Len(t, d.superStreams, 1, "the conflicting declaration is reported, not stored")
		})
	}
}

// TestDeclareSuperStreamConsumerIsAlwaysSingleActive pins the deviation from the
// plain lane: SAC is not the caller's choice on a super stream, because the
// promotion callback is the only per-partition offset seam the client offers.
func TestDeclareSuperStreamConsumerIsAlwaysSingleActive(t *testing.T) {
	d := NewDeclarations()
	d.DeclareSuperStream(testSuperStream, testPartitions, nil)

	d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream,
		Name:        testConsumerName,
		Handler:     noopHandler,
	})

	require.Len(t, d.consumers, 1)
	assert.True(t, d.consumers[0].SAC, "super-stream consumption is always a SAC group")
	assert.True(t, d.consumers[0].Super)
	require.NoError(t, d.Validate())
}

func TestDeclareSuperStreamConsumerCopiesOptions(t *testing.T) {
	d := NewDeclarations()
	opts := &SuperStreamConsumerOptions{
		SuperStream: testSuperStream,
		Name:        testConsumerName,
		Start:       OffsetFirst(),
		Handler:     noopHandler,
	}

	d.DeclareSuperStreamConsumer(opts)
	opts.Name = "mutated"
	opts.Start = OffsetLast()

	require.Len(t, d.consumers, 1)
	assert.Equal(t, testSuperStream, d.consumers[0].Stream)
	assert.Equal(t, testConsumerName, d.consumers[0].Name)
	assert.Equal(t, OffsetFirst(), d.consumers[0].Start)
}

func TestDeclareSuperStreamConsumerNilPanics(t *testing.T) {
	d := NewDeclarations()

	assert.PanicsWithValue(t,
		"streams: nil consumer declaration detected\n"+
			"  DeclareSuperStreamConsumer requires a non-nil *SuperStreamConsumerOptions\n"+
			"  Pass &streams.SuperStreamConsumerOptions{...} at every DeclareSuperStreamConsumer call within DeclareStreams",
		func() { d.DeclareSuperStreamConsumer(nil) })

	assert.True(t, d.IsEmpty(), "the rejected declaration is not stored")
}

func TestDeclareSuperStreamConsumerDuplicatePanics(t *testing.T) {
	d := NewDeclarations()
	d.DeclareSuperStream(testSuperStream, testPartitions, nil)
	d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
	})

	assert.PanicsWithValue(t,
		"streams: duplicate consumer declaration detected\n"+
			"  super stream=orders-partitioned name=orders-processor\n"+
			"  Ensure each DeclareSuperStreamConsumer call is unique within DeclareStreams",
		func() {
			d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
				SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
			})
		})
}

func TestStatsCountsSuperStreamsInBothFields(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)
	d.DeclareSuperStream(testSuperStream, testPartitions, nil)
	d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})
	d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
		SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
	})

	assert.Equal(t, Stats{Streams: 2, SuperStreams: 1, Consumers: 2}, d.Stats(),
		"Streams counts every declared stream so a super-stream-only service is not reported as having none")
	require.NoError(t, d.Validate())
}

// TestValidateSuperStreamDeclarations covers the rules a super stream adds, and in
// particular the four ways a consumer can name the wrong kind of target: the two
// kinds reach the broker through different client APIs, so each mismatch names the
// declare method that would have been right.
func TestValidateSuperStreamDeclarations(t *testing.T) {
	tests := []struct {
		name    string
		build   func(d *Declarations)
		wantErr string
	}{
		{
			name: "valid_super_stream_and_consumer",
			build: func(d *Declarations) {
				d.DeclareSuperStream(testSuperStream, testPartitions, &StreamSpec{MaxLengthBytes: 1024})
				d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
					SuperStream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
				})
			},
		},
		{
			name: "empty_super_stream_name",
			build: func(d *Declarations) {
				d.DeclareSuperStream("", testPartitions, nil)
			},
			wantErr: "super stream declaration has an empty name",
		},
		{
			name: "zero_partitions",
			build: func(d *Declarations) {
				d.DeclareSuperStream(testSuperStream, 0, nil)
			},
			wantErr: `super stream "orders-partitioned" declares 0 partitions; at least 1 is required`,
		},
		{
			name: "negative_partitions",
			build: func(d *Declarations) {
				d.DeclareSuperStream(testSuperStream, -2, nil)
			},
			wantErr: `super stream "orders-partitioned" declares -2 partitions; at least 1 is required`,
		},
		{
			name: "single_partition_is_allowed",
			build: func(d *Declarations) {
				d.DeclareSuperStream(testSuperStream, 1, nil)
			},
		},
		{
			name: "name_declared_as_both_kinds",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, nil)
				d.DeclareSuperStream(testStream, testPartitions, nil)
			},
			wantErr: `"orders" is declared both as a stream and as a super stream`,
		},
		{
			name: "negative_retention_names_the_super_stream_kind",
			build: func(d *Declarations) {
				d.DeclareSuperStream(testSuperStream, testPartitions, &StreamSpec{MaxAge: -time.Hour})
			},
			wantErr: `super stream "orders-partitioned" has a negative MaxAge (-1h0m0s); zero leaves retention to the broker`,
		},
		{
			name: "super_consumer_on_undeclared_super_stream",
			build: func(d *Declarations) {
				d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
					SuperStream: "ghost", Name: testConsumerName, Handler: noopHandler,
				})
			},
			wantErr: `consumer "orders-processor" references undeclared super stream "ghost"`,
		},
		{
			name: "super_consumer_on_a_plain_stream",
			build: func(d *Declarations) {
				d.DeclareStream(testStream, nil)
				d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
					SuperStream: testStream, Name: testConsumerName, Handler: noopHandler,
				})
			},
			wantErr: `consumer "orders-processor" consumes "orders" as a super stream, but it is declared as a plain stream; use DeclareConsumer`,
		},
		{
			name: "plain_consumer_on_a_super_stream",
			build: func(d *Declarations) {
				d.DeclareSuperStream(testSuperStream, testPartitions, nil)
				d.DeclareConsumer(&ConsumerOptions{
					Stream: testSuperStream, Name: testConsumerName, Handler: noopHandler,
				})
			},
			wantErr: `consumer "orders-processor" consumes "orders-partitioned" as a plain stream, but it is declared as a super stream; use DeclareSuperStreamConsumer`,
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

// TestDeclareConsumerAndSuperStreamConsumerShareTheDuplicateKey proves the two
// declare methods index into one namespace: the same name on the same target is a
// duplicate whichever method declared it first, so a typo cannot start two members
// of one offset group inside one process.
func TestDeclareConsumerAndSuperStreamConsumerShareTheDuplicateKey(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)
	d.DeclareConsumer(&ConsumerOptions{Stream: testStream, Name: testConsumerName, Handler: noopHandler})

	assert.Panics(t, func() {
		d.DeclareSuperStreamConsumer(&SuperStreamConsumerOptions{
			SuperStream: testStream, Name: testConsumerName, Handler: noopHandler,
		})
	})
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

func TestDeclarePublisherReturnsAnUnboundHandle(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)

	p := d.DeclarePublisher(&PublisherOptions{Stream: testStream})

	require.NotNil(t, p)
	assert.Equal(t, testStream, p.stream)
	assert.False(t, p.super)
	// IsEmpty is what app/ gates the "streams declared but not configured" startup
	// failure on, so a publisher-only service has to make it false.
	assert.False(t, d.IsEmpty())
	assert.Equal(t, Stats{Streams: 1, Publishers: 1}, d.Stats())
	require.NoError(t, d.Validate())
	assert.ErrorIs(t, p.Publish(context.Background(), &PublishMessage{Data: []byte("x")}), ErrPublisherNotStarted,
		"the handle stays inert until Manager.Start binds it")
}

func TestDeclarePublisherNilPanics(t *testing.T) {
	d := NewDeclarations()

	assert.PanicsWithValue(t,
		"streams: nil publisher declaration detected\n"+
			"  DeclarePublisher requires a non-nil *PublisherOptions\n"+
			"  Pass &streams.PublisherOptions{...} at every DeclarePublisher call within DeclareStreams",
		func() { d.DeclarePublisher(nil) })

	assert.True(t, d.IsEmpty(), "the rejected declaration is not stored")
}

func TestDeclarePublisherDuplicatePanics(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)
	d.DeclarePublisher(&PublisherOptions{Stream: testStream})

	assert.PanicsWithValue(t,
		"streams: duplicate publisher declaration detected\n"+
			"  stream=orders\n"+
			"  Ensure each DeclarePublisher call is unique within DeclareStreams",
		func() { d.DeclarePublisher(&PublisherOptions{Stream: testStream}) })

	assert.Equal(t, 1, d.Stats().Publishers, "the rejected declaration is not stored")
}

func TestDeclarePublisherOnADifferentStreamIsAllowed(t *testing.T) {
	d := NewDeclarations()
	d.DeclareStream(testStream, nil)
	d.DeclareStream("shipments", nil)

	first := d.DeclarePublisher(&PublisherOptions{Stream: testStream})
	second := d.DeclarePublisher(&PublisherOptions{Stream: "shipments"})

	assert.NotSame(t, first, second, "each target gets its own publisher")
	assert.Equal(t, Stats{Streams: 2, Publishers: 2}, d.Stats())
	require.NoError(t, d.Validate())
}

// TestValidateRejectsAMisdirectedPublisher pins the publisher's target rules: the
// target has to exist, and it has to be a plain stream — a super stream is reached
// through a different client API entirely.
func TestValidateRejectsAMisdirectedPublisher(t *testing.T) {
	tests := []struct {
		name    string
		declare func(d *Declarations)
		target  string
		wantErr string
	}{
		{
			name:    "undeclared_target",
			declare: func(*Declarations) {},
			target:  "ghost",
			wantErr: `publisher references undeclared stream "ghost"`,
		},
		{
			name:    "super_stream_target",
			declare: func(d *Declarations) { d.DeclareSuperStream(testSuperStream, testPartitions, nil) },
			target:  testSuperStream,
			wantErr: "but it is declared as a super stream",
		},
		{
			name:    "empty_target",
			declare: func(*Declarations) {},
			target:  "",
			wantErr: "publisher declaration has an empty stream name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeclarations()
			tt.declare(d)
			d.DeclarePublisher(&PublisherOptions{Stream: tt.target})

			err := d.Validate()

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestValidateReportsAnEmptyPublisherTargetOnce is the reason the empty-name check
// stops there: reporting an unnamed stream as "undeclared" as well would tell the
// operator to declare a stream called "".
func TestValidateReportsAnEmptyPublisherTargetOnce(t *testing.T) {
	d := NewDeclarations()
	d.DeclarePublisher(&PublisherOptions{Stream: ""})

	err := d.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "publisher declaration has an empty stream name")
	assert.NotContains(t, err.Error(), "references undeclared stream")
}
