# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- **`CONTEXT.md`** at the repo root — the glossary. It does not exist yet; `/domain-modeling` creates it lazily
  when a term actually gets resolved. It is allowlisted in `.gitignore` (`!CONTEXT.md`), so it will be tracked
  the moment it appears.
- **`wiki/architecture_decisions.md`** — the ADR index. Skim it for decisions touching the area you're about to
  work in, then read the linked `wiki/adr_NNN_<slug>.md` file(s).
- **`wiki/<topic>.md`** — deep dives per subsystem (`database.md`, `messaging.md`, `outbox.md`, …). CLAUDE.md's
  Quick Reference lists them; read the one for the package you're touching.

If `CONTEXT.md` doesn't exist, **proceed silently**. Don't flag its absence; don't suggest creating it upfront.

## File structure

Single-context repo. ADRs live in `wiki/`, **not** `docs/adr/` — never create `docs/adr/`.

```text
/
├── CONTEXT.md                        ← glossary (lazily created)
├── wiki/
│   ├── architecture_decisions.md     ← ADR index; every ADR has an entry here
│   ├── adr_001_enhanced_handler_system.md
│   ├── …
│   ├── adr_062_database_tls_fail_closed.md
│   ├── migrations.md                 ← breaking-change atoms, one per hop
│   └── <topic>.md                    ← subsystem deep dives
└── docs/agents/                      ← this file and its siblings
```

## Recording an ADR

When `/domain-modeling` (or any skill) records a decision, follow the existing convention exactly:

1. **File:** `wiki/adr_NNN_<snake_slug>.md`, `NNN` = next number after the highest existing one. In-flight PRs
   may already hold a number — check open PRs and cross-references, not just `ls`.
2. **Header:**

   ```markdown
   # ADR-NNN: <Title>

   **Status:** Accepted
   **Date:** YYYY-MM-DD

   ## Context
   ## Decision
   ## Consequences
   ```

3. **Index — mandatory:** add a `### [ADR-NNN: <Title>](adr_NNN_<slug>.md)` entry to
   `wiki/architecture_decisions.md` (date, status, one-paragraph summary, `**Key Benefits:**` line, `---`
   separator) **and** bump the `ADR-001 through ADR-NNN` counter at the foot of that file. File + index move
   together — an ADR without an index entry is a CodeRabbit finding.
4. **Breaking change?** Also add an atom to `wiki/migrations.md`, list it under CLAUDE.md `## Breaking Changes`,
   and use a `!` commit type (`fix(scope)!:`).

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, a test name), use
the term as defined in `CONTEXT.md`. Don't drift to synonyms the glossary explicitly avoids.

If the concept you need isn't in the glossary yet, that's a signal — either you're inventing language the project
doesn't use (reconsider) or there's a real gap (note it for `/domain-modeling`).

## Flag ADR conflicts

If your output contradicts an existing ADR, surface it explicitly rather than silently overriding:

> _Contradicts ADR-016 (session timezone UTC) — but worth reopening because…_
