# HTTP Client (Deep Dive)

The `httpclient` package provides a production-ready outbound HTTP client built around a fluent builder, with W3C trace propagation, retries with full-jitter exponential backoff, and an interceptor chain for cross-cutting concerns. It is the recommended client for any module making outbound HTTP calls within a GoBricks service.

## HTTP Client

The `httpclient` package provides a production-ready HTTP client with built-in observability and resilience.

**Key Features:**
- **Builder pattern**: Fluent configuration via `NewBuilder(logger).WithTimeout(...).Build()`
- **W3C trace propagation**: Automatic `traceparent`/`tracestate` header injection
- **Retry with backoff**: Exponential backoff with full jitter, configurable max retries
- **Interceptors**: Request/response interceptor chains for cross-cutting concerns
- **Structured logging**: Info-level metadata (no PII), optional debug payload logging

The logger is required: `NewBuilder`/`NewClient` panic on a nil logger at construction time rather than on the first request, since the built client's logging path dereferences it unguarded. The check covers both a nil interface and a non-nil interface holding a nil pointer (e.g. an unassigned `*logger.ZeroLogger` field); a non-nil logger that panics internally remains the caller's contract to keep.

```go
// Builder pattern with trace propagation
client := httpclient.NewBuilder(logger).
    WithTimeout(10 * time.Second).
    WithRetries(3, 500 * time.Millisecond).
    WithDefaultHeader("Accept", "application/json").
    WithW3CTrace(true).
    Build()

resp, err := client.Get(ctx, &httpclient.Request{
    URL: "https://api.example.com/users",
})
```

**Interface:** `Get`, `Post`, `Put`, `Patch`, `Delete` accept `context.Context` and `*Request`; `Do` additionally takes a `method string` (`Do(ctx, method, req)`). All return `*Response` and `error`.

### Transport composition

`RoundTripper` wrappers (e.g. `WithJOSE`) are applied at `Build()` time in a fixed
layer order, not in the order the `With*` options were called: the base transport
from `WithTransport` sits innermost, request signers sit next, and body transforms
such as JOSE sit outermost. This means calling `WithTransport` before or after
`WithJOSE` produces the same client — the JOSE layer can no longer be silently
discarded by a later `WithTransport` call:

```go
// Equivalent — layer order beats call order.
httpclient.NewBuilder(logger).WithJOSE(cfg).WithTransport(mTLSTransport).Build()
httpclient.NewBuilder(logger).WithTransport(mTLSTransport).WithJOSE(cfg).Build()
```

A `Transport` set directly on the `*http.Client` passed to `WithHTTPClient` is
**replaced, not wrapped**, as soon as any wrapper option is used: the chain then has
no base transport and dials via `net/http.DefaultTransport` — silently losing your
client certificate, pinned `RootCAs`, `MinVersion` and proxy settings. `Build()` logs
a WARN when it detects this. Always supply the base RoundTripper through
`WithTransport`:

```go
// WRONG — mTLS transport is replaced; requests dial via net/http.DefaultTransport.
httpclient.NewBuilder(logger).
    WithHTTPClient(&http.Client{Transport: mTLSTransport}).
    WithJOSE(cfg).Build()

// RIGHT — mTLS sits innermost, beneath the JOSE layer.
httpclient.NewBuilder(logger).WithTransport(mTLSTransport).WithJOSE(cfg).Build()
```

### Mutual TLS (client certificates)

`NewClientTLSConfig` turns declarative certificate material into a hardened
`*tls.Config` (TLS 1.2 floor, `InsecureSkipVerify` never set), and `WithTLSConfig`
installs it on the client:

```go
tlsCfg, err := httpclient.NewClientTLSConfig(&httpclient.ClientTLSConfig{
    CertFile:          os.Getenv("PARTNER_CLIENT_CERT"), // PEM path
    KeyFile:           os.Getenv("PARTNER_CLIENT_KEY"),
    CAFile:            os.Getenv("PARTNER_CA"),
    RequireClientCert: true,
})
if err != nil {
    return err
}
client := httpclient.NewBuilder(logger).WithTLSConfig(tlsCfg).Build()
```

**Sourcing.** Each piece — cert, key, CA — comes from either a PEM file path
(`CertFile`/`KeyFile`/`CAFile`) or a **base64-encoded PEM** string
(`CertValue`/`KeyValue`/`CAValue`, for env vars and secret managers). Setting both
sources for the same piece is an error. `Cert*` and `Key*` must be provided
together. `MinVersion` accepts `""`/`"1.2"` (default) or `"1.3"`; `ServerName`
overrides SNI/hostname verification.

