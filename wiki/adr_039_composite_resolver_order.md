# ADR-039: Require an Explicit Composite Tenant Resolver Order

**Status:** Accepted
**Date:** 2026-07-14

## Context

The composite tenant resolver (`multitenant.resolver.type: composite`) tries a
list of sub-resolvers in order and returns the first non-empty, regex-valid
match (`multitenant.CompositeResolver.ResolveTenant`, unchanged by this ADR).
`server/middleware.go`'s `buildTenantResolver` hardcoded that order to
**header → subdomain → path**, with no way for an operator to change it.

### The trust model, stated honestly

**All three tenant sources are written by the caller.** This is the premise the
rest of this ADR rests on, and it is worth being blunt about because it is easy
to get wrong:

- **Header** — `X-Tenant-ID` is a request header. Any caller can set it to any
  value.
- **Path** — the URL path is authored entirely by the caller. `PathResolver`
  reads `req.URL.Path` and takes the configured segment. With `segment: 2` and
  no prefix gate, a request to `/api/v1/orders` resolves the tenant `"v1"`.
- **Subdomain** — `SubdomainResolver` reads `req.Host`, and `Host` is *itself a
  request header*. It is constrained only if the ingress pins it (per-tenant
  DNS/vhost/SNI, or a `Host` allowlist). The framework cannot observe, verify,
  or enforce that the ingress does so.

GoBricks chooses **which caller-supplied field it reads**. It cannot make any of
them trustworthy. Anything in this codebase or its docs that describes subdomain
or path as a "network-bound" source — fixed by DNS/routing rather than by the
caller — is **wrong**, and this ADR retires that framing.

**Tenant resolution is identification, not authentication or authorization.**
The resolver extracts an identifier and injects it into `context.Context`; there
is no entitlement check anywhere in it. Whichever source is used, the deployment
must authorize the resolved tenant against the authenticated principal — e.g. an
app-level global middleware (ADR-036) comparing `multitenant.Tenant(ctx)` against
a tenant claim in the verified JWT.

### The actual defect in the hardcoded order

The `header` sub-resolver is the **only one that participates with zero
configuration**: it always exists and defaults to `X-Tenant-ID`. Under the
hardcoded header-first order it therefore *unconditionally preempted* whatever
source the operator had explicitly configured, on every composite deployment,
with no way to change it. An operator who stood up per-tenant DNS and wired
`subdomain` — and whose ingress genuinely does pin `Host` — was still resolving
tenants from an unauthenticated header the moment any caller set one, and had no
knob to say otherwise. That, not "the header is spoofable while the others are
not", is the defect.

The fix is **not** to declare subdomain/path trusted — they are not, and no
ordering makes them so. The fix is to make precedence an **explicit,
operator-declared decision**, because only the operator knows what their edge
enforces.

## Options Considered

### Option A: Keep header-first, document the risk (Rejected)

Leave the hardcoded header → subdomain → path order; add a wiki warning.

**Rejected because:** it leaves the zero-config sub-resolver permanently
outranking every explicitly-configured one, with no remediation available to an
operator who reads the warning and wants to act on it. A documentation-only fix
gives the reader nothing to change.

### Option B: Flip the default to subdomain → path → header (Rejected)

Keep the order implicit but reverse it, so the "spoofable header" is tried last;
optionally allow `order` as an escape hatch for those who want header-first back.

**Rejected because it silently escalates a real population.** A `domain` is
required for a composite configuration, so *every* composite deployment would
have a subdomain sub-resolver wired — including deployments fronted by a gateway
that authenticates the caller, strips the client's inbound `X-Tenant-ID`, and
sets its own authoritative value. Under a subdomain-first default, a
caller-supplied `Host: tenant-b.api.example.com` would **outrank the gateway's
assertion**. That is a privilege escalation — introduced by a change branded as a
security fix, landing silently on upgrade, in exactly the deployments that did
the most work to get tenancy right. The escape hatch does not save them: they get
the escalation on the upgrade that ships the new default, before anyone reads the
release note.

This option also encodes the false premise that subdomain/path are inherently
safer than the header. They are not (see Context). Which source is
attacker-reachable is a property of the **edge**, not of the source's name.

### Option C: Require an explicit `order`; no implicit default (Chosen)

Make `multitenant.resolver.order` **required** whenever
`multitenant.resolver.type: composite`. A composite config with no explicit
`order` fails `config.Validate()` at startup with an actionable error naming the
env var, the YAML key, the recommended order, and the gateway-fronted
alternative.

**Chosen because:** composite safety is a pure function of what the *edge*
enforces — whether the ingress pins `Host`, and who owns `X-Tenant-ID`. The
framework cannot observe that. **Any implicit default is therefore an unverifiable
bet, and both candidate defaults silently harm a real population:** header-first
lets a caller-supplied header override a subdomain/path scoping the operator
explicitly configured (Option A); subdomain-first silently escalates
gateway-fronted deployments (Option B). Refusing to guess — fail fast at startup,
one line for the operator to write — is the only option that does not silently
escalate someone.

