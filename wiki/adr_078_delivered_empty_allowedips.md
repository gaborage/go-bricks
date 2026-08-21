# ADR-078: A delivered-empty `debug.allowedips` fails configuration resolution

- **Status**: Accepted
- **Date**: 2026-08-21
- **Related**: [ADR-049](adr_049_debug_endpoints_fail_closed.md) (the fail-closed gate this closes a hole in; its premises are amended in an addendum there) · [ADR-051](adr_051_delivered_empty_database_identity.md) (the presence-check shape this copies) · [ADR-074](adr_074_delivered_empty_numeric_config.md) / [ADR-077](adr_077_delivered_empty_bool_config.md) (the delivered-empty family, at the decode seam rather than this one)

## Context

`debug.allowedips` is the only list key in the framework whose default is a
*control*: `["127.0.0.1", "::1"]`. Every other `[]string` key defaults to empty, so
clearing it either tightens the posture or fails elsewhere.

That makes it the only one where a delivered-empty value removes protection. The
env layer overwrites the default with `[]string{}`, and `RegisterDebugEndpoints`
then reads:

```go
if len(exposed) > 0 && len(d.config.AllowedIPs) == 0 && !d.bearerTokenConfigured() { ... }
...
if len(d.config.AllowedIPs) > 0 { g.Use(d.ipWhitelistMiddleware(trustedNets)) }
```

The gate is a **conjunction**. With `debug.bearertoken` set it is satisfied by the
token alone, so the abort never fires — and the second `if` then skips
`ipWhitelistMiddleware` entirely. A manifest that asked for allowlist **AND** token
runs with token only, and nothing says so. Probe-confirmed on `e33e0fbd`: the
service boots.

`DEBUG_ALLOWEDIPS=` is not exotic. It is what a Helm value left unset renders, what
`envsubst` over an undefined variable produces, and what a `secretKeyRef` with an
empty payload delivers — the same three shapes ADR-074 enumerated.

## Decision

A key-targeted presence check in the validate phase, beside
`validateNoDeliveredEmptyDatabase` and in the same step, rejects a listed key whose
**raw koanf value is a delivery that produces no entries** — a string the decoder
would split into nothing, or a YAML null.

The discriminator has to read the raw tree, and this is the whole subtlety.
`Exists` cannot tell the cases apart — the key carries a default, so it is always
present. koanf keeps the shape the source actually delivered:

| delivery | raw value | verdict |
| --- | --- | --- |
| unset | `[]string{"127.0.0.1","::1"}` | default, untouched |
| `DEBUG_ALLOWEDIPS=` | `""` | **rejected** |
| `allowedips: ""` | `""` | **rejected** |
| `DEBUG_ALLOWEDIPS=,` (or `,,,`, `" , "`) | `","` | **rejected** |
| `allowedips:` / `null` / `~` | `nil` | **rejected** |
| `allowedips: []` | `[]interface{}{}` | allowed |
| `allowedips: [""]` | `[]interface{}{""}` | allowed (one entry; fails closed at parse) |
| `allowedips: ["10.0.0.0/8"]` | `[]interface{}{...}` | allowed |

The test is the DECODER's rule, not `TrimSpace`. `splitAndTrimList` drops empty parts,
so a separator-only value trims non-empty and still yields nothing — which is what a
Helm `join ","` over unset values, or an `envsubst` over `"${A},${B}"`, renders. The
check calls that same function through the same `listSeparator`, so "no entries" cannot
be answered one way by the decoder and another by the guard.

**YAML null is where this key parts company with ADR-074 and ADR-077.** There a null
takes the default and is therefore absence, deliberately out of scope. Here it REPLACES
the default: unset decodes to the loopback pair, `allowedips:` decodes to nil. The same
spelling that is harmless for a numeric key removes a control for this one, so it is
rejected — and a bare `allowedips:` is exactly what
`allowedips: {{ .Values.debug.allowedIPs }}` renders when the value is unset.

A value that decodes to at least ONE entry is not this check's business, even when the
entry is junk: `allowedips: [""]` installs the middleware, which then parses no networks
and denies. That is already fail-closed, and turning it into a startup abort would buy
nothing.

An empty **sequence** is an operator writing "no entries" in a spelling no broken
template can produce. Rejecting the shape rather than the outcome is what keeps
ADR-049's sanctioned token-only posture expressible — this removes an accident, not a
choice.

The key list (`deliveredEmptyRejectingKeys`) holds `debug.allowedips` and nothing
else. The mechanism is list-driven so a future key joins by adding a name, but a key
earns its place only when clearing it **fails open**. A test pins the list's exact
contents alongside the other five `[]string` keys, so widening it silently fails.

Inert for hand-built configs (`cfg.k == nil`), exactly as ADR-051 is. That residual
is accepted for the same reason and covered the same way: ADR-049's `app`-layer
refusal stays, and remains load-bearing for configs assembled in Go.

## Consequences

- A deployment that renders an empty value into `debug.allowedips` now fails
  `config.Load` naming the key, where it previously booted with the IP dimension
  silently disabled. That is the point.
- The failure is **earlier** than before for the no-token case, which used to abort
  at registration (ADR-049). Same outcome, better seam: `config.Load` can name the
  key and say what to write.
- `allowedips: []` keeps working, and is now the *only* way to spell a deliberate
  clear. Operators using an empty env var to mean token-only must switch to it — the
  error names both routes.
- The other five `[]string` keys are untouched. `multitenant.resolver.order` keeps
  its own ADR-039 rejection rather than being shadowed by this one.
- The `Action` names an env var only when one actually reaches the key, reusing
  ADR-076's round-trip guard, so the hint never points at a variable that lands
  elsewhere.

Migration: [C60.20](migrations.md).

## Alternatives considered

**Extend the scalar decode guard (ADR-077) to slices.** The obvious move, and wrong
three times over: it would revoke legitimate `FOO=` clearing on the five keys where
clearing is safe or tightening, it would drag the `tools/migration` mirror into a
framework behaviour change, and ADR-077 explicitly recorded that guarding slices
"would break the documented comma-list behaviour, so that one needs its own
decision". This is that decision, and it lands at a different seam.

**Treat an empty allowlist as deny-all.** Supersedes ADR-049's sanctioned token-only
posture and converts a config typo into an outage during exactly the incident when
someone is reaching for the debug endpoints. A fail-open bug is not fixed by a
fail-shut one.

**Re-impose the loopback default on empty.** ADR-049 already rejected this and the
rejection still holds: silently restoring a control overwrites stated intent with a
guess, and leaves the operator believing something about their deployment that is not
true. The abort forces the decision to be spelled.

**Fix it in `app` alone, at the registration gate.** The gate cannot distinguish a
delivered-empty value from a deliberate `[]` — by then both are `[]string{}`. The
discriminator exists only in the raw koanf tree, which is why this check has to live
in `config`.
