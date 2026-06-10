# ADR-028: PostgreSQL `BuildUpsert` Binds Update Values (Parity With Oracle MERGE)

**Status:** Accepted
**Date:** 2026-06-10

## Context

`QueryBuilder.BuildUpsert(table, conflictColumns, insertColumns, updateColumns)` takes the insert values and the on-conflict update values as **two separate maps**. Oracle's `MERGE` implementation honors both: the `USING` clause binds the insert values and `WHEN MATCHED THEN UPDATE SET col = :N` binds the update values as separate parameters.

The PostgreSQL implementation did not. It generated:

```sql
ON CONFLICT (...) DO UPDATE SET "col" = EXCLUDED."col"
```

`EXCLUDED` is the row *proposed for insertion*, so the on-conflict update always reused the **insert** value and **silently ignored the caller's `updateColumns` values** (a High finding from the 2026-06-10 audit). Two concrete failures:

1. **Wrong data on conflict.** Insert `{count: 1}` with on-conflict update `{count: 5}` updated the row to `1`, not `5` — a silent data-integrity divergence from the same call on Oracle.
2. **Broken update-only columns.** An update column absent from the insert set produced `EXCLUDED."col"`, which references a column not in the inserted row — a runtime SQL error.

The divergence was silent: existing tests asserted only on `ON CONFLICT`/`DO UPDATE SET` substrings (or codified the `EXCLUDED` output as "expected"), never that the update values were honored.

## Decision

`buildPostgreSQLUpsert` now binds the `updateColumns` values as parameters, numbering the update placeholders after the insert placeholders:

```sql
INSERT INTO t ("id","name") VALUES ($1,$2)
ON CONFLICT ("id") DO UPDATE SET "name" = $3
```

The update values are appended to the args slice after the insert values. The `DO NOTHING` path (empty `updateColumns`) is unchanged.

## Consequences

**Breaking (intended):**

- The generated SQL changes: `DO UPDATE SET col = EXCLUDED.col` → `DO UPDATE SET col = $N`. Callers asserting on the exact SQL string must update their expectations.
- The runtime behavior changes **only** when `updateColumns` values differ from the corresponding `insertColumns` values (or when an update column is absent from the insert set): those calls now apply the caller's intended update value (correct), where they previously applied the insert value or errored.

**Non-breaking:**

- The common case — `updateColumns` carrying the same values as `insertColumns` — produces the same row result as before (the bound value equals the EXCLUDED value), only via a parameter instead of `EXCLUDED`.
- Oracle behavior is unchanged; PostgreSQL now matches it.

See [migrations.md](migrations.md) for the upgrade note.
