# ADR-080: `server.ClientIP` answers from observed hops only, and trusted-proxy lists refuse a default route

- **Status**: Accepted
- **Date**: 2026-08-21
- **Related**: [ADR-057](adr_057_trusted_proxy_ip_extraction.md) (the decision this COMPLETES; its comparison table is retired in the amendment there) · [ADR-043](adr_043_forwarded_client_cert.md) (trust is never derived from source IP or `X-Forwarded-For`) · [ADR-049](adr_049_debug_endpoints_fail_closed.md) (the fail-closed debug gate this defect walked past — see "Why ADR-049 did not catch this") · [ADR-015](adr_015_echo_v5_migration.md) (recorded the surviving fallback without justifying it; corrected in this change)

## Context

`server.ClientIP(r, trustedProxies)` derives the address used by the framework's two
IP-based **access-control** checks: the debug-endpoint allowlist
(`app/debug_handlers.go`) and the scheduler's CIDR middleware guarding `/_sys/job`
(`scheduler/cidr_middleware.go`). Both parse the returned string and fail closed when it
does not parse — but they **fail open when it parses and is wrong**.

### The reproduced bypass

`debug.trustedproxies: ["0.0.0.0/0"]` was **accepted**. The same value on
`server.trustedproxies` aborts startup, because that key routes to the strict
`ParseTrustedProxyCIDR`, which rejects a zero-length mask. `debug.trustedproxies` and
`scheduler.security.trustedproxies` routed to the lenient `validateCIDRList`, which only
asks whether an entry parses.

Trusting every address makes **every peer a trusted proxy**, so the forwarding-header
path opens for a caller who connects **directly**:

- `RemoteAddr: 203.0.113.9:5555` plus `X-Real-IP: 127.0.0.1` returned `127.0.0.1`,
  satisfying the shipped `debug.allowedips` default `["127.0.0.1", "::1"]`.
- The same against `/_sys/job` through the scheduler middleware, on **both** doors
  (`X-Forwarded-For` and `X-Real-IP`), and against the shipped **empty** allowlist,
  which means localhost-only.

**The precondition everyone assumed was false.** The prior analysis held that a
deployment was exposed only if it configured trusted proxies *and the attacker's traffic
transited one of them*. The transit requirement does not hold: a default route trusts the
attacker's own connection. What is required is only that an operator wrote a permissive
trusted-proxy list — which the framework accepted without complaint on two of three keys.

### The five defects in the walk

1. **Only the first `X-Forwarded-For` line was read.** `Header.Get` returns one value per
   key. A client sends its own line; a proxy that adds a *second* line rather than
   appending to the first leaves the real chain invisible. Silent and stable — legitimate
   traffic keeps working, so there is no operational tell.
