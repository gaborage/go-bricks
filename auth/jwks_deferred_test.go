package auth

import (
	"bufio"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"net"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
	obstesting "github.com/gaborage/go-bricks/observability/testing"
)

const (
	deferredWarnMessage = "auth: issuer key set fetch failed at startup; verifier starts without it"
	deferredInfoMessage = "auth: issuer key set fetched; deferred key set filled"
)

// syncBuffer is a log sink safe for the refresh goroutine to write to while the
// test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newBufferLogger is a logger.Logger writing JSON lines into a buffer, the
// sink-in-context form httpclient's tests use, so no test captures stdout.
func newBufferLogger() (log logger.Logger, sink *syncBuffer) {
	sink = &syncBuffer{}
	ctx := zerolog.New(sink).WithContext(context.Background())
	return logger.New("info", false).WithContext(ctx), sink
}

// logEntries parses every JSON line the sink holds.
func logEntries(t *testing.T, sink *syncBuffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(sink.String()))
	for scanner.Scan() {
		var entry map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &entry))
		entries = append(entries, entry)
	}
	return entries
}

// entriesWithMessage returns the entries at level carrying message.
func entriesWithMessage(t *testing.T, sink *syncBuffer, level, message string) []map[string]any {
	t.Helper()
	var matched []map[string]any
	for _, entry := range logEntries(t, sink) {
		if entry["level"] == level && entry["message"] == message {
			matched = append(matched, entry)
		}
	}
	return matched
}

// installDeferredResolverClock hands the resolver a fake clock WITHOUT
// re-basing fetchedAt, which installResolverClock does and which would turn a
// never-fetched key set into a fetched, empty one.
func installDeferredResolverClock(t *testing.T, v *Verifier, clock *fakeClock) *jwksResolver {
	t.Helper()
	r := v.owned
	require.NotNil(t, r, "NewVerifier must own the resolver it constructed")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = clock.Now
	return r
}

// newDeferredVerifier builds a verifier with a deferred key set over srv, which
// the caller has already put into a failing mode, and registers its Close.
func newDeferredVerifier(t *testing.T, srv *authtesting.JWKSServer, log logger.Logger, mp metric.MeterProvider) *Verifier {
	t.Helper()
	v, err := NewVerifier(jwksConfig(srv), log, mp, jwksClient(t, srv), WithDeferredKeySet())
	require.NoError(t, err)
	require.NotNil(t, v)
	t.Cleanup(func() { require.NoError(t, v.Close()) })
	v.now = fixedClock
	return v
}

// fastTickConfig makes the background refresh tick every 20ms on the real
// clock, so the ticker path is exercised without a clock seam.
func fastTickConfig(srv *authtesting.JWKSServer) Config {
	cfg := jwksConfig(srv)
	cfg.JWKS.TTL = 40 * time.Millisecond
	cfg.JWKS.MinRefreshInterval = 20 * time.Millisecond
	return cfg
}

// transportErrorClient fails every request with err and no response.
type transportErrorClient struct {
	httpclient.Client
	err error
}

func (c transportErrorClient) Get(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return nil, c.err
}

// buildClient wraps a net/http client in an httpclient with the given timeout.
func buildClient(t *testing.T, httpClient *nethttp.Client, timeout time.Duration) httpclient.Client {
	t.Helper()
	builder := httpclient.NewBuilder(logger.New("error", false)).WithHTTPClient(httpClient)
	if timeout > 0 {
		builder = builder.WithTimeout(timeout)
	}
	client, err := builder.Build()
	require.NoError(t, err)
	return client
}

// statusTLSServer answers every request with code over TLS.
func statusTLSServer(t *testing.T, code int) (uri string, client httpclient.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + authtesting.JWKSPath, buildClient(t, srv.Client(), 0)
}

// hungTLSServer never answers until the test ends.
func hungTLSServer(t *testing.T) (uri string, client httpclient.Client) {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewTLSServer(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL + authtesting.JWKSPath, buildClient(t, srv.Client(), 50*time.Millisecond)
}

// refusedURI is an https endpoint nothing listens on.
func refusedURI(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return "https://" + addr + authtesting.JWKSPath
}

// mismatchedHostClient dials srv whatever host the URL names, so a URL under a
// name the certificate does not carry fails hostname verification against a
// trusted certificate.
func mismatchedHostClient(t *testing.T, srv *authtesting.JWKSServer) (uri string, client httpclient.Client) {
	t.Helper()
	base, ok := srv.HTTPClient().Transport.(*nethttp.Transport)
	require.True(t, ok)
	transport := base.Clone()
	addr := strings.TrimPrefix(strings.TrimSuffix(srv.URL(), authtesting.JWKSPath), "https://")
	var dialer net.Dialer
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, addr)
	}
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return "https://mismatch.test:" + port + authtesting.JWKSPath, buildClient(t, &nethttp.Client{Transport: transport}, 0)
}

