package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"io"
	"math/big"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	authtesting "github.com/gaborage/go-bricks/auth/testing"
	"github.com/gaborage/go-bricks/config"
	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
)

const (
	testTTL          = 10 * time.Minute
	testStaleCeiling = 30 * time.Minute
	testMinRefresh   = time.Minute
	testMaxBody      = 32768
)

// fakeClock is the settable clock the resolver tests install. Every refresh
// decision the resolver makes — the stale ceiling, the rate floor, the key set
// age — reads it, so a test moves time by assignment and never sleeps.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: verifierNow} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newJWKSFixture starts a fake issuer plus its key set endpoint.
func newJWKSFixture(t *testing.T) *authtesting.JWKSServer {
	t.Helper()
	srv := authtesting.NewJWKSServer(newTestIssuer())
	t.Cleanup(srv.Close)
	return srv
}

// jwksConfig retargets the shared baseline at the fake key set endpoint, with
// refresh knobs small enough to reason about under the fake clock.
func jwksConfig(srv *authtesting.JWKSServer) Config {
	cfg := verifierConfig(srv.Issuer())
	cfg.JWKSURI = srv.URL()
	cfg.JWKS = config.AuthJWKSConfig{
		TTL:                testTTL,
		StaleCeiling:       testStaleCeiling,
		MinRefreshInterval: testMinRefresh,
		MaxBodyBytes:       testMaxBody,
	}
	return cfg
}

// jwksClient builds an httpclient that trusts the fake endpoint's certificate.
func jwksClient(t *testing.T, srv *authtesting.JWKSServer) httpclient.Client {
	t.Helper()
	client, err := httpclient.NewBuilder(logger.New("error", false)).
		WithHTTPClient(srv.HTTPClient()).
		Build()
	require.NoError(t, err)
	return client
}

// newJWKSVerifier builds a JWKS-backed verifier over the fake endpoint and
// registers its Close.
func newJWKSVerifier(t *testing.T, srv *authtesting.JWKSServer) *Verifier {
	t.Helper()
	cfg := jwksConfig(srv)
	v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, v.Close()) })
	v.now = fixedClock
	return v
}

// installResolverClock hands the verifier's own resolver a fake clock and
// re-bases its timestamps onto it, so the construction fetch reads as having
// happened at the fake clock's starting instant.
func installResolverClock(t *testing.T, v *Verifier, clock *fakeClock) *jwksResolver {
	t.Helper()
	r := v.owned
	require.NotNil(t, r, "NewVerifier must own the resolver it constructed")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = clock.Now
	r.fetchedAt = clock.Now()
	r.lastAttempt = clock.Now()
	return r
}

func TestNewVerifierFailsFastWhenTheKeySetCannotBeFetched(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)

	v, err := NewVerifier(jwksConfig(srv), nil, nil, jwksClient(t, srv))

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial issuer key set fetch failed")
	assert.Positive(t, srv.RequestCount(), "construction must attempt a fetch")
}

func TestNewVerifierSucceedsOnceTheIssuerIsHealthy(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)

	failed, err := NewVerifier(jwksConfig(srv), nil, nil, jwksClient(t, srv))
	require.Error(t, err)
	require.Nil(t, failed)

	srv.SetMode(authtesting.JWKSHealthy)
	v := newJWKSVerifier(t, srv)

	principal, err := v.Verify(context.Background(), srv.Issuer().Mint(authtesting.Claims{}))

	require.NoError(t, err)
	assert.Equal(t, srv.Issuer().IssuerURL(), principal.Issuer)
}

func TestNewVerifierRejectsAnUnvalidatedJWKSSource(t *testing.T) {
	srv := newJWKSFixture(t)
	tests := []struct {
		name      string
		mutate    func(*Config)
		wantField string
	}{
		{
			name:      "plaintext_jwks_uri",
			mutate:    func(c *Config) { c.JWKSURI = "http://idp.test/jwks.json" },
			wantField: "auth.jwt.jwksuri",
		},
		{
			name:      "empty_jwks_uri",
			mutate:    func(c *Config) { c.JWKSURI = "" },
			wantField: "auth.jwt.jwksuri",
		},
		{
			name:      "zero_max_body_bytes",
			mutate:    func(c *Config) { c.JWKS.MaxBodyBytes = 0 },
			wantField: "auth.jwt.jwks.maxbodybytes",
		},
		{
			name:      "zero_min_refresh_interval",
			mutate:    func(c *Config) { c.JWKS.MinRefreshInterval = 0 },
			wantField: "auth.jwt.jwks.minrefreshinterval",
		},
		{
			name:      "stale_ceiling_below_ttl",
			mutate:    func(c *Config) { c.JWKS.StaleCeiling = time.Second },
			wantField: "auth.jwt.jwks.staleceiling",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := jwksConfig(srv)
			tc.mutate(&cfg)

			v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))

			assert.Nil(t, v)
			var cerr *ConfigError
			require.ErrorAs(t, err, &cerr)
			assert.Equal(t, tc.wantField, cerr.Field)
		})
	}
}

func TestNewVerifierRejectsAnInvalidVerifierConfig(t *testing.T) {
	srv := newJWKSFixture(t)
	cfg := jwksConfig(srv)
	cfg.Issuer = ""

	v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.issuer", cerr.Field)
	assert.Zero(t, srv.RequestCount(), "an invalid config must fail before any fetch")
}

