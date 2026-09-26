package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
	"github.com/gaborage/go-bricks/logger"
)

const (
	probeTestBase         = "/api"
	probeTestNotReadyBody = `{"status":"not ready"}`
	probeStopOverrunMsg   = "Probe listener did not drain within its stop budget; closing it"
)

// newProbeTestConfig is newTestConfig with timeouts a held request outlives.
func newProbeTestConfig(basePath string) *config.Config {
	cfg := newTestConfig(basePath, "", "")
	cfg.Server.Timeout.Read = 5 * time.Second
	cfg.Server.Timeout.Write = 5 * time.Second
	return cfg
}

// newProbeTestServer builds a server whose probe listener binds 127.0.0.1:0.
func newProbeTestServer(cfg *config.Config, log *testLogger) *Server {
	return newServer(cfg, log, withEphemeralProbeListener())
}

// startServer runs Start in the background and returns its result channel.
func startServer(srv *Server) <-chan error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()
	return errCh
}

// noKeepAliveClient keeps each request on its own connection, so no idle connection
// outlives a test or holds a listener's drain open.
func noKeepAliveClient() *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 5 * time.Second}
}

type probeResponse struct {
	code   int
	body   string
	header http.Header
}

// doRequest sends method to url and returns the response it got.
func doRequest(ctx context.Context, client *http.Client, method, url string) (probeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, http.NoBody)
	if err != nil {
		return probeResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return probeResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return probeResponse{code: resp.StatusCode, body: string(body), header: resp.Header}, err
}

func probeURL(srv *Server, path string) string {
	return "http://" + srv.ProbeBoundAddr().String() + path
}

// serveEngine runs one in-process request through e.
func serveEngine(e *echo.Echo, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, http.NoBody)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

// heldHandler returns a handler that closes arrived and then blocks until release runs;
// release also runs at test cleanup.
func heldHandler(t *testing.T) (handler Handler, arrived <-chan struct{}, release func()) {
	t.Helper()
	arrivedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	release = sync.OnceFunc(func() { close(releaseCh) })
	t.Cleanup(release)
	return func(c HandlerContext) error {
		close(arrivedCh)
		<-releaseCh
		return c.String(http.StatusOK, "")
	}, arrivedCh, release
}

// sendAsync GETs url in the background and reports its outcome: nil only for a 200.
func sendAsync(url string) <-chan error {
	done := make(chan error, 1)
	go func() {
		res, err := doRequest(context.Background(), noKeepAliveClient(), http.MethodGet, url)
		if err == nil && res.code != http.StatusOK {
			err = fmt.Errorf("status %d", res.code)
		}
		done <- err
	}()
	return done
}

// actionLogValues returns key's value from every access-log (action) entry, in order.
func actionLogValues(log *testLogger, key string) []string {
	var values []string
	for _, e := range log.logEntries() {
		if e.values["log.type"] == "action" {
			values = append(values, e.values[key])
		}
	}
	return values
}

// requireProbeErrorsClosed fails unless ProbeErrors closes without carrying an error.
func requireProbeErrorsClosed(t *testing.T, srv *Server) {
	t.Helper()
	select {
	case err, open := <-srv.ProbeErrors():
		require.False(t, open, "ProbeErrors carried %v instead of closing", err)
	case <-time.After(2 * time.Second):
		t.Fatal("ProbeErrors was not closed")
	}
}

// occupyPort holds a loopback port for the rest of the test.
func occupyPort(t *testing.T) int {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return tcpAddr.Port
}

