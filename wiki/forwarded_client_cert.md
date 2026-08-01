# ALB Forwarded-Client-Cert Identity Middleware (Deep Dive)

`server.forwardedclientcert.*` (ADR-043) parses the partner-certificate identity an
AWS ALB verifies and forwards after terminating mutual TLS in **verify mode**, and exposes
it to handlers and other middleware via `server.ForwardedClientCertFromContext`.

> **Scope note:** this is the identity half only — **identification, not authorization**
> (same stance as ADR-039's tenant resolution). Deciding what an identity may do is
> application policy. This middleware is independent of `server.tls.*` (ADR-042, ALB→target
> listener TLS) — the `X-Amzn-Mtls-*` headers arrive whether or not that hop is TLS.

## Config Reference

| Key | Env var | Type | Notes |
|---|---|---|---|
| `server.forwardedclientcert.enabled` | `SERVER_FORWARDEDCLIENTCERT_ENABLED` | bool | Default `false` — middleware not wired |
| `server.forwardedclientcert.require` | `SERVER_FORWARDEDCLIENTCERT_REQUIRE` | bool | Default `false` — parse-and-expose only. `true` rejects (401) when **either**: both `-Subject` and `-Serial-Number` are absent, **or** any of the four headers carries more than one value (the duplicate check runs first, so a duplicated `-Issuer` alone rejects even with a valid Subject/Serial-Number). A missing or malformed `-Leaf` never rejects when either identity field is present. Requires `enabled: true` (config validation error otherwise). |

```yaml
server:
  forwardedclientcert:
    enabled: true
    require: true
```

`require: true` registers the middleware even if `enabled` was left `false`, emitting a
startup WARN that names both keys — otherwise a config assembled in Go and passed to
`app.NewWithConfig` (which skips `config.Validate`) would serve every request
unauthenticated while asserting the opposite. On the YAML path `config.Validate` still
rejects the combination outright, so the WARN only ever appears for programmatic configs.

## What gets parsed

AWS ALB verify mode forwards these headers (passthrough mode's single
`X-Amzn-Mtls-Clientcert`, the full chain, is out of v1 scope — its absence is not an
error):

| Header | Carried as |
|---|---|
| `X-Amzn-Mtls-Clientcert-Subject` | `ForwardedClientCert.Subject` (RFC2253 DN string, verbatim) |
| `X-Amzn-Mtls-Clientcert-Issuer` | `ForwardedClientCert.Issuer` (verbatim) |
| `X-Amzn-Mtls-Clientcert-Serial-Number` | `ForwardedClientCert.SerialNumber` (verbatim) |
| `X-Amzn-Mtls-Clientcert-Leaf` | Parsed into `ForwardedClientCert.Leaf *x509.Certificate` when it decodes cleanly |
| `X-Amzn-Mtls-Clientcert-Validity` | **Not carried** — read `Leaf.NotBefore`/`Leaf.NotAfter` instead, when `Leaf` is non-nil |

`Leaf` can be `nil` even when the rest of the identity is present — **always nil-check
before dereferencing it.** A `-Leaf` header that fails to decode (corrupt, oversized, or
malformed PEM/DER) is never treated as an absent identity: the ALB already verified the
`Subject` against its trust store, so the request still passes (with `Leaf == nil`).
`Require` rejects on exactly two conditions: *both* `-Subject` and `-Serial-Number`
missing, or any of the four headers carrying more than one value — see **Duplicated
headers** under [Trust model](#trust-model) for why a duplicate is never trusted and why
that check runs first.

### The encoding trap

AWS documents the `-Leaf` header (source: [Mutual authentication with TLS in Application
Load Balancer](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/mutual-authentication.html),
retrieved 2026-07-27) as:

> "This header contains a URL-encoded PEM format of the leaf certificate, with `+=/` as
> safe characters."

That means base64's `+`, `=`, and `/` are left **literal** in the header value, never
percent-encoded. `url.PathUnescape` is the correct decoder for this reason;
`url.QueryUnescape` additionally treats a literal `+` as a space and silently corrupts the
base64 payload. go-bricks pins the choice with a `+`-containing test vector and a mutation
check that swaps the decoder (`server/forwardedcert_test.go`).

## Trust model

Enabling `server.forwardedclientcert` is an explicit operator assertion of **three**
things, together — never a single flag that "just trusts AWS":

1. An **mTLS-verify** ALB listener fronts this service (not passthrough, not a plain HTTP/S
   listener).
2. Direct target access is closed (security groups) — nothing can reach the target group
   except through that listener.
3. The target group is reachable **only** through that mTLS-verify listener — no
   plaintext/non-mTLS listener routes traffic to the same targets.

Header trust rests on that deployment posture **alone** — never on an AWS header-
sanitization guarantee. That distinction matters because of what AWS's documentation
actually says (or rather, doesn't):

> **Doc-silence finding (verified 2026-07-27):** AWS does not publicly document that the
> ALB strips or overwrites client-supplied copies of `X-Amzn-Mtls-*` headers before
> inserting its own verified values. This was checked across
> [mutual-authentication.html](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/mutual-authentication.html),
> [x-forwarded-headers.html](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/x-forwarded-headers.html),
> [configuring-mtls-with-elb.html](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/configuring-mtls-with-elb.html),
> and [header-modification.html](https://docs.aws.amazon.com/elasticloadbalancing/latest/application/header-modification.html)
> (all under `docs.aws.amazon.com/elasticloadbalancing/latest/application/`), plus AWS's
> mTLS launch blog posts. None of them state, either way, whether a client-supplied
> `X-Amzn-Mtls-Clientcert-*` header surviving from the original request is dropped before
> the ALB inserts its own. The silence is total — not a denial, and not a confirmation.
> (This is precisely the guarantee RFC 9440 requires of a conforming client-cert-forwarding
> proxy; AWS makes no such conformance claim here.)

**Consequence — residual risk stated plainly:** if any of the three posture requirements
above is not actually true for a given deployment, a client that separately holds a
trust-store-valid certificate (or that reaches the target directly) could forge these
headers to claim a **different** subject, and no public AWS guarantee excludes that. With
the posture in place, the identity is trustworthy for **identification and audit**; when
any requirement is false, the headers are attacker-writable and **no** use of them — audit
trails included — is authoritative. Per-subject **authorization** additionally requires the
ALB's trust store to scope a **single partner CA** — deployments that share one trust store
across mutually-untrusting partners must **not** privilege-separate those partners on these
headers alone.

**No in-app IP/proxy trust.** This middleware never derives trust from source IP or
`X-Forwarded-For` — that is the anti-pattern already present elsewhere in this framework
(`server/ratelimit.go`'s `ctx.RealIP()` unconditionally trusting XFF, ledgered as F23).
`enabled` is the only trust signal; there is no source-IP/CIDR check to layer on top, and
adding one would be v2 scope creep, not a v1 gap.

If AWS ever publishes a sanitization guarantee for `X-Amzn-Mtls-*`, this section and
[ADR-043](adr_043_forwarded_client_cert.md)'s Consequences should be updated to cite it and
the trust model re-widened accordingly.

**Duplicated headers.** A request carrying more than one value for any single
`X-Amzn-Mtls-Clientcert-*` header is treated as absent identity and logged (fail closed
under `Require`, fail open without it — never first-value-wins). This check runs *before*
the `-Subject`/`-Serial-Number` absence check, so under `Require` a duplicated header alone
rejects the request even when Subject and Serial-Number are both present and valid — e.g. a
duplicated `-Issuer` with an otherwise-clean identity still returns 401. In the documented
posture the ALB sets each header exactly once, so a duplicate means a client-supplied copy
got through and the deployment posture above needs attention.

## Probe exemption

Health and ready probe paths (the same `healthPath`/`readyPath` passed into
`server.SetupMiddlewares`) always skip this middleware. ALB health checks present no client
certificate, so a non-exempt `Require` would take the target group down on every deploy.

## Authorization recipe (application code)

The middleware only identifies; enforcing which subjects may call which routes is your
module's job, typically via `GlobalMiddleware` (ADR-036) so it runs once per request after
tenant resolution and before handlers:

```go
func (m *WebhookModule) GlobalMiddleware() []server.MiddlewareFunc {
	return []server.MiddlewareFunc{
		func(c server.HandlerContext, next func() error) error {
			cert, ok := server.ForwardedClientCertFromContext(c.RequestContext())
			if !ok || cert.Subject != m.expectedPartnerSubject {
				return server.NewForbiddenError("unrecognized partner certificate")
			}
			return next()
		},
	}
}
```

Remember the trust-model boundary above: this recipe is only as strong as the ALB's trust
store scoping a single partner CA. If your trust store is shared across partners, **fail
closed when `cert.Leaf` is nil** and otherwise compare it against a specific expected
certificate/fingerprint rather than `Subject` alone — falling back to `Subject` when the
Leaf is unavailable would defeat the comparison exactly when it matters. Never assume any
partner header value is unforgeable purely because "the ALB set it."

## See Also

- [ADR-043](adr_043_forwarded_client_cert.md) — full design rationale and consequences
- [wiki/server_tls.md](server_tls.md) — the listener-TLS half (ADR-042); independent of this
  feature, but often deployed together on the same ALB
- [wiki/migrations.md](migrations.md) (`[C56.1]`) — upgrade note
- [wiki/multi_tenant_resolvers.md](multi_tenant_resolvers.md) — the identification-vs-
  authorization precedent this middleware follows (ADR-039)
