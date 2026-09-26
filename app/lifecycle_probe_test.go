package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/server"
)

const (
	probeSeamPort       = 9091
	probeSeamKey        = "server.probes.port"
	probeRunDeadline    = 5 * time.Second
	drainedCleanMsg     = "Server goroutine completed successfully"
	drainedFailureMsg   = "Server error channel returned error"
	drainTimeoutWarnMsg = "Timeout waiting for server goroutine to complete - this may indicate a shutdown issue"
	drainTimeoutErrText = "failed to complete within timeout"
)

// probeSeamServer is a mockServer that also implements the probe seam; the test owns the
// probe listener's error channel.
type probeSeamServer struct {
	*mockServer
	probeErrs chan error
}

func newProbeSeamServer() *probeSeamServer {
	return &probeSeamServer{mockServer: newMockServer(), probeErrs: make(chan error, 1)}
}

func (s *probeSeamServer) ProbeErrors() <-chan error { return s.probeErrs }
func (s *probeSeamServer) ProbeBoundAddr() net.Addr  { return nil }

// failProbes sends err as the probe listener's serve error and then closes the channel,
// as the real listener's Serve goroutine does.
func (s *probeSeamServer) failProbes(err error) {
	s.probeErrs <- err
	close(s.probeErrs)
}

// quitSignalHandler hands Run's quit channel to the test once Run registers it, so a test
// can request shutdown without racing the registration.
type quitSignalHandler struct {
	registered chan chan<- os.Signal
}

func newQuitSignalHandler() *quitSignalHandler {
	return &quitSignalHandler{registered: make(chan chan<- os.Signal, 1)}
}

func (h *quitSignalHandler) Notify(c chan<- os.Signal, _ ...os.Signal) { h.registered <- c }
func (h *quitSignalHandler) WaitForSignal(<-chan os.Signal)            {}

func (h *quitSignalHandler) requestShutdown(t *testing.T) {
	t.Helper()
	select {
	case quit := <-h.registered:
		quit <- os.Interrupt
	case <-time.After(probeRunDeadline):
		t.Fatal("Run never registered its signal handler")
	}
}

func probeTestConfig(probePort int) *config.Config {
	cfg := &config.Config{App: config.AppConfig{Name: testApp, Env: "test", Version: "1.0.0"}}
	cfg.Server.Probes.Port = probePort
	return cfg
}

// newProbeRunApp builds a Run-able App around srv with a recording logger and a signal
// handler the test drives.
func newProbeRunApp(t *testing.T, cfg *config.Config, srv ServerRunner) (*App, *quitSignalHandler) {
	t.Helper()
	a := newLifecycleCheckAppWithLogger(t, cfg, &recLogger{})
	a.server = srv
	sig := newQuitSignalHandler()
	a.signalHandler = sig
	a.timeoutProvider = &StandardTimeoutProvider{}
	return a, sig
}

func runInBackground(a *App) <-chan error {
	done := make(chan error, 1)
	go func() { done <- a.Run() }()
	return done
}

// loggedMsg reports whether a's recording logger recorded an event with msg.
func loggedMsg(t *testing.T, a *App, msg string) bool {
	t.Helper()
	rec, ok := a.logger.(*recLogger)
	require.True(t, ok, "the App must log through a recLogger")
	return rec.firstLine(func(e *recEvent) bool { return e.msg == msg }) != nil
}

func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(probeRunDeadline):
		t.Fatal("Run did not return")
		return nil
	}
}

// TestRequireProbeSeam pins that server.probes.port is never silently ignored: with the
// port set, a server without the probe seam fails startup naming the key, and with it
// unset (or no config at all) the seam is not required.
func TestRequireProbeSeam(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *config.Config
		srv     ServerRunner
		wantErr bool
	}{
		{name: "nil_config_needs_no_seam", cfg: nil, srv: newMockServer()},
		{name: "unset_port_needs_no_seam", cfg: probeTestConfig(0), srv: newMockServer()},
		{name: "set_port_without_seam_fails", cfg: probeTestConfig(probeSeamPort), srv: newMockServer(), wantErr: true},
		{name: "set_port_with_seam_passes", cfg: probeTestConfig(probeSeamPort), srv: newProbeSeamServer()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := &App{cfg: tc.cfg, server: tc.srv}
			err := a.requireProbeSeam()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), probeSeamKey)
		})
	}
}