// TestServerProbeListenerServesUnprefixedProbes pins the probe listener's route table: it
// serves /health and /ready at their unprefixed paths, and nothing else — not the
// base-prefixed probe paths, not module routes. Its http.Server takes server.timeout.*.
func TestServerProbeListenerServesUnprefixedProbes(t *testing.T) {
	cfg := newProbeTestConfig(probeTestBase)
	cfg.Server.Timeout.Write = 6 * time.Second
	cfg.Server.Timeout.Idle = 7 * time.Second
	srv := newProbeTestServer(cfg, &testLogger{})
	srv.ModuleGroup().Add(http.MethodGet, "/orders", func(c HandlerContext) error {
		return c.String(http.StatusOK, "orders")
	})
	errCh := startServer(srv)
	waitForServerReady(t, srv)
	require.NotNil(t, srv.ProbeBoundAddr())
	assert.NotEqual(t, srv.BoundAddr().String(), srv.ProbeBoundAddr().String())
	probe := srv.probe.Load()
	require.NotNil(t, probe)
	probeSrv := probe.srv
	assert.Equal(t, 5*time.Second, probeSrv.ReadTimeout)
	assert.Equal(t, 6*time.Second, probeSrv.WriteTimeout)
	assert.Equal(t, 7*time.Second, probeSrv.IdleTimeout)
	assert.Equal(t, 5*time.Second, probeSrv.ReadHeaderTimeout)

	client := noKeepAliveClient()
	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
		wantBody string
	}{
		{name: "health_get", method: http.MethodGet, path: healthRoute, wantCode: http.StatusOK, wantBody: `"status":"ok"`},
		{name: "health_head", method: http.MethodHead, path: healthRoute, wantCode: http.StatusOK},
		{name: "ready_get", method: http.MethodGet, path: testReadyRoute, wantCode: http.StatusOK, wantBody: `"status":"ready"`},
		{name: "ready_head", method: http.MethodHead, path: testReadyRoute, wantCode: http.StatusOK},
		{name: "prefixed_ready_absent", method: http.MethodGet, path: probeTestBase + testReadyRoute, wantCode: http.StatusNotFound},
		{name: "module_route_absent", method: http.MethodGet, path: probeTestBase + "/orders", wantCode: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := doRequest(t.Context(), client, tt.method, probeURL(srv, tt.path))
			require.NoError(t, err)
			assert.Equal(t, tt.wantCode, res.code)
			assert.Contains(t, res.body, tt.wantBody)
		})
	}

	requireStartRefused(t, srv)
	select {
	case <-srv.ProbeErrors():
		t.Fatal("a refused second Start closed the running probe listener's ProbeErrors")
	default:
	}

	shutdownAndDrain(t, srv, errCh)
	requireProbeErrorsClosed(t, srv)
	_, err := doRequest(t.Context(), client, http.MethodGet, probeURL(srv, healthRoute))
	require.Error(t, err, "the probe listener must be closed once Shutdown returns")
}

// TestServerProbeReadyAnswers503UntilReadyCh pins the ReadyCh gate on a live probe
// listener: it serves before the application listener's readiness commit, and /ready
// answers 503 there until ReadyCh closes, while /health already answers 200.
func TestServerProbeReadyAnswers503UntilReadyCh(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	parked := make(chan struct{})
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(releaseOnce)
	srv.testHookReadyCommit = func() {
		close(parked)
		<-release
	}

	errCh := startServer(srv)
	select {
	case <-parked:
	case err := <-errCh:
		t.Fatalf("Start returned before the readiness commit: %v", err)
	}

	client := noKeepAliveClient()
	before, err := doRequest(t.Context(), client, http.MethodGet, probeURL(srv, testReadyRoute))
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, before.code)
	assert.JSONEq(t, probeTestNotReadyBody, before.body)
	health, err := doRequest(t.Context(), client, http.MethodGet, probeURL(srv, healthRoute))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, health.code)

	releaseOnce()
	waitForServerReady(t, srv)
	after, err := doRequest(t.Context(), client, http.MethodGet, probeURL(srv, testReadyRoute))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, after.code)

	shutdownAndDrain(t, srv, errCh)
}

// TestServerProbeReadyGatesInProcess pins the probe /ready gates in order without a
// listener: 503 before ReadyCh, the registered handler after, 503 again once stopping.
func TestServerProbeReadyGatesInProcess(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	calls := 0
	srv.RegisterReadyHandler(func(c HandlerContext) error {
		calls++
		return c.JSON(http.StatusOK, map[string]string{"status": "custom"})
	})

	for _, method := range probeMethods {
		assert.Equal(t, http.StatusServiceUnavailable, serveEngine(srv.probeEcho, method, testReadyRoute).Code)
	}
	assert.JSONEq(t, probeTestNotReadyBody, serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute).Body.String())
	assert.Zero(t, calls, "the override must not run before ReadyCh closes")

	require.NoError(t, srv.onBeforeServe(&http.Server{}))
	ready := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)
	assert.Equal(t, http.StatusOK, ready.Code)
	assert.JSONEq(t, `{"status":"custom"}`, ready.Body.String())
	require.Equal(t, 1, calls)

	require.NoError(t, srv.Shutdown(context.Background()))
	stopping := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)
	assert.Equal(t, http.StatusServiceUnavailable, stopping.Code)
	assert.JSONEq(t, probeTestNotReadyBody, stopping.Body.String())
	assert.Equal(t, 1, calls, "the override must not run while stopping")
}

