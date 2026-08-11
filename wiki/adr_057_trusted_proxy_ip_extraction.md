# ADR-057: The client IP is derived through trusted proxies, not raw `X-Forwarded-For`

- **Status**: Accepted
- **Date**: 2026-08-10
- **Related**: [ADR-015](adr_015_echo_v5_migration.md) (recorded this follow-up), [ADR-043](adr_043_forwarded_client_cert.md) (named the anti-pattern), [migrations.md](migrations.md) `[C59.1]`

## Context

Echo v5.1.0 changed `RealIP()` to return `request.RemoteAddr` only — it stopped reading
proxy headers. To keep rate limiting, the IP pre-guard, and request logging working across
the v4→v5 hop, [ADR-015](adr_015_echo_v5_migration.md) installed `echo.LegacyIPExtractor()`
and wrote down the debt in the same breath:

> **Future hardening:** Replace `LegacyIPExtractor()` with trusted-proxy-aware extractors
> (`ExtractIPFromXFFHeader()` / `ExtractIPFromRealIPHeader()`) in a follow-up change.

Its Negative section repeats the point: "`LegacyIPExtractor()` is a compatibility shim —
should be replaced with proper trusted proxy configuration." This ADR is that follow-up.

The shim returns the **left-most** `X-Forwarded-For` entry with no validation, else
`X-Real-IP`, else the peer. Both header branches are caller-authored. Echo's own doc comment
says so: "No validation is performed on header values. This function trusts headers as-is
and is therefore not safe against spoofing… Use ExtractIPFromXFFHeader or
ExtractIPFromRealIPHeader instead of LegacyIPExtractor."

That made the identifier both of the framework's rate limiters throttle on a string the
caller writes:

- The IP pre-guard (`server/ip_preguard.go`) keys on `ctx.RealIP()` and nothing else, so
  rotating the header per request lands every request in a fresh bucket — the guard is
  defeated outright.
- The global limiter (`server/ratelimit.go`) keys on the resolved tenant when there is one
  and falls back to the IP otherwise, so it is defeated for exactly the untenanted traffic
  the fallback exists to cover — including every request to `/health` and `/ready`.

Both middlewares use `middleware.DefaultSkipper`, which never skips, so the probe paths are
inside both. `app/lifecycle.go` and [startup_defaults.md](startup_defaults.md) both describe
the pre-guard as the per-IP abuse ceiling protecting `/ready`, which runs a database round
trip per call — a claim that was false while the key was caller-chosen. Worse, because the
bucket key is the attacker's choice, so are collisions: a caller can send the address probe
traffic keys on and consume the prober's budget, pushing `/ready` to `429` and dropping the
instance from the load balancer's rotation.

The framework owns this decision whether or not it wants to. `server.Server` exposes no
`Echo()` accessor — [ADR-034](adr_034_echo_boundary_types.md) deliberately
sealed that seam — so a consumer cannot override the extractor.

[ADR-043](adr_043_forwarded_client_cert.md) named this exact finding as the anti-pattern it
refused to repeat: "**No in-app IP/proxy trust.** F23 (`server/ratelimit.go`'s
`ctx.RealIP()` falling back to unconditionally-trusted XFF) is the anti-pattern this
middleware does not repeat."

**What was never affected**, so the change is not over-sold: no access-control decision
depended on this. The debug-endpoint allowlist (`app/debug_handlers.go`) and the scheduler's
CIDR middleware (`scheduler/cidr_middleware.go`) already used the safe
`server.ClientIP(r, trustedNets)` path. This is about throttling and about the truthfulness
of logged client addresses.

## Decision

**Install `echo.ExtractIPFromXFFHeader(opts...)`**, keeping echo's private/loopback/
link-local trust defaults, and **add `server.trustedproxies`** — a list of CIDR strings,
default empty — each becoming one `echo.TrustIPRange(net)` option.

The extractor walks the `X-Forwarded-For` chain right-to-left and returns the first hop it
does not trust. `newIPChecker` starts from `{trustLoopback, trustLinkLocal, trustPrivateNet}`
all true, so the **no-configuration default is already correct** for the deployment posture
this framework documents ([ADR-042](adr_042_server_tls.md), [server_tls.md](server_tls.md)):