func TestNewVerifierRequiresALoggerWhenItBuildsTheDefaultClient(t *testing.T) {
	srv := newJWKSFixture(t)

	v, err := NewVerifier(jwksConfig(srv), nil, nil, nil)

	assert.Nil(t, v)
	var cerr *ConfigError
	require.ErrorAs(t, err, &cerr)
	assert.Equal(t, "auth.jwt.jwks.client", cerr.Field)
}

func TestNewVerifierOwnsTheResolverItConstructed(t *testing.T) {
	srv := newJWKSFixture(t)
	owning := newJWKSVerifier(t, srv)
	borrowing, err := NewVerifierWithResolver(verifierConfig(srv.Issuer()), nil, NewStaticKeyResolver(srv.Issuer().PublicKeys()))
	require.NoError(t, err)

	assert.NotNil(t, owning.owned)
	assert.Nil(t, borrowing.owned, "a caller-supplied resolver stays the caller's")
	require.NoError(t, borrowing.Close())
}

func TestVerifierCloseStopsTheOwnedResolver(t *testing.T) {
	srv := newJWKSFixture(t)
	cfg := jwksConfig(srv)
	v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))
	require.NoError(t, err)

	require.NoError(t, v.Close())
	require.NoError(t, v.Close(), "Close must be idempotent")

	select {
	case <-v.owned.done:
	default:
		t.Fatal("the background refresh goroutine is still running after Close")
	}
}

func TestJWKSResolverRefreshesOnceForConcurrentUnknownKids(t *testing.T) {
	const rotatedKID = "rotated-key-1"
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	clock.Advance(2 * testMinRefresh)

	srv.Rotate(rotatedKID)
	credential := srv.Issuer().Mint(authtesting.Claims{})
	srv.ResetRequests()

	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := v.Verify(context.Background(), credential)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	for err := range results {
		require.NoError(t, err)
	}
	assert.Equal(t, 1, srv.RequestCount(), "concurrent unknown-kid lookups must coalesce into one refresh")
}

func TestJWKSResolverHonorsTheMinimumRefreshInterval(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	r := installResolverClock(t, v, clock)
	srv.ResetRequests()

	// Inside the floor: the construction fetch counts as the last attempt, so
	// the unknown kid must not reach the issuer at all.
	_, err := r.PublicKey(context.Background(), "absent-kid")
	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Zero(t, srv.RequestCount())

	// Past the floor: one refresh, and the kid is still unknown afterwards.
	clock.Advance(testMinRefresh + time.Second)
	_, err = r.PublicKey(context.Background(), "absent-kid")
	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Equal(t, 1, srv.RequestCount())

	// Immediately again, still inside the floor from that attempt.
	_, err = r.PublicKey(context.Background(), "absent-kid")
	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Equal(t, 1, srv.RequestCount())

	clock.Advance(testMinRefresh + time.Second)
	_, err = r.PublicKey(context.Background(), "absent-kid")
	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Equal(t, 2, srv.RequestCount())
}

func TestJWKSResolverPicksUpARotatedKey(t *testing.T) {
	const rotatedKID = "rotated-key-2"
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	clock.Advance(2 * testMinRefresh)
	srv.ResetRequests()

	srv.Rotate(rotatedKID)
	credential := srv.Issuer().Mint(authtesting.Claims{})

	principal, err := v.Verify(context.Background(), credential)

	require.NoError(t, err)
	assert.Equal(t, authtesting.DefaultSubject, principal.Subject)
	assert.Equal(t, 1, srv.RequestCount(), "the unknown kid must trigger exactly one refresh")
}

func TestJWKSResolverServesAStaleKeySetUntilTheCeiling(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	credential := srv.Issuer().Mint(authtesting.Claims{})
	srv.SetMode(authtesting.JWKSServerError)
	srv.ResetRequests()

	// Inside the ceiling the cached key set still answers, and a known kid does
	// not go back to the unreachable issuer at all.
	clock.Advance(testStaleCeiling - time.Second)
	_, err := v.Verify(context.Background(), credential)
	require.NoError(t, err)
	assert.Zero(t, srv.RequestCount())

	// Past the ceiling the key set is gone: the refresh is attempted, fails, and
	// the answer is a server fault, never an accepted credential.
	clock.Advance(2 * time.Second)
	_, err = v.Verify(context.Background(), credential)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	require.NotErrorIs(t, err, ErrInvalidCredential)
	assert.Equal(t, 1, srv.RequestCount())

	// A second call inside the floor reports the same fault without a new fetch.
	_, err = v.Verify(context.Background(), credential)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	assert.Equal(t, 1, srv.RequestCount())

	// Recovery: the next allowed refresh restores normal behavior.
	srv.SetMode(authtesting.JWKSHealthy)
	clock.Advance(testMinRefresh + time.Second)
	principal, err := v.Verify(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, srv.Issuer().IssuerURL(), principal.Issuer)
	assert.Equal(t, 2, srv.RequestCount())
}

func TestJWKSResolverNeverForwardsTheFetchFailureToTheCaller(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	credential := srv.Issuer().Mint(authtesting.Claims{})
	srv.SetMode(authtesting.JWKSServerError)
	clock.Advance(testStaleCeiling + time.Second)

	_, err := v.Verify(context.Background(), credential)

	require.ErrorIs(t, err, ErrKeySetUnavailable)
	// The transport failure must not travel with the answer: no status code, no
	// endpoint, and nothing a VerificationError could carry as a Cause.
	var verr *VerificationError
	assert.NotErrorAs(t, err, &verr)
	assert.NotContains(t, err.Error(), "503")
	assert.NotContains(t, err.Error(), srv.URL())
}

