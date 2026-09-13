# Security Audit

Run a comprehensive security audit of the GoBricks codebase.

## Workflow

1. **Static analysis**: Run `golangci-lint run --enable gosec ./...` to find security issues.
2. **Raw-SQL escape hatch audit**: Run `git grep -nE 'f\.Raw\(|jf\.Raw\(|database\.Raw\(|SetExpr\(|Having\(|MustExpr\(|[.]Expr\(|RawExpression\{'` and flag every call site (a SQL-body construction, for the `Expr` forms) without an annotation matching the prefix `// SECURITY: Manual SQL review completed - ` directly above it or above an enclosing dispatch. Squirrel's own `Expr` inside `database/internal/builder` is plumbing — skip those hits. `database.Raw` deserves at least as much scrutiny as `f.Raw`/`jf.Raw`: it replaces the whole statement, bypassing the builder's identifier validation. The rule itself lives in root CLAUDE.md Security Guidelines.
3. **Secrets scan**: Search for patterns that suggest hardcoded secrets (API keys, passwords, tokens, connection strings) in Go files and config files. Exclude `.example` files and test fixtures.
4. **Input validation**: Check all HTTP handler request structs for `validate` tags. Flag handlers that accept user input without validation.
5. **Vulnerability check**: Run `govulncheck ./...` to find known vulnerabilities in dependencies.
6. **Attribution**: For each finding, run `git blame` on the relevant lines to identify when and who introduced the issue.
7. **Report**: Write findings to `VULNERABILITIES.md` with the following structure:

```markdown
# Security Audit Report
Generated: {date}

## Summary
- Critical: N | High: N | Medium: N | Low: N

## Findings

### [SEVERITY] Finding title
- **File**: path/to/file.go:line
- **Category**: {gosec|secrets|validation|vulnerability|sql-injection}
- **Introduced**: {commit hash} by {author} on {date}
- **Description**: What the issue is
- **Recommendation**: How to fix it
```

## Severity Classification

- **Critical**: Hardcoded secrets, SQL injection without sanitization, known CVEs with exploits
- **High**: Missing input validation on public endpoints, raw-SQL escape hatch (`f.Raw` / `jf.Raw` / `database.Raw`) without `// SECURITY:` annotation
- **Medium**: Missing validation tags, outdated dependencies with vulnerabilities
- **Low**: Informational findings, minor gosec warnings
