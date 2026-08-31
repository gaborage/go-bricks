# Shared panicking-logger test double (`logger/testing`)

**Decision:** Deferred (YAGNI) — the three per-package copies stay; no shared
double or `logger/testing` package until a trigger below fires.

**Reason:** ADR-081's work left three near-identical test doubles whose only
job is to prove a panic-report call is guarded by its own `recover()`: a
`logger.Logger` whose event flips a `reporting` flag when
`Str("panic_type", …)` is called, then panics in `Msg()` iff the flag is set.
They live in the migration audit-emitter tests, the scheduler module tests,
and the messaging delivery tests.

Extraction was considered and deliberately rejected twice (in the PR that
introduced them, and again at triage of #1139):

- The repo already carries ~18 hand-rolled fake loggers, one per test file.
  A per-package double is the established pattern, not an aberration.
- Extracting one means a new **exported** `logger/testing` package — real API
  surface growth to solve a three-copy duplication.
- The copies are not byte-identical (one embeds `logger.LogEvent`, one
  implements the full interface explicitly), so a shared version is a small
  design exercise, not a mechanical move.

**Aiming constraint for any future shared version:** the double must panic on
**one** keyed call (`Str("panic_type", …)` then `Msg()`), never on every call.
A double that panics on everything silently tests a different surface — it
proves the guard contains *any* logging panic, not that the *report path
specifically* is guarded. The aiming comment in the migration audit-emitter
test is the copy worth preserving. Note `server`'s `panickingLogger` (panics
in `Error()` itself) is intentionally different: it tests the outermost
recover, and is **not** a fourth copy of this double.

**Triggers to revisit:**

- A fourth true copy of the keyed double appears.
- `logger/testing` is created for another reason.

## Prior requests

- #1139 — "messaging: three copies of the panicking-logger test double"