func TestJWKSResolverReportsAnUnknownKidWithTheSentinelAlone(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	clock.Advance(2 * testMinRefresh)

	_, err := v.Verify(context.Background(), srv.Issuer().MintUnknownKeyID())

	assertRejectedWithClass(t, err, ClassKidUnknown)
	var verr *VerificationError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, ErrKidUnknown, verr.Cause, "the cause must be the sentinel, never the resolver's own error")
}

func TestNewVerifierRejectsAnOversizedKeySet(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSOversized)
	cfg := jwksConfig(srv)
	cfg.JWKS.MaxBodyBytes = 4096

	v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))

	assert.Nil(t, v)
	require.ErrorIs(t, err, errBodyTooLarge)
}

func TestNewVerifierRejectsAMalformedKeySet(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSMalformed)

	v, err := NewVerifier(jwksConfig(srv), nil, nil, jwksClient(t, srv))

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid json")
}

func TestNewVerifierRejectsAKeySetWithoutAnyRSAKey(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.AddECKey("ec-only-key")
	srv.SetMode(authtesting.JWKSNonRSAOnly)

	v, err := NewVerifier(jwksConfig(srv), nil, nil, jwksClient(t, srv))

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usable RSA signing key")
}

func TestNewVerifierDropsNonRSAKeysWithASingleWarning(t *testing.T) {
	const ecKID = "ec-key-to-drop"
	srv := newJWKSFixture(t)
	srv.AddECKey(ecKID)
	srv.AddRawKey(map[string]string{"kty": "oct", "kid": "symmetric-key", "k": "c2VjcmV0"})
	credential := srv.Issuer().Mint(authtesting.Claims{})

	var v *Verifier
	out := captureStdout(t, func() {
		built, err := NewVerifier(jwksConfig(srv), logger.New("warn", false), nil, jwksClient(t, srv))
		require.NoError(t, err)
		v = built
	})
	t.Cleanup(func() { require.NoError(t, v.Close()) })
	v.now = fixedClock

	assert.Equal(t, 1, strings.Count(out, "ignored unusable jwks entries"), "one line for the whole key set, not one per key")
	assert.Contains(t, out, ecKID)
	assert.Contains(t, out, "symmetric-key")

	principal, err := v.Verify(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, srv.Issuer().IssuerURL(), principal.Issuer)
}

// gatedJWKSClient blocks every fetch on a gate and records the context state the
// fetch actually ran under, which is how the detachment test observes that a
// canceled caller did not cancel the shared fetch.
type gatedJWKSClient struct {
	httpclient.Client
	gate    chan struct{}
	ctxErrs chan error
}

func (g *gatedJWKSClient) Get(ctx context.Context, req *httpclient.Request) (*httpclient.Response, error) {
	<-g.gate
	g.ctxErrs <- ctx.Err()
	return g.Client.Get(ctx, req)
}

func TestJWKSResolverDetachesTheFetchFromTheCallersContext(t *testing.T) {
	const rotatedKID = "detached-key"
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	r := installResolverClock(t, v, clock)
	clock.Advance(2 * testMinRefresh)

	gated := &gatedJWKSClient{Client: r.client, gate: make(chan struct{}), ctxErrs: make(chan error, 4)}
	r.client = gated
	srv.Rotate(rotatedKID)

	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- r.refresh(ctx) }()

	// Cancel while the fetch is parked on the gate: the caller stops WAITING,
	// but the fetch it started must survive for every coalesced waiter.
	cancel()
	require.ErrorIs(t, <-waited, context.Canceled)

	close(gated.gate)
	require.NoError(t, <-gated.ctxErrs, "the fetch must run on a context detached from the caller's")

	// Joining the in-flight call is how the test waits for it without sleeping.
	_ = r.refresh(context.Background())
	key, err := r.PublicKey(context.Background(), rotatedKID)
	require.NoError(t, err)
	assert.NotNil(t, key, "the detached fetch must still have updated the key set")
}

func TestCapResponseBodyEnforcesTheConfiguredCap(t *testing.T) {
	const limit = 16
	tests := []struct {
		name          string
		body          string
		contentLength int64
		wantErr       bool
	}{
		{name: "under_the_cap", body: "12345", contentLength: 5},
		{name: "exactly_at_the_cap", body: strings.Repeat("a", limit), contentLength: limit},
		{name: "one_byte_over_the_cap", body: strings.Repeat("a", limit+1), contentLength: limit + 1, wantErr: true},
		{name: "undeclared_length_over_the_cap", body: strings.Repeat("a", limit*4), contentLength: -1, wantErr: true},
	}
	interceptor := capResponseBody(limit)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &nethttp.Response{
				Body:          io.NopCloser(strings.NewReader(tc.body)),
				ContentLength: tc.contentLength,
			}

			err := interceptor(context.Background(), nil, resp)
			if err == nil {
				_, err = io.ReadAll(resp.Body)
			}

			if tc.wantErr {
				require.Error(t, err)
				// The declared-length refusal is errBodyTooLarge; the mid-stream
				// one is net/http's own *MaxBytesError. Both are the same fault.
				assert.True(t, isOversizedBody(err), "an over-cap body must classify as oversized")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIsOversizedBodyRecognizesBothCapFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "the_declared_length_sentinel", err: errBodyTooLarge, want: true},
		{name: "a_wrapped_declared_length_sentinel", err: fmt.Errorf("read: %w", errBodyTooLarge), want: true},
		{name: "net_http_max_bytes_error", err: &nethttp.MaxBytesError{Limit: 16}, want: true},
		{name: "a_wrapped_max_bytes_error", err: fmt.Errorf("read: %w", &nethttp.MaxBytesError{Limit: 16}), want: true},
		{name: "an_unrelated_transport_error", err: errors.New("connection refused")},
		{name: "no_error", err: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isOversizedBody(tc.err))
		})
	}
}

