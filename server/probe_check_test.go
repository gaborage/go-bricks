package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/internal/testutil"
)

const appListenerUnresponsiveMsg = "Application listener unresponsive"

var errDialRefused = errors.New("dial refused")

// stubApplicationListener stands in for the application listener the check HEADs: handler
// serves a live loopback socket, BoundAddr reports it, and srv's check is built against it.
func stubApplicationListener(t *testing.T, srv *Server, handler http.Handler) {
	t.Helper()
	app := httptest.NewServer(handler)
	t.Cleanup(app.Close)
	srv.onListenerBound(app.Listener.Addr())
	require.NoError(t, srv.buildAppListenerCheck(nil))
}

// markProbeReadyInProcess closes ReadyCh without Start, with the check pointed at a stub
// application listener answering the reserved route's 404.
func markProbeReadyInProcess(t *testing.T, srv *Server) {
	t.Helper()
	stubApplicationListener(t, srv, http.NotFoundHandler())
	require.NoError(t, srv.onBeforeServe(&http.Server{}))
}

// requireAppListenerWarn fails unless the check's WARN was logged with its error.
func requireAppListenerWarn(t *testing.T, log *testLogger) {
	t.Helper()
	entry := findLogEntry(log.logEntries(), appListenerUnresponsiveMsg)
	require.NotNil(t, entry, "the failed check must log its WARN")
	assert.Equal(t, "warn", entry.level)
	assert.NotEmpty(t, entry.values["error"])
}

// countingReadyHandler returns a ready override answering 200 and the count of its calls.
func countingReadyHandler() (Handler, *atomic.Int32) {
	calls := &atomic.Int32{}
	return func(c HandlerContext) error {
		calls.Add(1)
		return c.JSON(http.StatusOK, map[string]string{"status": "custom"})
	}, calls
}

// startProbeServer starts srv and fails unless it becomes ready.
func startProbeServer(t *testing.T, srv *Server) <-chan error {
	t.Helper()
	errCh := startServer(srv)
	waitForServerReady(t, srv)
	return errCh
}

// TestServerProbeReadyPassesAgainstTheLiveApplicationListener pins the check against a
// live application listener: its reserved route answers 404, which passes, so probe
// /ready reaches the registered handler on GET and HEAD and logs no WARN.
func TestServerProbeReadyPassesAgainstTheLiveApplicationListener(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(probeTestBase), log)
	handler, calls := countingReadyHandler()
	srv.RegisterReadyHandler(handler)
	errCh := startProbeServer(t, srv)

	for _, method := range probeMethods {
		res, err := doRequest(t.Context(), noKeepAliveClient(), method, probeURL(srv, testReadyRoute))
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, res.code, method)
	}
	assert.Equal(t, int32(len(probeMethods)), calls.Load())
	assert.Nil(t, findLogEntry(log.logEntries(), appListenerUnresponsiveMsg))

	shutdownAndDrain(t, srv, errCh)
}

// TestServerProbeReadyChGatesTheCheck pins the gate order: with the check built against a
// live application listener that would pass it, probe /ready answers 503 until ReadyCh
// closes, and the application listener receives no HEAD.
func TestServerProbeReadyChGatesTheCheck(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	var heads atomic.Int32
	stubApplicationListener(t, srv, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		heads.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))

	for _, method := range probeMethods {
		assert.Equal(t, http.StatusServiceUnavailable, serveEngine(srv.probeEcho, method, testReadyRoute).Code, method)
	}
	assert.Zero(t, heads.Load(), "the check must not run before ReadyCh closes")
	assert.Nil(t, findLogEntry(log.logEntries(), appListenerUnresponsiveMsg))
}

// TestServerProbeReadyLatchedMidCheckLogsNoWarn pins the latch re-read: a Shutdown that
// latches after the gate, while the check dials the listener it is closing, fails the
// check with a 503 but no WARN.
func TestServerProbeReadyLatchedMidCheckLogsNoWarn(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	markProbeReadyInProcess(t, srv)
	transport, ok := srv.appCheck.client.Transport.(*http.Transport)
	require.True(t, ok)
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		srv.stopping.Store(true)
		return nil, errDialRefused
	}

	rec := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, probeTestNotReadyBody, rec.Body.String())
	assert.Nil(t, findLogEntry(log.logEntries(), appListenerUnresponsiveMsg))
}

