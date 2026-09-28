package server

import (
	"context"
	goerrors "errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/internal/testutil"
)

// checkWaiterFrame is the frame of a probe parked waiting on the check flight.
const checkWaiterFrame = "server.(*Server).checkApplicationListener("

// checkPanicSentinel is carried by a check's panic value; it must never reach a log or body.
const checkPanicSentinel = "check-panic-sentinel-7f3a"

// flightValueKey carries a request-scoped value a flight must inherit from its leader.
type flightValueKey struct{}

// roundTripperFunc adapts a function to http.RoundTripper.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// gatedTransport holds every check at the transport until release, counting the checks that
// reach it. Once released it fails a check whose context was canceled meanwhile, so a flight
// that inherited a caller's cancellation fails however the release races it; otherwise it
// sends the HEAD on through next without the check's deadline, so the check's own
// appListenerCheckTimeout does not cap how long a test holds it.
type gatedTransport struct {
	next    http.RoundTripper
	gate    chan struct{}
	entered atomic.Int32
}

func (g *gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	g.entered.Add(1)
	<-g.gate
	if err := req.Context().Err(); goerrors.Is(err, context.Canceled) {
		return nil, err
	}
	return g.next.RoundTrip(req.WithContext(context.WithoutCancel(req.Context())))
}

// panickingTransport panics inside the check on the flight's own goroutine: http.Client.Do
// calls RoundTrip on its caller's goroutine, unlike a dial.
type panickingTransport struct{}

func (panickingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	panic("check failed: " + checkPanicSentinel)
}

// checkFlightFixture is a probe server ready in-process whose check HEADs a stub application
// listener through a gatedTransport, with a counting ready override behind the gates.
type checkFlightFixture struct {
	srv          *Server
	log          *testLogger
	transport    *gatedTransport
	heads        atomic.Int32
	handlerCalls *atomic.Int32
	release      func()
}

func newCheckFlightFixture(t *testing.T) *checkFlightFixture {
	t.Helper()
	f := &checkFlightFixture{log: &testLogger{}}
	f.srv = newProbeTestServer(newProbeTestConfig(""), f.log)
	stubApplicationListener(t, f.srv, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.heads.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	require.NoError(t, f.srv.onBeforeServe(&http.Server{}))
	handler, calls := countingReadyHandler()
	f.srv.RegisterReadyHandler(handler)
	f.handlerCalls = calls
	gate := make(chan struct{})
	f.release = sync.OnceFunc(func() { close(gate) })
	t.Cleanup(f.release)
	f.transport = &gatedTransport{next: f.srv.appCheck.client.Transport, gate: gate}
	f.srv.appCheck.client.Transport = f.transport
	return f
}

// probe serves one probe-listener /ready on ctx in the background.
func (f *checkFlightFixture) probe(ctx context.Context) <-chan *httptest.ResponseRecorder {
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		f.srv.probeEcho.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, testReadyRoute, http.NoBody))
		done <- rec
	}()
	return done
}

// probeAll starts n background probe-listener /ready requests on ctx.
func (f *checkFlightFixture) probeAll(ctx context.Context, n int) []<-chan *httptest.ResponseRecorder {
	responses := make([]<-chan *httptest.ResponseRecorder, n)
	for i := range responses {
		responses[i] = f.probe(ctx)
	}
	return responses
}

// awaitCheckInFlight waits until the leader's check reaches the gated transport.
func (f *checkFlightFixture) awaitCheckInFlight(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return f.transport.entered.Load() == 1 }, 5*time.Second, time.Millisecond)
}

// awaitCheckWaiters waits until n probes are parked on the check flight, so a release after
// it cannot outrun a probe still on its way to join.
func awaitCheckWaiters(t *testing.T, n int) {
	t.Helper()
	require.Eventually(t, func() bool { return testutil.ParkedInSelect(checkWaiterFrame) == n }, 5*time.Second, time.Millisecond,
		"expected %d probes parked on the check flight", n)
}