func TestCapResponseBodyToleratesAResponseWithoutABody(t *testing.T) {
	interceptor := capResponseBody(16)

	require.NoError(t, interceptor(context.Background(), nil, nil))
	require.NoError(t, interceptor(context.Background(), nil, &nethttp.Response{}))
}

func TestJWKSResolverReportsAnUnavailableKeySetWhenTheSetIsEmpty(t *testing.T) {
	r := &jwksResolver{client: errorClient{}, now: time.Now, staleCeiling: time.Hour, minRefresh: time.Minute}

	key, err := r.PublicKey(context.Background(), "any")

	assert.Nil(t, key)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
}

func TestJWKSTickIntervalStaysPositive(t *testing.T) {
	tests := []struct {
		name       string
		ttl        time.Duration
		minRefresh time.Duration
		want       time.Duration
	}{
		{name: "half_the_ttl_when_it_clears_the_floor", ttl: 10 * time.Minute, minRefresh: time.Minute, want: 5 * time.Minute},
		{name: "the_floor_when_half_the_ttl_is_shorter", ttl: time.Minute, minRefresh: time.Minute, want: time.Minute},
		{name: "half_the_ttl_exactly_at_the_floor", ttl: 2 * time.Minute, minRefresh: time.Minute, want: time.Minute},
		{name: "the_floor_for_a_zero_ttl", ttl: 0, minRefresh: 30 * time.Second, want: 30 * time.Second},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &jwksResolver{ttl: tc.ttl, minRefresh: tc.minRefresh}

			assert.Equal(t, tc.want, r.tickInterval())
		})
	}
}

func TestJWKSPeerNameFallsBackForAnUnparsableURI(t *testing.T) {
	assert.Equal(t, "idp.test", jwksPeerName("https://idp.test/jwks.json"))
	assert.Equal(t, "jwks", jwksPeerName("::not a url::"))
}

func TestJWKSResolverKeySetObservationReportsNoStateBeforeTheFirstFetch(t *testing.T) {
	r := &jwksResolver{now: time.Now}

	keys, age, ok := r.keySetObservation()

	assert.False(t, ok)
	assert.Zero(t, keys)
	assert.Zero(t, age)
}

func TestJWKSResolverFetchReportsTheStatusWithoutTheBody(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSServerError)
	r := &jwksResolver{
		uri:          srv.URL(),
		client:       jwksClient(t, srv),
		maxBodyBytes: testMaxBody,
		now:          time.Now,
	}

	outcome := r.fetch(context.Background())

	assert.Equal(t, refreshErrorStatus, outcome.errType)
	require.Error(t, outcome.err)
	assert.Contains(t, outcome.err.Error(), "503")
}

// errorClient fails every request without ever reaching the wire.
type errorClient struct{ httpclient.Client }

func (errorClient) Get(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestJWKSResolverFetchClassifiesATransportFailure(t *testing.T) {
	r := &jwksResolver{uri: "https://idp.test/jwks.json", client: errorClient{}, maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	assert.Equal(t, refreshErrorTransport, outcome.errType)
	require.Error(t, outcome.err)
}

// nilResponseClient violates the client contract by reporting neither a response
// nor an error.
type nilResponseClient struct{ httpclient.Client }

func (nilResponseClient) Get(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return nil, nil
}

func TestJWKSResolverFetchFailsClosedOnAContractViolation(t *testing.T) {
	r := &jwksResolver{uri: "https://idp.test/jwks.json", client: nilResponseClient{}, maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	assert.Equal(t, refreshErrorTransport, outcome.errType)
	require.Error(t, outcome.err)
}

func TestNewVerifierBuildsADefaultClientThatVerifiesTLS(t *testing.T) {
	srv := newJWKSFixture(t)

	// No client supplied, so the resolver builds its own — which trusts the
	// system roots only, and therefore refuses the fake issuer's certificate.
	// The failure proves the default client exists AND that it validates TLS.
	v, err := NewVerifier(jwksConfig(srv), logger.New("error", false), nil, nil)

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initial issuer key set fetch failed")
}

func TestJWKSResolverRejectsAnOversizedBodyThroughTheInterceptor(t *testing.T) {
	const capBytes = 4096
	srv := newJWKSFixture(t)
	srv.SetMode(authtesting.JWKSOversized)
	// The same composition defaultJWKSClient builds, over the fake endpoint's
	// certificate: the cap is enforced while the body streams.
	client, err := httpclient.NewBuilder(logger.New("error", false)).
		WithHTTPClient(srv.HTTPClient()).
		WithResponseInterceptor(capResponseBody(capBytes)).
		Build()
	require.NoError(t, err)
	r := &jwksResolver{uri: srv.URL(), client: client, maxBodyBytes: capBytes, now: time.Now}

	outcome := r.fetch(context.Background())

	assert.Equal(t, refreshErrorOversized, outcome.errType)
	require.ErrorIs(t, outcome.err, errBodyTooLarge)
}

func TestJWKSResolverRefreshesInTheBackground(t *testing.T) {
	srv := newJWKSFixture(t)
	cfg := jwksConfig(srv)
	cfg.JWKS.TTL = 2 * time.Millisecond
	cfg.JWKS.StaleCeiling = time.Hour
	cfg.JWKS.MinRefreshInterval = time.Millisecond
	v, err := NewVerifier(cfg, nil, nil, jwksClient(t, srv))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, v.Close()) })
	srv.ResetRequests()

	// The loop ticks on the real clock, so this is the one place a test waits on
	// it — by polling the request log, never by sleeping a fixed span.
	require.Eventually(t, func() bool { return srv.RequestCount() > 0 }, 5*time.Second, 2*time.Millisecond,
		"the background loop must refresh ahead of the ttl")
}

