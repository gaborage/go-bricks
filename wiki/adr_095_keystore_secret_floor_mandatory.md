# ADR-095: The keystore Secret-Length Floor Is Mandatory

- **Status**: Accepted
- **Date**: 2026-09-01
- **Related**: [ADR-065](adr_065_keystore_secretminlength_tristate.md) (the
  tri-state pointer and the deprecation window this decision closes),
  [ADR-064](adr_064_app_validates_every_config.md) (why `config.Validate` is the one
  gate every construction path runs)

## Context

ADR-065 made `keystore.secretminlength` a tri-state pointer: nil applies the
32-byte default, `0` keeps the floor off, `N > 0` sets it. The `0` arm was
kept as a deprecated opt-out with a deprecation window: `keystore.Module.Init`
WARNed once when the floor was off and once per admitted secret shorter than
32 bytes, pointing at #1036, so a consumer with a genuinely short
partner-mandated key could surface the need before the opt-out was removed.

The window has run and no consumer surfaced one. What the opt-out actually
protected was a silently weak HMAC/HKDF key — the exact defect the floor
exists to reject — and the WARN pair was startup noise a deployment could
ignore forever.

## Decision

The 32-byte floor is mandatory. `keystore.secretminlength` can only raise it:
`config.Validate` rejects any set value below `DefaultKeyStoreSecretMinLength`
(32) — `0` and negatives included — with a `*ConfigError` on
`keystore.secretminlength` naming the floor and this ADR. The check runs
before the empty-keys return, so a config no key follows is still rejected.
nil still means 32 through `normalizeKeyStore` and `SecretFloor()`.

The pointer stays: the koanf door must still tell an explicit `0` from an
absent key to reject the former, so `SecretMinLength` remains `*int`
(`new(n)` in Go literals). What changes is that the pointer separates absent
from explicit only — there is no off arm.

Both deprecation WARNs, `store.belowRecommended` and the `0`-means-off branch
in `loadSecretEntry` are deleted, not left unreachable.

`keystore.Module.Init` repeats the bound as a backstop. Every framework
construction path runs `config.Validate` (ADR-064), so the rule is already
discharged there — but `Init` takes an exported `*app.ModuleDeps`, and a
hand-built one reaches it without passing through `Validate`. The rejected
values are the *widening* ones (`0` disables the floor outright), so the
backstop **refuses** rather than clamps: it returns an error naming the floor,
before the empty-keys return, and loads nothing. Without it the claim below —
that a sub-32 secret has no keystore path — would hold only for configs that
happened to be validated.

Alternatives rejected:

- **Clamp a sub-32 value up to 32.** Silently honoring a config that asked for
  less is a hidden default; an operator who wrote `0` expecting the old
  behavior must be told, not corrected.
- **Revert `SecretMinLength` to a plain `int`.** `0` would then be
  indistinguishable from absent at the koanf door, so `secretminlength: 0`
  would boot with the 32-byte floor instead of failing — the same silent
  correction.
- **Keep the WARN one more release.** The window produced no request; another
  release adds weak-key exposure for no information.

## Consequences

- **Breaking, at startup.** `keystore.secretminlength: 0`, or any value below
  32, in YAML, environment (`KEYSTORE_SECRETMINLENGTH`) or a Go literal now
  fails `config.Validate` — and therefore `config.Load` and every `app`
  construction path (ADR-064). See [migrations.md](migrations.md) `[C62.3]`.
- A symmetric secret shorter than 32 bytes has no keystore path at all, on
  either door — `config.Validate` rejects the config and `Module.Init` refuses
  a floor that reached it unvalidated. A
  partner key that is genuinely that short must be loaded by the consumer's
  own code, outside `keystore` — the framework does not weaken the control
  for it.
- `keystore.secretminlength` is no longer a tri-state setting in the
  CONTEXT.md sense; `cache.critical` remains the reference example.
- ADR-065 stays accepted for the pointer encoding; its deprecation clause is
  closed by this ADR (amendment there).