// TestServerProbeReadyAbandonedMidCheckLogsNoWarn pins the abandoned-probe rule: a probe
// whose own context ends while the check is in flight, as when the prober's timeout
// expires, fails the check with a 503 but no WARN, since the failure judges the probe, not
// the application listener.
func TestServerProbeReadyAbandonedMidCheckLogsNoWarn(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	markProbeReadyInProcess(t, srv)
	probeCtx, abandon := context.WithCancel(t.Context())
	t.Cleanup(abandon)
	transport, ok := srv.appCheck.client.Transport.(*http.Transport)
	require.True(t, ok)
	transport.DialContext = func(dialCtx context.Context, _, _ string) (net.Conn, error) {
		abandon()
		<-dialCtx.Done()
		return nil, dialCtx.Err()
	}

	rec := httptest.NewRecorder()
	srv.probeEcho.ServeHTTP(rec, httptest.NewRequestWithContext(probeCtx, http.MethodGet, testReadyRoute, http.NoBody))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, probeTestNotReadyBody, rec.Body.String())
	assert.Nil(t, findLogEntry(log.logEntries(), appListenerUnresponsiveMsg))
}

// TestServerProbeReadyFailsOnAnApplicationListener5xx pins the status rule: a 500 at the
// reserved route fails the check, so probe /ready answers 503 with a WARN and the
// registered handler never runs.
func TestServerProbeReadyFailsOnAnApplicationListener5xx(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(probeTestBase), log)
	reserved := probeTestBase + testReadyRoute
	srv.echo.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if c.Path() == reserved {
				return echo.ErrInternalServerError
			}
			return next(c)
		}
	})
	handler, calls := countingReadyHandler()
	srv.RegisterReadyHandler(handler)
	errCh := startProbeServer(t, srv)

	res, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, probeURL(srv, testReadyRoute))
	require.NoError(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, res.code)
	assert.JSONEq(t, probeTestNotReadyBody, res.body)
	assert.Zero(t, calls.Load())
	requireAppListenerWarn(t, log)

	shutdownAndDrain(t, srv, errCh)
}

// TestServerProbeReadyFailsWhenTheApplicationListenerCloses pins the connection rule: with
// the application listener closed and the probe listener still serving, probe /ready
// answers 503 with a WARN.
func TestServerProbeReadyFailsWhenTheApplicationListenerCloses(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	errCh := startProbeServer(t, srv)

	require.NoError(t, srv.httpServer.Load().Close())
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return once the application listener closed")
	}

	res, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, probeURL(srv, testReadyRoute))
	require.NoError(t, err, "the probe listener outlives the application listener")
	assert.Equal(t, http.StatusServiceUnavailable, res.code)
	assert.JSONEq(t, probeTestNotReadyBody, res.body)
	requireAppListenerWarn(t, log)

	require.NoError(t, srv.stopProbeListener(t.Context(), srv.probe.Load()))
	requireProbeErrorsClosed(t, srv)
}

// TestServerProbeReadyTimesOutASlowApplicationListener pins the check's 500ms budget: an
// application listener that never answers fails the check with a 503 and a WARN once the
// budget runs out, not before and not at the probe's own deadline.
func TestServerProbeReadyTimesOutASlowApplicationListener(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	stubApplicationListener(t, srv, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	require.NoError(t, srv.onBeforeServe(&http.Server{}))

	start := time.Now()
	rec := serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute)
	elapsed := time.Since(start)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.GreaterOrEqual(t, elapsed, 500*time.Millisecond)
	assert.Less(t, elapsed, 1500*time.Millisecond)
	requireAppListenerWarn(t, log)
}

// TestServerProbeReadyWithoutABuiltCheckIsNotReady pins fail-closed: a server whose Start
// never built the check answers 503 once ReadyCh closes rather than skipping the gate.
func TestServerProbeReadyWithoutABuiltCheckIsNotReady(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(""), log)
	require.NoError(t, srv.onBeforeServe(&http.Server{}))

	assert.Equal(t, http.StatusServiceUnavailable, serveEngine(srv.probeEcho, http.MethodGet, testReadyRoute).Code)
	requireAppListenerWarn(t, log)
}