// TestServerApplicationEngineReservesProbePaths pins the application engine's side: with
// the probe listener enabled it answers 404 at <base>/health and <base>/ready, on GET and
// HEAD, even beside a module param or wildcard route that would otherwise match them.
func TestServerApplicationEngineReservesProbePaths(t *testing.T) {
	tests := []struct {
		name        string
		route       string
		controlPath string
	}{
		{name: "param_route", route: "/:id", controlPath: probeTestBase + "/42"},
		{name: "wildcard_route", route: "/*", controlPath: probeTestBase + "/a/b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newProbeTestServer(newProbeTestConfig(probeTestBase), &testLogger{})
			for _, method := range probeMethods {
				srv.ModuleGroup().Add(method, tt.route, func(c HandlerContext) error { return c.String(http.StatusOK, "module") })
			}

			for _, path := range []string{probeTestBase + healthRoute, probeTestBase + testReadyRoute} {
				for _, method := range probeMethods {
					assert.Equal(t, http.StatusNotFound, serveEngine(srv.echo, method, path).Code, "%s %s", method, path)
				}
				assert.Contains(t, serveEngine(srv.echo, http.MethodGet, path).Body.String(), `"NOT_FOUND"`,
					"the 404 goes through the framework error envelope")
			}
			control := serveEngine(srv.echo, http.MethodGet, tt.controlPath)
			assert.Equal(t, "module", control.Body.String(), "control: the module route serves its own paths")
			assert.Empty(t, srv.RouteConflicts())
		})
	}
}

// TestServerReservedProbePathStillConflicts pins that the reservation stays in the conflict
// tracker with the probe listener enabled: a module claiming <base>/health still fails.
func TestServerReservedProbePathStillConflicts(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(probeTestBase), &testLogger{})
	srv.ModuleGroup().Add(http.MethodGet, healthRoute, func(c HandlerContext) error { return c.String(http.StatusOK, "") })

	conflicts := srv.RouteConflicts()
	require.Len(t, conflicts, 1)
	assert.Equal(t, probeTestBase+healthRoute, conflicts[0].Path)
	assert.Equal(t, "healthCheck", conflicts[0].First.HandlerName)
}

// TestServerProbeListenerDisabledByDefault pins port 0: no probe engine, ProbeErrors closed
// from New, no probe address, and the application engine serves the probes as before.
func TestServerProbeListenerDisabledByDefault(t *testing.T) {
	srv := New(newProbeTestConfig(probeTestBase), &testLogger{})
	assert.Nil(t, srv.probeEcho)
	requireProbeErrorsClosed(t, srv)

	assertHealthEndpoints(t, srv, probeTestBase+healthRoute, probeTestBase+testReadyRoute)

	errCh := startServer(srv)
	waitForServerReady(t, srv)
	assert.Nil(t, srv.ProbeBoundAddr())
	shutdownAndDrain(t, srv, errCh)
	assert.Nil(t, srv.probe.Load())
}

// TestServerNewDerivesProbeAddressFromEffectiveHost pins that New binds the address
// validation judged: server.probes.host when set, else server.host.
func TestServerNewDerivesProbeAddressFromEffectiveHost(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Probes.Port = 9091
	assert.Equal(t, "127.0.0.1:9091", New(cfg, &testLogger{}).probeAddr)

	cfg.Server.Probes.Host = "localhost"
	assert.Equal(t, "localhost:9091", New(cfg, &testLogger{}).probeAddr)

	for _, host := range []string{"::1", "[::1]"} {
		cfg.Server.Probes.Host = host
		assert.Equal(t, "[::1]:9091", New(cfg, &testLogger{}).probeAddr, "IPv6 host %q", host)
	}
}

// TestServerProbeBindFailureStopsStart pins that a probe bind failure returns from Start
// before the application listener binds: no BoundAddr, ReadyCh open, ProbeErrors closed.
func TestServerProbeBindFailureStopsStart(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Probes.Host = "127.0.0.1"
	cfg.Server.Probes.Port = occupyPort(t)
	srv := New(cfg, &testLogger{})

	select {
	case err := <-startServer(srv):
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start served despite the probe bind failing")
	}
	assert.Nil(t, srv.BoundAddr(), "the application listener must not bind after the probe bind failed")
	assert.Nil(t, srv.ProbeBoundAddr())
	assert.False(t, isClosed(srv.ReadyCh()))
	requireProbeErrorsClosed(t, srv)
}