**A `CA*` value REPLACES the system roots.** Setting it pins server verification to
that CA alone, so a client configured for a private partner CA can no longer verify
any public-CA endpoint. If you need both, build the pool yourself and hand the
resulting `*tls.Config` to `WithTLSConfig` — and set `MinVersion` explicitly, since
a hand-built config does not get the loader's TLS 1.2 floor:

```go
pool, err := x509.SystemCertPool()
if err != nil {
    return err
}
if !pool.AppendCertsFromPEM(partnerCAPEM) {
    return errors.New("partner CA: no certificate appended")
}
cfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
```

Note `AppendCertsFromPEM` returns `true` when *any* block parsed, so it accepts a
partially-corrupt bundle and pins fewer roots than you intended — the failure
`NewClientTLSConfig` rejects outright. Check the count yourself if the bundle
carries more than one root.

**A CA-only config authenticates the SERVER, not the client.** It is valid (root
pinning) and `NewClientTLSConfig` accepts it, but no client certificate is
presented — so a deployment that omitted `Cert*`/`Key*` gets one-way TLS while
believing it configured mTLS. Set `RequireClientCert: true` whenever mutual TLS is
intended: the loader then fails loudly instead of silently degrading.

**No `InsecureSkipVerify`.** The loader never produces a config that skips
verification. The escape hatch is explicit and greppable: build a `*tls.Config` by
hand and pass it to `WithTLSConfig`.

**Base-transport slot.** `WithTLSConfig` fills the same base-transport slot as
`WithTransport`. When the slot already holds a `*http.Transport` — i.e.
`WithTransport` was called with one first — `WithTLSConfig` **clones and reuses
it** as the new base: your proxy, dialer and connection-pool settings survive.
The TLS material is always **overwritten, not merged**: `TLSClientConfig` on
the clone becomes exactly the config you pass to `WithTLSConfig`, and the
incumbent's `TLSClientConfig` — including any non-security fields on it such
as `NextProtos` — is discarded wholesale; copy anything you need from it into
`tlsCfg` yourself before calling `WithTLSConfig`. The clone also clears
`DialTLS`/`DialTLSContext` unconditionally (net/http skips its own TLS
handshake — and so ignores `tlsCfg` entirely — whenever a TLS dialer is set).
The rule for whether any of this is reported as a loss keys on whether the
incumbent carried **meaningful security material** of its own — in
`TLSClientConfig`: certificates, `RootCAs`, `InsecureSkipVerify`, a version
floor/ceiling, cipher suites, curve preferences, a non-default renegotiation
policy, `ServerName`, or a verification hook; or a non-nil `DialTLS`/
`DialTLSContext` (a custom TLS dialer is material of the same class — it can
implement certificate pinning or a TLS tunnel) — not on whether
`TLSClientConfig` is merely non-nil. (`*http.Transport.Clone()` itself can
leave a transport with a non-nil but ALPN-only `TLSClientConfig`, so a plain
nil check would produce false positives.) If the incumbent carries none of
that material, the composition is silent — but silent means *unreported*, not
lossless: the incumbent's `TLSClientConfig` is replaced whatever it held,
`NextProtos` and any other non-security fields on it included. Only meaningful
security material is what the report keys on. If it DOES carry its own client
certificate, pinned roots, or TLS dialer, that material is still discarded by
this replacement,
exactly as before this composition existed; only the tuning fields (proxy,
dialer, pool limits) are new — and `Build()` still WARNs for that case.
"Reuses" also never means warm connections: `Transport.Clone()` copies
exported fields only, never the idle-connection map, so a client built this
way still starts with a cold connection pool. When the slot is empty, the
base is a clone of `http.DefaultTransport`, or an equivalently-configured
transport (same proxy, HTTP/2 and pool settings) when that global has been
replaced. When the slot holds an opaque (non-`*http.Transport`) `RoundTripper`
instead, composition is not possible at all: the `RoundTripper` is discarded
wholesale along with any proxy, dialer or TLS settings it carried, and
`Build()` WARNs. The mirror direction WARNs too — calling `WithTransport`
after `WithTLSConfig` discards the loaded client certificate and pinned roots.
**None of this applies to `WithTLSConfig(nil)`**, which returns immediately: the
slot keeps whatever it held, nothing is cloned, replaced or cleared, and no
displacement is recorded. Wrapper layers always stack on top of whichever base
results:

```go
// mTLS base, JOSE on top — call order is irrelevant.
httpclient.NewBuilder(logger).WithTLSConfig(tlsCfg).WithJOSE(joseCfg).Build()
```