func TestNewVerifierWithDeferredKeySetToleratesAnOutage(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, cfg *Config) httpclient.Client
		stage string
	}{
		{
			name: "connection_refused",
			setup: func(t *testing.T, cfg *Config) httpclient.Client {
				cfg.JWKSURI = refusedURI(t)
				return buildClient(t, &nethttp.Client{}, 0)
			},
			stage: fetchStageConnect,
		},
		{
			name: "timeout",
			setup: func(t *testing.T, cfg *Config) httpclient.Client {
				uri, client := hungTLSServer(t)
				cfg.JWKSURI = uri
				return client
			},
			stage: fetchStageTimeout,
		},
		{
			name: "status_503",
			setup: func(t *testing.T, cfg *Config) httpclient.Client {
				srv := newJWKSFixture(t)
				srv.SetMode(authtesting.JWKSServerError)
				cfg.JWKSURI = srv.URL()
				return jwksClient(t, srv)
			},
			stage: fetchStageStatus,
		},
		{
			name: "status_429",
			setup: func(t *testing.T, cfg *Config) httpclient.Client {
				uri, client := statusTLSServer(t, nethttp.StatusTooManyRequests)
				cfg.JWKSURI = uri
				return client
			},
			stage: fetchStageStatus,
		},
		{
			name: "temporary_dns_failure",
			setup: func(_ *testing.T, cfg *Config) httpclient.Client {
				cfg.JWKSURI = "https://idp.test/jwks.json"
				return transportErrorClient{err: viaTransport(&net.DNSError{Err: "server misbehaving", Name: "idp.test", IsTemporary: true})}
			},
			stage: fetchStageDNS,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			iss := newTestIssuer()
			cfg := verifierConfig(iss)
			cfg.JWKS = config.AuthJWKSConfig{TTL: testTTL, StaleCeiling: testStaleCeiling, MinRefreshInterval: testMinRefresh, MaxBodyBytes: testMaxBody}
			client := tc.setup(t, &cfg)
			log, sink := newBufferLogger()

			v, err := NewVerifier(cfg, log, nil, client, WithDeferredKeySet())

			require.NoError(t, err)
			require.NotNil(t, v)
			t.Cleanup(func() { require.NoError(t, v.Close()) })
			v.now = fixedClock
			_, verifyErr := v.Verify(context.Background(), iss.Mint(authtesting.Claims{}))
			require.ErrorIs(t, verifyErr, ErrKeySetUnavailable)
			warns := entriesWithMessage(t, sink, "warn", deferredWarnMessage)
			require.Len(t, warns, 1)
			assert.Equal(t, failureClassOutage, warns[0][logFieldFailureClass])
			assert.Equal(t, tc.stage, warns[0][logFieldFailureStage])
		})
	}
}