// TestServerApplicationBindFailureClosesProbeListener pins cleanup after the probe bind:
// an application bind failure closes the probe listener before Start returns, so its port
// is free again.
func TestServerApplicationBindFailureClosesProbeListener(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Port = occupyPort(t)
	srv := newProbeTestServer(cfg, &testLogger{})

	require.Error(t, srv.Start())
	probeAddr := srv.ProbeBoundAddr()
	require.NotNil(t, probeAddr, "the probe listener binds before the application listener")
	assert.False(t, isClosed(srv.ReadyCh()))

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", probeAddr.String())
	require.NoError(t, err, "the probe port must be free once Start returns")
	require.NoError(t, ln.Close())
	requireProbeErrorsClosed(t, srv)
}

// TestServerStartRefusesProbeCollision pins the Start-time re-check for a Go-assembled
// config that skipped validation: probe and application listener on one port and an
// unspecified host fail before either bind, naming the key.
func TestServerStartRefusesProbeCollision(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.Port = testutil.ReserveFreePort(t)
	cfg.Server.Probes.Host = "127.0.0.1"
	cfg.Server.Probes.Port = cfg.Server.Port
	srv := New(cfg, &testLogger{})

	var err error
	select {
	case err = <-startServer(srv):
	case <-time.After(2 * time.Second):
		t.Fatal("Start bound and served despite the probe listener colliding with the application listener")
	}

	var cfgErr *config.ConfigError
	require.ErrorAs(t, err, &cfgErr)
	assert.Equal(t, "server.probes.port", cfgErr.Field)
	assert.Nil(t, srv.BoundAddr())
	assert.Nil(t, srv.ProbeBoundAddr())
	requireProbeErrorsClosed(t, srv)
}

// TestServerStartTLSFailureClosesProbeErrors pins the exit before the probe bind: a TLS
// config Start cannot build returns without binding either listener.
func TestServerStartTLSFailureClosesProbeErrors(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.TLS = config.ServerTLSConfig{Enabled: true, CertFile: "/nonexistent/cert.pem", KeyFile: "/nonexistent/key.pem"}
	srv := newProbeTestServer(cfg, &testLogger{})

	require.Error(t, srv.Start())
	assert.Nil(t, srv.ProbeBoundAddr())
	requireProbeErrorsClosed(t, srv)
}

// TestServerShutdownBeforeStartVetoesProbeListener pins the latch on both listeners:
// Start returns http.ErrServerClosed and binds neither.
func TestServerShutdownBeforeStartVetoesProbeListener(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	require.NoError(t, srv.Shutdown(context.Background()))

	require.ErrorIs(t, srv.Start(), http.ErrServerClosed)
	assert.Nil(t, srv.BoundAddr())
	assert.Nil(t, srv.ProbeBoundAddr())
	requireProbeErrorsClosed(t, srv)
}

// TestServerProbeStoreVetoedByLatch pins the probe store's own critical section: a latch
// set between Start's first check and the store vetoes it, closing the listener.
func TestServerProbeStoreVetoedByLatch(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Probes.Host = "127.0.0.1"
	cfg.Server.Probes.Port = testutil.ReserveFreePort(t)
	srv := New(cfg, &testLogger{})
	srv.stopping.Store(true)

	closeProbes, err := srv.startProbeListener()

	require.ErrorIs(t, err, http.ErrServerClosed)
	assert.Nil(t, closeProbes)
	assert.Nil(t, srv.probe.Load())
	assert.Nil(t, srv.ProbeBoundAddr())
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Server.Probes.Port))
	require.NoError(t, err, "a vetoed store must close the listener it bound")
	require.NoError(t, ln.Close())
	requireProbeErrorsClosed(t, srv)
}

// TestServerShutdownReleasesAnUntrackedProbeListener pins the stop for a probe listener
// stored before its Serve goroutine tracked the socket: http.Server.Shutdown closes nothing
// then, so Shutdown must close the socket itself before it returns.
func TestServerShutdownReleasesAnUntrackedProbeListener(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv.probe.Store(&probeListener{srv: &http.Server{ReadHeaderTimeout: time.Second}, ln: ln})

	require.NoError(t, srv.Shutdown(context.Background()))

	rebound, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", ln.Addr().String())
	require.NoError(t, err, "Shutdown must release a probe socket Serve never tracked")
	require.NoError(t, rebound.Close())
}

