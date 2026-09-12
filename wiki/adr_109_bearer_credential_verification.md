# ADR-109: Bearer Credential Verification Is a Top-Level RSA-Only `auth` Package

**Status:** Accepted
**Date:** 2026-09-12
**Issue:** #1596

## Context

GoBricks shipped no inbound request authentication. `app.GlobalMiddlewareRegisterer` and
`RouteRegistrar.Use`/`Group` were good seams but empty ones: `wiki/global_middleware.md`
documented "writing an auth gate" against a `jwt.NewVerifier` the application was expected
to supply, and `llms.txt` carried the same fictional API. `jose/` seals and opens request
and response *bodies*; it never reads `Authorization` and never produces an identity.
`keystore/` loads key material once at startup and holds it read-only — no fetch, no TTL,
no refresh. A repo-wide grep for `jwks`, `oidc` and `openid` returned zero Go hits.

The reporter's application-side implementation ran ~450 lines, and the parts that were
easy to get silently wrong were all general: bounding the JWKS body while it is still a
stream (`httpclient` buffers with `io.ReadAll`, so a length check on the returned bytes
limits nothing); rate-flooring the unknown-`kid` refresh, because `kid` is attacker-chosen
and an unbounded refresh lets any caller drive traffic at the issuer; keeping an
unreachable issuer (503) distinct from a bad credential (401), so a caller holding a valid
token is not told to stop retrying; rejecting a credential with no `exp`, which go-jose
skips rather than fails; and detaching the fetch from the caller's cancellation, since
every request queued behind one fetch is waiting on it.

## Options Considered

**A — a subpackage under `server/`.** The middleware is HTTP-shaped and `server/` already
owns `MiddlewareFunc`. Rejected: verification itself is transport-neutral — `Verify` takes
a credential string and returns a `Principal`, and `ContextWithPrincipal` is exported
precisely so a gRPC interceptor outside this repository can publish the identity on its
own terms. Burying that under `server/` would tie an identity model to one transport, and
`server/` is already the package with the strictest surface rules (ADR-034).

**B — a subpackage under `jose/`, reusing `jose.KeyResolver`.** The issue proposed exactly
this, and `jose.KeyResolver`'s own comment kept "the door open for future JWKS-URL backed
resolvers". Rejected on the interface, not the location: `jose.KeyResolver` carries a
`PrivateKey` method. A resolver whose key material arrives over the network from a remote
issuer must never be able to satisfy a private-key interface — the type system is the
cheapest place to make that impossible, and it costs one small interface to keep it so.

**C — OIDC discovery through `/.well-known/openid-configuration`.** Also in the issue, and
the conventional operator experience: configure the issuer, let the service find the key
set. Rejected for v1: discovery adds a second attacker-influenced document, and with it an
origin check on the returned `jwks_uri` that exists only to defend against the indirection
discovery introduced. An explicit `auth.jwt.jwksuri` has no such document and no such
check — the operator names the endpoint, and the URL is validated once at startup.

**D — a claim/authorization hook on the verifier.** The issue asked for one, so a service
could require its own claims without reimplementing signature verification around them.
Rejected: `Principal.Claim` already gives a handler every decoded claim, and a hook inside
the verifier would put authorization decisions on the identification path — the one place
this package promises not to make them.

## Decision

1. **A top-level `auth` package.** Verification is transport-neutral; the HTTP middleware
   is one consumer of it, in the same package but not the core of it.

2. **RSA-only, RS256 and PS256, allowlist threaded into the parser.**
   `signatureAlgorithms` is the single owner of the closed set: `Config.Validate` rejects
   any other spelling at startup, and `allowedAlgorithms` hands the same set to
   `jose.ParseSignedCompact`, so `alg=none`, `HS*` and `ES*` die inside the parser before
   any key is looked up. A key-confusion downgrade to HS256 has no code path to reach.
   Parsing is `ParseSignedCompact`, never `ParseSigned`: the latter dispatches to the JSON
   serialization for input starting with `{`, admitting an attacker-controlled
   unprotected header and an unbounded signatures array into the crypto layer.

3. **A public-key-only `PublicKeyResolver`.** It mirrors `jose.KeyResolver`'s house shape
   and deliberately omits `PrivateKey`, and it never grows one. `StaticKeyResolver` (pinned
   keys) and the unexported JWKS-backed resolver both implement it.

4. **Explicit `auth.jwt.jwksuri`, no discovery.** HTTPS with a hostname, validated at
   startup by both `config.checkAuthJWKS` and `auth`'s own `validateJWKSSource`.

5. **Per-route-group attachment.** `auth.Middleware(v)` is a `server.MiddlewareFunc`
   attached with `RouteRegistrar.Group`/`Use`. There is no `GlobalMiddlewareRegisterer`
   path and no path allowlist: a route that must stay open — a probe, a webhook with its
   own signature check — is exempted by not attaching the middleware to its group, which
   keeps the exemption visible at the registration site instead of buried in a skip list
   that a new route silently falls outside of.

6. **Identification, not authorization** (the stance ADR-039 and ADR-043 already take). A
   `Principal` on the context states that a credential verified against the configured
   issuer. Whether that identity may perform the operation stays the handler's decision.