// TestNewVerifierWithDeferredKeySetStillFailsOnAConfigurationError runs every
// configuration-class fixture with and without the option and requires the
// SAME construction error from both.
func TestNewVerifierWithDeferredKeySetStillFailsOnAConfigurationError(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T) (Config, logger.Logger, httpclient.Client)
		check func(t *testing.T, err error)
	}{
		{
			name: "tls_unknown_authority",
			setup: func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
				srv := newJWKSFixture(t)
				return jwksConfig(srv), logger.New("error", false), nil
			},
			check: func(t *testing.T, err error) {
				var authorityErr x509.UnknownAuthorityError
				require.ErrorAs(t, err, &authorityErr)
			},
		},
		{
			name: "tls_hostname_mismatch",
			setup: func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
				srv := newJWKSFixture(t)
				cfg := jwksConfig(srv)
				uri, client := mismatchedHostClient(t, srv)
				cfg.JWKSURI = uri
				return cfg, nil, client
			},
			check: func(t *testing.T, err error) {
				var hostnameErr x509.HostnameError
				require.ErrorAs(t, err, &hostnameErr)
			},
		},
		{
			name: "nxdomain",
			setup: func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
				cfg := jwksConfig(newJWKSFixture(t))
				cfg.JWKSURI = "https://jwks.invalid/jwks.json"
				return cfg, nil, buildClient(t, &nethttp.Client{}, 0)
			},
			check: func(t *testing.T, err error) {
				var dnsErr *net.DNSError
				require.ErrorAs(t, err, &dnsErr)
				assert.True(t, dnsErr.IsNotFound, "a .invalid host must fail as NXDOMAIN, not as a timeout")
			},
		},
		{
			name: "status_401",
			setup: func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
				cfg := jwksConfig(newJWKSFixture(t))
				uri, client := statusTLSServer(t, nethttp.StatusUnauthorized)
				cfg.JWKSURI = uri
				return cfg, nil, client
			},
		},
		{
			name: "status_404",
			setup: func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
				cfg := jwksConfig(newJWKSFixture(t))
				uri, client := statusTLSServer(t, nethttp.StatusNotFound)
				cfg.JWKSURI = uri
				return cfg, nil, client
			},
		},
		{
			name: "refused_redirect",
			setup: func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
				srv := newJWKSFixture(t)
				srv.SetRedirectLocation(newJWKSFixture(t).URL())
				cfg := jwksConfig(srv)
				cfg.JWKSURI = srv.RedirectURL()
				return cfg, nil, jwksRedirectClient(t, srv)
			},
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, errJWKSRedirectRefused) },
		},
		{
			name:  "oversized_body",
			setup: modeSetup(authtesting.JWKSOversized),
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, errBodyTooLarge) },
		},
		{
			name:  "parse_failure",
			setup: modeSetup(authtesting.JWKSMalformed),
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, errJWKSNotJSON) },
		},
		{
			name:  "empty_key_set",
			setup: modeSetup(authtesting.JWKSNonRSAOnly),
			check: func(t *testing.T, err error) { require.ErrorIs(t, err, errJWKSEmptyKeySet) },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, log, client := tc.setup(t)
			failFast, failFastErr := NewVerifier(cfg, log, nil, client)
			require.Nil(t, failFast)
			require.Error(t, failFastErr)

			v, err := NewVerifier(cfg, log, nil, client, WithDeferredKeySet())

			assert.Nil(t, v)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "initial issuer key set fetch failed")
			assert.Equal(t, failFastErr.Error(), err.Error(), "the option must not change the construction error")
			assert.Equal(t, fetchFailureConfiguration, classifyFetchFailure(err).class)
			if tc.check != nil {
				tc.check(t, err)
			}
		})
	}
}

// modeSetup serves the fixture in mode through a client that trusts it.
func modeSetup(mode authtesting.JWKSMode) func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
	return func(t *testing.T) (Config, logger.Logger, httpclient.Client) {
		srv := newJWKSFixture(t)
		srv.SetMode(mode)
		return jwksConfig(srv), nil, jwksClient(t, srv)
	}
}

func TestNewVerifierIgnoresANilJWKSOption(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)

	v, err := NewVerifier(jwksConfig(srv), nil, nil, jwksClient(t, srv), nil)

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial issuer key set fetch failed")
}

// TestDeferredKeySetAnswersUnavailableNeverUnknownKid pins that a never-fetched
// key set refuses every lookup as unavailable (503), never as an unknown kid
// (401), including right after a refresh failed.
func TestDeferredKeySetAnswersUnavailableNeverUnknownKid(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	v := newDeferredVerifier(t, srv, nil, nil)
	clock := newFakeClock()
	r := installDeferredResolverClock(t, v, clock)

	for _, kid := range []string{srv.Issuer().ActiveKeyID(), "never-published"} {
		clock.Advance(testMinRefresh)
		before := srv.RequestCount()

		_, err := r.PublicKey(context.Background(), kid)

		require.ErrorIs(t, err, ErrKeySetUnavailable)
		assert.NotErrorIs(t, err, ErrKidUnknown)
		assert.Equal(t, before+1, srv.RequestCount(), "the lookup must have attempted a refresh that failed")
	}
	_, err := v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))
	require.ErrorIs(t, err, ErrKeySetUnavailable)
}

func TestMiddlewareAnswers503WithTheRefreshFloorWhileTheKeySetIsDeferred(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	v := newDeferredVerifier(t, srv, nil, nil)

	run := runWithCredential(t, v, srv.Issuer().Mint(authtesting.Claims{}))

	assertAPIError(t, run.err, nethttp.StatusServiceUnavailable, "SERVICE_UNAVAILABLE")
	assert.Equal(t, retryAfterSeconds(testMinRefresh), run.rec.Header().Get(headerRetryAfter))
	assert.Equal(t, "60", run.rec.Header().Get(headerRetryAfter))
	assertRejected(t, run)
}

