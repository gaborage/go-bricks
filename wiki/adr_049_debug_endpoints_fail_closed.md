# ADR-049: Debug endpoints refuse to register without access control

- **Status**: Accepted
- **Date**: 2026-08-05
- **Related**: [ADR-038](adr_038_cors_dev_wildcard_opt_in.md), [ADR-046](adr_046_cache_readiness_strict_default.md), [ADR-047](adr_047_database_absence_vs_misconfiguration.md)

## Context

The debug group at `<debug.pathprefix>` (default `/_sys`) is where the framework
deliberately puts detail it withholds from `/ready`: `/_sys/health-debug` renders
each probe's full error — including the database connection identity and the
sanitized-away cache connector string (ADR-046, `[C56.12]`) — plus per-key
connection-pool detail. `/_sys/goroutines` dumps every stack. Its access control
is load-bearing.

That access control failed open. `ipWhitelistMiddleware` short-circuited to a
pass-through on an empty allowlist:

```go
if len(d.config.AllowedIPs) == 0 {
    return func(_ server.HandlerContext, next func() error) error {
        return next()
    }
}
```

and the bearer-token middleware registered only when `debug.bearertoken` was set.
With `debug.enabled: true`, an empty `debug.allowedips` and no bearer token, the
whole group was reachable by any peer that could reach the port.

The framework *knew* this. It enumerated the exposed endpoints and emitted a
startup WARN naming them — then served them anyway. A WARN in a startup log is
not a control: it competes with every other line at boot, it is invisible to a
config review, and nothing downstream consumes it.

Two conditions were being conflated in the code, and the mismatch is the bug's
shape: the WARN fired only when the allowlist AND the token were both absent,
while the pass-through fired on an **empty allowlist alone**. The two were not
actually in conflict — the token middleware is registered independently, so
"empty allowlist, token set" was genuinely protected — but nothing in the code
said so, and one edit to either branch could have broken the pairing silently.

## Decision

**`RegisterDebugEndpoints` returns an error, and refuses to register, when it
would expose one or more debug endpoints with neither `debug.allowedips` nor
`debug.bearertoken` set.** The error is fatal: `App.prepareRuntime` propagates it
and startup aborts.

```go
if len(exposed) > 0 && len(d.config.AllowedIPs) == 0 && !d.bearerTokenConfigured() {
    return fmt.Errorf("debug endpoints are enabled and would expose %s at %s with NO access control: …")
}
```

`bearerTokenConfigured` is `strings.TrimSpace(d.config.BearerToken) != ""`, not a bare
`!= ""` test, because a whitespace-only token is not a credential: `strings.Cut` splits
`Authorization: Bearer  ` into scheme `Bearer` and token `" "`, which
`ConstantTimeCompare` matches against a `" "` config value. Treating it as configured
would have let it satisfy this gate and then wire `authMiddleware` around a secret
guessable in one try. The same predicate governs wiring the middleware, the
`auth_enabled` field, and `authMiddleware`'s own defense-in-depth guard, so the three
cannot disagree.

Three properties fall out of that shape:

- **The escape hatch is an existing config key, deliberately set.** Either
  `debug.allowedips` or `debug.bearertoken` satisfies the check; so does
  `debug.enabled: false`. No new key was invented for the sole purpose of
  re-opening the hole.
- **`len(exposed) > 0` gates the refusal.** `debug.enabled: true` with every
  `debug.endpoints.*` flag off registers an empty group and exposes nothing, so
  it is not refused.
- **The two controls now compose explicitly.** Each middleware is applied only
  when its key is configured, and `ipWhitelistMiddleware` lost its pass-through
  branch. Where both are set, a request must come from an allowlisted IP AND
  carry the token; where the middleware is ever constructed against an empty
  allowlist, `NewIPWhitelist` yields a whitelist matching nothing — so the
  residual failure mode is deny-all rather than allow-all.

`RegisterDebugEndpoints` changing from `func(server.RouteRegistrar)` to
`func(server.RouteRegistrar) error` is an incompatible exported-API change. A
*direct* call written as an expression statement still compiles — Go permits
discarding a return value there — but it is reported by `apidiff` and will newly
be flagged by `errcheck`, and the source-compatibility only holds for that one
shape. Any consumer that names the old signature breaks at compile time: a method
value assigned to a `func(server.RouteRegistrar)` variable or struct field, a
call passed as such an argument, or an interface declaring the method without the
`error` result — the concrete type stops satisfying it. Those consumers must
adopt the new signature and handle or propagate the error:

```go
if err := debugHandlers.RegisterDebugEndpoints(r); err != nil {
    return fmt.Errorf("register debug handlers: %w", err)
}
```

Almost none exist: the framework calls it from `App.registerDebugHandlers`, and
`App.prepareRuntime` returns that error to startup untouched — like its six
sibling steps, it passes callee errors through, so the message above has to be
self-describing on its own, and is.

This repo has made the same call twice before. ADR-038 turned the dev-permissive
CORS wildcard into an explicit `CORS_DEV_WILDCARD=true` opt-in; ADR-046 made the
cache readiness probe critical by default with a WARN-on-every-boot opt-out. Both
started from "the safe posture depends on the operator noticing" and ended at
fail-closed. So does the framework's own stated posture: *"Module `Init()` errors
are fatal. Validation errors crash at startup, never degrade silently."*

## Consequences

### Positive

- The exposure that the WARN failed to prevent is now impossible to deploy. The
  endpoint's access control is a startup invariant, not an operator habit.