7. **Fail-fast key fetch, then a stale ceiling.** `NewVerifier` fetches the key set before
   it returns and reports a failed fetch as an error, so module `Init` aborts startup
   rather than booting a verifier that can verify nothing. Afterwards the set is refreshed
   on a ticker (half the TTL, floored at `minrefreshinterval`) and on an unknown `kid`
   (rate-floored and coalesced through singleflight, on a context detached from the
   caller's). A set the issuer stops serving is still served until it passes
   `staleceiling`, and then every lookup reports `ErrKeySetUnavailable` → 503. There is no
   path on which an unverifiable credential is accepted.

8. **Configuration through a registered `config/types.go` section.** `auth.jwt.*` is a real
   config section with framework defaults and load-time validation, not an
   `InjectInto` escape hatch: the escape hatch carries no defaults, no cross-field checks
   and no place for the rules that hold for every deployment. `auth.Config` is a package-
   local conversion of `config.AuthJWTConfig` (`auth.Config(deps.Config.Auth.JWT)`), so
   the conditional rules — the ones true only once a verifier is actually built — live
   next to the code that enforces them.

## What is deliberately not done

- **No ECDSA.** Same reasoning as `jose`'s allowlist (`.out-of-scope/ecdsa-jose-keys.md`):
  admitting a key shape is a permanent review obligation, and no integration needs it.
- **No claim or authorization hook** (option D). `Principal.Claim` is the seam.
- **No `ModuleDeps` slot and no framework module.** A consumer constructs its own
  `Verifier` in `Init` and closes it in `Shutdown`. A framework-owned verifier would have
  to decide which routes it guards, which is exactly the decision point 5 keeps at the
  registration site.
- **No cookie or query-string credentials.** `Authorization: Bearer` only: a credential the
  browser attaches on its own is a CSRF surface, and a credential in a URL lands in access
  logs.
- **No accept-unsealed / accept-unverified mode**, and no `insecureskipverify` equivalent
  for the JWKS endpoint.

## Consequences

- **A service gets bearer verification in three lines and loses the 450-line hand-roll.**
  Build a verifier in `Init`, attach `auth.Middleware` to the groups that need it, read
  `auth.PrincipalFromContext` in the handler.
- **A 503 is now distinguishable from a 401 on the wire.** An unusable key set answers 503
  with `Retry-After` derived from `minrefreshinterval` (floored at 1s, no new config key);
  every credential fault answers 401 with a `WWW-Authenticate` challenge that distinguishes
  "none presented" (`realm="<issuer>"`) from "presented and rejected"
  (`error="invalid_token"`). The realm is quoted-string-escaped, because the issuer is
  operator-supplied and a bare quote or CR would inject a header.
- **The rejection reason never reaches the caller.** A failure is reported by `Class` — a
  closed, low-cardinality vocabulary — to a DEBUG log and to the `auth.result` metric
  attribute only; `VerificationError.Error` and `Format` render the class and never the
  cause, because a library cause routinely embeds the credential it failed on.
- **`Principal` redacts itself under `String`, `Format` and `MarshalJSON`, but not under
  the logger's reflective filter.** `logger.LogEventAdapter.Interface` and
  `Logger.WithFields` rebuild a struct into a `map[string]any` by reflection before any
  marshaler runs, and the filter matches field NAMES, so `Subject` and issuer-chosen claim
  keys are not masked. No method on `Principal` can influence that path; the documented
  rule is not to hand a `Principal` to it (gaborage/go-bricks#1602).
- **`auth.jwt.telemetry.enduserid` is the one opt-in that records a subject.** Off by
  default: turning it on stamps `enduser.id` on the request span for every verified
  credential, which is a deliberate operator choice, not a default.
- **An issuer publishing a mixed key set degrades rather than fails.** Non-RSA entries,
  encryption-only keys, keys without a `kid`, duplicate `kid`s and moduli outside
  2048–16384 bits are dropped and named in one WARN per refresh; only a document that
  yields no usable key at all fails the refresh.
- **The JWKS body is capped twice.** A response interceptor on the default client refuses
  an over-`Content-Length` body unread and fails the read one byte past the cap otherwise;
  a second check after buffering covers a caller-supplied `httpclient.Client`, which has
  already read the body by the time the resolver sees it.

## References

- Issue #1596 (bearer/JWT verification with a remote JWKS key source)
- [ADR-034](adr_034_echo_boundary_types.md) — why the middleware surface is
  `server.MiddlewareFunc` and carries no `echo.*` type
- [ADR-039](adr_039_composite_resolver_order.md) and
  [ADR-043](adr_043_forwarded_client_cert.md) — the identification-not-authorization
  stance this package continues
- [ADR-081](adr_081_recovered_panic_values_reported_by_type.md) — the report-by-type-never-by-value rule
  `VerificationError` and `Principal` follow for a different secret
- [wiki/auth.md](auth.md) — the consumer-facing documentation
- `.out-of-scope/ecdsa-jose-keys.md` — why ES256 is off both allowlists
- `auth/doc.go`, `auth/verifier.go`, `auth/jwks.go`, `auth/middleware.go`,
  `auth/principal.go`, `auth/resolver.go`, `auth/metrics.go`, `config/auth_section.go`,
  `config/types.go` (`AuthConfig`)