This mirrors [ADR-038](adr_038_cors_dev_wildcard_opt_in.md), where the framework
likewise refuses to infer a security posture it cannot verify, and the manifesto's
**Explicit > Implicit** / **Fail Fast** principles.

**A caution that belongs in the decision, not a footnote:** "the operator declared
it" does **not** mean "it is trusted". The operator declares *where the tenant
identifier lives*; the framework honors that declaration. Neither statement makes
the value trustworthy. Authorization is still owed by the deployment (see
*Deployment obligations* below).

## Decision

`multitenant.resolver.order` is **required** for `type: composite`. There is no
implicit default.

- **Required, validated at startup** (`config.Validate` →
  `validateResolverOrder`): an empty `Order` on a `composite` resolver is a
  startup failure — a `multitenant.resolver.order` missing-field error whose
  `Action` names both `MULTITENANT_RESOLVER_ORDER` and the YAML key, and whose
  `Details` give the recommended order (`[subdomain, path, header]`) and the
  header-first alternative for gateway-fronted deployments.
- **Entry validation:** every entry must be `header`, `subdomain`, or `path`;
  duplicates are rejected; `order` on any non-composite type is rejected outright
  rather than silently ignored.
- **A named sub-resolver must be configured:** if `order` contains `path`, then
  `path.segment` must be `> 0`; if it contains `subdomain`, a real `domain` is
  required — a `domain` of `"."` (which strips to the empty string once the
  leading dot is trimmed, and would build a nil resolver) is now rejected too.
  Conversely, a composite whose `order` omits `subdomain` no longer needs a
  `domain` at all. `header` needs no configuration — it defaults to `X-Tenant-ID`,
  which is precisely why it could preempt everything else for free.
- **`config.DefaultResolverOrder()` is demoted.** It is no longer an implicit
  default. It is (i) the **recommended** order, `[subdomain, path, header]` — the
  value validation error messages and docs point operators at — and (ii) a
  last-resort fallback inside `server/middleware.go`
  (`compositeSubResolvers`) for a `ResolverConfig` that never went through
  `config.Validate()` (hand-built by an embedding app or a test). Without that
  fallback, an empty `Order` would build zero sub-resolvers and
  `buildCompositeTenantResolver` would return `nil` — silently disabling tenant
  resolution (fail-open). The fallback closes that gap; it is not a sanctioned
  way to skip declaring an order.
- `multitenant.CompositeResolver`'s first-match semantics
  (`multitenant/resolver.go`) are unchanged: it still returns the first
  non-empty, regex-valid result from the ordered resolver list.

## Consequences

### Positive

- **Precedence becomes an operator decision instead of a framework guess.** The
  zero-config `header` sub-resolver no longer preempts an explicitly-configured
  `subdomain` or `path` by default — and it no longer *loses* to one by default
  either. Whichever the deployment needs, it must say so.
- **For a deployment whose edge does pin `Host` per tenant, this is the
  difference between real isolation and none.** The old hardcoded order was
  actively throwing that edge control away: an operator who bought per-tenant DNS
  and enforced it at the ingress was still resolving tenants from any header a
  caller cared to send. Ordering subdomain ahead of the header is what lets that
  edge control actually reach the tenant lookup.
- **The resolved tenant stays in agreement with the rest of the stack.** With an
  explicit order, the tenant that `deps.DB(ctx)` selects is the one visible in the
  matched route, the access log, and the path segment an authorization middleware
  would check — instead of an invisible header silently desynchronizing "the
  tenant this request appears to be for" from "the tenant whose database this
  request gets".
- **Fail-fast, actionable.** The failure is at startup, in `config.Validate`, with
  the exact key, env var, and two candidate values in the error — not a
  behavior-change surprise in production traffic.

### Negative

- **Every composite deployment must add one line or the app will not boot.** This
  is a hard startup failure, not a behavior shift: a `composite` resolver with no
  `multitenant.resolver.order` fails `config.Validate()` and the process exits.
  There is no grace period and no fallback for a validated config. Tracked as
  migrations atom **C50.4**.
- **Both prior mental models lose something, and both populations are real:**
  - Deployments that (knowingly or not) depended on **header-first** — including
    every gateway-fronted deployment where the gateway owns `X-Tenant-ID` — must
    now pin `[header, subdomain, path]` explicitly. If they instead adopt the
    recommended order without thinking, a caller-controlled `Host` or path
    outranks their gateway's authoritative header. **The recommended order is
    wrong for them.**
  - Deployments that wanted **subdomain/path-first** get it, but only by asking:
    there is no free upgrade into the safer-for-them order.
- One more required config key on the composite path. Mitigated by the error
  message carrying the remediation inline.

### Neutral

- `multitenant.CompositeResolver`'s first-match algorithm and the individual
  sub-resolver implementations (`multitenant/resolver.go`) are unchanged — this
  ADR changes only which sub-resolvers `server/middleware.go` builds, and in what
  order.
- Non-composite resolver types (`header`, `subdomain`, `path`) are unaffected,
  except that setting `order` on one of them is now a startup error rather than a
  silent no-op.

