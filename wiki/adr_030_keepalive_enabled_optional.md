# ADR-030: `PoolKeepAliveConfig.Enabled` Is Optional (`*bool`) So an Explicit `false` Is Honored

**Status:** Accepted
**Date:** 2026-06-16

## Context

`applyDatabasePoolDefaults` (`config/validation.go`) applies production-safe pool
defaults when the corresponding values are zero. The TCP keep-alive defaulting
coupled two independent fields:

```go
// Before:
if cfg.Pool.KeepAlive.Interval == 0 {
    cfg.Pool.KeepAlive.Enabled = defaultKeepAliveEnabled // true
    cfg.Pool.KeepAlive.Interval = defaultKeepAliveInterval // 60s
}
```

With `Enabled bool`, a zero `Interval` was the trigger for defaulting **both**
fields. The natural opt-out — `database.pool.keepalive.enabled: false` while
leaving `interval` unset — was therefore silently overridden: the zero `Interval`
flipped `Enabled` back to `true` (M5). An operator who explicitly disabled
keep-alive still got keep-alive probes. A plain `bool` cannot distinguish "absent"
(apply the default) from "explicitly false" (honor the opt-out), because both
present as the zero value `false`.

The two vendor connection layers consume the resolved value
(`database/postgresql/connection.go`, `database/oracle/connection.go`), so the
bug shipped end-to-end: the only way to actually disable keep-alive was to set
`enabled: false` **and** a non-zero `interval`, which is non-obvious and undocumented.

## Decision

Change `PoolKeepAliveConfig.Enabled` from `bool` to `*bool` so the three states
are distinguishable:

- `nil` — key absent → default to `true` (recommended for cloud).
- `&true` — explicit enable → honored.
- `&false` — explicit disable → honored, **regardless of `Interval`**.

`Enabled` and `Interval` are now defaulted **independently** in
`applyDatabasePoolDefaults`:

```go
if cfg.Pool.KeepAlive.Enabled == nil {
    enabled := defaultKeepAliveEnabled
    cfg.Pool.KeepAlive.Enabled = &enabled
}
if cfg.Pool.KeepAlive.Interval == 0 {
    cfg.Pool.KeepAlive.Interval = defaultKeepAliveInterval
}
```

A nil-safe reader `PoolKeepAliveConfig.IsEnabled()` (`config/types.go`) treats nil
as disabled and is the single accessor used by both vendor connection layers, so
direct struct construction that bypasses validation (e.g. tests) cannot panic on a
nil `Enabled`. After validation, `Enabled` is always non-nil.

Rejected alternatives:

- **Keep `bool`, decouple the defaulting on a separate sentinel:** there is no
  spare sentinel — `false` is a meaningful value, so a plain `bool` cannot encode
  "unset." A `*bool` is the idiomatic Go way to make a boolean tri-state.
- **Add a separate `EnabledSet bool` field:** two fields to keep in sync, and the
  zero value (`Enabled:false, EnabledSet:false`) is still ambiguous to a caller
  constructing the struct directly. A pointer collapses both into one field.

## Consequences

- **Breaking API change (apiImpact: minor):** `PoolKeepAliveConfig.Enabled` is now
  `*bool`. Code that constructs `PoolKeepAliveConfig{Enabled: true}` /
  `{Enabled: false}` directly no longer compiles; use a `*bool` (e.g. a `boolPtr`
  helper or `observability.BoolPtr`). Code that reads `cfg.Pool.KeepAlive.Enabled`
  as a `bool` must switch to `cfg.Pool.KeepAlive.IsEnabled()`. YAML/env config is
  unchanged (`database.pool.keepalive.enabled: true|false` binds to the pointer).
  Documented in
  [migrations.md](migrations.md#poolkeepaliveconfigenabled-is-now-bool-adr-030).
- **Behavioral fix:** `enabled: false` with `interval` unset now disables
  keep-alive (previously re-enabled it). An omitted `enabled` key still defaults to
  `true`, so default deployments are unaffected.
- **Guard tests:** `TestApplyDatabasePoolDefaultsKeepAliveExplicitDisableHonored`
  reproduces M5 (explicit `false` + zero `interval` stays disabled);
  `TestPoolKeepAliveConfigIsEnabled` covers the nil/true/false reader.

## Related

- ADR-025 (pool idle tracks max), ADR-016/ADR-022/ADR-023/ADR-024 — other small,
  breaking config-default/convention changes.
- ADR-003 (database by intent) — pool defaults apply only when a database is
  explicitly configured.
