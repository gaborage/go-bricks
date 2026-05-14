# ADR-022: Environment Policy — Free-Form `app.env` with Predicate-Based Branching

**Status:** Accepted
**Date:** 2026-05-14
**Related issues:** [#435](https://github.com/gaborage/go-bricks/issues/435)

## Context

Until this ADR, `config/validation.go` rejected any `app.env` value outside the exact allowlist `{development, staging, production}`. Consumer projects with conventional short codes like `local`, `tst`, `stg`, `prd`, `dev`, `prod`, `qa`, `uat` were forced to either:

1. Rename their environments to the framework's three canonical names (cross-cutting org change), or
2. Fork the validator.

A grep of `cfg.App.Env` usage across the framework shows only **6 behavior-switching call sites**:

| Site | Logic |
|---|---|
| `server/server.go` × 2 | `SetCaptureStackTraces(isDevelopmentEnv(env))`; dev-only error details |
| `server/handler.go` × 2 | Dev-only error details on enveloped + raw responses |
| `server/cors.go` × 1 | Strict CORS when `APP_ENV == production` |
| `migration/flyway.go` × 1 | Auto-migrate in development |

All other ~8 usages of `cfg.App.Env` are pure passthrough — log fields, health endpoint, span attributes. **Nothing in the framework branches on `staging`**; it's treated identically to production except for CORS. The real semantics are binary (developer machine vs. real infrastructure); the three-value enum existed only at the validator.

The strict enum had also been quietly pushing back against reality:

- `server/env.go` (now removed) declared `envAliasDev = "dev"` and `isDevelopmentEnv(env) bool` accepting both `development` and `dev` — yet `"dev"` would never get past the validator, making the alias branch unreachable in practice.
- `app/app.go`'s bootstrap logger (which runs *before* validation) already inlined its own case-insensitive matching: `strings.EqualFold(env, "development") || strings.EqualFold(env, "dev")`.

Two separate places already had ad-hoc alias logic. The validator was the bottleneck preventing that logic from being uniform or useful.

## Options Considered

### Option 1: Expand the allowlist (Rejected)

Add `local`, `tst`, `stg`, `prd`, `dev`, `prod`, `qa`, `uat` to `validEnvs`. Keeps the existing validator shape; minimal code change.

**Rejected because:**

- Every consumer with a non-listed convention (custom region-suffixed envs like `production-eu`, `staging-us-east`) still hits the same wall.
- Doesn't address the binary semantic mismatch — six call sites still hard-code equality against canonical strings, so `prd` would pass validation but still not trigger production CORS.
- The allowlist becomes a maintenance liability: every new convention adds entries and edge cases.

### Option 2: Drop validation entirely (Rejected)

Accept any string for `app.env`, including empty.

**Rejected because:**

- Loses the fail-fast property that catches structural typos (uppercase, accidental spaces, garbage from misconfigured env-var substitution).
- An empty `app.env` would silently propagate into `config.<env>.yaml` filename construction (`config.go:36-42` uses `app.env` to load environment-specific YAML overlays), producing confusing missing-file behaviour rather than a clear validation error.

### Option 3: Soft format check + behavior predicates (Chosen)

Two-part design:

1. **Validator becomes a format rule.** Accept any string matching `^[a-z][a-z0-9-]{0,31}$` — non-empty, ≤32 chars, lowercase alphanumeric or hyphen, starting with a letter. Catches structural typos without enforcing semantic naming.
2. **Behavior switches go through predicates** with documented alias sets:
   - `func config.IsDevelopment(env string) bool` + `(*AppConfig).IsDevelopment()` — accepts `{development, dev, local}`.
   - `func config.IsProduction(env string) bool` + `(*AppConfig).IsProduction()` — accepts `{production, prod, prd}`.
   - Any value not in either alias set is **neutral**: treated as non-dev (no dev conveniences like stack-trace capture or error-detail leakage) and non-prod (no CORS lockdown). Examples: `staging`, `stg`, `tst`, `qa`, `uat`, `production-eu`.

**Chosen because:**

- Aligns the validator with how the framework actually consumes the value. The six behavior-switching call sites collapse cleanly to the two predicates; the passthrough sites don't care what the string is.
- Captures intent at the call site (`cfg.App.IsDevelopment()` reads better than `cfg.App.Env == config.EnvDevelopment`).
- Consolidates the previously inconsistent inline alias logic (`app/app.go`, `server/env.go`) into one map per direction.
- Predicate-based design is consistent with the rest of the framework's "be liberal in what you accept, strict in what you assert" surface — e.g., `database.Interface` accepts any DSN matching driver expectations, observability accepts any OTLP endpoint URL, etc.

## Decision

GoBricks ships a free-form `app.env` policy. The string is validated only for format (regex above); semantic branching goes through `IsDevelopment` / `IsProduction` predicates.

### Alias maps

```text
developmentAliases = {development, dev, local}
productionAliases  = {production, prod, prd}
```

Maps are **disjoint** (asserted by `TestEnvAliasesAreDisjoint`). Anything outside both is a "neutral" environment.

### Predicate API

Both free-function and method forms are exposed:

```go
// Free functions — used when the raw env string is in hand (e.g., reading
// os.Getenv directly during bootstrap, before AppConfig validation has run).
func config.IsDevelopment(env string) bool
func config.IsProduction(env string) bool

// Method forms — used when AppConfig is already loaded.
func (*AppConfig) IsDevelopment() bool
func (*AppConfig) IsProduction() bool
```

Predicates normalize via `strings.ToLower(strings.TrimSpace(env))` so callers that read raw env vars don't need to pre-normalize.

### Validator behavior

```go
var envFormat = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
```

Accepts: `development`, `local`, `tst`, `stg`, `prd`, `staging-eu`, `qa-us-east-1`, …
Rejects: empty, `Production` (uppercase), `prd eu` (space), `1prod` (leading digit), `-prd` (leading hyphen), 33+ chars.

### Canonical names retained

`EnvDevelopment`, `EnvStaging`, `EnvProduction` remain exported constants for use in framework code, framework tests, and as the documented "default" name. Consumer projects may use any conforming string.

## Implementation Details

**New files:**

- `config/env.go` — `envFormat` regex, alias maps, `IsDevelopment` / `IsProduction` free functions + methods.
- `config/env_test.go` — predicate coverage including nil-receiver safety, case/whitespace normalization, and the alias-disjointness invariant.

**Modified files:**

- `config/validation.go` — `validateApp` switches from `slices.Contains(validEnvs, cfg.Env)` to `envFormat.MatchString(cfg.Env)`.
- `config/validation_test.go` — replace the `Env: "invalid"` failure case (which now passes the format check) with cases that genuinely fail it: empty, uppercase, embedded space, leading digit, too-long. Adds positive cases for `local`, `tst`, `prd`, `production-eu`.
- `server/server.go`, `server/handler.go` — four call sites switched from `isDevelopmentEnv(cfg.App.Env)` to `cfg.App.IsDevelopment()`.
- `server/cors.go` — `os.Getenv("APP_ENV") == config.EnvProduction` → `config.IsProduction(os.Getenv("APP_ENV"))`. **Behavior change:** `APP_ENV=prd` or `APP_ENV=prod` now triggers strict CORS, matching the alias map.
- `server/cors_test.go` — adds `TestCORSProductionAliasesTriggerStrictMode` covering `prd`, `prod`, `production`, plus `local` and `tst` as negative cases.
- `migration/flyway.go` — `fm.config.App.Env == config.EnvDevelopment` → `fm.config.App.IsDevelopment()`. Auto-migrate now also fires for `APP_ENV=dev` or `APP_ENV=local`.
- `app/app.go` — bootstrap logger replaces inline `strings.EqualFold` calls with `config.IsDevelopment(env)`. Preserves the legacy "empty/whitespace `APP_ENV` → dev defaults" behaviour.

**Deleted files:**

- `server/env.go` — dead since the validator never let `dev` through; now subsumed by `config.IsDevelopment`.

## Consequences

### Positive

- Consumer projects use their own env conventions without forking the validator.
- Six behavior-switching call sites have consistent semantics: aliases that look like prod are treated like prod, regardless of which spelling lands in `APP_ENV`.
- Eliminates the previously dead `envAliasDev` code path and the duplicated inline alias logic in `app/app.go`.
- Predicate-based call sites read more clearly than enum equality.

### Negative

- Loses the fail-fast property that catches semantic typos within the canonical set: `develpoment` (intentional misspelling of `development` used as an example throughout this section) was previously rejected with "must be one of: development, staging, production", whereas it is now accepted by the format check (lowercase letters only — perfectly valid format) despite being semantically wrong. Mitigation: the format check still catches the most common structural typos (uppercase, spaces, length); pure semantic typos surface immediately in logs, span attributes, and the loaded `config.<env>.yaml` filename (a `develpoment` env would attempt to load `config.develpoment.yaml`, which is missing → falls back silently to the base `config.yaml`).
- Marginal observability complexity: dashboards filtering on `env=production` will not match `env=prd`. Mitigation: consumers picking short-code conventions should standardize their dashboard filters accordingly; the framework does not coerce the value before emitting it.

### Neutral

- `EnvDevelopment`, `EnvStaging`, `EnvProduction` constants remain exported — framework code and tests continue to use them as canonical names. No SemVer breakage at the constant level.

## Migration Impact

**Breaking change:** Yes, but only for two narrow categories of behavior:

1. **`APP_ENV=prd` / `APP_ENV=prod`** now triggers production-strict CORS in `server.CORS()`. Previously only literal `production` did. If an existing deployment relies on `APP_ENV=prd` *not* triggering strict CORS (unlikely — it would have been a misconfiguration), that environment will start enforcing `CORS_ORIGINS`. Action: set `CORS_ORIGINS` explicitly in those environments.
2. **`APP_ENV=dev` / `APP_ENV=local`** now enable framework dev conveniences (stack traces in errors, raw error details exposed in responses) and trigger auto-migrate in `migration.FlywayMigrator.RunMigrationsAtStartup`. Previously only literal `development` did. Consumers using these aliases should expect dev behavior, which is the intent.

**Non-breaking for** the documented canonical names — `development`, `staging`, `production` continue to behave identically.

**No SemVer breakage** for the public surface — the new predicates and free functions are additions; the constants and `validateApp` signature are unchanged.

## Related ADRs

- [ADR-006: OTLP Log Export](adr-006-otlp-log-export.md) — the bootstrap logger consolidated in this change uses the same env signal to pick log levels before OTLP wiring runs.
- [ADR-019: Migration Audit-Event Delivery](adr-019-migration-audit-delivery.md) — `migration.applied` audit events include `app.env` as an attribute; the new free-form policy means audit consumers should treat this field as opaque rather than enum-bounded.
