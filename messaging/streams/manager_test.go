package streams

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/ha"
	"github.com/rabbitmq/rabbitmq-stream-go-client/pkg/stream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/logger"
)

// fakeHandle stands in for *ha.ReliableConsumer so shutdown bookkeeping is
// testable without a broker.
type fakeHandle struct {
	mu       sync.Mutex
	events   []string
	status   int
	closeErr error
	storeErr error
}

func (f *fakeHandle) StoreCustomOffset(offset int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.storeErr != nil {
		return f.storeErr
	}
	f.events = append(f.events, fmt.Sprintf("store:%d", offset))
	return nil
}

func (f *fakeHandle) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "close")
	return f.closeErr
}

func (f *fakeHandle) GetStatus() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeHandle) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(ManagerOptions{
		URI:    "rabbitmq-stream://localhost:5552/%2f",
		Logger: logger.New("error", false),
	})
}

// attach wires a fake consumer into a manager as if Start had created it.
func attach(m *Manager, handle consumerHandle, tracker *offsetTracker) {
	m.consumers = append(m.consumers, &runningConsumer{
		stream:  testStream,
		name:    testConsumerName,
		handle:  handle,
		tracker: tracker,
	})
	m.started = true
}

func TestNewManagerAppliesOffsetStoreDefaults(t *testing.T) {
	m := NewManager(ManagerOptions{URI: "rabbitmq-stream://localhost:5552/"})

	assert.Equal(t, defaultOffsetStoreCount, m.opts.OffsetStoreCount)
	assert.Equal(t, defaultOffsetStoreInterval, m.opts.OffsetStoreInterval)
}

func TestNewManagerKeepsExplicitOffsetStoreTuning(t *testing.T) {
	m := NewManager(ManagerOptions{OffsetStoreCount: 7, OffsetStoreInterval: 250 * time.Millisecond})

	assert.Equal(t, 7, m.opts.OffsetStoreCount)
	assert.Equal(t, 250*time.Millisecond, m.opts.OffsetStoreInterval)
}

func TestManagerStartWithoutDeclarationsDoesNotDial(t *testing.T) {
	// Port 1 refuses connections, so any dial attempt would surface as an error.
	m := NewManager(ManagerOptions{URI: "rabbitmq-stream://guest:guest@127.0.0.1:1/%2f", Logger: logger.New("error", false)})

	require.NoError(t, m.Start(context.Background(), nil))
	require.NoError(t, m.Start(context.Background(), NewDeclarations()))

	assert.Nil(t, m.env)
	assert.False(t, m.started)
}

func TestManagerStartRejectsSecondStart(t *testing.T) {
	m := testManager(t)
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1, time.Hour, nil))

	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	err := m.Start(context.Background(), decls)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestManagerStopConsumersFlushesBeforeClosing(t *testing.T) {
	m := testManager(t)
	handle := &fakeHandle{status: ha.StatusOpen}
	tracker := newOffsetTracker(1000, time.Hour, nil)
	require.NoError(t, tracker.record(88, nil, &fakeStorer{}))
	attach(m, handle, tracker)

	m.StopConsumers()

	assert.Equal(t, []string{"store:88", "close"}, handle.recorded(),
		"a clean shutdown commits the last handled offset before the consumer goes away")
	assert.False(t, m.started)
	assert.Empty(t, m.consumers)
}

func TestManagerStopConsumersIsIdempotent(t *testing.T) {
	m := testManager(t)
	handle := &fakeHandle{status: ha.StatusOpen}
	attach(m, handle, newOffsetTracker(1000, time.Hour, nil))

	m.StopConsumers()
	m.StopConsumers()

	assert.Equal(t, []string{"close"}, handle.recorded(), "nothing pending, and no second close")
}

func TestManagerStopConsumersToleratesFlushAndCloseErrors(t *testing.T) {
	m := testManager(t)
	tracker := newOffsetTracker(1000, time.Hour, nil)
	require.NoError(t, tracker.record(4, nil, &fakeStorer{}))
	handle := &fakeHandle{
		status:   ha.StatusOpen,
		closeErr: errors.New("close failed"),
		storeErr: errors.New("store failed"),
	}
	attach(m, handle, tracker)

	assert.NotPanics(t, m.StopConsumers)
	assert.False(t, m.started)
}

func TestManagerStopConsumersCancelsConsumeContext(t *testing.T) {
	m := testManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1, time.Hour, nil))

	m.StopConsumers()

	require.Error(t, ctx.Err())
	assert.Nil(t, m.cancel)
}

func TestManagerCloseWithoutEnvironmentIsIdempotent(t *testing.T) {
	m := testManager(t)

	require.NoError(t, m.Close())
	require.NoError(t, m.Close())
}