// statusOnlyClient reports a non-2xx response without an error, the shape a
// caller-supplied client is free to return.
type statusOnlyClient struct{ httpclient.Client }

func (statusOnlyClient) Get(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return &httpclient.Response{StatusCode: nethttp.StatusInternalServerError}, nil
}

func TestJWKSResolverFetchFailsOnANonOKStatusWithoutAnError(t *testing.T) {
	r := &jwksResolver{uri: "https://idp.test/jwks.json", client: statusOnlyClient{}, maxBodyBytes: testMaxBody, now: time.Now}

	outcome := r.fetch(context.Background())

	assert.Equal(t, refreshErrorStatus, outcome.errType)
	require.Error(t, outcome.err)
	assert.Contains(t, outcome.err.Error(), "500")
}

// newStaleFixture builds a resolver holding one key fetched at the clock's
// current instant, for the stale-ceiling boundary.
func newStaleFixture(clock *fakeClock) *jwksResolver {
	return &jwksResolver{
		now:          clock.Now,
		staleCeiling: testStaleCeiling,
		keys:         map[string]*rsa.PublicKey{"k": {N: big.NewInt(1), E: 65537}},
		fetchedAt:    clock.Now(),
	}
}

func TestJWKSResolverTreatsTheStaleCeilingAsInclusive(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{name: "one_nanosecond_inside_the_ceiling", age: testStaleCeiling - time.Nanosecond, want: true},
		{name: "exactly_at_the_ceiling", age: testStaleCeiling, want: true},
		{name: "one_nanosecond_past_the_ceiling", age: testStaleCeiling + time.Nanosecond, want: false},
		{name: "exactly_at_the_fetch_instant", age: 0, want: true},
		// A clock that stepped BACKWARDS makes the age negative. Treating that as
		// fresh would keep an arbitrarily old key set usable forever, so it fails
		// closed instead.
		{name: "one_nanosecond_before_the_fetch_instant", age: -time.Nanosecond, want: false},
		{name: "a_clock_that_stepped_far_backwards", age: -2 * testStaleCeiling, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			r := newStaleFixture(clock)
			clock.Advance(tc.age)

			assert.Equal(t, tc.want, r.usable())
		})
	}
}

func TestJWKSResolverClaimAttemptAllowsAGapEqualToTheFloor(t *testing.T) {
	tests := []struct {
		name  string
		gap   time.Duration
		force bool
		want  bool
	}{
		{name: "one_nanosecond_inside_the_floor", gap: testMinRefresh - time.Nanosecond, want: false},
		{name: "exactly_at_the_floor", gap: testMinRefresh, want: true},
		{name: "one_nanosecond_past_the_floor", gap: testMinRefresh + time.Nanosecond, want: true},
		{name: "forced_inside_the_floor", gap: 0, force: true, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := newFakeClock()
			r := &jwksResolver{now: clock.Now, minRefresh: testMinRefresh, lastAttempt: clock.Now()}
			clock.Advance(tc.gap)

			assert.Equal(t, tc.want, r.claimAttempt(tc.force))
		})
	}
}

func TestJWKSResolverClaimAttemptAllowsTheFirstAttempt(t *testing.T) {
	clock := newFakeClock()
	r := &jwksResolver{now: clock.Now, minRefresh: testMinRefresh}

	assert.True(t, r.claimAttempt(false), "a resolver that has never fetched is not rate floored")
	assert.False(t, r.claimAttempt(false), "the attempt it just recorded starts the floor")
}

// bodyClient answers every request with one fixed 200 response, so a test can
// hand the resolver a body of an exact length.
type bodyClient struct {
	httpclient.Client
	body []byte
}

func (b bodyClient) Get(_ context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	return &httpclient.Response{StatusCode: nethttp.StatusOK, Body: b.body}, nil
}

func TestJWKSResolverFetchAcceptsABodyOfExactlyTheCap(t *testing.T) {
	body := []byte(`{"keys":[` + jwk(map[string]string{"kty": "RSA", "kid": "k", "n": validModulus(), "e": "AQAB"}) + `]}`)
	tests := []struct {
		name    string
		maxBody int64
		wantErr bool
	}{
		{name: "one_byte_below_the_cap", maxBody: int64(len(body)) - 1, wantErr: true},
		{name: "exactly_at_the_cap", maxBody: int64(len(body))},
		{name: "one_byte_above_the_cap", maxBody: int64(len(body)) + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &jwksResolver{uri: "https://idp.test/jwks.json", client: bodyClient{body: body}, maxBodyBytes: tc.maxBody, now: time.Now}

			outcome := r.fetch(context.Background())

			if tc.wantErr {
				assert.Equal(t, refreshErrorOversized, outcome.errType)
				require.ErrorIs(t, outcome.err, errBodyTooLarge)
				return
			}
			require.NoError(t, outcome.err)
			assert.Len(t, outcome.keys, 1)
		})
	}
}

