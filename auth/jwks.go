package auth

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	nethttp "net/http"
	"net/url"
	"strings"
	"sync"
	"time"

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

	// maxDroppedKidsNamed and maxDroppedKidRunes bound the dropped-keys WARN. A
	// hostile issuer can publish thousands of unusable entries carrying kids of
	// any length, and the line would otherwise grow to the whole body cap on
	// every refresh. The COUNT is always exact; only the naming is bounded.
	maxDroppedKidsNamed = 10
	maxDroppedKidRunes  = 64

	// droppedKidEllipsis marks a kid the WARN truncated.
	droppedKidEllipsis = "…"

	// maxJWKSRedirects bounds the redirect chain. Setting CheckRedirect replaces
	// net/http's own ten-hop default outright, so without a cap of our own a
	// same-origin redirect loop would spin until the fetch deadline.
	maxJWKSRedirects = 5

	// schemeHTTPS is the only scheme a key set hop may use.
	schemeHTTPS = "https"
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

	// base is the resolver-lifetime context every fetch derives from, and
	// baseCancel ends it in close. It is deliberately rooted at
	// context.Background rather than at a caller's context: see fetchBase.
	base       context.Context // NOSONAR S8242: resolver-lifetime cancellation, not a request context - close must abort an in-flight fetch that no caller is waiting on
	baseCancel context.CancelFunc

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
	// closed is set by close. It gates every attempt, so a stopped resolver
	// issues no further requests even when an unknown kid keeps arriving.
	closed bool

	// now is the clock behind the TTL, stale-ceiling and rate-floor comparisons.
	// Tests in this package replace it directly, exactly as Verifier does; there
	// is deliberately no exported option.
	now func() time.Time

	stop             chan struct{}
	done             chan struct{}
	stopOnce         sync.Once
	unregisterGauges func()
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

	base, baseCancel := context.WithCancel(context.Background())
	r := &jwksResolver{
		uri:          cfg.JWKSURI,
		client:       client,
		log:          log,
		metrics:      m,
		ttl:          cfg.JWKS.TTL,
		staleCeiling: cfg.JWKS.StaleCeiling,
		minRefresh:   cfg.JWKS.MinRefreshInterval,
		maxBodyBytes: cfg.JWKS.MaxBodyBytes,
		base:         base,
		baseCancel:   baseCancel,
		now:          time.Now,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(base, jwksFetchTimeout)
	defer cancel()
	if err := r.fetchAndStore(ctx, true); err != nil {
		baseCancel()
		return nil, fmt.Errorf("auth: initial issuer key set fetch failed: %w", err)
	}

	r.unregisterGauges = m.registerKeySetGauges(r, cfg.Issuer)
	go r.refreshLoop()
	return r, nil
}

// defaultJWKSClient builds the httpclient used when the caller supplies none.
// The body cap is enforced by a response interceptor, so an oversized key set is
// rejected while it streams instead of after it has been buffered whole, and the
// redirect policy is pinned to the configured origin — see jwksCheckRedirect.
//
// Both protections live on THIS client only. A caller-supplied
// httpclient.Client arrives already built, so neither can be installed on it;
// fetch re-checks the body size as a second line of defense, but a redirect is
// followed by net/http before any framework code runs and has no such backstop.
func defaultJWKSClient(cfg *Config, log logger.Logger) (httpclient.Client, error) {
	if isNilInterface(log) {
		return nil, NewConfigError(jwksFieldPrefix+"client", "a logger is required to build the default jwks http client", nil)
	}
	return httpclient.NewBuilder(log).
		WithPeerName(jwksPeerName(cfg.JWKSURI)).
		WithTimeout(jwksFetchTimeout).
		WithHTTPClient(newJWKSHTTPClient()).
		WithResponseInterceptor(capResponseBody(cfg.JWKS.MaxBodyBytes)).
		Build()
}

// newJWKSHTTPClient is the net/http client the default carries, and exists only
// to install jwksCheckRedirect: Builder has no redirect-policy option, and the
// *http.Client it shallow-copies is the single seam that survives Build. Its
// Transport is deliberately left nil — Build preserves it, so the client dials
// through net/http's default transport exactly as before — and its Timeout is
// filled from WithTimeout.
func newJWKSHTTPClient() *nethttp.Client {
	return &nethttp.Client{CheckRedirect: jwksCheckRedirect}
}

// errJWKSRedirectRefused reports a redirect the key set fetch will not follow.
var errJWKSRedirectRefused = errors.New("auth: jwks redirect refused")

