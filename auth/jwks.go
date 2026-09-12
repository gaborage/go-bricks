package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	nethttp "net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/singleflight"

	"github.com/gaborage/go-bricks/httpclient"
	"github.com/gaborage/go-bricks/logger"
)

const (
	// jwksFetchTimeout bounds one key-set fetch. The request context is
	// deliberately NOT the caller's (see refresh), so the fetch needs a deadline
	// of its own or a hung issuer would pin a goroutine forever. It is a
	// framework constant rather than a config key: it bounds one HTTP GET of a
	// small JSON document, which has no deployment-specific right answer.
	jwksFetchTimeout = 10 * time.Second

	// jwksRefreshKey is the singleflight key. There is exactly one key set per
	// resolver, so every refresh coalesces onto one constant.
	jwksRefreshKey = "jwks"

	// ktyRSA is the only "kty" this package can use; useSignature the only "use"
	// it accepts when the member is present.
	ktyRSA       = "RSA"
	useSignature = "sig"

	// RSA modulus bounds. Below the floor the key is not worth verifying
	// against; above the ceiling a single signature check becomes a denial of
	// service the issuer can hand us. A key outside the range is dropped like
	// any other unusable entry, named in the WARN.
	minRSAModulusBits = 2048
	maxRSAModulusBits = 16384

	// maxRSAExponentBits bounds "e" so the int conversion below cannot overflow.
	maxRSAExponentBits = 31

	// noKidPlaceholder labels an entry with no "kid" in the dropped-keys WARN.
	noKidPlaceholder = "<no kid>"
)

var _ PublicKeyResolver = (*jwksResolver)(nil)

// jwksResolver is a PublicKeyResolver over an issuer's JWKS endpoint. It holds
// the last successfully fetched key set, refreshes it in the background before
// the TTL lapses, and refreshes on demand when an unknown kid arrives — at most
// once per MinRefreshInterval, coalesced across concurrent callers.
//
// It is safe for concurrent use.
type jwksResolver struct {
	uri          string
	client       httpclient.Client
	log          logger.Logger
	metrics      *authMetrics
	ttl          time.Duration
	staleCeiling time.Duration
	minRefresh   time.Duration
	maxBodyBytes int64

	group singleflight.Group

	mu sync.RWMutex
	// keys is the last successfully fetched key set. It is replaced wholesale,
	// never written in place, so a map handed to a reader under RLock stays
	// immutable for that reader's lifetime.
	keys map[string]*rsa.PublicKey
	// fetchedAt is when keys was fetched; lastAttempt is when a refresh was last
	// STARTED, successful or not. The rate floor reads lastAttempt, so a failing
	// issuer cannot be hammered any harder than a healthy one.
	fetchedAt   time.Time
	lastAttempt time.Time

	// now is the clock behind the TTL, stale-ceiling and rate-floor comparisons.
	// Tests in this package replace it directly, exactly as Verifier does; there
	// is deliberately no exported option.
	now func() time.Time

	stop             chan struct{}
	done             chan struct{}
	stopOnce         sync.Once
	unregisterGauges func()
}

// NewVerifier builds a verifier over the issuer's JWKS endpoint. It is the
// consumer-facing door: the pinned-key NewVerifierWithResolver is for
// deployments that carry issuer keys out of band.
//
// The key set is fetched before this returns and a failed fetch is an error, so
// a module Init aborts startup rather than booting a verifier that can verify
// nothing. cfg is validated in full first — the auth.jwt.* rules Config.Validate
// owns plus the auth.jwt.jwks.* group, which only a fetching resolver makes
// live.
//
// mp may be nil, in which case the global MeterProvider is used. client may be
// nil, in which case a default httpclient is built with a peer name derived from
// the key set endpoint's host; building that default needs a logger, so a nil
// log and a nil client together are a configuration error.
//
// Ownership: the returned verifier CONSTRUCTED its resolver, so its Close stops
// the background refresh. Call it from the module's Shutdown.
//
//nolint:gocritic // hugeParam: Config is the injected value type, matching NewVerifierWithResolver.
func NewVerifier(cfg Config, log logger.Logger, mp metric.MeterProvider, client httpclient.Client) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfgErr := cfg.validateJWKSSource(); cfgErr != nil {
		return nil, cfgErr
	}
	if isNilInterface(log) {
		log = nil
	}

	m := newAuthMetrics(mp)
	resolver, err := newJWKSResolver(&cfg, log, m, client)
	if err != nil {
		return nil, err
	}

	verifier, err := NewVerifierWithResolver(cfg, log, resolver)
	if err != nil {
		resolver.close()
		return nil, err
	}
	verifier.metrics = m
	verifier.owned = resolver
	return verifier, nil
}