func TestJWKSResolverCloseUnregistersTheGaugesOnce(t *testing.T) {
	unregistered := 0
	r := &jwksResolver{stop: make(chan struct{}), done: make(chan struct{}), unregisterGauges: func() { unregistered++ }}
	close(r.done)

	r.close()
	r.close()

	assert.Equal(t, 1, unregistered, "close must unregister the gauges exactly once")
}

func TestJWKSResolverCloseWithoutRegisteredGauges(t *testing.T) {
	r := &jwksResolver{stop: make(chan struct{}), done: make(chan struct{})}
	close(r.done)

	assert.NotPanics(t, r.close, "a resolver that never registered gauges still closes")
}

func TestJWKSResolverStopsFetchingOnceClosed(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	r := installResolverClock(t, v, clock)
	// Well past the rate floor, so the only thing that can hold the fetch back
	// is the closed flag itself.
	clock.Advance(2 * testMinRefresh)

	require.NoError(t, v.Close())
	srv.ResetRequests()

	_, err := r.PublicKey(context.Background(), "unknown-kid-after-close")

	require.ErrorIs(t, err, ErrKidUnknown)
	assert.Zero(t, srv.RequestCount(), "a closed resolver must issue no further requests to the issuer")
}

// TestJWKSResolverKeepsServingTheCachedKeySetAfterClose pins the other half of
// the close contract: shutdown stops FETCHING, it does not invalidate the key
// set already held, which stays usable until its stale ceiling.
func TestJWKSResolverKeepsServingTheCachedKeySetAfterClose(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	installResolverClock(t, v, clock)
	credential := srv.Issuer().Mint(authtesting.Claims{})

	require.NoError(t, v.Close())
	srv.ResetRequests()

	clock.Advance(testStaleCeiling)
	principal, err := v.Verify(context.Background(), credential)
	require.NoError(t, err)
	assert.Equal(t, srv.Issuer().IssuerURL(), principal.Issuer)
	assert.Zero(t, srv.RequestCount())

	// Past the ceiling the set is gone, and the closed resolver cannot refetch
	// it: the answer is the server fault, never an accepted credential.
	clock.Advance(time.Nanosecond)
	_, err = v.Verify(context.Background(), credential)
	require.ErrorIs(t, err, ErrKeySetUnavailable)
	assert.Zero(t, srv.RequestCount())
}

// closeGatedClient parks every fetch until its context ends and reports the
// context error it observed, so a test can prove close ABORTS an in-flight fetch
// rather than waiting out jwksFetchTimeout.
type closeGatedClient struct {
	httpclient.Client
	entered chan struct{}
	ctxErr  chan error
}

func (c *closeGatedClient) Get(ctx context.Context, _ *httpclient.Request) (*httpclient.Response, error) {
	close(c.entered)
	<-ctx.Done()
	c.ctxErr <- ctx.Err()
	return nil, ctx.Err()
}

func TestJWKSResolverCloseAbortsAnInFlightFetch(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	r := installResolverClock(t, v, clock)
	clock.Advance(2 * testMinRefresh)

	gated := &closeGatedClient{Client: r.client, entered: make(chan struct{}), ctxErr: make(chan error, 1)}
	r.client = gated
	go func() { _ = r.refresh(context.Background()) }()
	<-gated.entered

	require.NoError(t, v.Close())

	require.ErrorIs(t, <-gated.ctxErr, context.Canceled,
		"close must cancel the fetch, not leave it to time out")
}

func TestJWKSResolverFetchBaseIsDetachedFromEveryCaller(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	r := v.owned

	require.NoError(t, r.fetchBase().Err(), "a live resolver's fetch context is not canceled")
	// The base carries no caller values, so no single tenant's trace id or
	// request id can be injected into the shared key set fetch.
	assert.Nil(t, r.fetchBase().Value(fetchBaseProbeKey{}))

	require.NoError(t, v.Close())
	require.ErrorIs(t, r.fetchBase().Err(), context.Canceled)
}

// fetchBaseProbeKey is a context key a test plants on a caller's context, to
// show it does not travel into the fetch context.
type fetchBaseProbeKey struct{}

func TestJWKSResolverFetchBaseFallsBackForALiteralResolver(t *testing.T) {
	r := &jwksResolver{}

	base := r.fetchBase()

	require.NotNil(t, base)
	assert.NoError(t, base.Err())
}

func TestSummarizeDroppedBoundsTheNamedKids(t *testing.T) {
	kids := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("kid-%d", i)
		}
		return out
	}
	tests := []struct {
		name        string
		dropped     []string
		wantNamed   int
		wantSuffix  string
		wantAbsent  string
		wantPresent string
	}{
		{name: "one_below_the_naming_cap", dropped: kids(maxDroppedKidsNamed - 1), wantNamed: maxDroppedKidsNamed - 1},
		{name: "exactly_at_the_naming_cap", dropped: kids(maxDroppedKidsNamed), wantNamed: maxDroppedKidsNamed},
		{
			name:       "one_above_the_naming_cap",
			dropped:    kids(maxDroppedKidsNamed + 1),
			wantNamed:  maxDroppedKidsNamed,
			wantSuffix: ", …+1 more",
			wantAbsent: fmt.Sprintf("kid-%d", maxDroppedKidsNamed),
		},
		{
			name:       "far_above_the_naming_cap",
			dropped:    kids(5000),
			wantNamed:  maxDroppedKidsNamed,
			wantSuffix: ", …+4990 more",
			wantAbsent: "kid-4999",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			summary := summarizeDropped(tc.dropped)

			assert.Equal(t, tc.wantNamed, strings.Count(summary, "kid-"))
			if tc.wantSuffix == "" {
				assert.NotContains(t, summary, " more")
				return
			}
			assert.True(t, strings.HasSuffix(summary, tc.wantSuffix), "summary %q must end in %q", summary, tc.wantSuffix)
			assert.NotContains(t, summary, tc.wantAbsent)
		})
	}
}

