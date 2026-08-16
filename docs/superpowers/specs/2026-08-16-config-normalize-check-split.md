# Config normalize/check split + derived koanf defaults — design

**Date:** 2026-08-16 · **Source:** grilling session over #1024 and #1023 (follow-ups 2 and 3 of the
2026-08-15 database-config normalization design) · **Glossary:** `CONTEXT.md` (Normalization widened
to any section; **Check** added)

## Problem

`config.Validate` interleaves two jobs per section — shaping the tree (defaults, inference,
prefixing, map write-backs) and rejecting it — so the phase boundary is invisible and untestable
as a whole. Its section order was described as load-bearing but is untested; the koanf `loadDefaults`
map and the `apply*Defaults` constants are two mechanisms pinned equal by a test (#1021, decision 9
of the 2026-08-15 design). #1024 makes the phases explicit; #1023 then derives koanf's defaults from
the normalize phase for the keys normalize owns.

### What the code actually says (facts that reshaped the design)

- `Multitenant.Enabled` is only ever *read* inside `Validate`; nothing mutates it. The section order
  carries exactly two real dependencies, both **error-message precedence**: delivered-empty
  (ADR-051) before database normalization so the specific error wins over generic "incomplete";
  root database normalization before `applyDatabaseManagerDefaults`. Everything else is free.
- `validateNoDeliveredEmptyDatabase` reads `cfg.k`; it is inert on hand-built configs — the path
  ADR-064 now validates. Whole-Config tests must cover both doors (koanf-loaded, literal).
- The koanf keys split into: a **flip set** that FAILS on zero for a hand-built config
  (`app.name`, `app.version`, `app.env`, `server.port`, `server.timeout.{read,write,middleware,shutdown}`,
  `log.level`); a **never-read set** (~25 keys `Validate` never touches — `server.host`, `server.path.*`,
  `debug.*`, `log.output.*`, `app.namespace`, `scheduler.timeout.*`, `keystore.secretminlength`, …);
  and an **owned set** normalize already fills (`app.startup.timeout`; `cache.redis.*` once the
  top-level cache gap is closed). Deriving the flip set would silently reverse part of ADR-064 days
  after it shipped; deriving the never-read set would flip at least one fail-closed posture
  (`debug.allowedips` on a hand-built `debug.enabled: true`, ADR-049).
- The database-section module (`config/database_section.go`) is the *template* only in shape: it
  still checks inside normalize (`normalizeWithFields` → `validateDatabaseType`), and PR #1016 kept
  that order deliberately ("decides which error a doubly-wrong section reports first").

## Decisions — #1024 (normalize/check split)