// TestDeferredKeySetFetchesOnTheFirstLookupThenRateFloors freezes the clock, so
// only a forgotten construction attempt lets the first lookup through.
func TestDeferredKeySetFetchesOnTheFirstLookupThenRateFloors(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	v := newDeferredVerifier(t, srv, nil, nil)
	r := installDeferredResolverClock(t, v, newFakeClock())
	require.Equal(t, 1, srv.RequestCount(), "construction attempts exactly one fetch")
	srv.SetMode(authtesting.JWKSHealthy)

	principal, err := v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))

	require.NoError(t, err)
	assert.Equal(t, srv.Issuer().IssuerURL(), principal.Issuer)
	assert.Equal(t, 2, srv.RequestCount(), "the first lookup fetches at once")

	_, err = r.PublicKey(context.Background(), "never-published")
	require.ErrorIs(t, err, ErrKidUnknown, "a filled key set reports an absent kid as unknown")
	assert.Equal(t, 2, srv.RequestCount(), "the next miss is rate-floored")
}

// TestDeferredKeySetLogsOneWarnAndOneInfo drives failed and successful
// on-demand refreshes and requires exactly one line of each kind.
func TestDeferredKeySetLogsOneWarnAndOneInfo(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	log, sink := newBufferLogger()
	v := newDeferredVerifier(t, srv, log, nil)
	clock := newFakeClock()
	r := installDeferredResolverClock(t, v, clock)
	credential := srv.Issuer().Mint(authtesting.Claims{})

	_, err := v.Verify(context.Background(), credential)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	clock.Advance(testMinRefresh)
	_, err = v.Verify(context.Background(), credential)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	require.Equal(t, 3, srv.RequestCount())
	assert.Empty(t, entriesWithMessage(t, sink, "info", deferredInfoMessage), "no fill yet")

	srv.SetMode(authtesting.JWKSHealthy)
	clock.Advance(testMinRefresh)
	_, err = v.Verify(context.Background(), credential)
	require.NoError(t, err)
	clock.Advance(testMinRefresh)
	_, err = r.PublicKey(context.Background(), "never-published")
	require.ErrorIs(t, err, ErrKidUnknown)
	require.Equal(t, 5, srv.RequestCount())

	warns := entriesWithMessage(t, sink, "warn", deferredWarnMessage)
	require.Len(t, warns, 1)
	assert.Equal(t, failureClassOutage, warns[0][logFieldFailureClass])
	assert.Equal(t, fetchStageStatus, warns[0][logFieldFailureStage])
	infos := entriesWithMessage(t, sink, "info", deferredInfoMessage)
	require.Len(t, infos, 1)
	assert.InDelta(t, 1, infos[0][logFieldKeys], 0)
	host := strings.TrimPrefix(strings.TrimSuffix(srv.URL(), authtesting.JWKSPath), "https://")
	assert.NotContains(t, sink.String(), host, "the logs must not carry the endpoint or the error text")
}

// TestDeferredKeySetAnnouncesAFillFromTheTicker lets the real background ticker
// fail, fill and refresh again, and requires one WARN and one INFO throughout.
func TestDeferredKeySetAnnouncesAFillFromTheTicker(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	log, sink := newBufferLogger()
	v, err := NewVerifier(fastTickConfig(srv), log, nil, jwksClient(t, srv), WithDeferredKeySet())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, v.Close()) })

	require.Eventually(t, func() bool { return srv.RequestCount() >= 3 }, 5*time.Second, 5*time.Millisecond)
	srv.SetMode(authtesting.JWKSHealthy)
	require.Eventually(t, v.owned.usable, 5*time.Second, 5*time.Millisecond)
	filledAt := srv.RequestCount()
	require.Eventually(t, func() bool { return srv.RequestCount() >= filledAt+2 }, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, v.Close())

	assert.Len(t, entriesWithMessage(t, sink, "warn", deferredWarnMessage), 1)
	assert.Len(t, entriesWithMessage(t, sink, "info", deferredInfoMessage), 1)
}

func TestNewVerifierWithDeferredKeySetSurvivesANilLogger(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	v := newDeferredVerifier(t, srv, nil, nil)
	srv.SetMode(authtesting.JWKSHealthy)

	_, err := v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))

	require.NoError(t, err)
}