// newJWKSResolver builds the resolver, performs the fail-fast initial fetch and
// starts the background refresh. Every failure leaves nothing running.
func newJWKSResolver(cfg *Config, log logger.Logger, m *authMetrics, client httpclient.Client) (*jwksResolver, error) {
	if isNilInterface(client) {
		built, err := defaultJWKSClient(cfg, log)
		if err != nil {
			return nil, err
		}
		client = built
	}

	r := &jwksResolver{
		uri:          cfg.JWKSURI,
		client:       client,
		log:          log,
		metrics:      m,
		ttl:          cfg.JWKS.TTL,
		staleCeiling: cfg.JWKS.StaleCeiling,
		minRefresh:   cfg.JWKS.MinRefreshInterval,
		maxBodyBytes: cfg.JWKS.MaxBodyBytes,
		now:          time.Now,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
	defer cancel()
	if err := r.fetchAndStore(ctx, true); err != nil {
		return nil, fmt.Errorf("auth: initial issuer key set fetch failed: %w", err)
	}

	r.unregisterGauges = m.registerKeySetGauges(r)
	go r.refreshLoop()
	return r, nil
}

// defaultJWKSClient builds the httpclient used when the caller supplies none.
// The body cap is enforced by a response interceptor, so an oversized key set is
// rejected while it streams instead of after it has been buffered whole.
func defaultJWKSClient(cfg *Config, log logger.Logger) (httpclient.Client, error) {
	if isNilInterface(log) {
		return nil, NewConfigError(jwksFieldPrefix+"client", "a logger is required to build the default jwks http client", nil)
	}
	return httpclient.NewBuilder(log).
		WithPeerName(jwksPeerName(cfg.JWKSURI)).
		WithTimeout(jwksFetchTimeout).
		WithResponseInterceptor(capResponseBody(cfg.JWKS.MaxBodyBytes)).
		Build()
}

// jwksPeerName derives the low-cardinality peer.service label from the key set
// endpoint's host. An unparsable URI cannot reach here — validateJWKSSource ran
// first — so the empty fallback is defensive only.
func jwksPeerName(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil || parsed.Hostname() == "" {
		return "jwks"
	}
	return parsed.Hostname()
}

// errBodyTooLarge reports a key set body past the configured cap.
var errBodyTooLarge = errors.New("auth: jwks response body exceeds the configured cap")

// capResponseBody bounds the response body at maxBytes. The interceptor runs
// before the client reads the body, so replacing the body here caps what is ever
// buffered. An over-cap body surfaces as a read error, which the client turns
// into a failed request — never a panic.
func capResponseBody(maxBytes int64) httpclient.ResponseInterceptor {
	return func(_ context.Context, _ *nethttp.Request, resp *nethttp.Response) error {
		if resp == nil || resp.Body == nil {
			return nil
		}
		// A declared Content-Length past the cap is refused without reading a
		// byte; a chunked or mis-declared body is caught by the reader below.
		if resp.ContentLength > maxBytes {
			return errBodyTooLarge
		}
		resp.Body = &cappedBody{inner: resp.Body, allowance: maxBytes + 1}
		return nil
	}
}

// cappedBody fails the read once the body produces more than the cap. It
// deliberately errors rather than truncating: a truncated JWKS would parse as
// malformed at best and as a SHORTER key set at worst.
//
// allowance is the cap PLUS ONE byte: a body of exactly the cap must succeed,
// so the overflow is detected by reading one byte past it rather than by the
// allowance reaching zero on a body that was still within bounds.
type cappedBody struct {
	inner     io.ReadCloser
	allowance int64
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.allowance <= 0 {
		return 0, errBodyTooLarge
	}
	if int64(len(p)) > c.allowance {
		p = p[:c.allowance]
	}
	n, err := c.inner.Read(p)
	c.allowance -= int64(n)
	if c.allowance <= 0 {
		return n, errBodyTooLarge
	}
	return n, err
}

func (c *cappedBody) Close() error { return c.inner.Close() }

// PublicKey implements PublicKeyResolver.
//
// A kid the cached set does not carry triggers one refresh — rate-floored by
// auth.jwt.jwks.minrefreshinterval and coalesced across concurrent callers — and
// the lookup is retried against the result. A key set past its stale ceiling is
// no key set at all: every lookup then reports ErrKeySetUnavailable, and there
// is deliberately no path that accepts a credential it cannot verify.
//
// The returned key is the resolver's own and MUST NOT be mutated. See the
// PublicKeyResolver aliasing contract.
func (r *jwksResolver) PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if key, ok := r.lookup(kid); ok {
		return key, nil
	}

	// The refresh error is deliberately discarded: the answer below is decided
	// by the resolver's STATE, not by why one fetch failed, and forwarding a
	// fetch error would let transport text reach the verifier.
	_ = r.refresh(ctx)

	if key, ok := r.lookup(kid); ok {
		return key, nil
	}
	if !r.usable() {
		return nil, ErrKeySetUnavailable
	}
	return nil, ErrKidUnknown
}