// TestPrepareRuntimeFailsClosedWithoutProbeSeam pins where the seam check runs: an
// injected server without the seam aborts startup before any kind starts, and one with
// the port unset starts as before.
func TestPrepareRuntimeFailsClosedWithoutProbeSeam(t *testing.T) {
	tests := []struct {
		name      string
		probePort int
		wantErr   bool
	}{
		{name: "set_port_aborts_before_kinds_start", probePort: probeSeamPort, wantErr: true},
		{name: "unset_port_starts", probePort: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newLifecycleCheckAppWithLogger(t, probeTestConfig(tc.probePort), &recLogger{})

			err := a.prepareRuntime(context.Background())

			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), probeSeamKey)
			assert.False(t, a.judge.started, "no kind may start once the probe seam check failed")
		})
	}
}

// TestRunEndsOnProbeListenerError pins the probe half of the fan-in: a serve error from
// the probe listener alone ends Run, which shuts the server down and returns that error.
func TestRunEndsOnProbeListenerError(t *testing.T) {
	srv := newProbeSeamServer()
	a, _ := newProbeRunApp(t, probeTestConfig(probeSeamPort), srv)
	probeErr := errors.New("probe listener serve failed")

	done := runInBackground(a)
	srv.failProbes(probeErr)

	err := awaitRun(t, done)
	require.ErrorIs(t, err, probeErr)
	assert.Equal(t, 1, srv.shutdownCount())
	assert.False(t, loggedMsg(t, a, drainedCleanMsg), "a failed serve must not be logged as a clean completion")
}

// TestRunIgnoresCleanProbeListenerStop pins that ProbeErrors closing without an error
// sends nothing: Run keeps serving until a shutdown signal, then returns cleanly.
func TestRunIgnoresCleanProbeListenerStop(t *testing.T) {
	srv := newProbeSeamServer()
	a, sig := newProbeRunApp(t, probeTestConfig(probeSeamPort), srv)

	done := runInBackground(a)
	close(srv.probeErrs)

	select {
	case err := <-done:
		t.Fatalf("Run returned %v after the probe listener stopped cleanly", err)
	case <-time.After(100 * time.Millisecond):
	}

	sig.requestShutdown(t)
	require.NoError(t, awaitRun(t, done))
	assert.Equal(t, 1, srv.shutdownCount())
	assert.True(t, loggedMsg(t, a, drainedCleanMsg))
}

// TestRunJoinsProbeErrorDrainedAfterApplicationError pins the drain on the server-error
// path: the application listener's error wakes Run, and a probe listener error that only
// arrives during shutdown is joined into Run's result rather than lost.
func TestRunJoinsProbeErrorDrainedAfterApplicationError(t *testing.T) {
	srv := newProbeSeamServer()
	appErr := errors.New("application listener serve failed")
	probeErr := errors.New("probe listener serve failed")
	srv.startErr = appErr
	srv.releaseStart()
	srv.onShutdown = func() { srv.failProbes(probeErr) }
	a, _ := newProbeRunApp(t, probeTestConfig(probeSeamPort), srv)

	err := awaitRun(t, runInBackground(a))

	require.ErrorIs(t, err, appErr)
	require.ErrorIs(t, err, probeErr)
}

// TestRunReturnsServerShutdownError pins that Run's result carries a failed application
// drain alongside the serve errors it joins.
func TestRunReturnsServerShutdownError(t *testing.T) {
	srv := newMockServer()
	shutdownErr := errors.New("application drain failed")
	srv.shutdownErr = shutdownErr
	a, sig := newProbeRunApp(t, probeTestConfig(0), srv)

	done := runInBackground(a)
	sig.requestShutdown(t)

	require.ErrorIs(t, awaitRun(t, done), shutdownErr)
}

