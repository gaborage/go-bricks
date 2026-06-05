# Design: comma-separated env vars for `[]string` config fields

**Status:** Approved (brainstorming) — pending implementation plan
**Date:** 2026-06-04
**Area:** `config` (root cause), `scheduler` (most visible symptom)
**Issue:** [#539](https://github.com/gaborage/go-bricks/issues/539)
**Kind:** bug / security

## Problem

A `[]string` config field cannot be set to a **multi-element** list via a single
environment variable. `SCHEDULER_SECURITY_CIDRALLOWLIST=10.0.0.0/8,192.168.0.0/16`
loads as a **one-element** slice `["10.0.0.0/8,192.168.0.0/16"]` — the comma is part of
the string, not a delimiter.

Root cause, verified against the source:

1. **Env provider passes the value through unchanged.** `config/config.go:45-53` registers
   an env `TransformFunc` that only re-cases the key (`return k, v`); koanf `env/v2` stores
   the value verbatim.
2. **Unmarshal has no string→slice hook.** `config/config.go:57` calls `k.Unmarshal("", &cfg)`,
   which uses koanf v2.3.5's **default** `DecoderConfig` (`koanf.go:265-272`):
   `WeaklyTypedInput: true` with `DecodeHook = StringToTimeDurationHookFunc + textUnmarshalerHookFunc`.
   There is **no** `StringToSliceHookFunc`.
3. **Weak typing lifts a scalar string into a 1-element slice.** `go-viper/mapstructure/v2@v2.5.0`
   wraps the whole string in `[]any{data}` for a `string → []string` conversion.
4. **Downstream rejection.** `scheduler/cidr_middleware.go:52` (`parseCIDRAllowlist`)
   `net.ParseCIDR`s the single element, it fails, zero valid nets remain, and the middleware
   silently falls back to **localhost-only** with only a middleware-time WARN.

A **single**-element env override works; only multi-element lists are broken. The same code
path affects **every** `[]string` field — all of which are security-relevant:

| Config key | Env var | Failure mode (multi-value via env) |
|---|---|---|
| `scheduler.security.cidrallowlist` | `SCHEDULER_SECURITY_CIDRALLOWLIST` | 0 valid CIDRs → **localhost-only** (fail-closed, allowlist ignored) |
| `scheduler.security.trustedproxies` | `SCHEDULER_SECURITY_TRUSTEDPROXIES` | 0 valid proxies → proxy headers untrusted, real client IPs lost |
| `debug.allowedips` | `DEBUG_ALLOWEDIPS` | debug-endpoint IP gate degraded |
| `log.sensitive_fields` | `LOG_SENSITIVE_FIELDS` | one bogus field name matches nothing → **redaction silently weakened** (fail-open; PCI/PII leak) |

> **Finding during implementation:** `log.sensitive_fields` has a *second, distinct* bug
> that the comma-split fix cannot reach. The env `TransformFunc` rewrites every `_` to `.`,
> so `LOG_SENSITIVE_FIELDS` becomes the koanf key `log.sensitive.fields`, which does **not**
> match the field's key `log.sensitive_fields` (underscore *inside* a segment). The env var is
> therefore dropped entirely — the value never reaches the field, split or not. This field is
> documented as YAML-or-code only (never env), so this is an undocumented-path bug with a
> separate root cause (env-key segment vs. nesting ambiguity) whose proper fix changes global
> env-key resolution. **Tracked as a follow-up issue; out of scope here.** The comma-split fix
> fully fixes the three env-mappable `[]string` fields (`cidrallowlist`, `trustedproxies`,
> `allowedips`) plus the YAML and `InjectInto` paths for all `[]string` fields.

There is a **second, independent path** with the same gap: `Config.InjectInto()`
(service-specific config) reads `c.k.Get(key)` directly and **bypasses** koanf's `Unmarshal`,
so any decode-hook fix does not reach it. It currently rejects `[]string` fields outright
with "unsupported type" (`config/injection.go:129-136`).

## Decision

Treat the comma as a delimiter for `string → []string` conversion, uniformly across **both**
config paths, and fail loudly at startup when a non-empty security CIDR list resolves to zero
valid entries.

### Scope decisions (locked during brainstorming)

| Decision | Choice | Rationale |
|---|---|---|
| Fix approach | **Universal decode hook** (not per-key env registry, not docs-only) | One change fixes every `[]string` field, present and future. No hand-maintained key list to forget. Matches "Explicit > Implicit / no silent failures". |
| Whitespace / empties | **Trim each element, drop empties** | `logger/filter.go:220` substring-matches sensitive-field names **without** trimming; the stock `mapstructure.StringToSliceHookFunc` would leave `" cvv2"` and silently fail to redact. |
| All-invalid security CIDR list | **Fail-fast at startup** (`config.Validate`) | A fat-fingered security control must fail loudly, not silently lock to localhost. |
| Fail-fast trigger | **Non-empty list, zero valid entries** | Matches the issue wording. Partial-invalid (≥1 valid) stays tolerated with the existing middleware WARN — minimal behavior change. |
| `InjectInto` `[]string` | **In scope** (per user) | Closes the symmetry gap so service config behaves like framework config. Shares the split helper so semantics can't drift. |
| `debug.allowedips` fail-fast | **Out of scope** | Mixed IP-or-CIDR parse rule; debug is disabled by default; the comma-split fix already resolves its core bug. |
| Scheduler CIDR-parse dedupe | **Out of scope** | Fail-fast lives in `config` (cannot import `scheduler` — circular). The scheduler file is left essentially untouched; no churn warranted. |

### Behavior contract (`string → []string`)

| Input (env value or `default:` tag) | Result |
|---|---|
| `"10.0.0.0/8,192.168.0.0/16"` | `["10.0.0.0/8", "192.168.0.0/16"]` |
| `"10.0.0.0/8"` | `["10.0.0.0/8"]` (unchanged from today) |
| `"pan, cvv2 , otp"` | `["pan", "cvv2", "otp"]` (trimmed) |
| `"a,,b,"` | `["a", "b"]` (empties dropped) |
| `""` or `" , "` | `[]string{}` |
| YAML sequence / `confmap` default `[]string` | passed through untouched (arrives as a slice, not a string) |

**Caveat (accepted):** assumes no single list element legitimately contains a comma. True for
CIDRs, IPs, and field names — the only `[]string` fields that exist.

## Detailed Design

### Change 1 — Prefactor: isolate the decoder config (behavior-preserving)

> *Make the change easy.* This commit changes no behavior; the entire existing `config`
> suite must stay green.

In `config/config.go`, extract a helper that **exactly replicates** koanf's current default
decoder, then route `Load()` through `UnmarshalWithConf`:

```go
// buildDecoderConfig replicates koanf's default Unmarshal decoder (koanf.go:265-272)
// so we control the DecodeHook chain. koanf fills in Result and TagName.
func buildDecoderConfig() *mapstructure.DecoderConfig {
    return &mapstructure.DecoderConfig{
        DecodeHook: mapstructure.ComposeDecodeHookFunc(
            mapstructure.StringToTimeDurationHookFunc(),
            mapstructure.TextUnmarshallerHookFunc(),
        ),
        WeaklyTypedInput: true,
    }
}
```

```go
// was: if err := k.Unmarshal("", &cfg); err != nil {
if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{
    DecoderConfig: buildDecoderConfig(),
}); err != nil {
    return nil, fmt.Errorf("failed to unmarshal config: %w", err)
}
```

Notes:
- koanf's default text hook (`textUnmarshalerHookFunc`) is unexported; we substitute the
  public `mapstructure.TextUnmarshallerHookFunc()`. Safe: the `Config` struct has **no**
  `[]byte`, custom string types, or `TextUnmarshaler` fields (verified), so the two behave
  identically here. Including it preserves forward-compatibility.
- `WeaklyTypedInput: true` is retained — the config relies on it (`"8080"`→int, `"true"`→bool).

### Change 2 — The fix (Unmarshal path): comma-split-and-trim hook

Add one hook to the chain in `buildDecoderConfig`, backed by a shared helper (see Change 4):

```go
// stringToTrimmedSliceHookFunc splits a scalar string into []string on sep,
// trimming each element and dropping empties. Scoped to string -> []string only
// (Type-level), so []byte and other slices/YAML sequences are untouched.
func stringToTrimmedSliceHookFunc(sep string) mapstructure.DecodeHookFunc {
    return func(f reflect.Type, t reflect.Type, data any) (any, error) {
        if f.Kind() != reflect.String || t != reflect.TypeOf([]string(nil)) {
            return data, nil
        }
        return splitAndTrimList(data.(string), sep), nil
    }
}
```

Added to the compose chain as the first hook: `stringToTrimmedSliceHookFunc(",")`.
Order is immaterial — each hook fires on a disjoint target type.

### Change 3 — Fail-fast on all-invalid security CIDR lists

In `config/validation.go`, extend `validateScheduler` (the existing startup-fail hook):

```go
func validateScheduler(cfg *SchedulerConfig) error {
    normalized, err := normalizeIANATimezone("scheduler.timezone", cfg.Timezone)
    cfg.Timezone = normalized
    if err != nil {
        return err
    }
    if err := validateCIDRList("scheduler.security.cidrallowlist", cfg.Security.CIDRAllowlist); err != nil {
        return err
    }
    return validateCIDRList("scheduler.security.trustedproxies", cfg.Security.TrustedProxies)
}

// validateCIDRList fails when a non-empty list contains zero parseable CIDRs.
// Empty lists are valid (localhost-only / no-trusted-proxy defaults). Partial-invalid
// lists pass here and keep the existing middleware-time WARN.
func validateCIDRList(field string, list []string) error {
    if len(list) == 0 {
        return nil
    }
    var invalid []string
    valid := 0
    for _, entry := range list {
        if _, _, err := net.ParseCIDR(strings.TrimSpace(entry)); err != nil {
            invalid = append(invalid, entry)
            continue
        }
        valid++
    }
    if valid == 0 {
        return &ConfigError{
            Category: errCategoryInvalid,
            Field:    field,
            Message:  fmt.Sprintf("no valid CIDR entries (all %d rejected: %v)", len(list), invalid),
            Action:   "use CIDR notation, e.g. 10.0.0.0/8; comma-separate multiple in one env var",
        }
    }
    return nil
}
```

Adds a `net` import to `config/validation.go`. The middleware's defensive localhost-only
fallback stays intact (it is exported and unit-tested directly); this is an additional,
earlier gate, not a replacement.

### Change 4 — `InjectInto` `[]string` support (shared split semantics)

**Shared seam** in `config/converters.go` so both paths produce identical results:

```go
// splitAndTrimList splits raw on sep, trims each element, and drops empties.
// Single source of truth for env/default string -> []string semantics, shared by
// the Unmarshal decode hook (Change 2) and InjectInto (below).
func splitAndTrimList(raw, sep string) []string {
    if strings.TrimSpace(raw) == "" {
        return []string{}
    }
    parts := strings.Split(raw, sep)
    out := make([]string, 0, len(parts))
    for _, p := range parts {
        if p = strings.TrimSpace(p); p != "" {
            out = append(out, p)
        }
    }
    return out
}

// toStringSlice converts a resolved config value to []string.
func toStringSlice(value any) ([]string, error) {
    switch v := value.(type) {
    case []string:
        return v, nil
    case []any: // YAML sequence
        out := make([]string, len(v))
        for i, el := range v {
            out[i] = fmt.Sprintf("%v", el)
        }
        return out, nil
    case string:
        return splitAndTrimList(v, ","), nil
    default:
        return nil, fmt.Errorf(errMsgUnsupportedType, value)
    }
}
```

In `config/injection.go`, add a `reflect.Slice` arm to `assignFieldValue`:

```go
case reflect.Slice:
    if field.Type().Elem().Kind() == reflect.String {
        return c.assignStringSliceField(field, configKey, required, value)
    }
    return &ConfigError{ /* unsupported type, as today */ }
```

```go
func (c *Config) assignStringSliceField(field reflect.Value, configKey string, required bool, value any) error {
    slice, err := toStringSlice(value)
    if err != nil {
        return &ConfigError{Category: errCategoryInvalid, Field: configKey, /* ... */}
    }
    if required && len(slice) == 0 {
        return &ConfigError{Category: errCategoryMissing, Field: configKey, /* ... */}
    }
    field.Set(reflect.ValueOf(slice))
    return nil
}
```

Semantics: env comma string → split; YAML sequence → as-is; `default:"a,b,c"` → split;
`required:"true"` + empty → missing error (mirrors `assignStringField`). Non-string element
kinds (e.g. `[]int`) keep today's "unsupported type" error — `[]string` is the documented
addition, nothing more.

### Documentation updates

- `config/injection.go` — `InjectInto` doc comment: add `[]string` to supported types.
- `CLAUDE.md` — "Configuration Injection → Supported Types": add `[]string`.
- `llms.txt` / `wiki/` — update if they enumerate `InjectInto` supported types.
- Note in config docs / `.env.example` (if present) that comma-separated env vars now
  populate multi-element `[]string` fields.

## Testing (TDD order)

**Step A — Fix tests first (red before Change 2):**
- `config.Load()` with `SCHEDULER_SECURITY_CIDRALLOWLIST=10.0.0.0/8,192.168.0.0/16` → len 2, exact values.
- `LOG_SENSITIVE_FIELDS=pan, cvv2 , otp` → `["pan","cvv2","otp"]` (trim + drop empties).
- Single-element env still works (regression).
- `DEBUG_ALLOWEDIPS` / `SCHEDULER_SECURITY_TRUSTEDPROXIES` multi-element.
- YAML-sequence path still yields the right slice (regression; temp `config.yaml` or existing fixtures).

**Step B — Prefactor regression (Change 1):** full existing `config` suite green.

**Step C — Fail-fast tests (Change 3):**
- Non-empty all-invalid `cidrallowlist` via env → `Load()` errors naming `scheduler.security.cidrallowlist`.
- Non-empty all-invalid `trustedproxies` → error naming the field.
- Partial-invalid (≥1 valid) → no error.
- Empty list → no error. Valid multi → no error.

**Step D — `InjectInto` tests (Change 4):**
- `[]string` field from env comma string → multi-element, trimmed.
- From YAML sequence → multi-element.
- From `default:"a,b,c"` tag → split.
- `required:"true"` + missing/empty → error.
- Single element. `toStringSlice` unit tests (string / `[]any` / `[]string` / unsupported).
- `TestConfigInjectionUnsupportedFieldType` (`chan int`) still passes.

All tests use camelCase function names; table cases use snake_case descriptions
(per CLAUDE.md). `make check` green before commit.

## Out of scope

- **`LOG_SENSITIVE_FIELDS` (and any underscore-in-segment key) env override** — the `_`→`.`
  transform mismaps it to `log.sensitive.fields`. Separate root cause, global blast radius;
  filed as a follow-up issue.
- `debug.allowedips` fail-fast (mixed IP-or-CIDR rule; the comma-split fix covers its bug).
- Deduping `scheduler/cidr_middleware.go` `parseCIDRAllowlist` / `parseTrustedProxies`.
- Any `[]string` element that must itself contain a comma.

## Risk / blast radius

- The decode hook is **additive** and Type-scoped to `string → []string`; `[]byte`,
  durations, text-unmarshalers, and YAML/`confmap` slices are unaffected (verified).
- Change 3 can newly fail `Load()` for an app that sets an **all-invalid** security CIDR env
  var (previously: silent localhost-only). Empty defaults are unaffected, so apps not touching
  these fields see no change. This is the intended, safer behavior.
- Change 4 only widens `InjectInto` from "unsupported" to "supported" for `[]string`; no
  existing supported type changes.