// lookup reads kid out of a usable key set. It reports ok=false both for an
// absent kid and for a key set that is past its stale ceiling, so the caller
// re-reads state after the refresh to tell the two apart.
func (r *jwksResolver) lookup(kid string) (key *rsa.PublicKey, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.usableLocked() {
		return nil, false
	}
	key, ok = r.keys[kid]
	return key, ok
}

// usable reports whether a key set exists and is inside its stale ceiling.
func (r *jwksResolver) usable() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.usableLocked()
}

// usableLocked is usable's body; the caller holds at least the read lock.
func (r *jwksResolver) usableLocked() bool {
	if r.fetchedAt.IsZero() || len(r.keys) == 0 {
		return false
	}
	return r.now().Sub(r.fetchedAt) <= r.staleCeiling
}

// keySetObservation implements keySetState for the key-count and age gauges.
func (r *jwksResolver) keySetObservation() (keys int64, ageSeconds float64, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fetchedAt.IsZero() {
		return 0, 0, false
	}
	return int64(len(r.keys)), r.now().Sub(r.fetchedAt).Seconds(), true
}

// refresh runs at most one fetch across all concurrent callers and waits for it
// on the CALLER's context.
//
// The fetch itself runs on a context detached from the caller's: singleflight
// shares one call across every waiter, so a request that cancels near its
// deadline would otherwise abort a fetch that other in-flight requests — and the
// cached key set — depend on. context.WithoutCancel keeps the caller's values
// (trace id, tenant) for the outbound request while severing cancellation, and
// jwksFetchTimeout supplies the deadline the caller's would have provided.
//
// A caller whose own context ends first stops WAITING; the detached fetch
// completes and still updates the cached key set.
func (r *jwksResolver) refresh(ctx context.Context) error {
	results := r.group.DoChan(jwksRefreshKey, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jwksFetchTimeout)
		defer cancel()
		return nil, r.fetchAndStore(fetchCtx, false)
	})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result := <-results:
		return result.Err
	}
}