// TestServerShutdownStopsProbeListenerLast pins the shutdown order: while the application
// listener drains a held request, the probe listener still serves and /ready answers 503;
// once Shutdown returns the probe listener is closed too.
func TestServerShutdownStopsProbeListenerLast(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	slow, arrived, release := heldHandler(t)
	srv.ModuleGroup().Add(http.MethodGet, "/slow", slow)

	errCh := startServer(srv)
	waitForServerReady(t, srv)
	client := noKeepAliveClient()
	held := sendAsync("http://" + srv.BoundAddr().String() + "/slow")
	<-arrived

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Shutdown(ctx) }()
	require.Eventually(t, srv.stopping.Load, 2*time.Second, 5*time.Millisecond)

	during, err := doRequest(t.Context(), client, http.MethodGet, probeURL(srv, testReadyRoute))
	require.NoError(t, err, "the probe listener must serve throughout the application drain")
	assert.Equal(t, http.StatusServiceUnavailable, during.code)
	assert.JSONEq(t, probeTestNotReadyBody, during.body)
	select {
	case shutdownErr := <-shutdownDone:
		t.Fatalf("Shutdown returned while the application listener was still draining: %v", shutdownErr)
	default:
	}

	release()
	require.NoError(t, <-held)
	require.NoError(t, <-shutdownDone)
	require.NoError(t, <-errCh)
	requireProbeErrorsClosed(t, srv)
	_, err = doRequest(t.Context(), client, http.MethodGet, probeURL(srv, testReadyRoute))
	require.Error(t, err, "the probe listener must be closed once Shutdown returns")
}

// TestServerProbeStopOverrunClosesAndWarns pins the probe listener's detached stop budget:
// a probe still in flight at the deadline is cut off with a WARN, and Shutdown succeeds.
func TestServerProbeStopOverrunClosesAndWarns(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	srv.probeStopBudget = 50 * time.Millisecond
	ready, arrived, release := heldHandler(t)
	srv.RegisterReadyHandler(ready)

	errCh := startServer(srv)
	waitForServerReady(t, srv)
	held := sendAsync(probeURL(srv, testReadyRoute))
	<-arrived

	require.NoError(t, srv.Shutdown(context.Background()), "a probe overrunning its budget alone is not a Shutdown error")
	entry := findLogEntry(log.logEntries(), probeStopOverrunMsg)
	require.NotNil(t, entry)
	assert.Equal(t, "warn", entry.level)
	require.Error(t, <-held, "Close cuts the held probe off")
	release()
	require.NoError(t, <-errCh)
	requireProbeErrorsClosed(t, srv)
}

// failingListener is a net.Listener whose Accept fails permanently.
type failingListener struct{}

var errAcceptFailed = errors.New("accept failed")

func (failingListener) Accept() (net.Conn, error) { return nil, errAcceptFailed }
func (failingListener) Close() error              { return nil }
func (failingListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// TestServerProbeServeErrorReachesProbeErrors pins the one error ProbeErrors carries: a
// serve failure other than http.ErrServerClosed is sent, then the channel closes.
func TestServerProbeServeErrorReachesProbeErrors(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})

	srv.serveProbes(&http.Server{ReadHeaderTimeout: time.Second}, failingListener{})

	require.ErrorIs(t, <-srv.ProbeErrors(), errAcceptFailed)
	requireProbeErrorsClosed(t, srv)
}

// TestServerProbeEngineChain pins the probe engine's middleware: no rate limiter or IP
// pre-guard (a burst far past both never answers 429 there, while the application engine's
// own reserved path does), and the Secure headers and request ID still apply.
func TestServerProbeEngineChain(t *testing.T) {
	cfg := newProbeTestConfig(probeTestBase)
	cfg.App.Rate.Limit = 1
	cfg.App.Rate.IPPreGuard.Enabled = true
	cfg.App.Rate.IPPreGuard.Threshold = 1
	srv := newProbeTestServer(cfg, &testLogger{})
	require.NoError(t, srv.onBeforeServe(&http.Server{}))

	const burst = 20
	appLimited := 0
	for i := range burst {
		probe := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)
		require.Equal(t, http.StatusOK, probe.Code, "probe request %d", i)
		if serveEngine(srv.echo, http.MethodGet, probeTestBase+testReadyRoute).Code == http.StatusTooManyRequests {
			appLimited++
		}
	}
	assert.Positive(t, appLimited, "control: the same burst must trip the application engine's limiter")

	probe := serveEngine(srv.probeEcho, http.MethodGet, healthRoute)
	assert.Equal(t, "nosniff", probe.Header().Get(echo.HeaderXContentTypeOptions))
	assert.Equal(t, "SAMEORIGIN", probe.Header().Get(echo.HeaderXFrameOptions))
	assert.NotEmpty(t, probe.Header().Get(echo.HeaderXRequestID))
}

