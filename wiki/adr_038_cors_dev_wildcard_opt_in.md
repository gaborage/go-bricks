# ADR-038: Require Explicit Opt-In for Dev-Permissive CORS

**Status:** Accepted
**Date:** 2026-07-12

## Context

Koanf defaults `app.env` to `development` when `APP_ENV` is unset (ADR-022). Combined
with `server/cors.go`'s origin policy, an **unset** `APP_ENV` — or any development alias
(`development`/`dev`/`local`) — with `CORS_ORIGINS` unset selects the most permissive CORS
posture available: `AllowOrigins=["*"]` reflected via `UnsafeAllowOriginFunc` (echoes back
whatever `Origin` header the request sends) combined with `AllowCredentials=true`. That
combination means any website can issue a credentialed cross-origin request to the service
and read the response — the deployment equivalent of disabling CORS entirely, but silently,
as a *default*.

PR #696 ("warn when CORS reflects any origin with credentials") made this posture loud: a
startup `WARN [server.cors] ...` names the effective `APP_ENV` and calls out when it was
never set in the process environment at all — closing the "I forgot `APP_ENV` in this
deployment" blind spot for anyone reading logs. But a WARN in a log stream nobody is
actively watching does not stop the credentialed cross-origin read from happening. The
posture remained the *default* outcome of forgetting a single env var, not something an
operator had to reach for.

## Options Considered

### Option A: Warn-only status quo (Rejected)

Keep the ADR-022/PR #696 behavior: dev aliases get the wildcard, loudly logged.

**Rejected because:** a log line is a detection mechanism, not a prevention mechanism. The
failure mode this ADR closes — "deployed to a real environment but `APP_ENV` was never set
or was left at a dev alias" — is exactly the scenario where nobody is watching startup logs
closely enough to catch a WARN before the service takes traffic.

### Option B: Opt-in env var gating the dev branch (Chosen)

Require `CORS_DEV_WILDCARD=true` (raw process env, parsed with `strconv.ParseBool`) in
addition to `config.IsDevelopment(appEnv)` before granting the wildcard posture. Without the
flag, a development-alias env now fails closed when no `CORS_ORIGINS` allowlist is configured
(the strict-allowlist branch always wins first; a configured allowlist still emits headers
for listed origins) — identically to neutral and production envs: no
`Access-Control-Allow-Origin` header is emitted, so browsers reject the cross-origin
request outright.

**Chosen because:** it flips the default from "permissive unless proven otherwise" to
"fail-closed unless proven otherwise," which is the framework's Fail-Fast / Explicit-over-
Implicit posture (CLAUDE.md manifesto) applied to a network-facing default. Forgetting the
flag now yields a *safe* outcome (no cross-origin credentialed access) instead of a
*dangerous* one. The flag only ever needs to be true in a genuine local-dev shell — it's a
one-line addition to a dev `.env` or compose file, not a per-request cost.

### Option C: Koanf config key (`server.cors.devwildcard`) (Rejected)

Gate the posture on a Koanf-managed config key instead of a raw env var.

**Rejected because:** `server/cors.go` is deliberately constructed from raw process env
(`CORS_ORIGINS` is the existing precedent) rather than the Koanf-loaded `*config.Config`,
because CORS is wired before the framework's config/logger are fully bootstrapped in some
call paths. A YAML-settable wildcard flag could also be **committed to a config file** and
inadvertently shipped to a real environment — the exact failure mode this ADR closes. Keeping
the knob env-only reduces committability exposure rather than eliminating it: the flag can
still land in compose files, `.env` files, or CI manifests, but doing so requires
deliberately naming `CORS_DEV_WILDCARD` rather than it riding along invisibly among dozens
of YAML config keys.

### Option D: Remove the wildcard posture entirely (Rejected)

Drop `UnsafeAllowOriginFunc`-based reflection; dev envs always require `CORS_ORIGINS`.

**Rejected because:** browser-based local development (a frontend on `localhost:3000`
against a backend on `localhost:8080`, with varying frontend ports across projects)
legitimately needs to echo an arbitrary `localhost` origin back with credentials. Forcing an
explicit allowlist for every local dev origin combination is real friction with no security
benefit in a non-production context; the opt-in flag captures the same intent as "I know I'm
running this locally" without removing the convenience.

## Decision

`server/cors.go`'s `config.IsDevelopment(appEnv)` branch now additionally requires
`devWildcardOptIn()` — `CORS_DEV_WILDCARD` read via `os.LookupEnv` and parsed with
`strconv.ParseBool` — to be true before granting the wildcard-echo + credentials posture.

- **Both conditions must hold.** `config.IsDevelopment(appEnv)` alone is no longer
  sufficient; `CORS_DEV_WILDCARD=true` alone is not sufficient either.
- **Without the flag**, a development-alias env falls through to `failClosed` — the same
  `AllowCredentials=false` + reject-all `UnsafeAllowOriginFunc` used by the neutral/production
  branch — with a new WARN (`emitDevOptInRequiredWarn`) explaining the fail-closed outcome and
  naming both remediation paths (`CORS_DEV_WILDCARD=true` for the wildcard, or `CORS_ORIGINS`
  for a strict allowlist).
