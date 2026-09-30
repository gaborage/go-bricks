# ADR-129: httpclient refuses a credential-carrying https→http redirect client-wide

**Status:** Accepted
**Date:** 2026-09-29

## Context

net/http follows redirects inside `Client.Do`, below every httpclient seam. Before each hop it
copies the first request's headers onto the next one, and strips six of them — `Authorization`,
`Www-Authenticate`, `Cookie`, `Cookie2`, `Proxy-Authorization` and `Proxy-Authenticate` — only
when the hop leaves the original hostname and its subdomains. It never looks at the scheme or the
port. An `https` request that a peer redirects to `http://` on the same hostname, on any port, or
to a subdomain, therefore sends those headers in cleartext.

httpclient puts credentials on the request from five places: `WithDefaultHeader`,
`WithBasicAuth`, `Request.Headers`, `Request.Auth` and a request interceptor, plus
`WithBearerTokenFile`. Only the last was guarded: `Build` installed a `CheckRedirect` refusing
the downgrade when the client carried a file bearer (#1840).

## Decision

1. **The guard is client-wide.** `Build` installs its redirect policy on every client whose
   `*http.Client` has no `CheckRedirect`: the default client, and a copy of one passed to
   `WithHTTPClient` with a nil `CheckRedirect`. `Build` already shallow-copies that client, so
   the caller's is never mutated.
2. **The header set is the request's credentials.** A hop is refused when the chain began at
   `https`, the hop's URL is not `https`, and the hop's request — the headers net/http actually
   forwarded — carries a non-empty `Authorization`, `Cookie` or `Proxy-Authorization`. These are
   the request-credential subset of net/http's six: `Www-Authenticate` and `Proxy-Authenticate`
   are response headers, and `Cookie2` is an obsolete version marker, not a credential. A
   downgrade hop carrying none of the three (a cross-host hop net/http already stripped) is
   followed.
3. **A caller's policy governs entirely.** A `CheckRedirect` on the client passed to
   `WithHTTPClient` is kept and not chained with this one, downgrade included; `auth`'s JWKS
   client is such a caller, and its policy refuses every non-https hop anyway.
4. **The refusal is an exported sentinel and terminal.** The error wraps
   `httpclient.ErrRedirectDowngrade` (match with `errors.Is`), never carries a header value, and
   the retry loop returns it at once whatever `WithRetries` says, since every retry meets the same
   redirect. Its `ErrorType` and OTel `error.type` classification is unchanged (#1629 owns it).
5. **The cap is restated.** Setting `CheckRedirect` replaces net/http's default ten-hop limit, so
   the policy stops after 10 redirects itself; its error reads
   `httpclient: stopped after 10 redirects`.

The guard is renamed away from "bearer" (`httpclient/redirect.go`: `guardRedirects`,
`checkRedirect`); the unexported `errBearerRedirectDowngrade` is replaced, not aliased.

## Consequences

- **A followed same-host downgrade now errors.** A client without its own `CheckRedirect` that
  was redirected from `https` to `http` with a credential header on the hop used to complete the
  request in cleartext; it now fails with `ErrRedirectDowngrade` and the `http` hop is never
  requested. Nothing fails to compile or start.
- **The cap error's text moved.** It is httpclient's, prefixed `httpclient:`; the
  `stopped after 10 redirects` substring is unchanged.
- **Unchanged:** `https`→`https` and `http`→`http` hops, which keep their headers; bearer-token-file
  clients, which get the same policy as before; and every client with its own `CheckRedirect`.
- **Not covered:** a custom credential header outside the three (an `x-pay-token`, an API key
  header) is not judged; net/http forwards those to any host, so such clients still need their
  own `CheckRedirect` (see [httpclient.md](httpclient.md)).

## References

- [httpclient.md](httpclient.md) `### Redirects`
- [migrations.md](migrations.md) `[C70.13]`, `[C69.7]` (the bearer-only guard)
- `httpclient/redirect.go` (`ErrRedirectDowngrade`, `guardRedirects`, `checkRedirect`),
  `httpclient/client.go` (`Build`, `shouldRetryOnError`)
- Go `net/http/client.go` (`makeHeadersCopier`, `shouldCopyHeaderOnRedirect`)
