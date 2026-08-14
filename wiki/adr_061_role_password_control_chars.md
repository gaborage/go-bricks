# ADR-061: Redact Role Passwords Before the First-Line Split, and Reject Control Characters in Them

- **Status**: Accepted
- **Date**: 2026-08-14
- **Related**: [migrations.md](migrations.md) `[C59.5]` · `migration/roles.go`

## Context

`summarizeStmt` exists for one reason: to keep a resolved role password out of the error strings
`ProvisionPGRoles` wraps around a failing statement, because callers log those errors. It did the
two operations in the wrong order.

The pre-fix body split the statement at its first newline **before** applying the redaction regex:

```go
first := stmt
if idx := strings.IndexByte(stmt, '\n'); idx > 0 {
	first = stmt[:idx]
}
first = strings.TrimSpace(first)
first = pgPasswordLiteralPattern.ReplaceAllString(first, "${1}'[REDACTED]'")
```

The pattern is `(?i)(PASSWORD\s+)'(?:[^']|'')*'` — it is anchored on the **closing** quote. A
password containing a newline produces a multi-line `ALTER ROLE … PASSWORD '…` statement whose
first-line fragment ends mid-literal, with no closing quote in it. The pattern matches nothing, the
substitution is a no-op, and the fragment — including the first line of the secret — is interpolated
verbatim into the returned error.

The failing input shape is not exotic. A trailing `\n` is what a file-sourced or `echo`-piped secret
normally carries, and nothing upstream normalizes: `quotePGStringLiteral` doubles single quotes and
passes newlines straight through, and `PGRoleSpec` has no in-repo producer at all — it is a
consumer-facing API, so the password arrives from caller code and `Validate` is the only boundary
the framework owns.

Two properties of the existing code are worth stating so they are not mistaken for part of the bug.
The 80-character truncation already ran *after* redaction; only the newline split ran before it.
And Go's `regexp` is RE2, where a negated class `[^']` **does** match `\n` (only `.` excludes it
without `(?s)`) — so applying the same pattern to the whole multi-line statement matches and
redacts correctly, and no flag change is needed.

## Decision

**Redact first, then split and truncate.** `summarizeStmt` now runs
`pgPasswordLiteralPattern.ReplaceAllString` over the full `stmt` and derives the first line from the
already-redacted string. The reorder alone closes the leak for every password shape, because the
pattern can now always see its closing quote.

**And reject CR, LF, and NUL in `PGRoleSpec.MigratorPassword` / `RuntimePassword`** at `Validate`,
via a new exported sentinel `ErrPGRolePasswordHasControlChar`. `Validate` is called by both entry
points that build role statements (`ProvisionPGRoles` and `PGRoleProvisioningSQL`), and
`buildPGRoleStatements` is unexported and reachable only through those two, so the guard is live on
every path. An empty password stays valid — `strings.ContainsAny("", …)` is false, and an empty
password deliberately emits no `ALTER ROLE … PASSWORD` statement at all.

The error names the field and never the value. The check is placed after the existing
identifier and role-differ checks, so all current error precedence is preserved.

The reorder is the fix; the rejection is defence in depth. A newline-bearing password still round-
trips safely through `summarizeStmt` after the reorder, but it remains a value the provisioning
path cannot represent on one line, and rejecting it removes a whole class of future single-line
assumptions rather than relying on every downstream formatter to be as careful.

**PostgreSQL itself accepts such passwords.** The restriction is this API's, taken because the
provisioning path cannot carry them log-safely — not a claim about what the server permits.

## Alternatives considered

**Normalize instead of reject** — trim or escape the newline before building the statement.
Rejected: it silently changes a credential. A caller who genuinely intended a trailing newline
would provision a role whose password is not the value they passed, then authenticate against
something they never set, and the mismatch would surface far from here as an unexplained auth
failure. Rejection at the boundary is the honest outcome; `migration/secrets.go` decodes a
`config.DatabaseConfig` from a secret payload and is deliberately left alone for the same reason.

**Reuse `flyway.go`'s `validateEnvFields` / `ErrEnvFieldHasControlChar`.** Rejected: that guard
protects a different boundary — the environment handed to the Flyway subprocess — and its message
("env field") would misname a `PGRoleSpec` failure. The two guards share a four-line shape and a
deliberately identical character set (CR/LF/NUL, not "all control characters") so the boundaries
agree, but they are not folded into a shared helper: one is `config.DatabaseConfig`-shaped, the
other `PGRoleSpec`-shaped, and the abstraction would cost more than the duplication.

**Add a minimum-length gate, mirroring `redactPassword`.** Rejected, and worth recording so it is
not proposed again. `flyway.go`'s `redactPassword` redacts by *substring*, comparing output against
the secret's bytes, which false-positives on short passwords — hence its
`minRedactablePasswordLength` floor and `ErrDatabasePasswordTooShort`. `summarizeStmt`'s redaction
is **structural**: it matches the `PASSWORD '…'` clause by shape and never looks at the secret's
bytes, so a 3-character role password redacts exactly as safely as a 40-character one. Importing
that coupling would add a failure mode with no corresponding hazard.

## Consequences

**Positive.** The redaction now holds for the input shape secrets pipelines most commonly produce.
A newline-bearing password is refused at the API boundary with a message that names the field and
not the value.

**Negative.** This is breaking. A `PGRoleSpec` carrying a CR/LF/NUL password used to provision
successfully; `ProvisionPGRoles` and `PGRoleProvisioningSQL` now return an error instead. All three
symbols are exported, `go get`-consumed API. Callers sourcing a password from a file or a command
substitution must `strings.TrimSpace` it before handing it over. The new sentinel is itself additive
and apidiff-compatible — the rejection is what breaks.

**Operational.** This fix stops future leaks, not past ones. Any environment where a provisioning
failure was logged while a role password contained a newline should treat that credential as
disclosed and rotate it. `[C59.5]` in [migrations.md](migrations.md) carries the detection command
and the rotation guidance.

**Maintenance.** If a third password field is ever added to `PGRoleSpec`, the control-char loop in
`Validate` must gain it — the loop is the single place that enumerates them.

## References

- `migration/roles.go` — `summarizeStmt`, `PGRoleSpec.Validate`, `ErrPGRolePasswordHasControlChar`
- `migration/flyway.go` — `validateEnvFields` / `ErrEnvFieldHasControlChar`, the precedent this mirrors
- [migration_roles.md](migration_roles.md) — the migrator-vs-runtime role-separation model
- [migrations.md](migrations.md) `[C59.5]`
