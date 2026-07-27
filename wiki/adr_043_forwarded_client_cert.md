# ADR-043: ALB Forwarded-Client-Cert Identity Middleware (`X-Amzn-Mtls-*`)

**Status:** Accepted
**Date:** 2026-07-27

## Context

Confirmed deployment posture: partners (Visa et al.) require mTLS + IP whitelisting for
webhooks, and deployments standardize on **ALB edge termination** — the ALB's trust store
verifies the partner's client certificate chains to the partner CA (+ CRL), and the
allowlist of partner source ranges lives at the edge. What the ALB does not do is
application-level identity authorization: after verification it forwards the certificate
data to the target as `X-Amzn-Mtls-Clientcert-*` headers (verify mode), and go-bricks had
no framework support to consume them. Every service would either hand-roll header parsing
(with a URL-decoding trap — see Decision) and its own trust model, or worse, skip the
identity check entirely and treat "the ALB let it through" as authorization.

ADR-042 (server TLS listener) shipped first and, in its Consequences section, assumed the
ALB "strips client-supplied copies" of these headers as the basis for trustworthiness. That
assumption was not independently verified at the time. Preparing this ADR required
confirming it against AWS's own documentation.

## Decision

A config-gated middleware (`server.forwardedclientcert.{enabled,require}`) parses the
verify-mode headers (`X-Amzn-Mtls-Clientcert-Subject`, `-Issuer`, `-Serial-Number`,
`-Leaf`) and exposes a typed `ForwardedClientCert` identity (`Subject`, `Issuer`,
`SerialNumber`, parsed `Leaf`) via `ForwardedClientCertFromContext`. Passthrough mode
(`X-Amzn-Mtls-Clientcert`, full chain) is out of v1 scope — its absence is not an error.
This is **identification, not authorization** (the same stance ADR-039 takes for tenant
resolution): the middleware exposes who the ALB verified; deciding what that identity may
do is application policy (handler or module-contributed `GlobalMiddleware`, ADR-036).

**Encoding.** AWS documents the `-Leaf` header as "a URL-encoded PEM format of the leaf
certificate, with `+=/` as safe characters" — i.e. base64's `+`, `=`, `/` are left literal,
never percent-encoded. `url.PathUnescape` is therefore the correct decoder:
`url.QueryUnescape` additionally treats a literal `+` as a space, silently corrupting the
base64 payload. Pinned by a `+`-containing test vector (a real self-signed certificate) and
a mutation check that swaps the decoder.

**No in-app IP/proxy trust.** F23 (`server/ratelimit.go`'s `ctx.RealIP()` falling back to
unconditionally-trusted XFF) is the anti-pattern this middleware does not repeat. Trust is
never derived from source IP or `X-Forwarded-For` inside this middleware — see Consequences
for what it rests on instead.

**`Require` semantics.** `Require=true` rejects (401, `NewUnauthorizedError`) only when
*both* `-Subject` and `-Serial-Number` are absent (`errNoForwardedCert`) — the sole
rejecting condition. A present, ALB-verified Subject whose `-Leaf` failed to decode is
**not** absence: the request passes with `Leaf == nil` plus a WARN naming the parse failure
and the encoded Leaf's byte length (never header content). `Require` without `Enabled` is a
config-validation error (`server.forwardedclientcert.require`) — rejecting on an identity
source that's never parsed would silently reject every request.

**Probe exemption.** Health/ready probes skip the middleware entirely: ALB health checks
present no client certificate, so a non-exempt `Require` would take the target group down
on every deploy.

## Consequences

**The stripping question, resolved by documentation search, not assumption.** AWS does
**not** publicly document that the ALB strips or overwrites client-supplied copies of
`X-Amzn-Mtls-*` headers before inserting its own verified values. Verified 2026-07-27
across: `mutual-authentication.html`, `x-forwarded-headers.html`,
`configuring-mtls-with-elb.html`, `header-modification.html` (all
`docs.aws.amazon.com/elasticloadbalancing/latest/application/`), and the AWS mTLS launch
blog posts. None state it either way for this specific header family — the silence is
total, not a denial. This corrects the assumption ADR-042's Consequences section carried
forward ("headers ... trustworthy only when the ALB strips client-supplied copies");
that wording predates this verification and should be read in light of this ADR. Notably,
this is precisely the guarantee RFC 9440 (client-cert forwarding headers) requires of a
conforming intermediary — AWS's ALB documentation does not make that conformance claim for
`X-Amzn-Mtls-*`.

**Trust model rests on deployment posture alone, not on an AWS sanitization guarantee.**
Enabling `server.forwardedclientcert` is an explicit operator assertion of all three:
(a) an mTLS-**verify** ALB listener fronts the service, (b) direct target access is closed
(security groups), and (c) the target group is reachable *only* through that listener — no
plaintext/non-mTLS listener routes to the same targets. If any of the three is false, a
client that separately possesses a trust-store-valid certificate (or reaches the target
directly) could forge these headers to claim an identity other than its own, and no public
AWS guarantee excludes that. With all three posture requirements in place, the identity is
trustworthy for **identification and audit**; when any of them is false, the headers are
attacker-writable and no use of them — audit trails included — is authoritative.
Per-subject **authorization** additionally requires the trust store to scope a single
partner CA; deployments where a trust store is shared across mutually-untrusting partners
must not privilege-separate on these headers alone.

**Additive-only:** `ServerConfig.ForwardedClientCertConfig` is a new comparable-typed field
(two bools); the zero value (`Enabled: false`) leaves every existing deployment unchanged.
No exported signature changes.

**Deferred / re-widen triggers:** passthrough-mode chain parsing and non-AWS proxy formats
(Envoy `x-forwarded-client-cert`, nginx `ssl-client-*`) are natural follow-ups. Each needs
its own parse function — the header-getter seam abstracts where header values are read from
(which is what makes parsing unit-testable without HTTP), not the identity's wire shape; a
multi-format future would introduce a resolver-style abstraction as `multitenant` did, not
reuse this function. If AWS ever publishes a
sanitization guarantee for `X-Amzn-Mtls-*`, the trust model in
[wiki/forwarded_client_cert.md](forwarded_client_cert.md) should be re-widened accordingly
and this ADR's Consequences updated to cite it. If plan 066 (app-terminated mTLS) ever
activates, application authorization gains a second identity source
(`Request().TLS.VerifiedChains`) alongside this middleware's accessor.

See [wiki/forwarded_client_cert.md](forwarded_client_cert.md) for the full config
reference, trust model, and an authorization recipe, [wiki/server_tls.md](server_tls.md)
for the listener-TLS half (ADR-042, independent of this feature), and
[wiki/migrations.md](migrations.md) (`[C56.1]`) for the upgrade note.