// TestServeClosesErrorChannelOnlyAfterBothSenders pins the WaitGroup gate: whichever
// listener finishes first, its value arrives, the channel stays open for the other, and it
// closes exactly once after both — never a send on a closed channel.
func TestServeClosesErrorChannelOnlyAfterBothSenders(t *testing.T) {
	tests := []struct {
		name       string
		probeFirst bool
	}{
		{name: "probe_listener_finishes_first", probeFirst: true},
		{name: "start_finishes_first", probeFirst: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newProbeSeamServer()
			srv.startErr = nil
			a := &App{server: srv, logger: &recLogger{}}
			probeErr := errors.New("probe listener serve failed")
			finishProbes := func() { srv.failProbes(probeErr) }
			first, second := finishProbes, srv.releaseStart
			want := []error{probeErr, nil}
			if !tc.probeFirst {
				first, second = srv.releaseStart, finishProbes
				want = []error{nil, probeErr}
			}

			errCh := a.serve()
			first()
			got := make([]error, 0, 2)
			got = append(got, receiveOpen(t, errCh))
			select {
			case err, open := <-errCh:
				t.Fatalf("channel yielded (%v, open=%t) before the second sender finished", err, open)
			case <-time.After(50 * time.Millisecond):
			}
			second()
			got = append(got, receiveOpen(t, errCh))

			assert.Equal(t, want, got)
			requireClosed(t, errCh)
		})
	}
}

// TestServeBuffersBothSendersWithoutAReader pins the two-slot buffer: both senders finish
// with nobody reading, as on Run's shutdown-timeout exit, so neither goroutine leaks.
func TestServeBuffersBothSendersWithoutAReader(t *testing.T) {
	srv := newProbeSeamServer()
	srv.startErr = nil
	a := &App{server: srv, logger: &recLogger{}}
	probeErr := errors.New("probe listener serve failed")

	errCh := a.serve()
	srv.failProbes(probeErr)
	srv.releaseStart()

	require.Eventually(t, func() bool { return len(errCh) == 2 }, probeRunDeadline, time.Millisecond,
		"both senders must complete with nobody reading")
	assert.ElementsMatch(t, []error{probeErr, nil}, []error{<-errCh, <-errCh})
	requireClosed(t, errCh)
}

func receiveOpen(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err, open := <-ch:
		require.True(t, open, "channel closed before delivering a value")
		return err
	case <-time.After(probeRunDeadline):
		t.Fatal("no value arrived")
		return nil
	}
}

func requireClosed(t *testing.T, ch <-chan error) {
	t.Helper()
	select {
	case err, open := <-ch:
		require.False(t, open, "channel carried %v instead of closing", err)
	case <-time.After(probeRunDeadline):
		t.Fatal("channel did not close after its senders finished")
	}
}

// TestServeWithoutProbeSeamHasOneSender pins that a server without the seam gets no
// forwarder: the channel closes as soon as Start's result is sent.
func TestServeWithoutProbeSeamHasOneSender(t *testing.T) {
	srv := newMockServer()
	srv.releaseStart()
	a := &App{server: srv, logger: &recLogger{}}

	errCh := a.serve()

	require.ErrorIs(t, receiveOpen(t, errCh), http.ErrServerClosed)
	requireClosed(t, errCh)
}

// TestServeSkipsForwarderForNilProbeErrors pins that a seam returning a nil ProbeErrors
// gets no forwarder: ranging over nil would block forever and hold the channel open.
func TestServeSkipsForwarderForNilProbeErrors(t *testing.T) {
	srv := newProbeSeamServer()
	srv.probeErrs = nil
	srv.releaseStart()
	a := &App{server: srv, logger: &recLogger{}}

	errCh := a.serve()

	require.ErrorIs(t, receiveOpen(t, errCh), http.ErrServerClosed)
	requireClosed(t, errCh)
}

