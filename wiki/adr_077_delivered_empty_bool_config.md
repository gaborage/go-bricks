# ADR-077: A delivered-empty bool config value fails startup

- **Status**: Accepted
- **Date**: 2026-08-21
- **Related**: [ADR-074](adr_074_delivered_empty_numeric_config.md) (the numeric rule this extends verbatim; its hook is renamed `EmptyStringToScalarGuardHookFunc` here) · [ADR-046](adr_046_cache_readiness_strict_default.md) (the strict-readiness tri-state this silently defeated) · [ADR-051](adr_051_delivered_empty_database_identity.md) (the delivered-empty rule both descend from)

## Context

ADR-074 closed `FOO=` for numeric config keys and named what it left open: the same
delivered-empty string bound to a **bool** target still decodes as a legal `false`.
That is one defect at a different target kind, and mapstructure's `WeaklyTypedInput`
gets there by an explicit branch — an empty string is not a parse failure, it is
`SetBool(false)`.

Measured against `main` at `daefb328` (the commit that shipped ADR-074):

- `DATABASE_POOL_KEEPALIVE_ENABLED=` produced a non-nil `*false`, a genuine
  **default-true → false flip**: normalization fills a *nil* pointer with `true`, and
  the decoder handed it a pointer to `false`, which reads as "the operator chose off".
  Keep-alive silently off is the posture `PoolKeepAliveConfig` itself documents as
  essential for cloud deployments behind an idle-timeout NAT.
- `CACHE_CRITICAL=` produced a non-nil `*false`, so `IsCacheCritical()` answered false
  and ADR-046's strict readiness was defeated: `/ready` answers 200 straight through a
  Redis outage.
- `SERVER_LOGROUTES=` produced a non-nil `*false`.

Those three are every `*bool` in `config.Config`. The non-pointer bools are the milder
half — `false` is their zero either way — but they are guarded on the same rule, because
the framework cannot tell "I meant off" from "my template rendered nothing" for either
shape.

The framework already contradicted itself across two public seams: `config.Load()`
accepted `""` for a bool, while `InjectInto`'s `convertToBool` had rejected it since it
was written (`'' is not a valid boolean` / `use true/false, 1/0, yes/no`). A consumer
injecting a bool got the loud failure; the framework's own keys did not.

## Decision

`configdecode.EmptyStringToNumericGuardHookFunc` becomes
`EmptyStringToScalarGuardHookFunc` and judges bool targets on exactly ADR-074's terms:
an empty or whitespace-only string bound to a bool fails the decode, pointer and
non-pointer alike, with mapstructure attaching the key. The predicate behind it,
`isNumericKind`, becomes `isWeakScalarKind` — the honest description of its members is
"the SCALAR kinds `WeaklyTypedInput` fills from a string with a zero value", which is the
numerics plus bool. Verified against mapstructure v2.5.0: the int/uint/float decoders each
rewrite `""` to `"0"` and the bool decoder calls `SetBool(false)`, while complex, map and
struct targets reject a string source loudly. Slice and array ELEMENTS re-enter the hook
individually, so a `[]bool` element is judged on the same rule. The rename is free: the
package is `internal/` and the symbol has never appeared in a release.

Nothing else about ADR-074 moves. Numeric behaviour, its message, and its tests are
unchanged; the bool rejection carries its own message (`boolean value delivered empty
— set an explicit true/false …`) in the same voice, so an operator reading a log can
tell which target kind refused.

The rule reaches all four seams ADR-074 named — the three Go seams compose the shared
hook, and the CLI applies its byte-identical mirror:
`buildDecoderConfig` (`config.Load`), `unmarshalDecoderConfig` (the public
`Config.Unmarshal`), `migration.decodeSecretConfig`, and the `tools/migration` CLI's
`tenantDecoderConfig` — which keeps a byte-identical copy of the hook rather than
importing it, kept honest by the source-comparison test in `internal/configdecode`.

YAML **null** stays absence, exactly as in ADR-074: koanf delivers a nil value there,
not `""`, so `critical:` with nothing after it still takes the default. A test pins
that boundary for bool as it does for numeric.

## Consequences

- `CACHE_CRITICAL=`, `DATABASE_POOL_KEEPALIVE_ENABLED=`, `SERVER_LOGROUTES=` and every
  other bool key delivered empty now fail startup naming the key. `CACHE_CRITICAL=false`
  still makes a failing cache probe non-fatal on `/ready` — that opt-out is deliberate and
  documented; what ends is reaching it without an explicit operator choice.
- A deployment that rendered an empty value into a bool key and was running on the
  resulting `false` fails after the upgrade. For `database.pool.keepalive.enabled` that
  deployment was already degraded, silently; for the non-pointer bools it was on `false`,
  which is also the documented default, so the failure is loud where the behaviour was
  benign. The trade-off is the same as in ADR-074: the framework cannot guess which value
  the operator intended.
- The public `Config.Unmarshal` seam enforces it too, so a consumer's own bool fields
  get the rule without opting in — and `config.Load` now agrees with `InjectInto`
  instead of contradicting it.
- Unset variables, YAML omission, YAML null, and every explicit spelling
  (`true`/`false`, `1`/`0`) behave exactly as before.
- Still open, and deliberately not closed here, both at other seams: the typed getters
  `Config.Int`/`Int64`/`Float64`/`Bool` return their default for a present-but-empty key
  rather than reporting it (#1111) — materially milder, since they swallow to the CALLER's
  stated default rather than the type's zero, so an empty value behaves as absence there;
  and `[]string`, where the framework's own comma-list hook maps `""` to `[]string{}` and a
  delivered-empty `debug.allowedips` therefore passes ADR-049's fail-closed gate on a
  configured bearer token and then silently skips the IP whitelist (#1120). Extending this
  guard to slices would break the documented comma-list behaviour, so that one needs its own
  decision.

Migration: [C60.18](migrations.md).

## Alternatives considered

**Leave bool alone, since `false` is a bool's zero anyway.** True for the non-pointer
bools and false for the three that matter: a `*bool` tri-state reads a non-nil `*false`
as an operator choice, which is how the keep-alive default flipped and how strict
readiness was disabled. Splitting the rule by pointer-ness would also mean a key's
guard depends on a struct-field detail no operator can see.

**A second hook for bool, leaving the numeric one untouched.** It keeps ADR-074's file
literally unchanged and costs a duplicated hook body, a second entry in every compose
chain, and a second mirror to drift. The two hooks would differ only in a predicate and
a message — one hook with one honest name is the same rule stated once.

**Fix it in `convertToBool` instead.** That is the `InjectInto` path, which already
rejects `""`. The keys that flipped never go through it: they decode into
`config.Config` at `Load`, which is precisely the seam that was silent.