// jwksCheckRedirect is the default client's redirect policy: a hop is followed
// only when it preserves BOTH the original host and the https scheme.
//
// auth.jwt.jwksuri is required to be an https URL with a hostname precisely so
// the key set — the verifier's whole trust anchor — arrives from a known origin
// over TLS. net/http follows redirects before the response is ever parsed, so
// an unrestricted client would hand that guarantee to whoever can answer the
// configured URL with a 302: a cross-host hop moves the trust anchor to an
// origin nothing vouched for, and an https→http hop serves it in the clear to
// any on-path attacker (CWE-346). Both are refused here.
//
// Same-origin hops are ALLOWED rather than refused outright because real
// issuers do serve their key set from one — a path rewrite, a trailing-slash
// normalization, a regional edge. Refusing every redirect would break those
// deployments while adding nothing: a same-host https hop is answered by the
// same TLS identity the direct fetch would have reached. Host is compared
// WITH its port, so an origin-changing port hop is refused too.
func jwksCheckRedirect(req *nethttp.Request, via []*nethttp.Request) error {
	if len(via) > maxJWKSRedirects {
		return fmt.Errorf("%w: more than %d hops", errJWKSRedirectRefused, maxJWKSRedirects)
	}
	origin := via[0].URL
	if req.URL.Scheme != schemeHTTPS {
		return fmt.Errorf("%w: scheme is not %s", errJWKSRedirectRefused, schemeHTTPS)
	}
	if !strings.EqualFold(req.URL.Host, origin.Host) {
		return fmt.Errorf("%w: host does not match the configured endpoint", errJWKSRedirectRefused)
	}
	return nil
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
//
// http.MaxBytesReader is the repo's standard cap (httpclient's JOSE transport,
// server/jose, migration's http source): it errors mid-stream rather than
// truncating, and admits a body of EXACTLY the cap. Its failure is a typed
// *http.MaxBytesError rather than errBodyTooLarge, which isOversizedBody folds
// back into one classification.
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
		resp.Body = nethttp.MaxBytesReader(nil, resp.Body, maxBytes)
		return nil
	}
}

// isOversizedBody reports whether err is an over-cap key set body, from either
// the declared-length refusal or net/http's mid-stream MaxBytesReader failure.
func isOversizedBody(err error) bool {
	if errors.Is(err, errBodyTooLarge) {
		return true
	}
	var maxErr *nethttp.MaxBytesError
	return errors.As(err, &maxErr)
}

// PublicKey implements PublicKeyResolver.
//
// A kid the cached set does not carry triggers one refresh — rate-floored by
// auth.jwt.jwks.minrefreshinterval and coalesced across concurrent callers — and
// the lookup is retried against the result. A key set past its stale ceiling is
// no key set at all: every lookup then reports ErrKeySetUnavailable, and there
// is deliberately no path that accepts a credential it cannot verify.
//
// A CLOSED resolver still answers from the key set it last held, until that set
// passes its stale ceiling; it simply never fetches again.
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
//
// A NEGATIVE age — the clock moved backwards behind us — is treated as stale
// rather than as fresh: a backwards step would otherwise make an arbitrarily old
// key set pass the ceiling forever, which fails open.
func (r *jwksResolver) usableLocked() bool {
	if r.fetchedAt.IsZero() || len(r.keys) == 0 {
		return false
	}
	age := r.now().Sub(r.fetchedAt)
	return age >= 0 && age <= r.staleCeiling
}

// keySetObservation implements keySetObserver for the key-count and age gauges.
func (r *jwksResolver) keySetObservation() (keys int64, ageSeconds float64, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.fetchedAt.IsZero() {
		return 0, 0, false
	}
	return int64(len(r.keys)), r.now().Sub(r.fetchedAt).Seconds(), true
}

// fetchBase is the context every key-set fetch derives from.
//
// It is rooted at context.Background, NOT at the caller whose unknown kid
// triggered the refresh. singleflight shares one fetch across every waiter, so
// inheriting the first caller's values would attribute the issuer request to one
// arbitrary tenant's trace and request id and make the outbound GET carry that
// request's traceparent for all of them. A key set is resolver-wide state, so it
// is fetched as resolver-wide work, under a span of its own.
//
// It is canceled by close, which is the ONLY cancellation a fetch honors: a
// caller that gives up stops waiting, while the fetch every other waiter depends
// on runs on.
//
// The nil fallback serves resolvers built as struct literals in tests.
func (r *jwksResolver) fetchBase() context.Context {
	if r.base == nil {
		return context.Background()
	}
	return r.base
}