// TestAppListenerCheckDialsFreshAndSendsOneHead pins the request the check sends: a HEAD of
// the reserved <base><ready path>, on a new connection every time, with a redirect taken
// as the answer rather than followed; and the transport that guarantees it.
func TestAppListenerCheckDialsFreshAndSendsOneHead(t *testing.T) {
	var conns, requests atomic.Int32
	var method, path atomic.Value
	stub := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		method.Store(r.Method)
		path.Store(r.URL.Path)
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	stub.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	stub.Start()
	t.Cleanup(stub.Close)

	check, err := newAppListenerCheck("127.0.0.1", probeTestBase+testReadyRoute, nil)
	require.NoError(t, err)
	const runs = 3
	for range runs {
		require.NoError(t, check.run(t.Context(), stub.Listener.Addr()))
	}

	assert.Equal(t, int32(runs), conns.Load(), "every check dials a fresh connection")
	assert.Equal(t, int32(runs), requests.Load(), "the redirect is not followed")
	assert.Equal(t, http.MethodHead, method.Load())
	assert.Equal(t, probeTestBase+testReadyRoute, path.Load())

	transport, ok := check.client.Transport.(*http.Transport)
	require.True(t, ok)
	assert.True(t, transport.DisableKeepAlives)
	assert.Nil(t, transport.Proxy, "the check never goes through a proxy")
	assert.Nil(t, transport.TLSClientConfig, "plain HTTP when server.tls is off")
	assert.Equal(t, "http", check.scheme)
}

// TestAppListenerCheckRejectsANonTCPAddress pins that the check refuses an address it
// cannot take a port from, such as the nil BoundAddr before the application bind.
func TestAppListenerCheckRejectsANonTCPAddress(t *testing.T) {
	check, err := newAppListenerCheck("127.0.0.1", testReadyRoute, nil)
	require.NoError(t, err)
	require.Error(t, check.run(t.Context(), nil))
}

// TestAppListenerCheckHonoursTheProbeRequestContext pins that the check runs on the probe
// request's context, so its cancellation or deadline bounds the check.
func TestAppListenerCheckHonoursTheProbeRequestContext(t *testing.T) {
	stub := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(stub.Close)
	check, err := newAppListenerCheck("127.0.0.1", testReadyRoute, nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, check.run(ctx, stub.Listener.Addr()), context.Canceled)
}

// TestAppListenerDialHost pins the dial-host table: an unspecified server.host dials its
// family's loopback, anything else dials as configured without brackets, and the address
// joins into a valid host:port. The check Start builds dials the host mapped from the
// configured server.host.
func TestAppListenerDialHost(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantHost string
		wantAddr string
	}{
		{name: "empty", host: "", wantHost: "127.0.0.1", wantAddr: "127.0.0.1:8080"},
		{name: "ipv4_unspecified", host: "0.0.0.0", wantHost: "127.0.0.1", wantAddr: "127.0.0.1:8080"},
		{name: "ipv6_unspecified", host: "::", wantHost: "::1", wantAddr: "[::1]:8080"},
		{name: "ipv6_unspecified_bracketed", host: "[::]", wantHost: "::1", wantAddr: "[::1]:8080"},
		{name: "ipv4_loopback", host: "127.0.0.1", wantHost: "127.0.0.1", wantAddr: "127.0.0.1:8080"},
		{name: "ipv6_loopback", host: "::1", wantHost: "::1", wantAddr: "[::1]:8080"},
		{name: "ipv6_loopback_bracketed", host: "[::1]", wantHost: "::1", wantAddr: "[::1]:8080"},
		{name: "hostname", host: "app.internal", wantHost: "app.internal", wantAddr: "app.internal:8080"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appListenerDialHost(tt.host)
			assert.Equal(t, tt.wantHost, got)
			assert.Equal(t, tt.wantAddr, hostPort(got, 8080))

			cfg := newProbeTestConfig("")
			cfg.Server.Host = tt.host
			srv := newProbeTestServer(cfg, &testLogger{})
			require.NoError(t, srv.buildAppListenerCheck(nil))
			assert.Equal(t, tt.wantHost, srv.appCheck.host)
		})
	}
}

// enabledServerTLS is server.tls enabled with certPEM and keyPEM as its values.
func enabledServerTLS(certPEM, keyPEM []byte) config.ServerTLSConfig {
	return config.ServerTLSConfig{
		Enabled:   true,
		CertValue: base64.StdEncoding.EncodeToString(certPEM),
		KeyValue:  base64.StdEncoding.EncodeToString(keyPEM),
	}
}

// serverTLSConfigFor loads certPEM and keyPEM the way Start does.
func serverTLSConfigFor(t *testing.T, certPEM, keyPEM []byte) *tls.Config {
	t.Helper()
	tlsValues := enabledServerTLS(certPEM, keyPEM)
	tlsCfg, err := buildServerTLSConfig(&tlsValues)
	require.NoError(t, err)
	return tlsCfg
}