## Deployment assumptions (what this ADR does *not* guarantee)

**Ordering is not authorization.** The framework resolves an identifier; it never
checks entitlement. The obligations below are **normative** — a composite
deployment that does not meet them does not have tenant isolation, regardless of
which order it declares.

Each obligation is scoped to the order you actually declare — an obligation about a
sub-resolver you did not list does not apply to you.

1. **If `subdomain` participates in the order, the ingress MUST validate `Host`
   against the tenant's own DNS name.** `Host` is a request header; on a permissive
   wildcard vhost a caller can send `Host: other-tenant.api.example.com` and
   `SubdomainResolver` will read `other-tenant`. With `proxies: true`, the ingress
   MUST additionally ensure only the trusted proxy can set `X-Forwarded-Host`.
2. **If `path` participates in the order, the tenant segment MUST be authorized
   against the authenticated principal.** The path is caller-authored. **The path
   segment is not an authorization boundary, and this ADR does not make it one.**
3. **If `header` participates in the order, the gateway MUST strip or overwrite
   inbound `X-Tenant-ID`** so the value reaching the service cannot be
   attacker-supplied.
4. **Conversely: if your gateway OWNS `X-Tenant-ID`, you MUST pin a header-first
   order** (`[header, subdomain, path]`). Otherwise a caller-controlled `Host` or
   path outranks the gateway's authoritative assertion. This one is unconditional:
   it is a statement about your edge, not about your order.
5. **If `header` sits behind another source in the order, the edge MUST force every
   tenant-scoped request to carry a resolvable subdomain/path** — otherwise the
   fall-through below silently reinstates header trust for requests that match
   neither. An order that omits `header` entirely is not exposed to this: a request
   matching no source simply fails resolution (fail-closed).

### The fall-through bypass (first-match is defeated by making the winner fail)

`CompositeResolver` returns the **first non-empty** match. A higher-precedence
sub-resolver that produces *no match* does not block the request — it hands off to
the next one. So an attacker does not need to beat the ordering; they only need to
make the higher-precedence resolver **fail**.

Concretely, with `order: [subdomain, path, header]`, `domain: api.example.com`,
and `path.prefix: /itsp`:

> A caller hits the **apex host** `api.example.com` (no tenant label → the
> subdomain resolver returns no match) on a path **outside** the configured prefix
> (→ the path resolver returns no match), while sending `X-Tenant-ID: b`. Both
> higher-precedence sources decline, and resolution falls through to the header
> anyway.

**Ordering therefore only removes the header override for requests where a
higher-precedence source actually matches.** If the edge lets tenant-scoped
requests through on the apex host or on unprefixed paths, header trust is
reinstated via fall-through. The edge must force every tenant-scoped request to
carry a resolvable subdomain/path (obligation 5) — or, if the header genuinely
cannot be trusted, not include `header` in the order at all.

### What the tenant-ID regex and the known-tenant lookup do *not* do

Every resolved tenant ID is shape-checked against a regex (default
`^[a-z0-9-]{1,64}$`) and must resolve to a known tenant. **These reject garbage,
not cross-tenant access.** A forged value naming a *real* tenant passes both
checks and resolves fine — that is the whole attack. They are input-sanitization
controls (tenant strings become schema names, cache keys, span attributes), not an
isolation boundary, and no text in this repo should present them as one.

## Migration Impact

**Breaking change (config):** every `composite` resolver deployment must now set
`multitenant.resolver.order` explicitly. Without it, `config.Validate()` fails and
**the application does not start**. This affects 100% of composite deployments,
whether or not they previously relied on the header override.

- If a **trusted gateway owns `X-Tenant-ID`** (it authenticates the caller and
  strips/overwrites the inbound header), pin
  `multitenant.resolver.order: [header, subdomain, path]`.
- Otherwise, the recommended order is
  `multitenant.resolver.order: [subdomain, path, header]` — and read the
  *Deployment assumptions* above before treating it as sufficient.
- A sub-resolver named in the order must be configured: `path` requires
  `path.segment > 0`; `subdomain` requires a real `domain`.
- Env form is a comma-separated list:
  `MULTITENANT_RESOLVER_ORDER=header,subdomain,path`.

See [wiki/migrations.md](migrations.md) atom **C50.4** for the
detect/gate/apply/verify runbook entry, and
[wiki/multi_tenant_resolvers.md](multi_tenant_resolvers.md#composite-resolver) for
the configuration reference.

## Related ADRs

- [ADR-038: Require Explicit Opt-In for Dev-Permissive CORS](adr_038_cors_dev_wildcard_opt_in.md) —
  the same reasoning applied to a different subsystem: when the framework cannot
  verify the deployment property a security posture depends on, it refuses to
  infer that posture and requires the operator to declare it.
- [ADR-036: Global Middleware](adr_036_global_middleware.md) — the seam where an
  application authorizes the resolved tenant against the authenticated principal;
  tenant *resolution* deliberately performs no entitlement check.
- [ADR-022: Environment Policy](adr_022_env_policy.md) — precedent for
  behavior-by-explicit-declaration over behavior-by-inference.