func TestManagerStats(t *testing.T) {
	m := NewManager(ManagerOptions{
		URI:                 "rabbitmq-stream://localhost:5552/",
		OffsetStoreCount:    9,
		OffsetStoreInterval: 2 * time.Second,
		Logger:              logger.New("error", false),
	})
	tracker := newOffsetTracker(1, time.Hour, nil)
	require.NoError(t, tracker.record(31, nil, &fakeStorer{}))
	attach(m, &fakeHandle{status: ha.StatusOpen}, tracker)

	stats := m.Stats()

	assert.Equal(t, true, stats["started"])
	assert.Equal(t, 1, stats["consumers"])
	assert.Equal(t, true, stats["ready"])
	assert.Equal(t, map[string]int64{testStream + "/" + testConsumerName: 31}, stats["stored_offsets"])
	assert.Equal(t, 9, stats["offset_store_count"])
	assert.Equal(t, "2s", stats["offset_flush_interval"])
}

func TestManagerStatsOmitsUncommittedOffsets(t *testing.T) {
	m := testManager(t)
	attach(m, &fakeHandle{status: ha.StatusOpen}, newOffsetTracker(1000, time.Hour, nil))

	stats := m.Stats()

	assert.Empty(t, stats["stored_offsets"])
}

func TestManagerReady(t *testing.T) {
	tests := []struct {
		name    string
		started bool
		status  int
		want    bool
	}{
		{name: "open_consumer_is_ready", started: true, status: ha.StatusOpen, want: true},
		{name: "reconnecting_consumer_is_not_ready", started: true, status: ha.StatusReconnecting, want: false},
		{name: "closed_consumer_is_not_ready", started: true, status: ha.StatusClosed, want: false},
		{name: "never_started_is_not_ready", started: false, status: ha.StatusOpen, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testManager(t)
			attach(m, &fakeHandle{status: tt.status}, newOffsetTracker(1, time.Hour, nil))
			m.started = tt.started

			assert.Equal(t, tt.want, m.Ready())
			assert.Equal(t, tt.want, m.Stats()["ready"])
		})
	}
}

func TestOffsetSpecFor(t *testing.T) {
	tests := []struct {
		name     string
		stored   int64
		queryErr error
		start    OffsetStart
		want     stream.OffsetSpecification
	}{
		{
			name:   "stored_offset_resumes_one_past_it",
			stored: 17,
			start:  OffsetFirst(),
			want:   stream.OffsetSpecification{}.Offset(18),
		},
		{
			name:     "no_stored_offset_uses_declared_start",
			queryErr: stream.OffsetNotFoundError,
			start:    OffsetFirst(),
			want:     stream.OffsetSpecification{}.First(),
		},
		{
			name:     "query_failure_uses_declared_start",
			queryErr: errors.New("boom"),
			start:    OffsetLast(),
			want:     stream.OffsetSpecification{}.Last(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, offsetSpecFor(tt.stored, tt.queryErr, tt.start))
		})
	}
}

func TestStreamOptionsFrom(t *testing.T) {
	assert.Equal(t, stream.NewStreamOptions(), streamOptionsFrom(nil))
	assert.Equal(t, stream.NewStreamOptions(), streamOptionsFrom(&StreamSpec{}),
		"a zero spec leaves every retention knob to the broker")

	opts := streamOptionsFrom(&StreamSpec{
		MaxAge:              45 * time.Minute,
		MaxLengthBytes:      2048,
		MaxSegmentSizeBytes: 1024,
	})

	require.NotNil(t, opts)
	assert.Equal(t, 45*time.Minute, opts.MaxAge)
	assert.Equal(t, stream.ByteCapacity{}.B(2048), opts.MaxLengthBytes)
	assert.Equal(t, stream.ByteCapacity{}.B(1024), opts.MaxSegmentSizeBytes)
}

// TestStreamOptionsFromClampsMaxAge pins the retention rendering against
// messaging.StreamQueueSpec: truncate toward whole seconds, floor a non-zero
// value at 1s, and leave zero alone so the broker default still applies. The
// client formats MaxAge with %.0f (round to nearest), so 1500ms would reach the
// broker as 2s here and 1s in the AMQP lane without the clamp.
func TestStreamOptionsFromClampsMaxAge(t *testing.T) {
	tests := []struct {
		name   string
		maxAge time.Duration
		want   time.Duration
	}{
		{name: "sub_second_floors_to_one_second", maxAge: 500 * time.Millisecond, want: time.Second},
		{name: "nanosecond_floors_to_one_second", maxAge: time.Nanosecond, want: time.Second},
		{name: "fractional_second_truncates_down", maxAge: 1500 * time.Millisecond, want: time.Second},
		{name: "whole_seconds_pass_through", maxAge: 90 * time.Second, want: 90 * time.Second},
		{name: "zero_emits_no_max_age", maxAge: 0, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := streamOptionsFrom(&StreamSpec{MaxAge: tt.maxAge, MaxLengthBytes: 2048})

			assert.Equal(t, tt.want, opts.MaxAge)
			assert.Equal(t, stream.ByteCapacity{}.B(2048), opts.MaxLengthBytes,
				"the MaxAge clamp must not disturb the other retention knobs")
		})
	}
}

