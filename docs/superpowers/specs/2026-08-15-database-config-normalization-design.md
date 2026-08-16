# Database-config normalization + Validate as the universal door — design

**Date:** 2026-08-15 · **Source:** architecture review (candidates #2 and #3) + grilling session · **Glossary:** `CONTEXT.md`

## Problem

`config/validation.go` is the hottest file in the repo (30 commits since 2026-06). Five of the last
ten touched one cluster: five entry points normalize a *database section* with subtly different rule
subsets — `Validate` (root), the named loop, the tenant loop, the exported `ApplyDatabasePoolDefaults`
(per-connection door used by `database.DbManager`), and an `app/`-side re-walk
(`untypedConnectionStringPaths`) that exists only because `app.NewWithConfig` never calls
`config.Validate`. That same bypass is why `app/managers.go` and `messaging/manager.go` carry
mirrored default constants ("kept in sync with config/validation.go") and why `app/lifecycle.go`
carries a guard that "only covers Validate-bypassing callers".

## Decisions

| # | Decision | Rationale |
| --- | -------- | --------- |
| 1 | Three dependent PRs, one stack (A → B → C). | #3's deletions are only safe after #2 gives every path one normalizer and B closes the bypass. |
| 2 | Normalization module lives **inside `config`** (unexported), with two doors: `Validate` (startup) and `ApplyDatabasePoolDefaults` (connect). | Its collaborators (`ConfigError` constructors, `Manager.isSet`, koanf presence) are package-private; a sub-package would move complexity, not concentrate it. |
| 3 | Module inputs are **placement** (`root` / `named` / `tenant`) and **strictness** (`startup` / `connect`). | The named and tenant loops are near-identical because placement *is* an input. Strictness names ADR-050's deliberate asymmetry. |
| 4 | `ApplyDatabasePoolDefaults` keeps its name and signature. | Rename = apidiff break + ADR + atom for zero caller leverage. |
| 5 | **`app.Builder.WithConfig` runs `config.Validate`** — every construction path, including `NewWithConfig` and direct `Builder` use. Breaking: `fix(app)!`, ADR-064, migrations atom. | ADR-050 already documents "hand-built configs must run `config.Validate` before `NewWithConfig`" as an obligation; enforcing it is Fail Fast. `Builder` is exported, so `NewWithConfig` alone would leave a bypass and keep the mirrors load-bearing. |
| 6 | Untyped-DSN detection moves into `config` as `UntypedDatabaseSections(cfg) []string`; `app` applies the connector policy. | ADR-050's connector exemption stays app-side; the third traversal of the database tree leaves `app/`. |
| 7 | Presence ("section delivered?") module: **out of scope**, follow-up issue. | ADR-047/051 mechanics; needs a second adapter to justify the seam. |
| 8 | Delete `app/managers.go` mirrored constants + single-tenant fallbacks and the `lifecycle.go` bypass guard; **keep** `messaging.NewMessagingManager`'s own fallback (reworded). | After #5 the app-side copies are dead on every path; messaging's is that standalone package's interface default. |
| 9 | One constants table read by both koanf `loadDefaults` and `apply*Defaults` for keys present in both; pinning test asserts equality. **No new `apply*Defaults` for koanf-only fields** (server timeouts, `app.env`, `server.port`). | Adding them would flip an explicit `server.timeout.read: 0` from a fail-closed error into a silent default — a second silent-config atom. Deferred with the "derive `loadDefaults` from the normalize phase" follow-up. |
| 10 | Database-cluster tests migrate to the module's doors; other sections' white-box tests untouched. | The interface is the test surface; folded helpers' tests test code that no longer exists. |
| 11 | One new ADR (064) in PR B; ADR-050 gets a second amendment in PR A. Atom in `wiki/migrations.md`, line in CLAUDE.md Breaking Changes. | One decision, one ADR; the module is its mechanism. |
| 12 | `Load()` → `NewWithConfig` runs `Validate` twice. Accepted. | Idempotent, microseconds, no hidden "already validated" state. |
| 13 | Config-wide normalize/check two-phase split: out of scope, follow-up issue. | Touches every section; its own candidate. |
| 14 | Names: normalization, strictness, placement, verdict, absence, delivered-but-empty — see `CONTEXT.md`. | Reuse ADR-047's "verdict"; don't coin. |
| 15 | Follow-ups become GitHub issues (`config: …`, `area/config` + kind + `needs-triage`). | Tracked backlog; ADRs record decisions, not todos. |

## Assumptions carried

- Error *wording* for named/tenant sections changes: the module emits one `ConfigError` shape with a
  placement-aware `Field` (`databases.<name>`, `multitenant.tenants.<id>.database`, `<path>.manager`)
  and wraps normalization errors as `<path>: …`. `errors.As(*ConfigError)` and `Category` stay stable.
- Connect strictness = today's `ApplyDatabasePoolDefaults` steps exactly (infer without conflict error,
  vendor rules, pool defaults; **no** identity/core-field checks — the dial is the check). Startup
  strictness = today's `validateDatabase` steps exactly, per-path order preserved.
- One shared iterator over the tree (root → sorted `databases.*` → sorted `multitenant.tenants.*.database`
  gated on `Multitenant.Enabled`, with map write-back) serves `validateNoDeliveredEmptyDatabase` and
  `UntypedDatabaseSections`. `Validate`'s named and tenant loops keep their positions (ordering is
  load-bearing) and call the module directly.

## Follow-ups (filed as issues at the end of the stack)

1. Presence module over koanf key-presence + decoded values (blind spot for hand-built / dynamic configs).
2. Derive koanf `loadDefaults` from the normalize phase; decide once which koanf-only fields default vs require (server timeouts, `app.env`, `server.port`, `app.name`).
3. Config-wide normalize-then-check split with whole-`Config` as the test door.