// fetchAndStore performs one refresh attempt and replaces the cached key set on
// success. force bypasses the rate floor; only the construction-time fetch uses
// it, because there is no cached key set to protect yet.
//
// A skipped attempt is not a failure and is not counted: the floor exists so an
// unknown kid cannot be used to hammer the issuer, and counting every skip would
// make the refresh counter measure request volume instead.
func (r *jwksResolver) fetchAndStore(ctx context.Context, force bool) error {
	if !r.claimAttempt(force) {
		return nil
	}

	outcome := r.fetch(ctx)
	r.metrics.recordRefresh(ctx, outcome.errType)
	if outcome.err != nil {
		return outcome.err
	}

	r.warnDropped(outcome.dropped)
	r.mu.Lock()
	r.keys = outcome.keys
	r.fetchedAt = r.now()
	r.mu.Unlock()
	return nil
}

// claimAttempt applies the rate floor and records the attempt. It reports
// whether the caller may proceed.
func (r *jwksResolver) claimAttempt(force bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if !force && !r.lastAttempt.IsZero() && now.Sub(r.lastAttempt) < r.minRefresh {
		return false
	}
	r.lastAttempt = now
	return true
}

// fetchOutcome is one refresh attempt's result: the parsed key set, the kids
// that were dropped as unusable, and — on failure — the closed-vocabulary
// error.type for the refresh counter.
type fetchOutcome struct {
	keys    map[string]*rsa.PublicKey
	dropped []string
	errType string
	err     error
}

// fetch performs the HTTP GET and parses the document. It never returns the
// issuer's response body in an error: the body is attacker-influenced and the
// error can reach a startup log.
func (r *jwksResolver) fetch(ctx context.Context) fetchOutcome {
	resp, err := r.client.Get(ctx, &httpclient.Request{URL: r.uri})
	if err != nil {
		if resp != nil && resp.StatusCode != nethttp.StatusOK {
			return fetchOutcome{errType: refreshErrorStatus, err: fmt.Errorf("auth: jwks endpoint returned status %d", resp.StatusCode)}
		}
		if errors.Is(err, errBodyTooLarge) {
			return fetchOutcome{errType: refreshErrorOversized, err: errBodyTooLarge}
		}
		return fetchOutcome{errType: refreshErrorTransport, err: fmt.Errorf("auth: jwks request failed: %w", err)}
	}
	if resp == nil {
		return fetchOutcome{errType: refreshErrorTransport, err: errors.New("auth: jwks request returned no response")}
	}
	if resp.StatusCode != nethttp.StatusOK {
		return fetchOutcome{errType: refreshErrorStatus, err: fmt.Errorf("auth: jwks endpoint returned status %d", resp.StatusCode)}
	}
	// Second line of defense behind the response interceptor, which only the
	// default client carries: a caller-supplied httpclient.Client has already
	// buffered the body by the time it reaches here, but an over-cap key set
	// still fails the refresh rather than being parsed.
	if int64(len(resp.Body)) > r.maxBodyBytes {
		return fetchOutcome{errType: refreshErrorOversized, err: errBodyTooLarge}
	}

	keys, dropped, err := parseJWKS(resp.Body)
	if err != nil {
		return fetchOutcome{errType: refreshErrorParse, err: err}
	}
	if len(keys) == 0 {
		return fetchOutcome{errType: refreshErrorEmpty, err: errors.New("auth: jwks document carries no usable RSA signing key")}
	}
	return fetchOutcome{keys: keys, dropped: dropped}
}

// warnDropped emits ONE warning naming every dropped kid, not one line per key:
// an issuer publishing a large non-RSA key set would otherwise flood the log on
// every refresh.
func (r *jwksResolver) warnDropped(dropped []string) {
	if r.log == nil || len(dropped) == 0 {
		return
	}
	r.log.Warn().
		Int("dropped", len(dropped)).
		Str("kids", strings.Join(dropped, ", ")).
		Msg("auth: ignored unusable jwks entries")
}

// jwksDocument is the subset of RFC 7517 this package reads.
type jwksDocument struct {
	Keys []jwksKey `json:"keys"`
}

