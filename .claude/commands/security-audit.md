# Security Audit

Run a security audit of the current branch's changes in GoBricks; audit the whole codebase only when the user asks for a full audit.

## Scope

By default, audit the current branch's changes:

```bash
git fetch origin main --quiet 2>/dev/null || true
git diff --merge-base origin/main --stat
git diff --merge-base origin/main
git ls-files --others --exclude-standard
```

No diff shows an untracked file, so the last command lists them: read each one in full and treat every line as added. These commands read the working tree, while a push sends commits, so run `git status --short` too: when it lists a change that will not be part of the push, also audit the committed state, with `git diff --merge-base origin/main HEAD` for the diff and `git show HEAD:<path>` for a file's content. If the diffs and that list are all empty, fall back to `git diff HEAD` and `git diff --staged`. The repository-wide commands below still run over the whole tree, but in this scope each step reports only findings the diff introduces or exposes, cross-checking every hit against the diff. Such a finding can sit on an added or modified line, at a deletion (a removed check or guard), or in unchanged code that the change makes newly reachable. When the user asks for a full audit, report every finding in the codebase.

## Workflow

1. **Static analysis**: Run `golangci-lint run --enable gosec ./...` and report the gosec findings.
2. **Raw-SQL escape hatch audit**: Run `git grep --untracked -nE 'f\.Raw\(|jf\.Raw\(|database\.Raw\(|SetExpr\(|Having\(|MustExpr\(|[.]Expr\(|RawExpression\{|(^|[^A-Za-z0-9_])(Prefix|Suffix|Options)\('` and flag every call site (a SQL-body construction, for the `Expr` forms) without an annotation matching the prefix `// SECURITY: Manual SQL review completed - ` directly above it or above an enclosing dispatch. A multi-line call is in scope when any of its argument lines changed. Squirrel's own `Expr` inside `database/internal/builder`, and the `Expr`/`MustExpr` doors themselves (`database/types`, the builder, `testing/mocks`), are plumbing — skip those hits, and the `Prefix`/`Suffix`/`Options` plumbing hits listed in wiki/database.md ("What is not an identifier door"). `database.Raw` deserves at least as much scrutiny as `f.Raw`/`jf.Raw`: it replaces the whole statement, bypassing the builder's identifier validation. The rule itself lives in root CLAUDE.md Security Guidelines.
3. **Recovered panic values**: Check every `recover()` whose result becomes a log line, an error, or a message against the panic-value rule in root CLAUDE.md Security Guidelines (ADR-081), which is the authority for the forbidden operands and the annotation form. `git grep --untracked -n 'SECURITY: panic value' -- '*.go'` lists the annotated sites; check that each rationale says why that value cannot carry a secret.
4. **Shutdown paths**: In `Shutdown`/`Close`/teardown code, look for panic and race classes: a double close, a send on a closed channel, use of a resource after it is closed, and state shared with in-flight work without synchronization.
5. **Secrets scan**: Search Go and config files for patterns that suggest hardcoded secrets (API keys, passwords, tokens, connection strings), and check that code does not put secrets in logs or error messages. Exclude `.example` files and test fixtures.
6. **Input validation**: Check HTTP handler request structs for `validate` tags. Flag handlers that accept user input without validation.
7. **Vulnerability check**: Run `govulncheck ./...`. In the default scope, report the findings that a changed dependency (`go.mod`/`go.sum`) introduces or that changed code reaches; list the remaining ones separately as pre-existing.
8. **Attribution**: For each finding, run `git blame` on the relevant lines to identify when and who introduced the issue. Skip `git blame` for an untracked file, where it fails, and report a finding there or on any line with no commit yet as uncommitted.
9. **Report**: In the default scope, report findings inline in your reply and leave `VULNERABILITIES.md` untouched, because it holds the last full-audit report. On a full audit, write findings to `VULNERABILITIES.md`. Both use the following structure:

```markdown
# Security Audit Report
Generated: {date}
Scope: {branch diff against origin/main | full codebase}

## Summary
- Critical: N | High: N | Medium: N | Low: N

## Findings

### [SEVERITY] Finding title
- **File**: path/to/file.go:line
- **Category**: {gosec|secrets|validation|vulnerability|sql-injection|panic-value|shutdown}
- **Introduced**: {commit hash} by {author} on {date}
- **Description**: What the issue is
- **Recommendation**: How to fix it
```

## Severity Classification

- **Critical**: Hardcoded secrets, SQL injection without sanitization, known CVEs with exploits
- **High**: Missing input validation on public endpoints, raw-SQL escape hatch (`f.Raw` / `jf.Raw` / `database.Raw` / `SetExpr` / string `Having` / INSERT `Prefix` / `Suffix` / `Options` / `RawExpression` body via `qb.Expr` / `qb.MustExpr`) without `// SECURITY:` annotation, a recovered panic value rendered by value without a `// SECURITY: panic value` annotation, a panic or race on a shutdown path
- **Medium**: Missing validation tags, outdated dependencies with vulnerabilities
- **Low**: Informational findings, minor gosec warnings