// receiveRecorder returns a background probe's response, failing if it does not arrive.
func receiveRecorder(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case rec := <-done:
		return rec
	case <-time.After(5 * time.Second):
		t.Fatal("the probe did not answer")
		return nil
	}
}

// TestServerProbeReadyCoalescesConcurrentChecks pins the check flight (ADR-120): concurrent
// probe /ready requests share one in-flight check, so one HEAD reaches the application
// listener and every probe gets its verdict, while the ready override behind the gates still
// runs once per request. A finished check is never reused: the next probe sends its own HEAD.
func TestServerProbeReadyCoalescesConcurrentChecks(t *testing.T) {
	f := newCheckFlightFixture(t)
	const probes = 8
	responses := f.probeAll(t.Context(), probes)
	awaitCheckWaiters(t, probes)
	f.release()

	for _, done := range responses {
		rec := receiveRecorder(t, done)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"status":"custom"}`, rec.Body.String())
	}
	assert.Equal(t, int32(1), f.transport.entered.Load(), "concurrent probes share one check")
	assert.Equal(t, int32(1), f.heads.Load(), "one HEAD reaches the application listener")
	assert.Equal(t, int32(probes), f.handlerCalls.Load(), "the ready override is not coalesced")

	assert.Equal(t, http.StatusOK, serveEngine(f.srv.probeEcho, http.MethodGet, testReadyRoute).Code)
	assert.Equal(t, int32(2), f.heads.Load(), "a finished check is never reused")
	assert.Nil(t, findLogEntry(f.log.logEntries(), appListenerUnresponsiveMsg))
}

// TestServerProbeReadyCoalescedFailureFailsEveryProbe pins that a shared failing check fails
// every probe that joined it: each answers 503 and logs its own WARN, and none reaches the
// ready handler behind the gate.
func TestServerProbeReadyCoalescedFailureFailsEveryProbe(t *testing.T) {
	f := newCheckFlightFixture(t)
	f.transport.next = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: http.NoBody, Request: req}, nil
	})
	const probes = 8
	responses := f.probeAll(t.Context(), probes)
	awaitCheckWaiters(t, probes)
	f.release()

	for _, done := range responses {
		rec := receiveRecorder(t, done)
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.JSONEq(t, probeTestNotReadyBody, rec.Body.String())
	}
	assert.Equal(t, int32(1), f.transport.entered.Load(), "concurrent probes share one check")
	assert.Zero(t, f.handlerCalls.Load(), "no probe passes a failed check")
	warns := 0
	for _, entry := range f.log.logEntries() {
		if entry.msg == appListenerUnresponsiveMsg {
			warns++
		}
	}
	assert.Equal(t, probes, warns, "each probe logs its own WARN")
}

// TestServerProbeReadyCheckKeepsTheLeaderValues pins that the check flight inherits the
// leader's request-scoped values, a trace span among them: it detaches the leader's
// cancellation, not its context.
func TestServerProbeReadyCheckKeepsTheLeaderValues(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	markProbeReadyInProcess(t, srv)
	var seen any
	next := srv.appCheck.client.Transport
	srv.appCheck.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		seen = req.Context().Value(flightValueKey{})
		return next.RoundTrip(req)
	})

	ctx := context.WithValue(t.Context(), flightValueKey{}, "leader")
	rec := httptest.NewRecorder()
	srv.probeEcho.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, testReadyRoute, http.NoBody))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "leader", seen)
}

// TestServerProbeReadyCheckFlightIsPerServer pins the check flight's scope: a probe on one
// server never joins another server's in-flight check.
func TestServerProbeReadyCheckFlightIsPerServer(t *testing.T) {
	a := newCheckFlightFixture(t)
	held := a.probe(t.Context())
	a.awaitCheckInFlight(t)
	b := newCheckFlightFixture(t)
	b.release()

	assert.Equal(t, http.StatusOK, receiveRecorder(t, b.probe(t.Context())).Code)
	assert.Equal(t, int32(1), b.heads.Load(), "the probe ran its own server's check")
	a.release()
	assert.Equal(t, http.StatusOK, receiveRecorder(t, held).Code)
}

// TestServerProbeReadyLeaderCancellationFailsNoFollower pins the flight's detached context:
// the probe that started the check walks away mid-flight and answers 503 at once, without a
// WARN, while the probes that joined its check still get the live listener's verdict.
func TestServerProbeReadyLeaderCancellationFailsNoFollower(t *testing.T) {
	f := newCheckFlightFixture(t)
	leaderCtx, cancelLeader := context.WithCancel(t.Context())
	t.Cleanup(cancelLeader)
	leader := f.probe(leaderCtx)
	f.awaitCheckInFlight(t)
	const followers = 4
	responses := f.probeAll(t.Context(), followers)
	awaitCheckWaiters(t, followers+1)

	cancelLeader()
	rec := receiveRecorder(t, leader)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, probeTestNotReadyBody, rec.Body.String())
	f.release()

	for _, done := range responses {
		assert.Equal(t, http.StatusOK, receiveRecorder(t, done).Code)
	}
	assert.Equal(t, int32(1), f.heads.Load())
	assert.Nil(t, findLogEntry(f.log.logEntries(), appListenerUnresponsiveMsg))
}

// TestServerProbeReadyCanceledFollowerLeavesTheFlight pins the per-waiter select: a probe
// that joined a check and is then abandoned answers 503 while the check is still held,
// without a WARN, and the check runs on for the probe that started it.
func TestServerProbeReadyCanceledFollowerLeavesTheFlight(t *testing.T) {
	f := newCheckFlightFixture(t)
	leader := f.probe(t.Context())
	f.awaitCheckInFlight(t)
	followerCtx, cancelFollower := context.WithCancel(t.Context())
	t.Cleanup(cancelFollower)
	follower := f.probe(followerCtx)
	awaitCheckWaiters(t, 2)

	cancelFollower()
	rec := receiveRecorder(t, follower)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, probeTestNotReadyBody, rec.Body.String())
	f.release()

	assert.Equal(t, http.StatusOK, receiveRecorder(t, leader).Code)
	assert.Equal(t, int32(1), f.handlerCalls.Load(), "only the probe still waiting reaches the handler")
	assert.Nil(t, findLogEntry(f.log.logEntries(), appListenerUnresponsiveMsg))
}

// TestServerProbeReadyContainsACheckPanic pins the flight's panic containment: a check that
// panics fails the gate with a 503 and a WARN naming the panic's type, the flight logs the
// panic once at ERROR with the stack of the panic site, the process survives DoChan's
// re-panic path, and the panic's value reaches no log line and no body (ADR-081).
func TestServerProbeReadyContainsACheckPanic(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	markProbeReadyInProcess(t, srv)
	srv.appCheck.client.Transport = panickingTransport{}

	rec := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, probeTestNotReadyBody, rec.Body.String())
	requireAppListenerWarn(t, log)
	entry := findLogEntry(log.logEntries(), appListenerUnresponsiveMsg)
	require.NotNil(t, entry)
	assert.Equal(t, "application-listener check panicked (type: string)", entry.values["error"])
	panicked := findLogEntry(log.logEntries(), "Application-listener check panicked")
	require.NotNil(t, panicked)
	assert.Equal(t, "error", panicked.level)
	assert.Equal(t, entry.values["error"], panicked.values["error"])
	assert.Contains(t, panicked.values["stack"], "panickingTransport.RoundTrip", "the stack is the panic site's")
	for _, logged := range log.logEntries() {
		assert.NotContains(t, fmt.Sprintf("%+v", logged), checkPanicSentinel)
	}
	assert.NotContains(t, rec.Body.String(), checkPanicSentinel)
}