// TestServerProbeReadyPinsTheTLSLeaf pins the check under server.tls: it speaks HTTPS and
// verifies the application listener against its own leaf, named by the first DNS SAN, else
// the first IP SAN, and never skips verification.
func TestServerProbeReadyPinsTheTLSLeaf(t *testing.T) {
	_, issueLeaf := newTestCAWithSANs(t, "test-ca")
	tests := []struct {
		name           string
		dnsNames       []string
		ips            []net.IP
		wantServerName string
	}{
		{name: "dns_san", dnsNames: []string{"localhost", "app.internal"}, ips: []net.IP{net.ParseIP("127.0.0.1")}, wantServerName: "localhost"},
		{name: "ip_san_only", ips: []net.IP{net.ParseIP("127.0.0.1")}, wantServerName: "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			certPEM, keyPEM := issueLeaf("leaf", tt.dnsNames, tt.ips)
			cfg := newProbeTestConfig(probeTestBase)
			cfg.Server.TLS = enabledServerTLS(certPEM, keyPEM)
			log := &testLogger{}
			srv := newProbeTestServer(cfg, log)
			errCh := startProbeServer(t, srv)

			res, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, probeURL(srv, testReadyRoute))
			require.NoError(t, err)
			assert.Equal(t, http.StatusOK, res.code)
			assert.Nil(t, findLogEntry(log.logEntries(), appListenerUnresponsiveMsg))

			transport, ok := srv.appCheck.client.Transport.(*http.Transport)
			require.True(t, ok)
			pinned := transport.TLSClientConfig
			require.NotNil(t, pinned)
			assert.False(t, pinned.InsecureSkipVerify, "verification is pinned, never skipped")
			assert.Equal(t, tt.wantServerName, pinned.ServerName)
			assert.NotNil(t, pinned.RootCAs)
			assert.GreaterOrEqual(t, pinned.MinVersion, uint16(tls.VersionTLS12))
			assert.Equal(t, "https", srv.appCheck.scheme)

			shutdownAndDrain(t, srv, errCh)
		})
	}
}

// TestAppListenerCheckRejectsACertificateItDidNotPin pins that the pin verifies: a check
// pinned to one leaf fails against a listener serving another, even one the same CA signed,
// and passes against the leaf it pinned.
func TestAppListenerCheckRejectsACertificateItDidNotPin(t *testing.T) {
	_, issueLeaf := newTestCAWithSANs(t, "test-ca")
	loopback := []net.IP{net.ParseIP("127.0.0.1")}
	servedCert, servedKey := issueLeaf("served", nil, loopback)
	otherCert, otherKey := issueLeaf("other", nil, loopback)
	served := serverTLSConfigFor(t, servedCert, servedKey)

	stub := httptest.NewUnstartedServer(http.NotFoundHandler())
	stub.TLS = served.Clone()
	stub.StartTLS()
	t.Cleanup(stub.Close)

	otherCheck, err := newAppListenerCheck("127.0.0.1", testReadyRoute, serverTLSConfigFor(t, otherCert, otherKey))
	require.NoError(t, err)
	require.Error(t, otherCheck.run(t.Context(), stub.Listener.Addr()), "a leaf the check did not pin must fail verification")

	servedCheck, err := newAppListenerCheck("127.0.0.1", testReadyRoute, served)
	require.NoError(t, err)
	require.NoError(t, servedCheck.run(t.Context(), stub.Listener.Addr()), "control: the pinned leaf verifies")
}

// TestServerStartRefusesATLSLeafWithoutSAN pins the Start-time refusal: with the probe
// listener and server.tls both enabled, a leaf with no SAN fails Start before either bind,
// naming server.probes.port and server.tls. With the probe listener off the same leaf is
// not judged.
func TestServerStartRefusesATLSLeafWithoutSAN(t *testing.T) {
	_, issueLeaf := newTestCAWithSANs(t, "test-ca")
	certPEM, keyPEM := issueLeaf("no-san", nil, nil)
	cfg := newProbeTestConfig("")
	cfg.Server.TLS = enabledServerTLS(certPEM, keyPEM)
	cfg.Server.Probes.Host = "127.0.0.1"
	cfg.Server.Probes.Port = testutil.ReserveFreePort(t)
	srv := New(cfg, &testLogger{})

	cfgErr := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite a TLS leaf the check cannot pin")
	assert.Equal(t, "server.probes.port", cfgErr.Field)
	assert.Contains(t, cfgErr.Error(), "server.tls")

	cfg.Server.Probes.Port = 0
	closeProbes, err := New(cfg, &testLogger{}).startProbeListener(serverTLSConfigFor(t, certPEM, keyPEM))
	require.NoError(t, err)
	closeProbes()
}