```go
// WithTransport supplies a *http.Transport; WithTLSConfig clones it and installs
// tlsCfg — proxy/dialer/pool settings from customTransport survive.
httpclient.NewBuilder(logger).WithTransport(customTransport).WithTLSConfig(tlsCfg).Build()
```

The passed `*tls.Config` is cloned, so one loaded config can be shared across
clients safely. The copy is **shallow**, so treat the config and everything it
references as immutable once `WithTLSConfig` has seen it: every reference-typed
field — the `Certificates` slice, the `RootCAs`/`ClientCAs` pools, `NextProtos`,
`CipherSuites`, `CurvePreferences`, `NameToCertificate` — still points at the
caller's storage, and writing through one is a data race against every in-flight
handshake (`tlsCfg.Certificates[0] = newPair` is the tempting one). Reassigning a
whole field on the original config is inert rather than racy, but only for clients
already built. Rotate certificates through `GetClientCertificate`, which the clone
preserves; for anything else, build a fresh config.

### OAuth 1.0a request signing (partner APIs)

Some partner gateways (e.g. Mastercard APIs) authenticate outbound requests with
OAuth 1.0a request signing — RSA-SHA256 over a signature base string plus an
`oauth_body_hash` parameter — rather than OAuth2/JWT.

go-bricks does not ship an OAuth 1.0a signer. The framework provides the
composition seam (`WithRequestInterceptor`); the partner's own published signing
library provides the protocol. This mirrors how `jose/` delegates JWE/JWS to
`github.com/go-jose/go-jose/v4` rather than implementing the spec itself.

```go
import "github.com/mastercard/oauth1-signer-go" // package name is oauth, not oauth1

func oauth1Signer(consumerKey, keyName string, keys app.KeyStore) httpclient.RequestInterceptor {
    return func(_ context.Context, req *http.Request) error {
        key, err := keys.PrivateKey(keyName)
        if err != nil {
            return fmt.Errorf("oauth1: resolve signing key %q: %w", keyName, err)
        }
        signer := &oauth.Signer{ConsumerKey: consumerKey, SigningKey: key}
        if err := signer.Sign(req); err != nil {
            return err
        }
        // The signer swaps req.Body for a plain NopCloser and never updates
        // ContentLength, so an empty body stops being http.NoBody and net/http
        // sends it chunked. Restore the framing.
        if req.ContentLength == 0 {
            req.Body = http.NoBody
            req.GetBody = func() (io.ReadCloser, error) { return http.NoBody, nil }
        }
        return nil
    }
}

client := httpclient.NewBuilder(deps.Logger).
    WithPeerName("partner-api").
    WithRetries(3, 500*time.Millisecond).
    WithTLSConfig(tlsCfg).
    WithRequestInterceptor(oauth1Signer(cfg.ConsumerKey, "partner-signing", deps.KeyStore)).
    Build()
```

**Per-attempt re-signing is automatic.** Interceptors run inside `buildRequest`,
which the retry loop calls fresh per attempt, so every retry carries a new
nonce, timestamp and signature.

**The empty-body guard is required.** The signer replaces an `http.NoBody` body
with a plain `NopCloser` without touching `ContentLength`, which makes an empty
POST/PUT/PATCH go out chunked with no Content-Length — and partner edges and
WAFs answer that with 411, 400, or 501, which reads like an auth failure.

**Client TLS still composes.** A hand-rolled `WithTransport` signer builds fine
entirely on its own — the base-transport slot only becomes a problem once
`WithTLSConfig` also joins the chain. When it does, the two fill the same slot,
and what happens depends on what the signer supplied. A raw `*http.Transport`
always composes — its proxy, dialer and pool settings survive into the new base
— and *lossless* composition needs it to carry no meaningful TLS material of its
own; if it carries a client certificate, pinned roots or a TLS dialer, that
material is still replaced and `Build()` WARNs. A signer that wraps another
`RoundTripper` is opaque, cannot be cloned, and so is discarded wholesale —
also a WARN. The interceptor path sidesteps all of this — it leaves
`WithTLSConfig` intact regardless, because interceptors never touch the
base-transport slot.

**Do not let a signed request follow redirects.** go-bricks sets no
`CheckRedirect`, so the stdlib default follows redirects below `buildRequest`
without re-running interceptors, which either forwards a signature computed
over the wrong URL (same-host redirect) or drops `Authorization` entirely
(cross-host redirect) — install a `CheckRedirect` returning
`http.ErrUseLastResponse` on your own client, or ensure the partner endpoint
does not redirect.