// TestServerShutdownStopsProbeListenerWhenTheDrainFails pins that a failed application
// drain is returned and still stops the probe listener, closing ProbeErrors.
func TestServerShutdownStopsProbeListenerWhenTheDrainFails(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	slow, arrived, release := heldHandler(t)
	srv.ModuleGroup().Add(http.MethodGet, "/slow", slow)
	errCh := startServer(srv)
	waitForServerReady(t, srv)
	held := sendAsync("http://" + srv.BoundAddr().String() + "/slow")
	<-arrived

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	require.ErrorIs(t, srv.Shutdown(ctx), context.DeadlineExceeded, "the application drain's error is returned")
	requireProbeErrorsClosed(t, srv)
	_, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, probeURL(srv, healthRoute))
	require.Error(t, err, "the probe listener stops even when the application drain fails")

	release()
	<-held
	select {
	case <-errCh:
	case <-time.After(15 * time.Second):
		t.Fatal("Start did not return")
	}
}

// TestServerProbeStopIsDetachedFromShutdownContext pins the probe stop's own budget: a
// caller's already-canceled ctx neither fails the stop nor cuts a probe in flight, and a
// clean stop logs no overrun.
func TestServerProbeStopIsDetachedFromShutdownContext(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	assert.Equal(t, time.Second, srv.probeStopBudget)
	ready, arrived, release := heldHandler(t)
	srv.RegisterReadyHandler(ready)
	errCh := startServer(srv)
	waitForServerReady(t, srv)
	probeAddr := srv.ProbeBoundAddr().String()
	held := sendAsync(probeURL(srv, testReadyRoute))
	<-arrived

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- srv.Shutdown(canceled) }()
	// Release only once the probe socket is closed, so the probe is in flight when the stop
	// consults its own ctx.
	require.Eventually(t, func() bool {
		conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(t.Context(), "tcp", probeAddr)
		if err != nil {
			return true
		}
		_ = conn.Close()
		return false
	}, 2*time.Second, 5*time.Millisecond)
	release()

	require.NoError(t, <-shutdownDone, "the probe stop runs on its own budget, not the caller's canceled ctx")
	require.NoError(t, <-held, "a probe in flight within the budget completes")
	assert.Nil(t, findLogEntry(log.logEntries(), probeStopOverrunMsg), "a clean stop logs no overrun")
	require.NoError(t, <-errCh)
	requireProbeErrorsClosed(t, srv)
}

var errProbeCloseFailed = errors.New("probe close failed")

// closeErrListener is a net.Listener whose Close fails after closing the socket.
type closeErrListener struct{ net.Listener }

func (l closeErrListener) Close() error {
	_ = l.Listener.Close()
	return errProbeCloseFailed
}

// TestServerShutdownReturnsANonDeadlineProbeStopError pins that only the stop budget's
// deadline is swallowed: any other probe-stop error reaches Shutdown's caller.
func TestServerShutdownReturnsANonDeadlineProbeStopError(t *testing.T) {
	srv := newProbeTestServer(newProbeTestConfig(""), &testLogger{})
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	probeSrv := &http.Server{Handler: srv.probeEcho, ReadHeaderTimeout: time.Second}
	srv.probe.Store(&probeListener{srv: probeSrv, ln: ln})
	go func() { _ = probeSrv.Serve(closeErrListener{ln}) }()
	// One served request proves Serve has tracked the listener.
	res, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, "http://"+ln.Addr().String()+healthRoute)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, res.code)

	require.ErrorIs(t, srv.Shutdown(context.Background()), errProbeCloseFailed)
}