- **Behind an ALB in a VPC.** AWS ALB's default `routing.http.xff_header_processing.mode` is
  `append`: it preserves what the client sent and appends the address it observed. The chain
  is `[<client-authored…>, <real client public IP>, <ALB private IP>]`. The ALB entry is
  private → trusted → skipped; the real client's public address is untrusted → returned. The
  forged entries to its left are never reached.
- **Direct to the pod from the internet, with a forged header.** The chain is
  `[<forged>, <attacker's public IP>]`. The attacker's own address is public → untrusted →
  returned. Spoofing defeated.

`server.trustedproxies` is **additive** trust, for a proxy that sits on a public address
(CloudFront, a partner edge) rather than inside a private range. Empty is the correct value
for a standard VPC deployment.

**Validation aborts startup on a bad entry rather than warning.** A trusted-proxy list is a
security control; a typo silently changes who is trusted, and the framework's recent
direction on security config is to fail closed
([ADR-049](adr_049_debug_endpoints_fail_closed.md) debug endpoints,
[ADR-054](adr_054_cache_construction_fails_startup.md) cache construction, #892 database
wiring). Three rejections, each verified against `net.ParseCIDR`:

| Entry | Why it is rejected |
| --- | --- |
| anything `net.ParseCIDR` rejects, including a bare IP (`10.0.0.5`) | a single host address would otherwise be dropped silently; the operator gets a clear error naming the `/32` form |
| host bits set (`10.1.2.3/8`) | parses cleanly and silently masks to `10.0.0.0/8`, **widening** the trusted set past what the operator wrote |
| a default route (`0.0.0.0/0`, `::/0`) | both parse cleanly; "trust everyone" walks the chain to the left-most entry and restores exactly the behavior this ADR removes |

**`X-Real-IP` is not honored at all.** `ExtractIPFromXFFHeader` ignores it, and that is the
point: a deployment whose ALB is set to `xff_header_processing.mode = remove` strips XFF, and
under the shim echo then fell through to a client-authored `X-Real-IP` — the hole survived
the deployment-side "fix". No fallback is added.

### Rejected alternatives

**Promoting `server.ClientIP(r, trustedProxies)` to the framework default.** The repo already
carries a hand-rolled equivalent whose doc comment even claims it is "the secure extraction
that access-control decisions (debug allowlist) and rate limiting must use". Echo's version is
strictly better on three points, each a real defect in ours:

| | `server.ClientIP` | `echo.ExtractIPFromXFFHeader` |
| --- | --- | --- |
| Repeated `X-Forwarded-For` field lines | `Header.Get` reads **only the first** | reads `req.Header[...]` and joins all |
| An XFF entry that fails to parse | `continue` — **skips past it** and keeps walking left, so an `ip:port` entry can cause an attacker-authored entry to be returned | returns the direct peer — fails closed |
| IPv6 in brackets | not stripped | stripped |

`server.ClientIP` stays where it is and keeps its two callers, which take a raw
`*http.Request` outside echo's extractor seam. Fixing its defects is a separate finding.

**Simply dropping the shim.** Echo's bare default returns `RemoteAddr`, which collapses every
client behind a load balancer into one bucket — the failure mode the shim was installed to
avoid in the first place.

**Passing `TrustPrivateNet(false)`.** It would break the zero-config ALB case by making the
ALB's own entry untrusted, so every request keys on the load balancer's address and the whole
fleet collapses into one bucket.

## Consequences