// TestForwardProbeErrorSendsOnlyTheFirstFailure pins the forwarder's at-most-once send of
// a real failure, which keeps serve's two-slot buffer from blocking and a clean stop from
// ending Run even if a server broke the ProbeErrors contract; it still reads the channel
// to its close.
func TestForwardProbeErrorSendsOnlyTheFirstFailure(t *testing.T) {
	first, second := errors.New("first"), errors.New("second")
	probeErrs := make(chan error, 4)
	probeErrs <- nil
	probeErrs <- http.ErrServerClosed
	probeErrs <- first
	probeErrs <- second
	close(probeErrs)
	errCh := make(chan error, 2)

	forwardProbeError(probeErrs, errCh)
	close(errCh)

	var got []error
	for err := range errCh {
		got = append(got, err)
	}
	assert.Equal(t, []error{first}, got)
	assert.Empty(t, probeErrs, "the forwarder reads ProbeErrors until it closes")
}

// TestDrainServerErrorJoinsEveryListenerFailure pins drain-until-closed: every failure
// either listener sent is joined, and clean stops are dropped value by value so the
// sentinel never masks a real failure in the joined result.
func TestDrainServerErrorJoinsEveryListenerFailure(t *testing.T) {
	appErr := errors.New("application listener serve failed")
	probeErr := errors.New("probe listener serve failed")
	tests := []struct {
		name   string
		values []error
		want   []error
	}{
		{name: "joins_both_listeners_failures", values: []error{appErr, probeErr}, want: []error{appErr, probeErr}},
		{name: "clean_stop_does_not_mask_a_failure", values: []error{http.ErrServerClosed, probeErr}, want: []error{probeErr}},
		{name: "clean_stops_drain_to_nil", values: []error{nil, http.ErrServerClosed}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan error, len(tc.values))
			for _, v := range tc.values {
				ch <- v
			}
			close(ch)
			rec := &recLogger{}
			a := &App{logger: rec}

			err := a.drainServerError(ch)

			logged := rec.firstLine(func(e *recEvent) bool { return e.msg == drainedFailureMsg })
			if len(tc.want) == 0 {
				require.NoError(t, err)
				assert.Nil(t, logged, "a clean drain logs no failure")
				return
			}
			for _, w := range tc.want {
				require.ErrorIs(t, err, w)
			}
			require.NotErrorIs(t, err, http.ErrServerClosed)
			require.NotNil(t, logged, "the joined failure is logged")
			assert.Equal(t, err.Error(), logged.err)
		})
	}
}

// TestDrainServerErrorWaitsForALateSender pins that the drain does not stop at the first
// value: Start may report only once its own drain ends, after the probe forwarder sent.
// The bound is the outer timeout, not the inner: with a 1ns shutdown timeout the late
// sender is still waited for.
func TestDrainServerErrorWaitsForALateSender(t *testing.T) {
	probeErr := errors.New("probe listener serve failed")
	appErr := errors.New("application listener serve failed")
	ch := make(chan error, 2)
	ch <- probeErr
	go func() {
		time.Sleep(50 * time.Millisecond)
		ch <- appErr
		close(ch)
	}()
	cfg := &config.Config{}
	cfg.Server.Timeout.Shutdown = time.Nanosecond
	a := &App{cfg: cfg, logger: &recLogger{}}

	err := a.drainServerError(ch)

	require.ErrorIs(t, err, probeErr)
	require.ErrorIs(t, err, appErr)
}

// TestReceiveServeFailuresStopsAtTimeout pins the outer bound: a sender that never
// finishes does not hold the drain past its timeout, and is itself reported as a failure.
func TestReceiveServeFailuresStopsAtTimeout(t *testing.T) {
	timeout := make(chan time.Time, 1)
	timeout <- time.Now()
	rec := &recLogger{}
	a := &App{logger: rec}

	failures := a.receiveServeFailures(make(chan error), timeout)

	require.Len(t, failures, 1)
	require.ErrorContains(t, failures[0], drainTimeoutErrText)
	assert.NotNil(t, rec.firstLine(func(e *recEvent) bool {
		return e.level == "warn" && e.msg == drainTimeoutWarnMsg
	}))
}