**Limitation: signing does not cover body transforms.** Interceptors run
*before* the transport chain, so the signature covers the body as
`buildRequest` produced it. If you also install a body-transforming layer such
as `WithJOSE`, the signature will NOT cover the transformed bytes. A framework
hook for that case is deferred until the field-level-encryption work
([#765](https://github.com/gaborage/go-bricks/issues/765), untriaged) creates a
consumer for it; today the workaround is a custom `RoundTripper` passed to
`WithTransport` (which does sit beneath body transforms), at the cost of
building the base transport yourself.

### Visa x-pay-token (API key + shared secret)

Some Visa Developer APIs authenticate outbound calls with a per-request HMAC token in an
`x-pay-token` header instead of, or alongside, mutual TLS. The token is a lowercase-hex
HMAC-SHA256 over a concatenation of the timestamp, the resource path, the query string and
the request body, with a short validity window.

go-bricks does not ship an x-pay-token helper. The concatenation is a handful of stdlib
calls; the part that is genuinely easy to get wrong is **which resource path Visa expects**,
and that is per-product data published by Visa — some products use the entire path, others
strip a context path such as `/vdp/`, `/cybersource/`, `/wallet-services-web/` or `/one/`.
**Take the byte order and the path rule from Visa's own current documentation for the
product you are calling**, not from this page; the framework provides the composition seam
(`WithRequestInterceptor`) and the secret storage (`app.KeyStore.Secret`).

```go
import (
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "fmt"
    "io"
    nethttp "net/http"
    "net/url"
    "strconv"
    "strings"
    "time"
)

// resourcePath maps the request URL to the path string the partner includes in the
// token. This is product-specific — consult the partner's documentation. Passing the
// wrong one produces a valid-looking token that the gateway rejects with a bare 401.
func xPayToken(keys app.KeyStore, secretName string, resourcePath func(*url.URL) string) httpclient.RequestInterceptor {
    return func(_ context.Context, req *nethttp.Request) error {
        body, err := bodyForSigning(req)
        if err != nil {
            return fmt.Errorf("x-pay-token: read body: %w", err)
        }

        secret, err := keys.Secret(secretName)
        if err != nil {
            return fmt.Errorf("x-pay-token: resolve secret %q: %w", secretName, err)
        }
        defer func() { clear(secret) }() // caller owns the copy — zeroize after use

        ts := strconv.FormatInt(time.Now().Unix(), 10)

        mac := hmac.New(sha256.New, secret)
        mac.Write([]byte(ts))
        mac.Write([]byte(resourcePath(req.URL)))
        mac.Write([]byte(req.URL.RawQuery))
        mac.Write(body)

        req.Header.Set("x-pay-token", "xv2:"+ts+":"+hex.EncodeToString(mac.Sum(nil)))
        return nil
    }
}

// bodyForSigning returns the request body WITHOUT consuming req.Body.
func bodyForSigning(req *nethttp.Request) ([]byte, error) {
    if req.GetBody == nil { // no body on this request
        return nil, nil
    }
    rc, err := req.GetBody()
    if err != nil {
        return nil, err
    }
    defer rc.Close()
    return io.ReadAll(rc)
}

client := httpclient.NewBuilder(deps.Logger).
    WithPeerName("visa-vts").
    WithRetries(3, 500*time.Millisecond).
    WithRequestInterceptor(xPayToken(deps.KeyStore, "visa-shared-secret",
        func(u *url.URL) string { return strings.TrimPrefix(u.Path, "/") })).
    Build()
```

**Build the query string yourself, sorted.** The token covers `req.URL.RawQuery` exactly as
it will be sent, so the URL you hand `httpclient.Request` must already carry every required
parameter (the API key among them) in the order the partner expects.
`url.Values.Encode()` sorts by key, which is usually what you want. The interceptor must
never reorder the query — that would sign a string the wire does not carry.

**Per-attempt freshness is automatic.** Interceptors run inside `buildRequest`, which
`executeAttempt` calls fresh on every retry, so each attempt carries a new timestamp and a
new token. This matters more here than for OAuth 1.0a: the token has a short validity
window, so a retry that reused the first attempt's timestamp would start failing as soon as
the backoff exceeded it.

**Read the body through `GetBody`, and never replace `req.Body`.** `buildRequest` builds the
body from a `*bytes.Reader`, so net/http populates both `ContentLength` and `GetBody` —
calling `GetBody()` hands you a fresh reader without draining the one that will be sent.
Nil-check it: a request with no body has neither. Draining `req.Body` and re-wrapping it
instead leaves `ContentLength` and `GetBody` stale, and `buildRequest` does **not**
re-normalize framing after interceptors run.

**Do not let a signed request follow redirects.** go-bricks sets no `CheckRedirect`, so the
stdlib default follows redirects below `buildRequest` without re-running any interceptor.
On a same-host redirect the token covers the wrong path. On a **cross-host** redirect the
consequence is worse than for OAuth 1.0a: net/http strips only `Authorization`,
`Www-Authenticate`, `Cookie`, `Cookie2`, `Proxy-Authorization` and `Proxy-Authenticate` when
crossing origins — a custom `x-pay-token` header is **not** on that list and is forwarded
verbatim to the redirect target. Install a `CheckRedirect` returning
`http.ErrUseLastResponse` on your own client, or confirm the partner endpoint does not
redirect.

**Client TLS still composes.** The interceptor path leaves `WithTLSConfig` intact regardless,
because interceptors never touch the base-transport slot. A hand-rolled `WithTransport` signer
builds fine entirely on its own; the slot only becomes a problem once `WithTLSConfig` also
joins the chain. A raw `*http.Transport` signer always composes — proxy, dialer and pool
settings survive — and composes *losslessly* only when it carries no meaningful TLS material
of its own; carrying a client certificate, pinned roots or a TLS dialer means that material is
replaced and `Build()` WARNs. A signer wrapping another `RoundTripper` is opaque, cannot be
cloned, and is discarded wholesale — also a WARN.
Visa presents mutual TLS and x-pay-token as *alternative* authentication methods rather than
requiring both, but some deployments run mTLS at the egress boundary as well — if yours
does, use the interceptor, not a custom transport.

**Limitation: the token does not cover body transforms.** Interceptors run *before* the
transport chain, so the HMAC covers the body as `buildRequest` produced it. If you also
install a body-transforming layer such as `WithJOSE`, the token will NOT cover the
transformed bytes and the partner will reject the request. This combination is realistic for
Visa Token Services, which is both a JOSE and an x-pay-token product. A framework hook that
runs inside the transport chain is deferred to the field-level-encryption work
([#765](https://github.com/gaborage/go-bricks/issues/765)); today the workaround is a custom
`RoundTripper` passed to `WithTransport`, which does sit beneath body transforms, at the
cost of building the base transport yourself.

## Metrics

### Overview

The `httpclient` package emits five OpenTelemetry instruments under the meter name `go-bricks/httpclient`. All instruments are initialized lazily on first use via `otel.GetMeterProvider()` and governed by `observability.enabled` — when observability is disabled a no-op provider is active and there is zero overhead.

**Meter scope:** `go-bricks/httpclient`

### Instrument Reference

| Name | Kind | Unit | Description |
|---|---|---|---|
| `http.client.request.duration` | `Float64Histogram` | `s` | Duration of HTTP client requests |
| `http.client.active_requests` | `Int64UpDownCounter` | `{request}` | Number of in-flight HTTP client requests |
| `http.client.request.body.size` | `Int64Histogram` | `By` | Size of HTTP client request bodies |
| `http.client.response.body.size` | `Int64Histogram` | `By` | Size of HTTP client response bodies |
| `http.client.retries.total` | `Int64Counter` | `{retry}` | Total number of HTTP client retry attempts |

> **Duration histogram bucket boundaries (explicit):** `0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10` (seconds). These follow the OTel HTTP client semconv recommendation.

### Attribute Reference

**Base attributes** (present on `http.client.request.duration`, `http.client.request.body.size`, and `http.client.response.body.size`):

| Attribute | Type | Notes |
|---|---|---|
| `peer.service` | string | Logical peer name set via `WithPeerName`. Omitted when not configured. |
| `server.address` | string | Hostname extracted from the request URL. |
| `server.port` | int | Port extracted from URL; defaults to 80 (http) or 443 (https) when absent. |
| `url.scheme` | string | URL scheme (e.g. `"https"`). |
| `http.request.method` | string | Uppercase canonical HTTP method. Non-standard methods emit `"_OTHER"`. |

**Duration-only additional attributes** (on `http.client.request.duration` only):

| Attribute | Notes |
|---|---|
| `http.response.status_code` | Integer status code. Omitted on transport errors (no response received). |
| `error.type` | OTel error type enum. Omitted on success and on 4xx/5xx responses — only set for transport-level failures. See `error.type` Enum below. |
| `http.request.resend_count` | Number of prior attempts. `0` on the first (non-retry) attempt. |

**`http.client.active_requests` attributes:**

| Attribute | Type | Notes |
|---|---|---|
| `peer.service` | string | Omitted when `WithPeerName` is not set. |
| `http.request.method` | string | Canonical HTTP method. |
| `server.address` | string | Omitted when URL parsing fails or the URL has no hostname. |

**`http.client.retries.total` attributes:**

| Attribute | Type | Notes |
|---|---|---|
| `peer.service` | string | Omitted when `WithPeerName` is not set. |
| `http.request.method` | string | Canonical HTTP method. |
| `retry.reason` | string | One of `"timeout"`, `"network"`, `"5xx"`, `"build_response"`. |

### `error.type` Enum

| Value | Condition |
|---|---|
| `"timeout"` | Framework `TimeoutError`, `context.DeadlineExceeded`, or any `net.Error` where `Timeout() == true` (after more-specific classifiers below are checked). |
| `"context_canceled"` | `context.Canceled` |
| `"name_resolution_error"` | `*net.DNSError` (DNS lookup failure, including DNS timeouts) |
| `"tls_error"` | `*tls.RecordHeaderError` or `*tls.CertificateVerificationError` |
| `"connection_error"` | `*net.OpError` with `Op == "dial"` (TCP connection refused / unreachable) |
| `"interceptor_failed"` | `InterceptorError` from a request or response interceptor |
| `"panic"` | Panic recovered in user-supplied code (interceptor or custom `RoundTripper`). Emitted on both the attempt span and the parent Do span. |
| `"_OTHER"` | Any other `NetworkError` or unclassified error |

### `WithPeerName` Example

Set a low-cardinality logical service name at client construction time. It populates the `peer.service` attribute on all five instruments and is the recommended primary dimension for SLO dashboards.

```go
client := httpclient.NewBuilder(log).
    WithTimeout(10 * time.Second).
    WithPeerName("visa-vts").
    Build()
```

### Cardinality Guidance

Prefer `peer.service` over `server.address` when writing SLO queries and alerts. `peer.service` is a short, stable string set at builder construction time, so cardinality is bounded by the number of downstream services your application calls. `server.address` is the actual hostname resolved at request time and can explode for webhook-dispatcher or fan-out clients that call arbitrary external URLs. No URL path or query string is ever emitted as an attribute.

### JOSE Body-Size Caveat

For clients constructed with `WithJOSE(...)`, the `http.client.request.body.size` and `http.client.response.body.size` histograms measure the plaintext (application-level) body before encryption and after decryption respectively — not the encrypted wire size. A JOSE-aware variant that also records wire sizes is a possible follow-up.

### Test Utilities

**External module tests (the common case):** Use the `observability/testing` helpers to install an in-memory meter provider, exercise code that calls httpclient, then assert instruments were recorded:

```go
import (
    "github.com/gaborage/go-bricks/httpclient"
    obtest "github.com/gaborage/go-bricks/observability/testing"
    "go.opentelemetry.io/otel"
)

func TestMyService(t *testing.T) {
    mp := obtest.NewTestMeterProvider()
    prev := otel.GetMeterProvider()
    otel.SetMeterProvider(mp)
    t.Cleanup(func() { otel.SetMeterProvider(prev) })

    // ... exercise code that uses httpclient ...

    rm := mp.Collect(t)
    obtest.AssertMetricExists(t, rm, "http.client.request.duration")
}
```

**Internal httpclient tests only:** `tracking.ResetMeterForTesting()` (in `httpclient/internal/tracking/testing.go`) resets cached instrument state so the next `InitHTTPMeter()` re-registers against whichever provider is active. The `internal/` boundary limits this to code within the `httpclient/**` package tree.

## Tracing

### Overview

The `httpclient` package emits OpenTelemetry **CLIENT** spans for every outbound HTTP call under the tracer name `go-bricks/httpclient`. The tracer is initialized lazily on first use via `otel.GetTracerProvider()` and governed by `observability.enabled` — when observability is disabled the global tracer is a no-op and there is zero overhead per request.

**Tracer scope:** `go-bricks/httpclient`

### Span Tree Structure

Each call to `Client.Do` (and its method shortcuts `Get` / `Post` / etc.) emits:

1. **One parent "Do" span** — the logical request rollup. Opened in `Do` after request validation; closed when `Do` returns with the *final* attempt's status, error type, and response body size.
2. **One child attempt span per attempt** — opened in `executeAttempt` after `buildRequest` succeeds; closed at the same point the per-attempt metric is recorded, so span status and metric attribution stay in sync.

Every attempt span has the parent Do span as its direct parent in the trace tree. No span links are emitted between siblings — parent-child structure plus the `http.request.resend_count` attribute is sufficient to query the retry tree.

### Span Naming

| Condition | Span name |
|---|---|
| `PeerName` set via `WithPeerName` | `"{METHOD} {peer}"` (e.g. `"POST stripe"`) |
| `PeerName` unset | `"HTTP {METHOD}"` (e.g. `"HTTP GET"`) |
| Non-standard HTTP method | `METHOD` canonicalises to `"_OTHER"` |

URL paths are **never** in the span name. Without route templating (which the client does not have), path-in-name is a cardinality bomb.

### Attribute Reference

Set at span start (both Do span and attempt span unless noted):

| Attribute | Notes |
|---|---|
| `peer.service` | From `WithPeerName`. Omitted when empty. |
| `http.request.method` | Canonical uppercase. `"_OTHER"` for non-standard methods. |
| `server.address` | Hostname from the parsed URL. |
| `server.port` | Port from URL, defaulting to 80 (http) or 443 (https). |
| `url.scheme` | `"http"` or `"https"`. |
| `url.path` | Path component only — query string and userinfo are not included (`url.URL.Path` already excludes them). Omitted when empty. |
| `network.protocol.name` | Constant `"http"`. HTTP/1.1 vs HTTP/2 is not distinguished — the OTel-recommended low-cardinality default. |
| `http.request.resend_count` | **Attempt span only.** Omitted when `0` (first attempt) and always omitted on the parent Do span (which is the rollup, not any single attempt). |

Set at span end:

| Attribute | Notes |
|---|---|
| `http.response.status_code` | Set when a response is received. Omitted on transport error. |
| `http.response.body.size` | Set when response body bytes > 0. |
| `error.type` | Set on transport error (status 0), on response-build errors, and on the parent Do span for a terminal HTTP-status error (any 4xx, or 5xx after retries are exhausted — classified as `_OTHER`). Does not mirror the duration histogram's `error.type`, which stays empty for any completed roundtrip regardless of status code. |

`url.full` is **never** emitted. Even with userinfo and query-string redaction, paths can still leak (`/users/{secret-token}/...`). Emitting `server.address` + `url.scheme` + `url.path` is sufficient for service-graph slicing without the leakage surface.

### Span Status Mapping

OTel HTTP **client** span status convention (different from server spans):

| Outcome | Span status | Exception event? |
|---|---|---|
| 2xx / 3xx response (no err) | `codes.Unset` (default OK) | no |
| 4xx response (no err) | `codes.Unset` | no |
| 5xx response (no err) | `codes.Error`, description `"HTTP {code}"` | no |
| Transport error (no response) | `codes.Error`, description = error.type | yes — sanitized event |
| Non-5xx response + non-nil err (interceptor/build failure on a 2xx/3xx/4xx) | `codes.Error`, description = error.type (or err.Error() when error.type is empty). Note: 5xx responses always take the row-3 path regardless of whether err is non-nil — `codes.Error`, description `"HTTP {code}"`. | yes — sanitized event |

Rationale for the 4xx-as-OK convention: client spans treat 4xx as a normal flow-control signal (the server told you something legitimate about the request). 5xx signals a server-side failure; transport errors signal a network-side failure on the path between us and the server. Any err alongside a response (e.g. a response interceptor failing on a 200) takes the error path so the span doesn't silently look like a success.

**Sanitized exception events.** Where the table says "yes — sanitized event," the framework emits the exception as an explicit `AddEvent("exception", ...)` rather than `span.RecordError(err)`. Go's stdlib `*url.Error.Error()` includes the full request URL with query string (Go redacts userinfo passwords but not query strings), and Go's default `RecordError` would export those bytes to every configured OTel backend. The framework walks the error chain via `errors.As(*url.Error)` and strips both `RawQuery` and `User` from any embedded URL before recording the exception message — so credentials passed as `?token=...` or in `user:pass@` userinfo never reach trace exporters. Defense-in-depth note: this is per-package, not framework-wide; until a global span-attribute SensitiveDataFilter ships, callers adding new span attributes carrying header/body bytes must run their own redaction.

### W3C `traceparent` Propagation

`httpclient` injects `traceparent` / `tracestate` headers per attempt with this precedence:

1. **OTel propagator path** — when a recording span is active on the request context (the attempt span this package opens, *or* a surrounding span from `server/` middleware), `otel.GetTextMapPropagator().Inject(ctx, headerCarrier)` writes the *real* traceparent matching that span. The framework registers `propagation.TraceContext{}` as the default global propagator.
2. **Legacy fallback** — when `c.config.EnableW3CTrace == true` AND no span is active on the context, the existing `TraceParentFromContext` / `GenerateTraceParent` path emits a synthetic traceparent. This preserves backward compatibility for callers wiring `httpclient` without an OTel tracer.
3. **Disabled** — `WithW3CTrace(false)` disables W3C injection entirely.

You don't need to change anything to benefit from the OTel propagator — leave `EnableW3CTrace` at its default `true` and register a tracer provider (`app.New(...)` does this automatically when `observability.enabled: true`). Downstream services receive a real traceparent that joins your trace.

### Zero-overhead when disabled

When `observability.enabled: false`, the framework installs `noop.NewTracerProvider()` as the global tracer. `StartHTTPClientSpan` returns a non-recording span; `EndHTTPClientSpan` is a no-op; every span method call is a no-op. The only per-request cost is one `otel.Tracer(...)` lookup (cached after the first call inside `sync.Once`).

### Testing Tracing

Use `observability/testing` helpers — they're exactly the same shape as the metrics test helpers documented above:

```go
import (
    obtest "github.com/gaborage/go-bricks/observability/testing"
    "github.com/gaborage/go-bricks/httpclient/internal/tracking"
    "go.opentelemetry.io/otel"
)

func TestModuleEmitsHTTPSpans(t *testing.T) {
    tp := obtest.NewTestTraceProvider()
    original := otel.GetTracerProvider()
    otel.SetTracerProvider(tp.TracerProvider)
    tracking.ResetTracerForTesting()
    defer func() {
        otel.SetTracerProvider(original)
        tracking.ResetTracerForTesting()
    }()

    // ... exercise code that performs an outbound HTTP call ...

    collector := obtest.NewSpanCollector(t, tp.Exporter)
    collector.AssertCount(2) // 1 parent Do span + 1 child attempt span
    span := collector.WithName("GET stripe").First()
    obtest.AssertSpanAttribute(t, &span, "peer.service", "stripe")
}
```

**Internal httpclient tests only:** `tracking.ResetTracerForTesting()` resets cached tracer state so the next `InitHTTPTracer()` re-registers against whichever provider is active. Same `internal/` boundary as `ResetMeterForTesting`.

## Payload Logging

> **Warning:** payload logging is a debug aid for development/staging only. Enabling it in production widens the audit-log surface and may expose sensitive data to log pipelines.
>
> **PCI/PII workloads must extend the default sensitive-field list.** The framework ships defaults like `password`, `token`, `api_key`, `authorization`, but workload-specific fields (`pan`, `cvv2`, `cvv`, `otp`, `ssn`, …) need to be added via `log.sensitivefields` in YAML or `app.Options.LoggerFilterConfig` in code before enabling payload logging — see [observability.md](observability.md#sensitive-data-filtering) for the full field list and customization seams.

By default the client logs only request/response metadata (method, URL, status, elapsed, body size). Debug-level payload logging can be enabled via the builder:

```go
client := httpclient.NewBuilder(logger).
    WithLogPayloads(true).
    WithMaxPayloadLogBytes(2048). // default 1024; values ≤ 0 are ignored and the default applies
    Build()
```

**Content-type-aware logging:** Request and response bodies are handled differently depending on the `Content-Type` header:

| Content-Type | Behaviour |
|---|---|
| `application/json` or `*+json` | Body is parsed with `json.Unmarshal`. If the root is a JSON object, it is logged as `body_preview` after `SensitiveDataFilter` walks it to mask sensitive keys (`password`, `token`, `api_key`, …); nested maps and arrays inside that object root are processed recursively. Primitive and array roots are dropped — the filter requires a top-level JSON object with keys to walk and mask, so root-level scalars (`"secret-token"`, `123456`) and bare arrays would land verbatim without one. |
| Everything else (form-urlencoded, binary, multipart, missing/unknown) | Bytes are **not** logged. Instead `body_content_type` and `body_preview_dropped` (byte count) appear in the log. Form-urlencoded bodies often carry credential pairs; multipart and binary blobs are not filterable. |

**JSON parse failure:** If the Content-Type is JSON but the body is malformed (e.g. truncated by `MaxPayloadLogBytes`), `body_content_type` and `body_preview_status: json_parse_failed` are logged instead of raw bytes.

**Recommendation:** Keep `WithLogPayloads` disabled in production configs. If you need body inspection in production, log only the specific fields you need at the application layer (before or after the HTTP call) rather than enabling blanket payload logging.