**Positive.** Both limiters now throttle on an address the caller cannot choose. The
pre-guard's per-IP ceiling on `/ready` becomes a true ceiling, so the documented rationale in
`app/lifecycle.go` and [startup_defaults.md](startup_defaults.md#probe-endpoints-and-rate-limiting)
is now accurate. The bucket-collision attack on probe traffic is closed. `client_ip` in access
logs stops being caller-authored, which is the telemetry-integrity half of the finding.

**Negative — values change at all five `RealIP()` call sites.** Two are the limiter keys; the
other three emit the address into a log and every one of them changes value:

| Site | What it feeds |
| --- | --- |
| `server/ip_preguard.go` (`logIPPreGuardRejection`) | the pre-guard's 429 log line |
| `server/logger.go` | `ClientAddr` on the access-log record — the `client_ip` field |
| `server/tenant_middleware.go` | the tenant-resolution-failure 400 log line |

None needed a code change; they already call `RealIP()`, which now resolves correctly. But a
dashboard or alert keyed on any of those values sees different data after the bump, and no
repo-local grep reaches an out-of-repo dashboard.

**Negative — a proxy on a public address must now be configured.** Without a matching
`server.trustedproxies` entry it is untrusted, so it is itself returned as the client and
every client behind it collapses into one bucket.

**Negative — a proxy that writes a non-IP XFF entry keys the entire fleet on the proxy.**
This is the deployment this change makes strictly *worse*, and it is not self-evident from
the diff. Fail-closed cuts both ways: an entry `net.ParseIP` rejects does not get skipped —
echo abandons the entire chain and returns the direct peer.

AWS ALB's `routing.http.xff_client_port.enabled` appends `client_ip:port` instead of
`client_ip`. `net.ParseIP("203.0.113.7:41234")` is nil, so on such a deployment **every
request** returns the ALB's private address: one rate-limit bucket for the whole fleet, and
`client_ip` in every access log reading as the load balancer. Under the shim that deployment
keyed per client-and-port — a near-useless bucket, but a distinct one. The same shape reaches
any proxy writing a non-IP XFF entry: RFC 7239 `for=` syntax, an obfuscated identifier, a
hostname. **`server.trustedproxies` does not rescue it** — the chain is abandoned before
trust is consulted. The only remedy is deployment-side (turn the attribute off, or normalize
the header). `[C59.1]` in [migrations.md](migrations.md) carries this as a preflight action.

An ALB in `xff_header_processing.mode = remove` has no XFF to walk at all and likewise keys
everyone on the ALB address — with the shim's `X-Real-IP` fallback now gone, deliberately.

**Negative — startup now aborts on a malformed `server.trustedproxies` entry** that
previously would not have existed as a key at all. This is intended (see Decision).

**Negative — per-request cost rises whenever `X-Forwarded-For` is present.** The shim
was allocation-free substring slicing (`strings.IndexAny` plus prefix/suffix trims); the
new extractor does a `strings.Join` and `strings.Split` over the header values plus one
`net.ParseIP` per hop it walks. Requests without the header are unaffected — `ip.go`
returns the direct peer on `len(xffs) == 0` before any of that — but `echo.Context.RealIP()`
is not memoized (it calls `IPExtractor(c.request)` on every invocation), so each of the
framework's five call sites re-derives the address independently within one request.

**Negative — the walk is O(hops until the first untrusted entry, counting from the right),
which a caller can inflate.** In the zero-config ALB posture this ADR recommends the direct
peer is trusted, so a caller reaching that proxy can pad `X-Forwarded-For` with entries
formatted to look like trusted ranges and each one costs a `net.ParseIP` before the walk
terminates. Nothing bounds that but the HTTP header size limit.

**What this does not fix.** The tenant-keyed half of the finding is untouched: when a tenant
resolves, the global limiter still keys on the tenant, so one caller can exhaust a whole
tenant's budget. That is a separate design question about limiter keying and remains open.

## Consistency

[ADR-043](adr_043_forwarded_client_cert.md) established the model this ADR follows: in-app
header trust exists only behind an explicit operator assertion about the deployment, and
identification is never conflated with authorization. ADR-043 named F23 — this finding — as
the anti-pattern it refused to repeat; this ADR removes the anti-pattern rather than routing
around it. As there, the resolved client IP is **identification, not authorization**: it keys
throttling and labels logs, and the deployment's network posture is still what bounds who can
reach the service.

## References

- [ADR-015](adr_015_echo_v5_migration.md) — installed the shim and recorded this follow-up
- [ADR-043](adr_043_forwarded_client_cert.md) — named F23 as the anti-pattern
- [ADR-034](adr_034_echo_boundary_types.md) — why consumers cannot override the extractor
- `server/server.go` — `trustedProxyOptions` and the extractor install
- `config/validation.go` — `validateServerTrustedProxies`
- [startup_defaults.md](startup_defaults.md#probe-endpoints-and-rate-limiting) — the probe rate-limiting rationale
- [migrations.md](migrations.md) `[C59.1]`
