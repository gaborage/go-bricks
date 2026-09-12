# Bearer Credential Verification (`auth`) — Deep Dive

`auth` verifies an `Authorization: Bearer` JWT against a configured issuer and attaches the
identity it asserts — an `auth.Principal` — to the request context, where
`auth.PrincipalFromContext` reads it back. Issuer signing keys come from the issuer's JWKS
endpoint, refreshed in the background and on an unknown `kid`. See
[ADR-109](adr_109_bearer_credential_verification.md) for the design rationale.

> **Scope note:** this is **identification, not authorization** (the stance ADR-039's tenant
> resolution and ADR-043's forwarded client certificate already take). A `Principal` on the
> context means the credential verified against the configured issuer — nothing more. Nothing
> in this package inspects claims, cross-checks the tenant, or decides what an identity may
> do; that stays the handler's decision.

Verification is **RSA-only**: `RS256` and `PS256` are the sole accepted signature algorithms,
and the allowlist is threaded into the parser, so `alg=none`, `HS*` and `ES*` are rejected
before any key is looked up. A key-confusion downgrade to HS256 has no code path to reach.

## Config Reference

Every key lives under `auth.jwt`. The environment variable is the key upper-snake-cased
(`auth.jwt.jwks.ttl` → `AUTH_JWT_JWKS_TTL`).

| Key | Default | Notes |
| --- | --- | --- |
| `auth.jwt.issuer` | *(none)* | Matched **exactly** against the `iss` claim. Required by `Config.Validate`, not by config load: a service that builds no verifier configures none. Also rendered into the `WWW-Authenticate` realm. |
| `auth.jwt.audience` | *(none)* | Accepted `aud` values; the credential must carry at least one of them. At least one entry required by `Config.Validate`; blank entries are rejected at config load. |
| `auth.jwt.jwksuri` | *(none)* | The issuer's key set endpoint. **https with a hostname**, validated at startup. Required by `auth.NewVerifier`; leave unset when pinning keys through `NewVerifierWithResolver`. No OIDC discovery — name the endpoint. |
| `auth.jwt.algorithms` | `[RS256, PS256]` | Closed allowlist, matched case-sensitively. Any other spelling fails startup. |
| `auth.jwt.leeway` | `30s` | Clock skew absorbed on `exp`/`nbf`/`iat`, and nothing else. Non-negative, capped at `config.MaxAuthLeeway` (5m). |
| `auth.jwt.typ` | *(unset)* | Optional JOSE `typ` allowlist (e.g. `at+jwt`), compared case-insensitively per RFC 7515. Unset means no `typ` check. |
| `auth.jwt.jwks.ttl` | `15m` | How long a fetched key set is served before a refresh is due. `0` means "treat every entry as due"; negative fails startup. |
| `auth.jwt.jwks.staleceiling` | `1h` | How long a key set may still be served when the issuer is unreachable. Must be ≥ `ttl`, and ≥ `minrefreshinterval` whenever `jwksuri` is set — so `0` is no longer a valid ceiling for a fetching verifier, since it sits below every positive refresh floor. Past the ceiling, every lookup is a 503. A key set whose age reads *negative* (the clock stepped backwards) is stale too, never fresh. |
| `auth.jwt.jwks.minrefreshinterval` | `30s` | Floor between key set fetches, so an attacker-chosen unknown `kid` cannot drive traffic at the issuer. Must be **positive, and no greater than `staleceiling`, when `jwksuri` is set**; merely non-negative otherwise — a pinned-key deployment fetches nothing and may leave it zero. A floor above the ceiling is a self-inflicted outage. Also the source of the 503's `Retry-After`. |
| `auth.jwt.jwks.maxbodybytes` | `1048576` | Cap on the fetched key set body (1 MiB). Same conditional rule: positive when `jwksuri` is set, non-negative otherwise — and, on that same condition, never above `config.MaxAuthJWKSBodyBytes` (16 MiB; exactly 16 MiB is accepted). The ceiling keeps the cap arithmetic-safe downstream and stops a stray digit turning the body cap into no cap. |
| `auth.jwt.telemetry.enduserid` | `false` | Opt in to the `enduser.id` span attribute. **It records the credential's subject** — see [Security](#security). |

```yaml
auth:
  jwt:
    issuer: "https://issuer.example.com"
    audience:
      - "api://orders"
    jwksuri: "https://issuer.example.com/.well-known/jwks.json"
    algorithms: [RS256]
    leeway: 30s
    typ:
      - at+jwt
    jwks:
      ttl: 15m
      staleceiling: 1h
      minrefreshinterval: 30s
      maxbodybytes: 1048576
    telemetry:
      enduserid: false
```

Two layers validate this section. `config.Validate` (see `config/auth_section.go`) rejects
only what is wrong for **every** deployment — an algorithm outside the closed set, a negative
duration, a stale ceiling below its TTL, a plaintext JWKS endpoint — plus the four rules that
become live the moment `jwksuri` is set: a positive refresh floor, a refresh floor no greater
than the stale ceiling, a positive body cap, and a body cap no greater than 16 MiB.
The rules that hold only once a verifier exists — issuer and audience are required — belong
to `auth.Config.Validate`, and the JWKS-backed resolver re-checks its own group when it is
built, so a caller bypassing `config.Load` is held to the same rule. Either way the failure
is a `*auth.ConfigError` naming the offending key.

## Wiring

There is no framework module, no `ModuleDeps` slot, and no global registration: a consumer
builds its own `Verifier` in `Init`, attaches the middleware to the route groups that need
it, and closes the verifier in `Shutdown`.

```go
package orders

import (
	"github.com/gaborage/go-bricks/app"
	"github.com/gaborage/go-bricks/auth"
	"github.com/gaborage/go-bricks/server"
)

type Module struct {
	verifier *auth.Verifier
}

func (m *Module) Name() string { return "orders" }

func (m *Module) Init(deps *app.ModuleDeps) error {
	// auth.Config is the framework's auth.jwt section, converted at the seam.
	// NewVerifier fetches the issuer key set BEFORE it returns and reports a failed
	// fetch as an error, so a bad endpoint aborts startup rather than failing every
	// request later. The nil argument is the httpclient: nil builds a default one
	// (peer name derived from the JWKS host, body cap enforced while it streams).
	v, err := auth.NewVerifier(
		auth.Config(deps.Config.Auth.JWT),
		deps.Logger,
		deps.MeterProvider,
		nil,
	)
	if err != nil {
		return err // fail fast: a verifier that can verify nothing must not boot
	}
	m.verifier = v
	return nil
}

func (m *Module) RegisterRoutes(hr *server.HandlerRegistry, r server.RouteRegistrar) {
	// PER GROUP. A route that must stay open is exempted by not being in this group —
	// the exemption is visible here, not buried in a skip list.
	guarded := r.Group("/orders", auth.Middleware(m.verifier))
	server.GET(hr, guarded, "/mine", m.listMine)
}

func (m *Module) listMine(_ ListRequest, ctx server.HandlerContext) (server.Result[[]Order], server.IAPIError) {
	// ok == false means the request skipped the middleware. Under the middleware it is
	// always true: a rejection never calls next().
	p, ok := auth.PrincipalFromContext(ctx.RequestContext())
	if !ok {
		return server.Result[[]Order]{}, server.NewUnauthorizedError("Authentication required")
	}
	// Identification only — authorize here, from p.Subject and p.Claim("scope").
	scope, _ := p.Claim("scope")
	_ = scope
	return server.Result[[]Order]{Data: m.forSubject(ctx.RequestContext(), p.Subject)}, nil
}

// Shutdown stops the background key-set refresh. Close is idempotent, always returns nil,
// and blocks until the refresh goroutine has exited. The nil guard matters: Init may have
// returned before the verifier was assigned.
func (m *Module) Shutdown() error {
	if m.verifier == nil {
		return nil
	}
	return m.verifier.Close()
}
```

**Pinned keys instead of JWKS.** `auth.NewVerifierWithResolver(cfg, log, resolver)` takes any
`auth.PublicKeyResolver` — `auth.NewStaticKeyResolver(map[string]*rsa.PublicKey{...})` ships
for out-of-band keys. That verifier fetches nothing, leaves the whole `auth.jwt.jwks.*` group
inert, records no metrics, and **does not own the resolver**: its `Close` is a no-op, so a
resolver with resources of its own is shut down by whoever built it.

**`Verify` without HTTP.** `v.Verify(ctx, credential)` is transport-neutral and returns the
`Principal` rather than attaching it; `auth.ContextWithPrincipal` publishes it. That is the
seam for a non-HTTP interceptor, and `Verifier`'s method set keeps it honest: `Verify` and
`Close` are its whole exported surface, and no method on it constructs a response or touches
a header. Status codes and challenge headers belong to the middleware alone.

## Failure matrix

The middleware returns a `server.IAPIError`, so the framework's error handler renders the
standard `{error, meta}` envelope; **no rejection calls `next()`**. The response body never
names the rule that rejected the credential — the class goes to the `auth.result` metric
attribute, never to an unauthenticated caller.

Not every outcome below is logged. A credential that was actually *judged* emits exactly one
class-only DEBUG line (`auth: credential rejected`), emitted by the verifier where the rule
fires; the middleware adds none of its own. The `missing_credential` row emits **no** DEBUG
breadcrumb at all — nothing was presented to classify, so `Verify` answers the sentinel before
reaching the rejection path — and is visible only as the `missing_credential` value on
`auth.verification.total`. Every outcome, logged or not, is on that counter.

Every row below is in evaluation order, and every row is pinned by
`TestDenyAnswersTheDocumentedFailureMatrix` in `auth/middleware_test.go`; a `Class` constant
with no row there fails `TestDenyCoversEveryDeclaredClassConstant`. This table is a
transcription of that test. `Class` is the value recorded on the metric and — where a DEBUG
line is emitted at all — logged; it is never rendered into the response. `missing_credential`
is the counter's label for a request that presented nothing, not a `Class` constant.

Unless a row says otherwise, the status is 401, the code is `UNAUTHORIZED` and the response
header is `WWW-Authenticate: Bearer error="invalid_token"`.

| Failure | `Class` | Status | Code | Response header |
| --- | --- | --- | --- | --- |
| No `Authorization` header, a non-`Bearer` scheme, no space after the scheme, or an empty/whitespace token | `missing_credential` | 401 | `UNAUTHORIZED` | `WWW-Authenticate: Bearer realm="<issuer>"` |
| Credential longer than 64 KiB | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| `alg` outside the configured allowlist — `none`, `HS*`, `ES*` included | `algorithm` | 401 | `UNAUTHORIZED` | *(default)* |
| Not a parsable compact JWS, or more than one signature | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| Protected header carries no `kid` | `kid_missing` | 401 | `UNAUTHORIZED` | *(default)* |
| Protected `typ` not in `auth.jwt.typ` (only when that key is set) | `type` | 401 | `UNAUTHORIZED` | *(default)* |
| `kid` absent from an otherwise usable key set, after one rate-floored refresh | `kid_unknown` | 401 | `UNAUTHORIZED` | *(default)* |
| Key set never fetched or past `staleceiling`, or a resolver fault | `key_set_unavailable` | **503** | `SERVICE_UNAVAILABLE` | `Retry-After: <minrefreshinterval, floored at 1s>` |
| Signature does not verify | `signature` | 401 | `UNAUTHORIZED` | *(default)* |
| Payload is not a JSON object | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| `iss` missing, not a string, or not exactly `auth.jwt.issuer` | `issuer` | 401 | `UNAUTHORIZED` | *(default)* |
| `aud` is neither a string nor an array of strings | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| `aud` shares no value with `auth.jwt.audience` | `audience` | 401 | `UNAUTHORIZED` | *(default)* |
| `exp` present but not a numeric date, or outside the accepted range | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| `exp` absent | `missing_expiry` | 401 | `UNAUTHORIZED` | *(default)* |
| `exp` at or before `now - leeway` (exclusive deadline, RFC 7519 §4.1.4) | `expired` | 401 | `UNAUTHORIZED` | *(default)* |
| `nbf` present but not a numeric date, or outside the accepted range | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| `nbf` after `now + leeway` (equality passes, RFC 7519 §4.1.5) | `not_yet_valid` | 401 | `UNAUTHORIZED` | *(default)* |
| `iat` present but not a numeric date, or outside the accepted range | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |
| `iat` after `now + leeway` | `issued_in_future` | 401 | `UNAUTHORIZED` | *(default)* |
| `sub` present but not a string | `malformed` | 401 | `UNAUTHORIZED` | *(default)* |

A `Class` added later and not routed explicitly falls into `deny`'s default arm and answers
401 with the `invalid_token` challenge — fail-closed, and pinned by its own matrix row.

The 503 is the point of the split: an unreachable issuer is a **server** fault, and answering
401 for it tells a caller holding a valid credential to stop retrying. Callers match on the
sentinels, never on a class: `errors.Is(err, auth.ErrInvalidCredential)` holds for every
rejection above (whatever its class), `auth.ErrMissingCredential` and
`auth.ErrKeySetUnavailable` are deliberately outside that chain, and
`VerificationError.Cause` is **not** in the `errors.Is` chain, so no cause can reclassify a
401. `sub` is not required — a credential may assert an audience-scoped identity with no
subject, and `Principal.Subject` is then empty.

The `WWW-Authenticate` realm is rendered as an HTTP quoted-string: control characters are
dropped and a quote or backslash is escaped, because the issuer is operator-supplied and a
bare quote would close the parameter.

## Key-set lifecycle

- **Fail-fast first fetch.** `NewVerifier` fetches before it returns; a failure is an error,
  and nothing is left running.
- **Background refresh.** A ticker at `max(ttl/2, minrefreshinterval)` refreshes ahead of the
  TTL, taking the same singleflight path as an on-demand refresh, so a tick landing on an
  in-flight fetch joins it instead of opening a second connection.
- **`Close` really stops fetching.** It sets the resolver's closed flag, cancels the
  resolver-lifetime context, and blocks until the refresh goroutine has exited. After it
  returns, *nothing* reaches the issuer again: no unknown-`kid` lookup, no tick, and no
  coalesced waiter. It cancels rather than waits, so an in-flight fetch is aborted instead of
  holding shutdown for the 10s fetch timeout. It is idempotent, always returns nil, and
  unregisters the key-set gauges.
- **Unknown-`kid` refresh.** A `kid` the cached set does not carry triggers one refresh and
  retries the lookup. It is rate-floored by `minrefreshinterval` (measured from the last
  *attempt*, so a failing issuer is not hammered harder than a healthy one) and coalesced
  through singleflight, so concurrent callers share one fetch. A skipped attempt is not a
  failure and is not counted.
- **Resolver-owned fetch.** The GET runs on a resolver-lifetime context rooted at
  `context.Background`, with its own 10s timeout — **not** on the context of the caller whose
  unknown `kid` triggered it. Singleflight shares one call across every waiter, so inheriting
  the first caller's context would both abort the fetch the others depend on and attribute the
  issuer request to one arbitrary tenant. The practical consequence: the JWKS request carries
  **no caller context values** — no inherited `traceparent`, no `X-Request-ID` — and is a
  root span of its own, so a JWKS fetch cannot be correlated to the request that triggered it.
  A caller whose context ends first stops *waiting*; the fetch completes and still updates the
  cache. The only cancellation a fetch honors is `Close`.
- **Stale ceiling.** While the issuer is unreachable the last good set keeps verifying until
  it is older than `staleceiling`. Past that it is no key set at all — every lookup reports
  `ErrKeySetUnavailable` (503). There is no path that accepts a credential it cannot verify.
  A *negative* age — the clock stepped backwards behind the resolver — is unusable for the
  same reason: it fails closed rather than making an arbitrarily old set pass the ceiling
  forever. A **closed** resolver keeps answering from the set it last held under exactly these
  rules, and then reports `ErrKeySetUnavailable` permanently, because it can no longer refetch.
- **Body cap, twice.** The default client's response interceptor refuses a declared
  `Content-Length` over `maxbodybytes` without reading a byte, and otherwise wraps the body in
  `net/http.MaxBytesReader` — which admits a body of *exactly* the cap and fails one byte past
  it, never truncating, since a truncated JWKS parses as a *shorter* key set. The mid-stream
  failure is therefore a `*http.MaxBytesError`, not the package's internal sentinel; both
  classify as `error.type=oversized`, so a consumer matching the sentinel across a custom
  client should match `*http.MaxBytesError` too. A caller-supplied `httpclient.Client` has
  already buffered the body, so a second length check after the fetch covers it.
- **Redirects stay on the configured origin.** The framework-built client refuses any hop that
  leaves it — a cross-host redirect or an `https`→`http` downgrade — and caps a same-origin
  chain at five, since setting `CheckRedirect` replaces net/http's own ten-hop limit rather
  than adding to it. Same-host `https` hops are allowed, because issuers do serve a key set
  through a path rewrite or a regional edge and such a hop is answered by the same TLS
  identity the direct fetch would have reached. **A caller-supplied `httpclient.Client` cannot
  be given this policy**: the `*http.Client` inside it is unexported, and net/http follows a
  redirect before any framework code sees a response — so unlike the body cap, there is no
  second line of defense. Prefer the framework-built client unless you need your own
  interceptors.
- **Unusable entries are dropped, not fatal.** A non-RSA `kty`, an `use` other than `sig`, a
  present-but-non-empty `key_ops` that does not contain `verify` (an absent `key_ops` is fine —
  most issuers omit it), a missing `kid`, a duplicate `kid`, an undecodable or padded base64
  value, a modulus outside 2048–16384 bits, an **even** modulus (an RSA modulus is a product of
  two odd primes, so an even one is not a key), or an even/unit exponent — each is dropped and reported in **one** WARN per
  refresh (`auth: ignored unusable jwks entries`). Only a document yielding no usable key at
  all fails the refresh. The WARN's naming is **bounded**: at most 10 kids are named, each
  truncated to 64 runes, with a `…+N more` tail — the issuer chooses both how many entries it
  publishes and how long each kid is, so an unbounded join would hand it a log-volume lever.
  The `dropped` count field stays exact, so do not read the `kids` field as the full list.

## Metrics

Meter `go-bricks/auth`, off the `MeterProvider` passed to `NewVerifier` (nil falls back to the
global one). A verifier built through `NewVerifierWithResolver` records nothing. Instrument
creation failures degrade to a no-op and a stderr WARNING, and so do the gauge-callback
registration and unregistration — which report as an `auth metrics operation … failed`
WARNING rather than as a failure to initialize a metric, since no instrument is missing.
Telemetry never fails a verification.

| Metric | Instrument | Unit | Attributes |
| --- | --- | --- | --- |
| `auth.verification.total` | Int64 counter | `{verification}` | `auth.result`: `success`, `missing_credential`, `key_set_unavailable`, or the rejection `Class` (`malformed`, `algorithm`, `kid_missing`, `kid_unknown`, `type`, `signature`, `issuer`, `audience`, `expired`, `not_yet_valid`, `issued_in_future`, `missing_expiry`) |
| `auth.keyset.refresh.total` | Int64 counter | `{refresh}` | `auth.result`: `success` or `failure`; on failure also `error.type`: `transport`, `status`, `oversized`, `parse`, `empty_key_set` |
| `auth.keyset.age` | Float64 observable gauge | `s` | `auth.issuer` — age of the cached set since its last successful fetch |
| `auth.keyset.key.count` | Int64 observable gauge | `{key}` | `auth.issuer` — usable RSA keys in the cached set |

`auth.issuer` is the configured `auth.jwt.issuer` (the literal `unset` when unconfigured). It is
on the gauges because two verifiers sharing one `MeterProvider` register two callbacks against the
same instruments, and the OpenTelemetry callback contract requires observations to be unique —
without an identity the two series collide and the SDK's behavior is undefined. Issuer is safe as
an attribute because it is per-process configuration: it is never caller-, request- or
credential-derived, so no amount of traffic can inflate its cardinality.

Exactly one `auth.verification.total` observation is recorded per `Verify` call, including the
`missing_credential` one for a request with no usable `Authorization` header. The gauges are
registered only on the JWKS path and observe nothing before the first successful fetch.
`error.type` names the **stage** that failed, never the underlying error text, so the
dimension stays low-cardinality.

## Security

**What never leaves the package.** Nothing logs, records or renders the credential string or the
signature bytes, ever. The `sub` claim has exactly one sanctioned sink, off by default: with
`auth.jwt.telemetry.enduserid: true` the middleware records `Principal.Subject` as `enduser.id` on
the request span, described further down this section. Nothing else — no log line, no metric
attribute, no error string — carries it in either setting. A rejection is reported by `Class` only —
`VerificationError.Error` renders the class and never `Cause`, and it implements `fmt.Formatter`
so `%v`, `%s`, `%q` **and `%#v`** all render the same safe body rather than dumping the exported
`Cause` field. `Cause` exists for framework DEBUG inspection, is not part of the compatibility
promise, and must not be rendered into a response body. The resolver's own error is deliberately
dropped: a `PublicKeyResolver` is consumer-supplied and could otherwise push arbitrary text into
`Cause` or reclassify a 503 as a 401.

**`Principal` redacts itself — except on one path.** `String`, `Format` and `MarshalJSON` all
derive from one redacted shape that keeps issuer, audience and expiry and replaces the subject
and every claim value with an elision marker, for both `Principal` and `*Principal`.

> **The framework logger's reflective filter bypasses all of it.**
> `logger.LogEventAdapter.Interface` and `Logger.WithFields` route through
> `SensitiveDataFilter.FilterValue`, which rebuilds a struct into a `map[string]any` by
> reflection — reading exported fields directly, before any marshaler or formatter runs. No
> method on `Principal` can influence that, and the filter matches field **names**, so neither
> `Subject` nor an issuer-chosen claim key is masked. **Do not hand a `Principal` to
> `logger.Interface` or `WithFields`** (tracked as [gaborage/go-bricks#1602](https://github.com/gaborage/go-bricks/issues/1602)).
> Log `p.Subject`-derived values only after you have decided they are safe, or log the
> `Principal` through `%v`/`%s`, which is elided.

**`enduser.id` is the one opt-in that records a subject.** With
`auth.jwt.telemetry.enduserid: true` the middleware stamps `enduser.id` (the OTel
semantic-convention attribute) with `Principal.Subject` on the request span, for every verified
credential. It is off by default; turning it on sends subjects to the trace backend, which is a
deliberate operator decision.

**Credentials come from `Authorization: Bearer` only.** Cookies and query parameters are
deliberately not consulted: a credential the browser attaches on its own is a CSRF surface, and
a credential in a URL lands in access logs. The scheme is matched case-insensitively per
RFC 7235.

**`Principal.Claims` and resolved keys are read-only.** Neither is copied on the way out, so
every reader of a request's context holds the same map, and a write in one handler corrupts what
every other reader sees — a data **race** once a `context.WithoutCancel` background goroutine is
one of those readers. Copy any value out before modifying it.

## Testing

`auth/testing` is a fake issuer and a fake JWKS endpoint, so a service can test its guarded
routes without a real IdP.

- `authtesting.NewIssuer()` mints credentials with `Mint(Claims{...})` / `MintWith(MintOptions{...})`
  and carries a single-shape minter for each failure the matrix above lists — `MintExpired`,
  `MintExpiredWithin`, `MintWrongAudience`, `MintWrongIssuer`, `MintUnknownKeyID`,
  `MintMissingKeyID`, `MintMissingExpiry`, `MintFutureNotBefore`, `MintFutureIssuedAt`,
  `MintAlgNone`, `MintES256`, `MintWithType`, `MintBadSignature`, `MintCorruptSignature`,
  `MintPS256`. `Rotate(kid)` switches the active signing key and keeps the old one verifiable.
- `authtesting.NewJWKSServer(issuer)` serves the issuer's keys over TLS. `SetMode` drives the
  failure modes (`JWKSServerError`, `JWKSMalformed`, `JWKSOversized`, `JWKSNonRSAOnly`, back to
  `JWKSHealthy`), `SetOversizedBytes` sizes the padding that exercises the body cap, `AddECKey` / `AddRawKey` exercise the drop-unusable-entries path, and `Requests()` /
  `RequestCount()` assert the rate floor and the singleflight coalescing. `HTTPClient()` returns a
  client trusting its certificate.
- `issuer.PublicKeys()` feeds `auth.NewStaticKeyResolver` directly for tests that want no HTTP at
  all.

## Related

- [ADR-109](adr_109_bearer_credential_verification.md) — this package's design decisions
- [wiki/global_middleware.md](global_middleware.md) — the *global* middleware seam, and why this
  middleware deliberately does not use it
- [wiki/forwarded_client_cert.md](forwarded_client_cert.md) and
  [wiki/multi_tenant_resolvers.md](multi_tenant_resolvers.md) — the other
  identification-not-authorization middlewares
- [wiki/jose.md](jose.md) — body-level protection, a different layer with its own key material
- [wiki/observability.md](observability.md) — the log filter this package's `Principal` warning
  refers to