// TestServerProbeEnginePanicIsRecoveredByTypeOnly pins the probe engine's Recover,
// sanitizePanicValue and shared error handler (ADR-081): a panicking probe answers the
// framework's 500 envelope, and neither the body nor any log field carries the value.
func TestServerProbeEnginePanicIsRecoveredByTypeOnly(t *testing.T) {
	log := &testLogger{}
	cfg := newProbeTestConfig("")
	cfg.App.Debug = true // the debug branch logs the error itself; only sanitizePanicValue keeps the value out
	srv := newProbeTestServer(cfg, log)
	require.NoError(t, srv.onBeforeServe(&http.Server{}))
	srv.RegisterReadyHandler(func(HandlerContext) error { panic(recoverProbeSecret) })

	rec := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error"`, "the framework envelope, not echo's default body")
	assert.NotContains(t, rec.Body.String(), recoverProbeSecret)
	entry := findLogEntry(log.logEntries(), "Panic recovered")
	require.NotNil(t, entry)
	assert.Contains(t, entry.fields, "stack", "Echo's Recover caught it and httpErrorHandler logged it")
	for _, e := range log.logEntries() {
		for key, value := range e.values {
			assert.NotContains(t, value, recoverProbeSecret, "%q field %q carries the panic value", e.msg, key)
		}
	}
}

// armedPanicLogger panics from WithContext once armed, so the request logger panics
// between the outermost recover and Echo's Recover.
type armedPanicLogger struct {
	testLogger
	armed atomic.Bool
}

func (l *armedPanicLogger) WithContext(any) logger.Logger {
	if l.armed.Load() {
		panic(recoverProbeSecret)
	}
	return l
}

// TestServerProbeEngineOutermostRecoverCatchesPreRecoverPanic pins the probe engine's
// outermost recover: a panic above Echo's Recover is caught and reported by type.
func TestServerProbeEngineOutermostRecoverCatchesPreRecoverPanic(t *testing.T) {
	log := &armedPanicLogger{}
	srv := newServer(newProbeTestConfig(""), log, withEphemeralProbeListener())
	log.armed.Store(true)

	var rec *httptest.ResponseRecorder
	require.NotPanics(t, func() { rec = serveEngine(srv.probeEcho, http.MethodGet, "/not-a-probe") })

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	entry := findLogEntry(log.logEntries(), "Panic recovered")
	require.NotNil(t, entry)
	assert.Equal(t, "string", entry.values["panic_type"])
}

// TestServerProbeEngineAppliesMiddlewareTimeout pins server.timeout.middleware on the probe
// chain: a probe's request context carries a deadline.
func TestServerProbeEngineAppliesMiddlewareTimeout(t *testing.T) {
	cfg := newProbeTestConfig("")
	cfg.Server.Timeout.Middleware = 2 * time.Second
	srv := newProbeTestServer(cfg, &testLogger{})
	require.NoError(t, srv.onBeforeServe(&http.Server{}))
	var hasDeadline bool
	srv.RegisterReadyHandler(func(c HandlerContext) error {
		_, hasDeadline = c.RequestContext().Deadline()
		return c.String(http.StatusOK, "")
	})

	serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)

	assert.True(t, hasDeadline, "server.timeout.middleware bounds a probe request")
}

// TestServerProbeEngineRequestLoggerSkipsOnlyItsProbePaths pins the probe engine's access
// logger: it skips the unprefixed probe paths it serves, a pre-ReadyCh 503 included, and
// logs everything else.
func TestServerProbeEngineRequestLoggerSkipsOnlyItsProbePaths(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(probeTestBase), log)

	serveEngine(srv.probeEcho, http.MethodGet, healthRoute)
	serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute) // 503 before ReadyCh: a WARN if it were logged
	serveEngine(srv.probeEcho, http.MethodGet, probeTestBase+testReadyRoute)

	assert.Equal(t, []string{probeTestBase + testReadyRoute}, actionLogValues(log, "url.path"))
}

// TestServerProbeEngineSharesTheClientIPExtractor pins that the probe access log resolves
// client.address by the application engine's trusted-proxy walk, not the raw peer.
func TestServerProbeEngineSharesTheClientIPExtractor(t *testing.T) {
	log := &testLogger{}
	cfg := newProbeTestConfig("")
	cfg.Server.TrustedProxies = []string{"192.0.2.0/24"}
	srv := newProbeTestServer(cfg, log)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/not-a-probe", http.NoBody)
	req.RemoteAddr = "192.0.2.1:1234"
	req.Header.Set(echo.HeaderXForwardedFor, "203.0.113.9")

	srv.probeEcho.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, []string{"203.0.113.9"}, actionLogValues(log, "client.address"))
}