type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// parseJWKS decodes the document and keeps the RSA signing keys. Anything else —
// a non-RSA kty, an encryption-only key, a key with no kid, an undecodable or
// out-of-range modulus or exponent, a duplicate kid — is DROPPED and named in
// dropped, never promoted into an error: one unusable entry must not cost the
// deployment every other key the issuer published.
//
// A document that yields no usable key at all is the caller's failure to report,
// not this function's.
func parseJWKS(body []byte) (keys map[string]*rsa.PublicKey, dropped []string, err error) {
	var doc jwksDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, nil, errors.New("auth: jwks document is not valid json")
	}

	keys = make(map[string]*rsa.PublicKey, len(doc.Keys))
	for i := range doc.Keys {
		entry := &doc.Keys[i]
		key, ok := parseRSAKey(entry)
		if !ok || keys[entry.Kid] != nil {
			dropped = append(dropped, kidLabel(entry.Kid))
			continue
		}
		keys[entry.Kid] = key
	}
	return keys, dropped, nil
}

// kidLabel renders a kid for the dropped-keys warning, standing in for an entry
// that carried none.
func kidLabel(kid string) string {
	if kid == "" {
		return noKidPlaceholder
	}
	return kid
}

// parseRSAKey converts one JWK into an RSA public key, reporting ok=false for
// every entry this package cannot verify with.
func parseRSAKey(entry *jwksKey) (key *rsa.PublicKey, ok bool) {
	if entry.Kty != ktyRSA || entry.Kid == "" {
		return nil, false
	}
	if entry.Use != "" && entry.Use != useSignature {
		return nil, false
	}
	modulus, ok := decodeUint(entry.N, minRSAModulusBits, maxRSAModulusBits)
	if !ok {
		return nil, false
	}
	exponent, ok := decodeUint(entry.E, 1, maxRSAExponentBits)
	if !ok || !exponent.IsInt64() {
		return nil, false
	}
	value := exponent.Int64()
	// An even or unit exponent is not a usable RSA public exponent; rsa.Verify
	// would reject it later, so drop it here where it is named in the WARN.
	if value < 3 || value%2 == 0 {
		return nil, false
	}
	return &rsa.PublicKey{N: modulus, E: int(value)}, true
}

// decodeUint decodes a base64url big-endian unsigned integer and bounds its bit
// length. RFC 7518 mandates the unpadded encoding, so a padded value is
// rejected rather than silently accepted.
func decodeUint(encoded string, minBits, maxBits int) (value *big.Int, ok bool) {
	if encoded == "" {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, false
	}
	value = new(big.Int).SetBytes(raw)
	bits := value.BitLen()
	if bits < minBits || bits > maxBits {
		return nil, false
	}
	return value, true
}

// refreshLoop refreshes the key set ahead of its TTL.
//
// The tick is half the TTL, floored at the configured minimum refresh interval,
// so the cached set is replaced before it expires without the loop ever
// out-running the rate floor. A zero TTL — "treat every entry as due" — falls
// back to the floor, which is required to be positive.
func (r *jwksResolver) refreshLoop() {
	defer close(r.done)
	ticker := time.NewTicker(r.tickInterval())
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), jwksFetchTimeout)
			// The background refresh takes the same singleflight path as an
			// on-demand one, so a tick landing on an in-flight fetch joins it
			// instead of opening a second connection to the issuer.
			_ = r.refresh(ctx)
			cancel()
		}
	}
}

// tickInterval is the background refresh period. It is always positive:
// minRefresh is validated positive before a resolver is built.
func (r *jwksResolver) tickInterval() time.Duration {
	half := r.ttl / 2
	if half < r.minRefresh {
		return r.minRefresh
	}
	return half
}

// close stops the background refresh and unregisters the gauges. It is
// idempotent and blocks until the refresh goroutine has exited, so a stopped
// resolver issues no further requests.
func (r *jwksResolver) close() {
	r.stopOnce.Do(func() {
		close(r.stop)
		<-r.done
		if r.unregisterGauges != nil {
			r.unregisterGauges()
		}
	})
}