func TestTruncateKidBoundsOneKidsLength(t *testing.T) {
	tests := []struct {
		name      string
		kid       string
		want      string
		wantRunes int
	}{
		{name: "one_rune_below_the_cap", kid: strings.Repeat("a", maxDroppedKidRunes-1), wantRunes: maxDroppedKidRunes - 1},
		{name: "exactly_at_the_cap", kid: strings.Repeat("a", maxDroppedKidRunes), wantRunes: maxDroppedKidRunes},
		{name: "one_rune_above_the_cap", kid: strings.Repeat("a", maxDroppedKidRunes+1), wantRunes: maxDroppedKidRunes + 1},
		// Cutting on a BYTE boundary would split the last rune and put invalid
		// UTF-8 into the log line.
		{name: "multi_byte_runes_above_the_cap", kid: strings.Repeat("é", maxDroppedKidRunes+10), wantRunes: maxDroppedKidRunes + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateKid(tc.kid)

			assert.Len(t, []rune(got), tc.wantRunes)
			assert.True(t, utf8.ValidString(got), "a truncated kid must stay valid UTF-8")
			if len([]rune(tc.kid)) <= maxDroppedKidRunes {
				assert.Equal(t, tc.kid, got)
				return
			}
			assert.True(t, strings.HasSuffix(got, droppedKidEllipsis))
		})
	}
}

// TestWarnDroppedKeepsTheCountExactWhileBoundingTheNaming pins the WARN a
// hostile issuer sees: the count is whole, the payload is not.
func TestWarnDroppedKeepsTheCountExactWhileBoundingTheNaming(t *testing.T) {
	const dropped = 40
	entries := make([]string, dropped)
	for i := range entries {
		entries[i] = strings.Repeat("z", maxDroppedKidRunes*4)
	}
	// The logger must be built INSIDE the capture: it binds os.Stdout once, at
	// construction.
	out := captureStdout(t, func() {
		r := &jwksResolver{log: logger.New("warn", false)}
		r.warnDropped(entries)
	})

	assert.Contains(t, out, `"dropped":40`)
	assert.Contains(t, out, fmt.Sprintf("+%d more", dropped-maxDroppedKidsNamed))
	assert.NotContains(t, out, strings.Repeat("z", maxDroppedKidRunes+1), "no kid may be rendered past its cap")
}

// TestJWKSResolverClaimAttemptRefusesOnceClosed isolates the closed flag from
// the canceled base context: both stop a fetch end to end, so the flag needs a
// test that reaches it with no transport in the way.
func TestJWKSResolverClaimAttemptRefusesOnceClosed(t *testing.T) {
	clock := newFakeClock()
	r := &jwksResolver{now: clock.Now, minRefresh: testMinRefresh, stop: make(chan struct{}), done: make(chan struct{})}
	close(r.done)

	require.True(t, r.claimAttempt(false), "an open resolver that has never fetched may fetch")
	clock.Advance(2 * testMinRefresh)
	require.True(t, r.claimAttempt(false), "an open resolver outside the floor may fetch")

	r.close()
	clock.Advance(2 * testMinRefresh)

	assert.False(t, r.claimAttempt(false), "a closed resolver must refuse a fetch outside the floor")
	assert.False(t, r.claimAttempt(true), "closed outranks force")
}

// valueCarryingClient records the probe value visible on the context the fetch
// actually ran under.
type valueCarryingClient struct {
	httpclient.Client
	seen chan any
}

func (c *valueCarryingClient) Get(ctx context.Context, req *httpclient.Request) (*httpclient.Response, error) {
	c.seen <- ctx.Value(fetchBaseProbeKey{})
	return c.Client.Get(ctx, req)
}

// TestJWKSResolverFetchCarriesNoCallerValues pins the attribution rule: one
// fetch is shared by every coalesced waiter, so it must not inherit the values —
// trace id, request id — of whichever caller happened to trigger it.
func TestJWKSResolverFetchCarriesNoCallerValues(t *testing.T) {
	srv := newJWKSFixture(t)
	v := newJWKSVerifier(t, srv)
	clock := newFakeClock()
	r := installResolverClock(t, v, clock)
	clock.Advance(2 * testMinRefresh)

	probing := &valueCarryingClient{Client: r.client, seen: make(chan any, 1)}
	r.client = probing
	ctx := context.WithValue(context.Background(), fetchBaseProbeKey{}, "caller-trace-id")

	require.NoError(t, r.refresh(ctx))

	assert.Nil(t, <-probing.seen, "the fetch must not inherit the triggering caller's context values")
}