2. **An unparseable entry was skipped and the walk continued left**, letting it reach a
   caller-authored entry. AWS ALB's `routing.http.xff_client_port.enabled` appends
   `client_ip:port` on **every** request, so this is a shipping configuration, not a
   hypothetical (ADR-057, "a proxy that writes a non-IP XFF entry keys the entire
   fleet on the proxy").
3. **Bracketed IPv6 entries were rejected.** `net.ParseIP("[2001:db8::1]")` is nil, so a
   legitimate IPv6 client behind a bracketing proxy was skipped and then denied. This one
   fails *closed*: an availability bug, not an access-control one.
4. **A whitespace-only `X-Forwarded-For` fell through to `X-Real-IP`**, which was returned
   verbatim with no parse check and no trust check on the value.
5. **When every hop was trusted, the left-most entry was returned** — a value the earliest
   caller wrote.

## Decision

**One rule governs the whole function: the answer is either an identified untrusted hop,
or the peer address we actually observed — never a value the caller wrote.**

Concretely: read every `X-Forwarded-For` line and join them; normalize each entry before
reading it; stop the walk on an entry that carries no readable address and answer with the
peer (echo's "cannot trust entire records" posture); answer with the peer when every hop is
trusted; and never consult `X-Real-IP`.

**"Unreadable" means no address is present — not merely that our parser did not accept the
shape.** That distinction is load-bearing, because the fail-closed stop is a denial, and a
denial aimed at the wrong shape is an outage. Two shapes carry a perfectly good address in
a form `net.ParseIP` alone refuses: an IPv6 entry in brackets, and an entry with a port
suffix — which is exactly what ALB's documented toggle appends on every request. Stopping
on those would deny a correctly-configured deployment at both doors, on every request, and
call it fail-closed. It is not: it is shipping this parser's limitation to operators as an
incident. Both are normalized before the read. What still earns the stop is an entry with
no address in it at all — RFC 7239's `for=_hidden`, `unknown`, or garbage — where no amount
of parsing produces one.

The normalization order is itself a correctness constraint: `net.ParseIP` runs first,
because `net.SplitHostPort` rejects a bare IPv6 address; and the port fallback reads the RAW
entry, because bracket-trimming `[2001:db8::1]:443` yields `2001:db8::1]:443` — the leading
bracket goes, the trailing one is not final so it survives — which fails both parsers and
would leave the shape broken while appearing handled.

**Removing the `X-Real-IP` fallback completes ADR-057; it does not decide anything new.**
ADR-057 said `X-Real-IP` "is not honored at all… No fallback is added," and
`server/server.go` implements exactly that. `server.ClientIP` simply never got the memo.
See the amendment at the top of ADR-057 for the precise scope of that claim.

**Defect 5's earlier exclusion expired.** It was previously left alone on the reasoning
that "changing it alone buys nothing" — an attacker reached the same outcome through
defect 4. That premise held only while defect 4 was open. Once the `X-Real-IP` fallback
and the whitespace fall-through are closed, the left-most fallback is the *last* remaining
path that returns a caller-influenced value, so it goes with them.

**Trusted-proxy lists refuse a default route on all three keys.** `debug.trustedproxies`
and `scheduler.security.trustedproxies` now reject `0.0.0.0/0` and `::/0` with the message
`server.trustedproxies` has always used. The lenient partial-invalid tolerance is
deliberately preserved on those two keys: a single typo must not silently disable the
whole trusted set, so this adds one refusal rather than tightening the syntax.

**Allowlists are deliberately NOT covered by that rule.** `debug.allowedips` and
`scheduler.security.cidrallowlist` may legitimately admit everything — ADR-049 actively
recommends `["0.0.0.0/0"]` for the former. An allowlist that admits everything is a
posture; a *trust* list that trusts everything re-opens header spoofing. The two keys look
alike and mean opposite things.

**`debug.allowedips` gains CIDR-syntax validation**, accepting **bare addresses** because
the shipped default is `["127.0.0.1", "::1"]`, which the strict proxy parser refuses. It
previously had none, so a typo produced a silent runtime deny-all; a startup error is
strictly better than locking an operator out of their own debug endpoints with no message.

## Why ADR-049 did not catch this

ADR-049 made the debug endpoints fail closed: startup aborts when `debug.enabled: true`
would expose an endpoint with neither `allowedips` nor `bearertoken` set. It contains
**zero** mentions of IP derivation. It reasoned entirely about whether a control was
*configured*, never about whether the value that control is evaluated against could be
chosen by the caller. That gap is why an allowlist ADR-049 considered correctly configured
could still be satisfied by a header the attacker wrote.

## Consequences

Deployments that configured a default route in `debug.trustedproxies` or
`scheduler.security.trustedproxies` now **fail startup** with a named message and action.
That is the intended outcome: those deployments were the exposed ones.

Deployments that legitimately sit behind a proxy see the derived client IP change — most
visibly, `X-Real-IP` no longer moves the answer at all.

**The direction of change is not uniform, and an earlier draft claimed it was.** It said
the change "never grants where it previously denied, with one exception". The audit found
that wrong: several shapes that used to be unreadable — and therefore denied — are now read
and can grant. Those are entries carrying a real address in a form `net.ParseIP` alone
rejects: an IPv6 entry in brackets, and any entry with a port, which AWS ALB's
`routing.http.xff_client_port.enabled` appends on every request. Reading them is the
intended fix; refusing them was this parser's limitation exported to operators as an
incident.

What must NOT newly grant is a malformed entry, and the first draft did: `strings.Trim`
stripped an UNPAIRED bracket, so `127.0.0.1[` read as a clean address, and
`net.SplitHostPort` accepts any port text, so `127.0.0.1:notaport` did too. Brackets must
now be paired and the port must be a number in range; a malformed entry stops the walk, as
an unreadable one always did.

### Known residual — and a correction

**An earlier draft of this ADR said `["0.0.0.0/1","128.0.0.0/1"]` was a documented residual
that "no longer yields a caller-authored answer, only a peer-derived one". That was false,
and the adversarial audit disproved it by reproducing a grant.** `net.IPNet.Contains` is
family-asymmetric — an IPv4 net never contains an IPv6 address — so with that trust set a
direct attacker sending `X-Forwarded-For: ::1` had it judged untrusted, returned as the
identified client, and matched the shipped `debug.allowedips` default. The residual was not
a hardening item; it was the enabler.

Two changes close it, and both are needed:

- **Trusted-proxy lists are refused when their MERGED coverage spans an entire address
  family**, not merely when one entry is a default route. `["0.0.0.0/1","128.0.0.0/1"]` and
  `["0.0.0.0/1","128.0.0.0/2","192.0.0.0/2"]` are rejected; so is `::ffff:0.0.0.0/96`, a
  v4-mapped default route that measures 96 of 128 mask bits while behaving as `0.0.0.0/0`.
  Coverage is the property; a default route is just its most obvious spelling.
  The rule reaches all three keys and both doors: `server.trustedproxies` gets the same
  set-level check as the two lenient keys, `ParseTrustedProxyCIDR` measures the normalized
  mask so the exported per-entry parser refuses a v4-mapped default route on its own, and
  `server.trustedProxyOptions` re-applies the set check at runtime for a `server.New` that
  never passes `config.Validate`. A re-audit caught this: the first fix closed the two
  lenient keys and three artifacts — this ADR, the atom, and a test whose name claimed
  every key while calling only two of the three — asserted it had closed all of them.
  That test now says what it runs: `TestLenientTrustedProxyKeysRejectEveryDefaultRouteSpelling`
  covers the per-entry spellings at the two lenient keys, and
  `TestEveryTrustedProxyKeyRejectsDefaultRoute` drives all three through `config.Validate`.

- **`ClientIP` never answers with a non-routable hop.** Loopback, unspecified and
  link-local entries are what an attacker writes to impersonate a local caller, and they
  are exactly what the shipped allowlists contain. No proxy observes a real client at
  `127.0.0.1`. This holds even where the trust set is broad but not total, which the
  coverage check by design permits.

**Why it hid, which is worth more than the fix.** The test's name claimed every key. Its
table contained the exact payload that defeated the third. It called two. A second test DID
reach all three keys, but fed them only `0.0.0.0/0` — the one spelling the strict parser
already rejected — so it passed while a v4-mapped default route and a split-coverage pair
walked through. **A test can be right about its keys and wrong about its payloads, and its
name will still read as complete coverage** to a reviewer and to its author. The rule now
runs 3 keys × 5 payloads, and the revert drill confirms the split-coverage cases fail
without the fix.

The inverse of that trap appeared in the same pass: after the set-level check landed,
reverting the per-entry normalization broke no test at all, because coverage caught the
shape first. The guard was correct and completely unpinned, and "no test complained" came
close to being read as "the guard is dead". It has its own test now.

**What remains, stated plainly so this paragraph does not become the next false claim:**
the coverage check is exact and has no threshold. A list covering everything *except* a
single `/32` is accepted and is nearly as dangerous, since any attacker who is not that one
address is trusted. No threshold is imposed deliberately — every cut-off is arbitrary and
would refuse legitimate large lists, and a list built that way is not an accident but
someone routing around the check, which validation cannot prevent. The structural answer is
not a better predicate: **an IP-derived value is identification, not authorization**
([ADR-043](adr_043_forwarded_client_cert.md)), so an IP allowlist should not be the only
control — [ADR-049](adr_049_debug_endpoints_fail_closed.md) recommends pairing
`debug.allowedips` with `debug.bearertoken`, a pairing recommended in both directions and
enforced in neither.

## Alternatives considered

**Delegate to `echo.ExtractIPFromXFFHeader`.** Rejected — but NOT for the plumbing reason
ADR-057 gave. That reason does not survive checking: `echo.IPExtractor` is
`func(*http.Request) string` (`echo/v5@v5.3.1/ip.go:203`), and both consumers hold exactly
that argument, so there is no seam in the way. The reasons that actually hold are
semantic, and both are verifiable against the pinned version:

1. **Echo's extractor trusts more peers than an allowlist should.** `newIPChecker`
   hardcodes `trustLoopback`, `trustLinkLocal` and `trustPrivateNet` to true
   (`ip.go:174-175`, in `newIPChecker`). That is correct for throttling and
   too permissive for a control an operator opts into: delegating would silently widen the
   debug allowlist and the `/_sys/job` guard to trust every RFC1918 peer — the class this
   decision exists to close. `app/debug_handlers.go` already records this reasoning at its
   call site.
2. **Echo still has defect 5.** When every hop is trusted it returns
   `strings.TrimSpace(ips[0])` — the left-most, caller-authored entry — with the comment
   "All of the IPs are trusted; return first element because it is furthest from server
   (best effort strategy)" (`ip.go:264-265`). This decision returns the peer instead. So
   after this change `server.ClientIP` matches echo on the three points ADR-057 tabulated
   and DELIBERATELY diverges on a fourth; delegating would reintroduce the defect.

Two implementations are the cost of those two divergences, and both are load-bearing.

**Reject default routes on the allowlist keys too.** Rejected: it would break the posture
ADR-049 recommends, and a reviewer would correctly read it as scope creep. See the
Decision.

**Truncate or repair a malformed chain rather than stopping.** Rejected for the reason
echo gives: a chain with an unreadable hop cannot be attributed at all, and guessing
produces a plausible wrong answer, which is worse than a denial an operator can see.