- Every doc that describes `/_sys/health-debug` as access-controlled becomes true
  unconditionally. Plans that route more `/ready` detail there — the database
  driver error, the tenant-key enumeration — no longer have to qualify the claim.
- The failure surfaces at deploy time with an error that names what would have
  been exposed, at which prefix, and both keys that resolve it — rather than at
  incident time.

### Negative

- **It is a deploy-time break.** A service currently running with
  `debug.enabled: true`, **at least one `debug.endpoints.*` flag enabled**, an
  empty allowlist and no token will not start after the bump. All four conjuncts
  are required: the refusal is gated on `len(exposed) > 0`, so an enabled group
  with every endpoint flag off exposes nothing and still boots. That is the
  point, and it is the reason this ships with a migration atom (`[C57.7]`)
  carrying a `detect` an operator can run against their own config before
  upgrading.
- The abort is unconditional on environment. A developer who cleared
  `debug.allowedips` locally to reach the endpoint from a container or a LAN peer
  now has to say so — `debug.allowedips: ["0.0.0.0/0"]` is the explicit form of
  what the empty list used to mean implicitly, and it is greppable.
- The exported signature change costs an `apidiff` acknowledgement.
- **A bearer token now satisfies the gate on its own, and nothing constrains its
  strength.** `config.validateDebug` checks only `debug.trustedproxies`; there is
  no minimum length, entropy, or character-class requirement on
  `debug.bearertoken`, so `bearertoken: x` opens the same door as a 32-byte
  random string. The rate-limit backstop is weaker than it looks: `app.rate.limit`
  defaults to 100 rps and `app.rate.ippreguard.threshold` to 2000, but both are
  *koanf* defaults — a `*config.Config` assembled in Go leaves them at the zero
  value, and `rateLimitEcho` is a pass-through for `<= 0`, so that deployment has
  no brute-force ceiling at all. The one shape rejected is a blank one — a
  whitespace-only value counts as unset, so it aborts startup rather than
  installing a one-guess credential. Token-only access control is therefore intended
  for deployments that are not internet-facing; pair it with `debug.allowedips`
  wherever the port is reachable from outside the perimeter. A minimum-length
  floor on `debug.bearertoken` is a candidate follow-up — deliberately not taken
  here, since it would widen an already-breaking change into a second one.

## Alternatives considered

**Deny-by-default middleware.** Register the group as before, but have it refuse
every request when no control is configured (reusing `handleAccessDenied`), and
keep the startup WARN. Rejected: the operator finds out at request time, during
the incident they opened the debug endpoint to investigate; the endpoint stays
*registered* without protection, so every doc describing it as access-controlled
still needs a qualifier; and a middleware that denies unconditionally is a
control that exists only to fail, which is harder to reason about than one that
is never wired. Its single advantage — it cannot take a service down on deploy —
is exactly the property that let the current hole persist.

**Treat an empty allowlist as localhost-only**, the way
`scheduler.CIDRMiddleware` already does — `parseCIDRAllowlist` returns
`localhostOnly = true` for an empty list (`scheduler/cidr_middleware.go:60-63`),
which is fail-closed and breaks no deploy. Rejected because the two keys do not
start from the same place: `scheduler.security.allowedips` has no loopback
default, so an empty value there is simply "unset", while `debug.allowedips`
*defaults* to `["127.0.0.1", "::1"]` — an empty value is something an operator
had to write. Silently re-imposing localhost would overwrite that stated
intent with a guess, and would leave the deployment believing it had opened the
endpoint when it had not. The abort forces the decision to be made explicitly;
`allowedips: ["0.0.0.0/0"]` is the honest spelling of what the empty list was
being used for, and unlike an empty list it is greppable.

**Validate in `config.validateDebug` *instead*.** Fails even earlier, at
`config.Load`, and needs no signature change. Rejected as a *replacement*: a
programmatically assembled config never calls `config.Validate` —
`app.NewWithConfig` goes straight to the builder — so it would slip past, the
same gap `[C56.5]` closed for `forwardedclientcert.require`. The `app`-layer
refusal is therefore load-bearing and cannot be moved.

Note what this does **not** argue. `validateDebug` receives the whole
`*DebugConfig`, `Endpoints` included, so every conjunct of the predicate is
available at load time; there is no registration-time knowledge involved. A
config-layer clause is thus perfectly implementable, and the two precedents this
ADR leans on ship *both* halves: ADR-043 validates `forwardedclientcert` at load
time **and** backstops it in `server`, and ADR-050 validates the connection-string
scheme at load time **and** backstops it in `app_builder`. Adding the same clause
to `validateDebug` — which would surface the error before the broker and database
are dialed, and put it beside `debug.trustedproxies`, the sibling rule on the same
config block that already fails at `config.Load` — is a worthwhile follow-up,
deliberately not taken here so that this change ships one new failure path rather
than two.

**Leave the WARN, raise it to ERROR.** Rejected on the same grounds as the WARN
itself: severity is not a control.

## References

- `app/debug_handlers.go` — `RegisterDebugEndpoints`, `exposedEndpoints`,
  `ipWhitelistMiddleware`
- `app/lifecycle.go` — `registerDebugHandlers`, `prepareRuntime`
- [migrations.md](migrations.md) `[C57.7]`
- [ADR-038](adr_038_cors_dev_wildcard_opt_in.md) — dev CORS wildcard opt-in
- [ADR-046](adr_046_cache_readiness_strict_default.md) — cache readiness strict by default