| # | Decision | Rationale |
| --- | -------- | --------- |
| 1 | Unexported `normalize(*Config) error` + `check(*Config) error`; `Validate` = normalize → check, surface unchanged. | No consumer needs a phase alone; whole-Config tests live in package `config`. Export later only for a real caller (YAGNI, no apidiff surface). |
| 2 | Glossary: **Normalization** widened to any section / the whole tree; **Check** = rejecting a normalized configuration without changing it. `Validate` keeps its name as the composed door. | ADR-064 made `Validate` the contract every construction path calls. |
| 3 | Normalize **may reject**, but only what it cannot shape: a contradiction, a value a consumer would silently drop, a section delivered but empty. Missing-required and cross-field/cross-section rules are check's. | Matches `CONTEXT.md` and the database module's pinned first-error order; a "pure" normalize could not see a contradiction it had already overwritten (explicit `type` vs DSN scheme). |
| 4 | Database-section module stays **opaque**: one normalize step per section, its internal error order untouched. Named/tenant sections same. | #1016 kept that order on purpose; re-splitting reopens it for no consumer gain. |
| 5 | Idempotency is a tested contract: `Validate` twice → deep-equal. | Every construction path validates (ADR-064); `Load()` → `NewWithConfig` already runs it twice. |
| 6 | Order lives in the body of `normalize()`/`check()`; dependencies pinned **behaviorally** by whole-Config tests, not by a step-name table. | A step-name pin proves the list didn't move; a behavioral pin proves the dependency holds. |
| 7 | Whole-Config tests **layered on** the existing white-box per-section tests; nothing rewritten. | White-box tests are the mutation-gate coverage. |
| 8 | Static `multitenant.tenants.<id>.*` in the loaded Config are normalized by the whole-Config phase; dynamic-source tenant sections keep the section-level connect door. | Status quo; the connect door is ADR-050's deliberate asymmetry. |
| 9 | Delivered-empty (ADR-051) runs as the **presence step at the head of normalize** — a rejection that precedes shaping. | It cannot run in check without losing its message to database normalize's generic error; matches `.out-of-scope/config-presence-module.md` ("natural home is the normalize phase"). Documented on `normalize()`: *presence rejections precede shaping*. |
| 10 | New `config/phases.go` (drivers) + `config/phases_test.go` (whole-Config door). Split functions stay in `validation.go` as `normalizeX` + `checkX`; per-section file moves, if ever, are separate mechanical PRs. | Split-and-move in one diff hides the semantic change under 1,800 shuffled lines. |
| 11 | First-error precedence **changes**: normalize's rejections now precede check's, across sections (a database normalize error beats an app-name error) *and* within one (a negative `app.startup.timeout` beats a missing `app.name`). Accepted; the opaque database module's internal order is the only intra-section order preserved. | No ADR/atom promises first-error order between sections or between a fill and a required-field rule. Verified in PR1 that no test pins one; commit body records the change. |
| 12 | **No ADR.** | Hard-to-reverse fails (unexported); the contract lives in `CONTEXT.md`, doc comments, and tests. Revisit only if a phase is exported. |
| 13 | `normalizeCache` fills redis defaults **unconditionally**; `checkCache` stays gated on `Enabled`. Closes the top-level gap where an enabled hand-built cache with zero `port`/`poolsize` failed while koanf gives 6379/10 (per-tenant path already defaults). | koanf sets `cache.redis.*` while disabled; messaging materializes its defaults in every mode; and `normalize(zero)` must yield the owned set for #1023. Loosening, no atom. |
| 14 | Stack of three PRs from `main`, each behavior-identical (modulo #11): **PR1** `phases.go` drivers + app/server/scheduler/log/keystore/debug + tests 1–2 + `CONTEXT.md` + this spec; **PR2** multitenant + database root/named/manager + presence step + tests 3, 4, 6; **PR3** cache + messaging + tests 5, 7. | Section groups keep each PR self-contained; a by-phase split would leave `Validate` half-split mid-stack. |
| 15 | Execution: `task-pipeline` per PR, spec-driven. | Three bounded tasks; the review lens catches "which error wins" regressions. |

### Whole-Config test pins (`config/phases_test.go`)

1. Idempotency — `Validate` ×2, deep-equal.
2. Check purity — `check(c)` leaves `c` deep-equal (pins decision 3).
3. koanf door — delivered-empty error wins over "incomplete" for the same section.
4. Literal door — delivered-empty inert; "incomplete" surfaces.
5. `Multitenant.Enabled` true/false → manager / cache / messaging mode defaults; a disabled cache
   still carries redis defaults (decision 13).
6. Manager defaults on the root only; named/tenant untouched, and rejected if set.
7. Checks see normalized values — messaging `reconnect.maxdelay >= reconnect.delay` runs against filled
   defaults, not zero.

### Phase assignment per section (today's `validateX` → `normalizeX` / `checkX`)

| Section | normalize (mutates; may reject per decision 3 — a fill step also owns the "zero fills, negative rejects" rule for the keys it fills) | check (rejects only) |
| --- | --- | --- |
| presence | delivered-empty over every database section the deployment consumes (koanf door only) | — |
| app | startup timeout defaults | name/version required, env format, rate non-negative |
| server | — | port, timeouts (incl. middleware < write), gzip/bodylimit non-negative, trusted proxies, TLS material shape, FCC require-without-enabled |
| scheduler | timezone normalization | CIDR lists |
| multitenant | resolver header/domain defaults, limits default, tenant database sections (opaque), tenant cache defaults | resolver type/order/domain/path rules, limits cap, source type, delivered-but-empty static map, tenant ID rules, cross-tenant messaging consistency, tenant cache, single-tenant conflicts |
| database | root section (opaque), manager defaults (root only), named sections (opaque) | name-vs-tenant collisions, manager-outside-root (stays inside the opaque module) |
| log | — | level |
| cache | redis defaults (unconditional), manager defaults when enabled | type enumeration and redis field ranges when enabled |
| messaging | reconnect / publisher / streams offset-store defaults (negatives rejected by the fill) | `reconnect.maxdelay >= reconnect.delay`, streams URI scheme/host and single-tenant-only, address-resolver both-or-neither |
| keystore | — | secretminlength non-negative, entry shape |
| debug | — | trusted proxies |

## Decisions — #1023 (derived koanf defaults; after #1024 merges)

| # | Decision | Rationale |
| --- | -------- | --------- |
| 16 | Direction: Go-side constants in normalize are the truth; koanf derives. | Normalize must handle hand-built configs regardless (ADR-064). |
| 17 | **Narrow** form: `loadDefaults` = `koanfOnlyDefaults` (flip set + never-read set, explicit map) ∪ pick(flatten(normalize(zero)), `derivedDefaultKeys` allowlist). One mechanism *per key*, not one mechanism. No flips → no atoms. | Full derivation would undo ADR-064 within days and flip a fail-closed posture; closing #1023 would leave the owned set to drift by discipline. The allowlist grows deliberately as sections adopt normalize. |
| 18 | Tests: `koanfOnlyDefaults` ∩ `derivedDefaultKeys` = ∅; every allowlisted key present in the flatten; **mode-invariance** — allowlisted values equal for `normalize(zero{Multitenant:false})` and `normalize(zero{Multitenant:true})`. | Bars `*.manager.maxsize/idlettl` and `publisher.maxcached`: a koanf default of `10` would override multi-tenant's "zero = unlimited". |
| 19 | One PR; plan written after #1024 PR3 lands. | The owned set is not final until normalize is. |

## Assumptions carried

- Flatten = mapstructure struct→map (tags already present) + `koanf/maps.Flatten`; no new module.
  Durations rendered `.String()` so koanf getter types are unchanged (as #1021's pinning test does).
- Owned set at #1023 start: `app.startup.timeout`, `cache.redis.*`. `scheduler.timeout.*` and
  `server.bodylimit` join it only if their drift issues (below) move the fill into normalize first.
- Q16 pin (8) — named-database name colliding with a static tenant id — was proposed and dropped;
  the existing white-box test on `validateNamedDatabases` covers it.

## Follow-ups (filed as issues)

1. #1029 — `scheduler.timeout.*` never normalized in config; `slowjob` koanf 25s vs module 30s.
2. #1030 — `keystore.secretminlength` zero on a hand-built config silently disables the floor.
3. #1031 — `server.bodylimit` default applied in `server.SetupMiddlewares`, not config normalization.