// TestServerStartRefusesATLSLeafThePinCannotVerify pins the Start-time verification: with
// the probe listener and server.tls both enabled, a leaf every check's handshake would
// reject, one issued for clientAuth only or one not yet valid, fails Start before either
// bind, naming server.probes.port and server.tls.
func TestServerStartRefusesATLSLeafThePinCannotVerify(t *testing.T) {
	_, issueLeaf := newTestCAWithSANs(t, "test-ca")
	tests := []struct {
		name string
		edit func(*x509.Certificate)
	}{
		{name: "client_auth_only", edit: func(leaf *x509.Certificate) {
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		}},
		{name: "not_yet_valid", edit: func(leaf *x509.Certificate) {
			leaf.NotBefore = time.Now().Add(time.Hour)
			leaf.NotAfter = time.Now().Add(2 * time.Hour)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			certPEM, keyPEM := issueLeaf("leaf", nil, []net.IP{net.ParseIP("127.0.0.1")}, tt.edit)
			cfg := newProbeTestConfig("")
			cfg.Server.TLS = enabledServerTLS(certPEM, keyPEM)
			cfg.Server.Probes.Host = "127.0.0.1"
			cfg.Server.Probes.Port = testutil.ReserveFreePort(t)
			srv := New(cfg, &testLogger{})

			cfgErr := requireStartRefusedBeforeBind(t, srv, "Start bound and served despite a TLS leaf the pin cannot verify")
			assert.Equal(t, "server.probes.port", cfgErr.Field)
			assert.Contains(t, cfgErr.Error(), "server.tls")
		})
	}
}

// TestPinnedLeafTLSConfigLeafSources pins where the pin's leaf comes from: the loaded
// certificate's parsed Leaf, else its first DER certificate, and an error when neither
// yields one.
func TestPinnedLeafTLSConfigLeafSources(t *testing.T) {
	_, issueLeaf := newTestCAWithSANs(t, "test-ca")
	certPEM, keyPEM := issueLeaf("leaf", []string{"app.internal"}, nil)
	loaded := serverTLSConfigFor(t, certPEM, keyPEM).Certificates[0]
	unparsed := tls.Certificate{Certificate: loaded.Certificate, PrivateKey: loaded.PrivateKey}

	tests := []struct {
		name    string
		certs   []tls.Certificate
		wantErr bool
	}{
		{name: "parsed_leaf", certs: []tls.Certificate{loaded}},
		{name: "unparsed_leaf", certs: []tls.Certificate{unparsed}},
		{name: "no_certificate", wantErr: true},
		{name: "empty_chain", certs: []tls.Certificate{{}}, wantErr: true},
		{name: "malformed_der", certs: []tls.Certificate{{Certificate: [][]byte{[]byte("not der")}}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinned, err := pinnedLeafTLSConfig(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: tt.certs})
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "app.internal", pinned.ServerName)
			assert.Equal(t, uint16(tls.VersionTLS13), pinned.MinVersion, "the check's floor follows the server's")
		})
	}
}

// TestServerProbeReadyPassesWhenTheApplicationLimiterRefusesTheCheck pins that the check stays
// inside the application listener's limiters and still passes there: a limiter's 429 is a live
// listener. The refusal is injected deterministically rather than by draining the real rate
// limiter, whose bucket refills on wall-clock time.
func TestServerProbeReadyPassesWhenTheApplicationLimiterRefusesTheCheck(t *testing.T) {
	log := &testLogger{}
	srv := newProbeTestServer(newProbeTestConfig(probeTestBase), log)
	reserved := probeTestBase + testReadyRoute
	var refused atomic.Int32
	srv.echo.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			if c.Request().Method == http.MethodHead && c.Path() == reserved {
				refused.Add(1)
				return echo.NewHTTPError(http.StatusTooManyRequests, msgRateLimitExceeded)
			}
			return next(c)
		}
	})
	handler, calls := countingReadyHandler()
	srv.RegisterReadyHandler(handler)
	errCh := startProbeServer(t, srv)

	res, err := doRequest(t.Context(), noKeepAliveClient(), http.MethodGet, probeURL(srv, testReadyRoute))
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, res.code)
	assert.Equal(t, int32(1), refused.Load(), "the check's HEAD must meet the refusal")
	assert.Equal(t, int32(1), calls.Load())
	assert.Nil(t, findLogEntry(log.logEntries(), appListenerUnresponsiveMsg))

	shutdownAndDrain(t, srv, errCh)
}
