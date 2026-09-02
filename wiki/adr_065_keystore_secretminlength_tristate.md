# ADR-065: keystore.secretminlength Is a Tri-State Setting

**Status:** Accepted
**Date:** 2026-08-16

> **Amended (2026-09-01, the opt-out removed):** [ADR-095](adr_095_keystore_secret_floor_mandatory.md)
> closes the deprecation this ADR opened. The `0` arm and both startup WARNs are
> gone: `config.Validate` rejects any `keystore.secretminlength` below 32, nil
> still means 32, and the pointer now separates absent from explicit only. The
> encoding decision below stands; read "0 = off" as history.

## Context

ADR-064 made `config.Validate` mandatory on every construction path,
exposing a pre-existing bug in `KeyStoreConfig.SecretMinLength`: as a plain
`int`, `0` (floor off) and "never set" were indistinguishable, so a
hand-built `KeyStoreConfig{Keys: …}` booted with the floor silently off
while `config.Load` gave the same absence 32. A Tri-state setting (see
[CONTEXT.md](../CONTEXT.md)) had been encoded as a two-state `int`.

## Decision

`KeyStoreConfig.SecretMinLength` becomes `*int`, adopting `cache.critical`'s
tri-state pattern (ADR-046): `nil` applies the documented default, `0` is a
deprecated explicit opt-out, `N > 0` sets the floor to `N`. As with
`IsCacheCritical`, `config` owns the nil semantics through an accessor —
`KeyStoreConfig.SecretFloor()` — so `keystore` keeps an `int` API and never
sees the pointer. `normalizeKeyStore` fills nil with the same answer
(`config.DefaultKeyStoreSecretMinLength`, 32), so `Validate` output is total;
`checkKeyStore` rejects only negatives; `loadDefaults`'s koanf default reads
the same constant. Two deliberate divergences from ADR-046: the koanf default
is kept (it equals the documented default, so the koanf door still yields
"absent → 32"), and normalize fills the pointer (the derived-defaults work
in #1023 reads `normalize(zero)`). Go literals write `new(n)`.

`0` still works but is deprecated: `Module.Init` WARNs once when the floor is
disabled, and once per admitted secret shorter than 32 bytes, naming the key
and length — never the material. Alternatives rejected: a `-1` sentinel (flips
the meaning of an existing YAML `0`); dropping the opt-out now (no
deprecation window for a documented feature — that is #1036).

## Consequences

- **Breaking, compile-time for Go literals only.** `SecretMinLength: 0` or
  `SecretMinLength: N` no longer compiles; both become
  `SecretMinLength: new(0)` / `new(N)`. YAML and env config
  are unchanged — `keystore.secretminlength: 0` still means off *at this
  ADR*; ADR-095 later rejects it. See
  [migrations.md](migrations.md) `[C59.13]` (Go literals) and `[C59.14]` (a config that never set it).
- A hand-built config that treated absence as "off" now gets the 32-byte
  floor — a shorter secret that used to boot now fails startup.
- The `0` opt-out is deprecated, not removed *by this ADR*. A later ADR is
  expected to remove it and make 32 mandatory, letting `secretminlength` only
  raise the floor (tracked in #1036) — which
  [ADR-095](adr_095_keystore_secret_floor_mandatory.md) did on 2026-09-01.
