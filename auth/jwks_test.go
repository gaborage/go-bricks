package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
				require.ErrorIs(t, err, errBodyTooLarge)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestCapResponseBodyToleratesAResponseWithoutABody(t *testing.T) {
	interceptor := capResponseBody(16)

	require.NoError(t, interceptor(context.Background(), nil, nil))
	require.NoError(t, interceptor(context.Background(), nil, &nethttp.Response{}))
}

// jwk renders one JWKS entry for the parser tests.
func jwk(members map[string]string) string {
	parts := make([]string, 0, len(members))
	for _, key := range []string{"kty", "kid", "use", "n", "e", "crv"} {
		if value, ok := members[key]; ok {
			parts = append(parts, fmt.Sprintf("%q:%q", key, value))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// validModulus is a 2048-bit modulus in the base64url encoding RFC 7518 mandates.
func validModulus() string {
	return base64.RawURLEncoding.EncodeToString(append([]byte{0xC0}, make([]byte, 255)...))
}

func TestParseJWKSDropsUnusableEntries(t *testing.T) {
	tests := []struct {
		name    string
		members map[string]string
	}{
		{name: "non_rsa_kty", members: map[string]string{"kty": "EC", "kid": "k", "crv": "P-256"}},
		{name: "missing_kid", members: map[string]string{"kty": "RSA", "n": validModulus(), "e": "AQAB"}},
		{name: "encryption_use", members: map[string]string{"kty": "RSA", "kid": "k", "use": "enc", "n": validModulus(), "e": "AQAB"}},
		{name: "missing_modulus", members: map[string]string{"kty": "RSA", "kid": "k", "e": "AQAB"}},
		{name: "padded_modulus", members: map[string]string{"kty": "RSA", "kid": "k", "n": "AAAA=", "e": "AQAB"}},
		{name: "modulus_below_the_floor", members: map[string]string{"kty": "RSA", "kid": "k", "n": "AQAB", "e": "AQAB"}},
		{name: "missing_exponent", members: map[string]string{"kty": "RSA", "kid": "k", "n": validModulus()}},
		{name: "even_exponent", members: map[string]string{"kty": "RSA", "kid": "k", "n": validModulus(), "e": "BAAA"}},
		{name: "unit_exponent", members: map[string]string{"kty": "RSA", "kid": "k", "n": validModulus(), "e": "AQ"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			keys, dropped, err := parseJWKS([]byte(`{"keys":[` + jwk(tc.members) + `]}`))

			require.NoError(t, err)
			assert.Empty(t, keys)
			assert.Len(t, dropped, 1)
		})
	}
}

func TestParseJWKSKeepsTheFirstOfADuplicateKid(t *testing.T) {
	first := jwk(map[string]string{"kty": "RSA", "kid": "dup", "n": validModulus(), "e": "AQAB"})
	second := jwk(map[string]string{"kty": "RSA", "kid": "dup", "n": base64.RawURLEncoding.EncodeToString(append([]byte{0xFF}, make([]byte, 255)...)), "e": "AQAB"})

	keys, dropped, err := parseJWKS([]byte(`{"keys":[` + first + "," + second + `]}`))

	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, []string{"dup"}, dropped)
	assert.Equal(t, byte(0xC0), keys["dup"].N.Bytes()[0], "the first entry must win")
}

func TestParseJWKSNamesAnEntryWithoutAKid(t *testing.T) {
	body := []byte(`{"keys":[` + jwk(map[string]string{"kty": "EC", "crv": "P-256"}) + `]}`)

	_, dropped, err := parseJWKS(body)

	require.NoError(t, err)
	assert.Equal(t, []string{noKidPlaceholder}, dropped)
}

func TestParseJWKSRejectsANonDocument(t *testing.T) {
	_, _, err := parseJWKS([]byte("not json"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not valid json")
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

// countingCloser reports whether the reader it wraps was closed.
type countingCloser struct {
	io.Reader
	closed bool
}

func (c *countingCloser) Close() error {
	c.closed = true
	return nil
}

func TestCappedBodyClosesTheUnderlyingBody(t *testing.T) {
	inner := &countingCloser{Reader: strings.NewReader("{}")}
	body := &cappedBody{inner: inner, allowance: 8}

	require.NoError(t, body.Close())

	assert.True(t, inner.closed)
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

func TestCappedBodyKeepsFailingAfterTheCapIsPassed(t *testing.T) {
	body := &cappedBody{inner: io.NopCloser(strings.NewReader("0123456789")), allowance: 3}

	_, first := body.Read(make([]byte, 8))
	_, second := body.Read(make([]byte, 8))

	require.ErrorIs(t, first, errBodyTooLarge)
	require.ErrorIs(t, second, errBodyTooLarge, "a body past the cap must not become readable again")
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