// refresh runs at most one fetch across all concurrent callers and waits for it
// on the CALLER's context.
//
// The rate floor is deliberately NOT pre-checked before the singleflight call.
// Entering DoChan unconditionally is what makes a caller arriving DURING an
// in-flight fetch join it and see its result; a cheap pre-check would send that
// caller home with the pre-refresh key set, because the fetch it should have
// waited for has already recorded the attempt that floors it.
//
// A caller whose own context ends first stops WAITING; the fetch completes and
// still updates the cached key set.
func (r *jwksResolver) refresh(ctx context.Context) error {
	results := r.group.DoChan(jwksRefreshKey, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(r.fetchBase(), jwksFetchTimeout)
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
// success. force bypasses the rate floor — but never the closed flag; only the
// construction-time fetch forces, because there is no cached key set to protect
// yet.
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

// attemptAllowedLocked reports whether a fetch may start: never once closed,
// always when forced, otherwise only outside the rate floor. The caller holds
// the write lock.
func (r *jwksResolver) attemptAllowedLocked(force bool) bool {
	if r.closed {
		return false
	}
	if force || r.lastAttempt.IsZero() {
		return true
	}
	return r.now().Sub(r.lastAttempt) >= r.minRefresh
}

// claimAttempt applies the closed flag and the rate floor and records the
// attempt. It reports whether the caller may proceed.
func (r *jwksResolver) claimAttempt(force bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.attemptAllowedLocked(force) {
		return false
	}
	r.lastAttempt = r.now()
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
		if isOversizedBody(err) {
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

// warnDropped emits ONE warning naming the dropped kids, not one line per key:
// an issuer publishing a large non-RSA key set would otherwise flood the log on
// every refresh. The naming is bounded by summarizeDropped; the "dropped" count
// stays exact.
func (r *jwksResolver) warnDropped(dropped []string) {
	if r.log == nil || len(dropped) == 0 {
		return
	}
	r.log.Warn().
		Int("dropped", len(dropped)).
		Str("kids", summarizeDropped(dropped)).
		Msg("auth: ignored unusable jwks entries")
}

// summarizeDropped renders at most maxDroppedKidsNamed kids, each truncated to
// maxDroppedKidRunes, and reports the remainder as a count. The issuer chooses
// both how many entries it publishes and how long each kid is, so an unbounded
// join hands it a log-volume lever bounded only by the body cap.
func summarizeDropped(dropped []string) string {
	named := dropped[:min(len(dropped), maxDroppedKidsNamed)]
	parts := make([]string, len(named))
	for i, kid := range named {
		parts[i] = truncateKid(kid)
	}
	summary := strings.Join(parts, ", ")
	if remaining := len(dropped) - len(named); remaining > 0 {
		summary += fmt.Sprintf(", %s+%d more", droppedKidEllipsis, remaining)
	}
	return summary
}

// truncateKid bounds one kid's rendered length. It cuts on a RUNE boundary, so a
// multi-byte kid cannot be truncated into invalid UTF-8 inside the log line.
func truncateKid(kid string) string {
	runes := []rune(kid)
	if len(runes) <= maxDroppedKidRunes {
		return kid
	}
	return string(runes[:maxDroppedKidRunes]) + droppedKidEllipsis
}

// refreshLoop refreshes the key set ahead of its TTL.
//
// The tick is half the TTL, floored at the configured minimum refresh interval,
// so the cached set is replaced before it expires without the loop ever
// out-running the rate floor. A zero TTL — "treat every entry as due" — falls
// back to the floor, which is required to be positive.
//
// It WAITS on the resolver-lifetime context, so close aborts an in-flight
// background fetch instead of holding shutdown for jwksFetchTimeout.
func (r *jwksResolver) refreshLoop() {
	defer close(r.done)
	ticker := time.NewTicker(r.tickInterval())
	defer ticker.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			// The background refresh takes the same singleflight path as an
			// on-demand one, so a tick landing on an in-flight fetch joins it
			// instead of opening a second connection to the issuer.
			_ = r.refresh(r.fetchBase())
		}
	}
}

// tickInterval is the background refresh period. It is always positive:
// minRefresh is validated positive before a resolver is built.
func (r *jwksResolver) tickInterval() time.Duration {
	return max(r.ttl/2, r.minRefresh)
}

// close stops the background refresh and unregisters the gauges. It is
// idempotent and blocks until the refresh goroutine has exited, so a stopped
// resolver issues no further requests: the closed flag refuses every new
// attempt and the canceled base context aborts any fetch already in flight.
func (r *jwksResolver) close() {
	r.stopOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		if r.baseCancel != nil {
			r.baseCancel()
		}
		close(r.stop)
		<-r.done
		if r.unregisterGauges != nil {
			r.unregisterGauges()
		}
	})
}