// jwksRedirectClient builds an httpclient over the PRODUCTION redirect policy,
// dialing through the fake endpoint's certificate: newJWKSHTTPClient supplies
// the policy exactly as defaultJWKSClient gets it, and only the transport is
// swapped so the fake issuer is reachable.
func jwksRedirectClient(t *testing.T, srv *authtesting.JWKSServer) httpclient.Client {
	t.Helper()
	httpClient := newJWKSHTTPClient()
	require.NotNil(t, httpClient.CheckRedirect, "the default jwks client must pin a redirect policy")
	httpClient.Transport = srv.HTTPClient().Transport
	client, err := httpclient.NewBuilder(logger.New("error", false)).
		WithHTTPClient(httpClient).
		Build()
	require.NoError(t, err)
	return client
}

// mustRedirectRequest builds the *http.Request net/http would hand
// jwksCheckRedirect for a hop to rawURL.
func mustRedirectRequest(t *testing.T, rawURL string) *nethttp.Request {
	t.Helper()
	req, err := nethttp.NewRequestWithContext(context.Background(), nethttp.MethodGet, rawURL, nethttp.NoBody)
	require.NoError(t, err)
	return req
}

func TestJWKSCheckRedirectPinsTheOrigin(t *testing.T) {
	const origin = "https://issuer.example.com/.well-known/jwks.json"
	tests := []struct {
		name    string
		target  string
		allowed bool
	}{
		{name: "same_origin_other_path", target: "https://issuer.example.com/keys", allowed: true},
		{name: "same_origin_host_case_differs", target: "https://ISSUER.example.com/keys", allowed: true},
		{name: "scheme_downgraded_to_http", target: "http://issuer.example.com/.well-known/jwks.json"},
		{name: "cross_host", target: "https://attacker.example.net/.well-known/jwks.json"},
		{name: "same_host_other_port", target: "https://issuer.example.com:8443/.well-known/jwks.json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			via := []*nethttp.Request{mustRedirectRequest(t, origin)}

			err := jwksCheckRedirect(mustRedirectRequest(t, tc.target), via)

			if tc.allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errJWKSRedirectRefused)
			assert.NotContains(t, err.Error(), tc.target, "the refusal must not echo the issuer-chosen target")
		})
	}
}

// TestJWKSCheckRedirectBoundsTheHopChain sits exactly on the cap: installing
// CheckRedirect replaces net/http's own ten-hop default, so a same-origin loop
// is bounded only by this guard.
func TestJWKSCheckRedirectBoundsTheHopChain(t *testing.T) {
	const origin = "https://issuer.example.com/.well-known/jwks.json"
	tests := []struct {
		name    string
		hops    int
		allowed bool
	}{
		{name: "one_hop_below_the_cap", hops: maxJWKSRedirects - 1, allowed: true},
		{name: "exactly_at_the_cap", hops: maxJWKSRedirects, allowed: true},
		{name: "one_hop_past_the_cap", hops: maxJWKSRedirects + 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			via := make([]*nethttp.Request, tc.hops)
			for i := range via {
				via[i] = mustRedirectRequest(t, origin)
			}

			err := jwksCheckRedirect(mustRedirectRequest(t, origin), via)

			if tc.allowed {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, errJWKSRedirectRefused)
		})
	}
}

// TestJWKSFetchFollowsASameOriginRedirect is the compatibility half of the
// policy: an issuer that serves its key set from a same-host https redirect
// still works, and the hop is visible in the request log.
func TestJWKSFetchFollowsASameOriginRedirect(t *testing.T) {
	srv := newJWKSFixture(t)
	srv.SetRedirectLocation(srv.URL())
	cfg := jwksConfig(srv)
	cfg.JWKSURI = srv.RedirectURL()

	v, err := NewVerifier(cfg, nil, nil, jwksRedirectClient(t, srv))

	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, v.Close()) })
	assert.Equal(t, 2, srv.RequestCount(), "the redirect hop plus the key set fetch")
}

// TestJWKSFetchRefusesAnOffOriginRedirect proves the trust anchor cannot be
// moved to another origin — here a second fake issuer on its own port, with its
// own certificate. The destination must never be contacted at all, which is what
// separates a refused hop from one that was followed and then failed.
func TestJWKSFetchRefusesAnOffOriginRedirect(t *testing.T) {
	srv := newJWKSFixture(t)
	elsewhere := newJWKSFixture(t)
	srv.SetRedirectLocation(elsewhere.URL())
	cfg := jwksConfig(srv)
	cfg.JWKSURI = srv.RedirectURL()

	v, err := NewVerifier(cfg, nil, nil, jwksRedirectClient(t, srv))

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Equal(t, 1, srv.RequestCount(), "only the redirect hop itself")
	assert.Equal(t, 0, elsewhere.RequestCount(), "the off-origin destination must never be fetched")
}

// TestJWKSFetchRefusesAnHTTPSDowngrade is the same proof for the scheme: the
// plaintext destination records no request.
func TestJWKSFetchRefusesAnHTTPSDowngrade(t *testing.T) {
	var plaintextHits atomic.Int64
	plaintext := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		plaintextHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	}))
	t.Cleanup(plaintext.Close)

	srv := newJWKSFixture(t)
	srv.SetRedirectLocation(plaintext.URL + authtesting.JWKSPath)
	cfg := jwksConfig(srv)
	cfg.JWKSURI = srv.RedirectURL()

	v, err := NewVerifier(cfg, nil, nil, jwksRedirectClient(t, srv))

	assert.Nil(t, v)
	require.Error(t, err)
	assert.Equal(t, int64(0), plaintextHits.Load(), "the plaintext destination must never be fetched")
}
