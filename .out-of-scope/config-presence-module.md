# Config presence module

**Decision:** Deferred (YAGNI) — not built until a trigger below fires.

**Reason:** "Was this section delivered?" is answered two ways today: koanf
key presence (`databaseIdentityKeys`, consulted by the delivered-empty check)
and decoded-value emptiness (`IsDatabaseConfigured`). A single presence seam
that answers from whichever backing exists would move the hand-built /
dynamic-tenant blind spot from a comment into a code path — but a seam with
one adapter and one consumer is an abstraction without a second point to
justify it. The two answers stay pinned together by a test until then
(ADR-047, ADR-051 mechanics).

**Reopen when either fires:**

1. A second consumer needs the presence answer — a non-database section that
   must distinguish "delivered empty" from "absent".
2. The hand-built / dynamic-source blind spot causes a real incident — a
   section boots as absent when it was delivered empty.

If built, the natural home is the normalize phase from the `Validate`
normalize/check split.

**Prior requests:**

- [#1022](https://github.com/gaborage/go-bricks/issues/1022) — closed
  2026-08-16 (deferred, this entry)