func TestManagerEnvironmentOptions(t *testing.T) {
	t.Run("without_address_resolver", func(t *testing.T) {
		m := NewManager(ManagerOptions{URI: "rabbitmq-stream://localhost:5552/%2f"})

		opts := m.environmentOptions()

		assert.Nil(t, opts.AddressResolver)
		require.Len(t, opts.ConnectionParameters, 1)
		assert.Equal(t, "rabbitmq-stream://localhost:5552/%2f", opts.ConnectionParameters[0].Uri)
	})

	t.Run("with_address_resolver", func(t *testing.T) {
		m := NewManager(ManagerOptions{
			URI:                 "rabbitmq-stream://localhost:5552/%2f",
			AddressResolverHost: "lb.example.com",
			AddressResolverPort: 5553,
		})

		opts := m.environmentOptions()

		require.NotNil(t, opts.AddressResolver)
		assert.Equal(t, "lb.example.com", opts.AddressResolver.Host)
		assert.Equal(t, 5553, opts.AddressResolver.Port)
	})
}

func TestRedactStreamURI(t *testing.T) {
	// Fixture value only — no real credential appears in this repository.
	const fixturePassword = "fixture-pw"

	tests := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "masks_password_keeps_username",
			uri:  "rabbitmq-stream://svc:" + fixturePassword + "@broker:5552/%2f",
			want: "rabbitmq-stream://svc:****@broker:5552/%2f",
		},
		{
			name: "masks_both_when_no_userinfo",
			uri:  "rabbitmq-stream://broker:5552/vhost",
			want: "rabbitmq-stream://****:****@broker:5552/vhost",
		},
		{
			name: "masks_query_string",
			uri:  "rabbitmq-stream://svc:" + fixturePassword + "@broker:5552/?token=abc",
			want: "rabbitmq-stream://svc:****@broker:5552/?<redacted>",
		},
		{
			name: "unparseable_uri_degrades_to_placeholder",
			uri:  "rabbitmq-stream://svc:" + fixturePassword + "@broker:55 52/%2f",
			want: redactedStreamURI,
		},
		{
			name: "empty_uri_degrades_to_placeholder",
			uri:  "",
			want: redactedStreamURI,
		},
		{
			name: "tls_scheme_is_preserved",
			uri:  "rabbitmq-stream+tls://svc:" + fixturePassword + "@broker:5551/%2f",
			want: "rabbitmq-stream+tls://svc:****@broker:5551/%2f",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactStreamURI(tt.uri)

			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, fixturePassword, "the password must never survive redaction")
		})
	}
}

// TestManagerStartDoesNotLeakURIOnParseFailure covers the path reachable when
// config.Validate never ran (app.NewWithConfig): the client returns a *url.Error
// whose Error() renders the raw URI, credentials included.
func TestManagerStartDoesNotLeakURIOnParseFailure(t *testing.T) {
	const fixturePassword = "fixture-pw"
	m := NewManager(ManagerOptions{
		// A space makes url.Parse fail inside the client's environment constructor.
		URI:    "rabbitmq-stream://svc:" + fixturePassword + "@broker:55 52/%2f",
		Logger: logger.New("error", false),
	})
	decls := NewDeclarations()
	decls.DeclareStream(testStream, nil)

	err := m.Start(context.Background(), decls)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), fixturePassword, "the credential must not survive into the error")
	assert.Contains(t, err.Error(), "invalid stream URI")
	assert.Contains(t, err.Error(), redactedStreamURI,
		"an unparseable endpoint degrades to the fixed placeholder rather than echoing the input")
	assert.False(t, m.started)
}

func TestSafeEnvError(t *testing.T) {
	// Not const: a constant expression here lets staticcheck evaluate the
	// deliberately-invalid URL and report SA1007 on the fixture itself.
	fixturePassword := "fixture-pw"
	raw := "rabbitmq-stream://svc:" + fixturePassword + "@broker:55 52/"
	_, parseErr := url.Parse(raw)
	require.Error(t, parseErr)
	require.Contains(t, parseErr.Error(), fixturePassword, "the premise: the vendor's error carries the credential")

	safe := safeEnvError(parseErr)

	assert.NotContains(t, safe.Error(), fixturePassword)
	assert.Contains(t, safe.Error(), "invalid stream URI")

	other := errors.New("connection refused")
	assert.Equal(t, other, safeEnvError(other), "non-URL errors pass through untouched")
}