// TestNewVerifierWithDeferredKeySetBehavesAsDefaultOnSuccess pins that the
// option changes nothing when the construction fetch succeeds: no line is
// logged and the rate floor starts at construction as usual.
func TestNewVerifierWithDeferredKeySetBehavesAsDefaultOnSuccess(t *testing.T) {
	srv := newJWKSFixture(t)
	log, sink := newBufferLogger()
	v := newDeferredVerifier(t, srv, log, nil)
	r := v.owned

	_, err := v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))
	require.NoError(t, err)
	_, err = r.PublicKey(context.Background(), "never-published")

	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Equal(t, 1, srv.RequestCount(), "the construction fetch floors the first miss")
	assert.Empty(t, logEntries(t, sink))
}

func TestDeferredKeySetCloseStopsTheRefreshOfANeverFilledVerifier(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	v, err := NewVerifier(fastTickConfig(srv), nil, nil, jwksClient(t, srv), WithDeferredKeySet())
	require.NoError(t, err)
	require.Eventually(t, func() bool { return srv.RequestCount() >= 2 }, 5*time.Second, 5*time.Millisecond, "the ticker refreshes a never-filled key set")

	require.NoError(t, v.Close())

	select {
	case <-v.owned.done:
	default:
		t.Fatal("the background refresh goroutine is still running after Close")
	}
	stopped := srv.RequestCount()
	assert.Never(t, func() bool { return srv.RequestCount() > stopped }, 200*time.Millisecond, 10*time.Millisecond)
	_, err = v.owned.PublicKey(context.Background(), "any")
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	assert.Equal(t, stopped, srv.RequestCount(), "a closed resolver sends nothing on a lookup either")
}

// unregisterCountingProvider counts Unregister calls on the gauge callback
// registration over a real meter.
type unregisterCountingProvider struct {
	embedded.MeterProvider
	real  metric.MeterProvider
	count *atomic.Int32
}

func (p unregisterCountingProvider) Meter(name string, opts ...metric.MeterOption) metric.Meter {
	return unregisterCountingMeter{Meter: p.real.Meter(name, opts...), count: p.count}
}

type unregisterCountingMeter struct {
	metric.Meter
	count *atomic.Int32
}

func (m unregisterCountingMeter) RegisterCallback(f metric.Callback, instruments ...metric.Observable) (metric.Registration, error) {
	registration, err := m.Meter.RegisterCallback(f, instruments...)
	if err != nil {
		return registration, err
	}
	return countingRegistration{Registration: registration, count: m.count}, nil
}

type countingRegistration struct {
	metric.Registration
	count *atomic.Int32
}

func (r countingRegistration) Unregister() error {
	r.count.Add(1)
	return r.Registration.Unregister()
}

func TestDeferredKeySetGaugesAndRefreshCounter(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	mp := obstesting.NewTestMeterProvider()
	var unregistered atomic.Int32
	v, err := NewVerifier(jwksConfig(srv), nil, unregisterCountingProvider{real: mp.MeterProvider, count: &unregistered}, jwksClient(t, srv), WithDeferredKeySet())
	require.NoError(t, err)
	v.now = fixedClock
	installDeferredResolverClock(t, v, newFakeClock())

	rm := mp.Collect(t)
	assert.Nil(t, obstesting.FindMetric(rm, metricKeySetKeyCount), "no series before the first fill")
	assert.Nil(t, obstesting.FindMetric(rm, metricKeySetAge), "no series before the first fill")
	assert.Equal(t, int64(1), counterValue(t, rm, metricKeySetRefreshTotal,
		attribute.String(attrAuthResult, resultFailure),
		attribute.String(attrErrorType, refreshErrorStatus),
	), "the tolerated construction attempt still counts as a failure")

	srv.SetMode(authtesting.JWKSHealthy)
	_, err = v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))
	require.NoError(t, err)
	rm = mp.Collect(t)
	assert.Equal(t, int64(1), gaugeValue[int64](t, rm, metricKeySetKeyCount))
	assert.Equal(t, int64(1), counterValue(t, rm, metricKeySetRefreshTotal, attribute.String(attrAuthResult, resultSuccess)))

	require.NoError(t, v.Close())
	require.NoError(t, v.Close())

	assert.Equal(t, int32(1), unregistered.Load(), "Close unregisters the gauges exactly once")
	assert.Nil(t, obstesting.FindMetric(mp.Collect(t), metricKeySetKeyCount), "no series after Close")
}
