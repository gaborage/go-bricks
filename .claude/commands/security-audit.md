# Security Audit

Run a comprehensive security audit of the GoBricks codebase.

## Workflow

1. **Static analysis**: Run `golangci-lint run --enable gosec ./...` to find security issues.
2. **WhereRaw audit**: Search for all `WhereRaw()` usages and verify each has the required security annotation comment: `// SECURITY: Manual SQL review completed - identifier quoting verified`. Flag any missing annotations.
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
- **High**: Missing input validation on public endpoints, unsafe WhereRaw without annotation
- **Medium**: Missing validation tags, outdated dependencies with vulnerabilities
- **Low**: Informational findings, minor gosec warnings