// TestReceiveServeFailuresKeepsFailuresCollectedBeforeTimeout pins that a timeout adds to
// the failures already received rather than replacing them. Once the send completes, ch
// has no sender left, so only timeout can be ready.
func TestReceiveServeFailuresKeepsFailuresCollectedBeforeTimeout(t *testing.T) {
	probeErr := errors.New("probe listener serve failed")
	ch := make(chan error)
	timeout := make(chan time.Time)
	go func() {
		ch <- probeErr
		timeout <- time.Now()
	}()
	a := &App{logger: &recLogger{}}

	failures := a.receiveServeFailures(ch, timeout)

	require.Len(t, failures, 2)
	require.ErrorIs(t, failures[0], probeErr)
	assert.ErrorContains(t, failures[1], drainTimeoutErrText)
}

// TestRunServesRealProbeListenerUntilShutdown runs the fan-in against a real server: the
// probe listener answers while Run serves, and a shutdown signal ends Run cleanly with
// ProbeErrors closed, so the drain saw both senders finish rather than timing out.
func TestRunServesRealProbeListenerUntilShutdown(t *testing.T) {
	cfg := probeTestConfig(testutil.ReserveFreePort(t))
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Timeout.Read = 5 * time.Second
	cfg.Server.Timeout.Write = 5 * time.Second
	srv := server.New(cfg, &recLogger{})
	a, sig := newProbeRunApp(t, cfg, srv)

	done := runInBackground(a)
	require.Eventually(t, func() bool { return srv.ProbeBoundAddr() != nil }, probeRunDeadline, 10*time.Millisecond)

	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: probeRunDeadline}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+srv.ProbeBoundAddr().String()+"/health", http.NoBody)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	sig.requestShutdown(t)
	require.NoError(t, awaitRun(t, done))
	_, open := <-srv.ProbeErrors()
	assert.False(t, open, "ProbeErrors must be closed once Run returns")
}

// TestPostRegisterRoutesSeesProbeDescriptorsOnTheirListener pins the route table the hook
// receives in both modes: exactly four probe descriptors, base-prefixed on the application
// listener at port 0, unprefixed on the probe listener with the port set, where the
// application listener's 404 reservations carry no descriptor.
func TestPostRegisterRoutesSeesProbeDescriptorsOnTheirListener(t *testing.T) {
	tests := []struct {
		name      string
		probePort int
		want      map[string]string // HandlerID -> Listener
	}{
		{name: "probe_listener_disabled", probePort: 0, want: map[string]string{
			"GET:/api/health": "", "HEAD:/api/health": "", "GET:/api/ready": "", "HEAD:/api/ready": "",
			"GET:/api/orders": "", "POST:/api/orders": "",
		}},
		{name: "probe_listener_enabled", probePort: probeSeamPort, want: map[string]string{
			"probes:GET:/health": server.ListenerProbes, "probes:HEAD:/health": server.ListenerProbes,
			"probes:GET:/ready": server.ListenerProbes, "probes:HEAD:/ready": server.ListenerProbes,
			"GET:/api/orders": "", "POST:/api/orders": "",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server.DefaultRouteRegistry.Clear()
			t.Cleanup(server.DefaultRouteRegistry.Clear)
			cfg := minimalAppConfig("/api")
			cfg.Server.Probes.Port = tt.probePort
			var got []server.RouteDescriptor
			a := newConfiguredApp(t, cfg, &Options{PostRegisterRoutes: func(routes []server.RouteDescriptor) error {
				got = routes
				return nil
			}})
			require.NoError(t, a.RegisterModule(&routeTableModule{name: "orders"}))

			require.NoError(t, a.prepareRuntime(context.Background()))

			require.Len(t, a.probeRoutes, 4)
			listeners := map[string]string{}
			for _, d := range got {
				listeners[d.HandlerID] = d.Listener
			}
			assert.Len(t, got, len(tt.want), "one descriptor per route, none for a reservation")
			assert.Equal(t, tt.want, listeners)
		})
	}
}