- **Unparseable values** (e.g. `CORS_DEV_WILDCARD=ture`, a typo) are treated as `false` — fail
  closed, with a WARN naming the bad value, rather than silently granting or silently denying
  without explanation.
- **Containment property:** the flag is inert outside `config.IsDevelopment`. A non-dev env
  with `CORS_DEV_WILDCARD=true` set still fails closed exactly as before, with an additional
  WARN noting the flag is being ignored. This is the property a reviewer should scrutinize
  most closely — any code path where the flag grants the wildcard for a non-dev env would be
  a security regression.
- The plan-009 permissive WARN (`emitDevPermissiveWarn`) still fires when the flag **is**
  set — opting in does not buy silence, only the posture itself.

This amends the CORS paragraph of [ADR-022](adr_022_env_policy.md#option-3-soft-format-check--behavior-predicates-chosen);
the `IsDevelopment`/`IsProduction` alias sets themselves are unchanged and out of scope.

## Implementation Details

**Modified files:**

- `server/cors.go` — extracted a shared `failClosed(cfg *middleware.CORSConfig)` helper
  (previously duplicated inline at two sites; now three call sites: the strict-branch
  all-invalid-entries case, the new dev-without-opt-in case, and the `default:` non-dev case).
  Added `devWildcardOptIn() bool` and `emitDevOptInRequiredWarn(appEnv string)`. The
  `default:` branch also surfaces a WARN when `CORS_DEV_WILDCARD` is set but ignored (env is
  not a development alias) — an operator debugging "why doesn't the flag do anything" signal.
- `server/cors_test.go` — four new tests covering the opt-in matrix
  (`TestCORSDevWithoutOptInFailsClosed`, `TestCORSDevOptInEnablesWildcard`,
  `TestCORSDevOptInInvalidValueFailsClosed`, `TestCORSDevWildcardIgnoredOutsideDev`); thirteen
  existing tests updated to set `CORS_DEV_WILDCARD=true` so they keep exercising the
  dev-permissive branch they were written against.
- `server/handler_test.go` — `TestPublicMiddlewareConstructorsReturnFlatForm` opts in for its
  end-to-end CORS assertion.

No exported signature changed: `CORS(exposeResponseTime bool, envOverride ...string)` and the
unexported `corsEcho` keep their existing shapes.

## Consequences

### Positive

- Forgetting `APP_ENV` (or leaving it at a dev alias) in a real deployment now fails CORS
  closed instead of granting the most permissive posture available — unless that deployment
  also explicitly ships `CORS_DEV_WILDCARD=true`. The change eliminates the *accidental*
  default (wildcard-by-omission); the residual risk becomes a deliberate, grep-able, named
  action rather than an invisible one.
- The opt-in is a single env var, matching the existing `CORS_ORIGINS` raw-env precedent —
  no new config surface, no Koanf key, reduced risk of the flag riding along invisibly in a
  committed config file (see Option C).
- The containment property (flag inert outside `config.IsDevelopment`) is proven by a
  dedicated regression test and by the non-dev cases of
  `TestCORSProductionAliasesTriggerStrictMode` running with the flag set.

### Negative

- **Breaking change to local-dev DX.** Every local dev setup (shell profile, `.env` file,
  Docker Compose, devcontainer) that relied on the implicit dev wildcard must now set
  `CORS_DEV_WILDCARD=true` explicitly, or switch to `CORS_ORIGINS` for a strict allowlist.
  Tracked as migrations atom **C50.3**.
- One more env var for operators to know about, alongside `CORS_ORIGINS`. Mitigated by the
  WARN naming both remediation paths whenever CORS fails closed on a dev-alias env.

### Neutral

- Production/staging/neutral-env CORS behavior is unchanged — this ADR only narrows the
  conditions under which the dev-permissive branch is reached.

## Migration Impact

**Breaking change:** local development environments that relied on the implicit
reflect-any-origin + credentials CORS posture must now set `CORS_DEV_WILDCARD=true`
explicitly (or configure `CORS_ORIGINS` for a strict allowlist, which works in any
environment). Production and staging deployments are unaffected — they already required
`CORS_ORIGINS` to receive any CORS headers at all. Residual case: a deployment that
explicitly ships `CORS_DEV_WILDCARD=true` alongside an unset (koanf-defaulted) `APP_ENV`
still receives the wildcard — the opt-in eliminates the accidental default, not the
deliberate action.

See [wiki/migrations.md](migrations.md) atom **C50.3** for the detect/gate/apply/verify
runbook entry.

## Related ADRs

- [ADR-022: Environment Policy](adr_022_env_policy.md) — defines the `IsDevelopment`/
  `IsProduction` alias predicates this ADR gates on; amended by this ADR to add the
  `CORS_DEV_WILDCARD` opt-in requirement on top of the `IsDevelopment` branch.
