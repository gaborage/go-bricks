# Contributing to GoBricks

Thank you for your interest in contributing to GoBricks! This document provides guidelines for contributing to this enterprise-grade Go framework.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/your-username/go-bricks.git`
3. Install dependencies: `go mod download`
4. Run tests to ensure everything works: `make test`

## Development Workflow

### Prerequisites
- Go 1.26 or later
- golangci-lint for linting
- Make for build automation

### Making Changes

1. Create a new branch for your feature/fix:
   ```bash
   git checkout -b feature/your-feature-name
   ```

2. Make your changes following the project conventions
3. Run pre-commit checks:
   ```bash
   make check
   ```
   This runs formatting, linting, tests, alloc-stability guards, and a vulnerability scan.

### Code Quality Standards

- **Formatting**: Use `make fmt` to format code with `go fmt`
- **Linting**: Code must pass `make lint` (golangci-lint)
- **Testing**: Add tests for new functionality and ensure `make test` passes
- **Security**: Code must pass `gosec` security checks
- **Coverage**: Aim for good test coverage on new code

### Project Structure

- `app/`: Application framework and module system
- `config/`: Configuration management
- `database/`: Multi-database interface (PostgreSQL, Oracle)
- `logger/`: Structured logging
- `messaging/`: AMQP messaging client
- `server/`: HTTP server with middleware
- `migration/`: Database migration support

### Coding Conventions

- Follow standard Go conventions and idioms
- Use meaningful variable and function names
- Add appropriate documentation for public APIs
- Handle errors properly - never ignore returned errors
- Use the existing interfaces and patterns in the codebase

### Testing

- Write unit tests for new functionality
- Place tests in `*_test.go` files alongside source code
- Use testify for assertions and mocking
- Run specific package tests: `go test ./package-name`
- Check coverage: `make test-coverage`

### Commit messages, changelog & breaking changes

PRs are **squash-merged**, so the **PR title becomes the commit** — and the PR title is the
source of truth for releases. It MUST be a Conventional Commit:

- `feat(scope): ...` → MINOR · `fix(scope): ...` → PATCH · `refactor:`/`perf:`/`docs:`/`chore:`/`test:`/`build:`/`ci:`
- **Breaking changes MUST use `feat!:` / `fix!:` or a `BREAKING CHANGE:` footer.** This is the
  primary, enforced signal: `release-please` reads it to compute the bump and emit the
  `### ⚠ BREAKING CHANGES` banner. A new breaking change also requires a new ADR (file **and**
  index entry in `wiki/architecture_decisions.md`) and a `wiki/migrations.md` row.

You do **not** edit `CHANGELOG.md` by hand — `release-please` generates it from PR titles.
See [RELEASING.md](RELEASING.md).

## Submitting Changes

1. Push your branch to your fork
2. Create a pull request with:
   - Clear description of what the change does
   - Reference any related issues
   - Include test results if applicable

3. Ensure all CI checks pass
4. Address any review feedback

## Reporting Issues

GoBricks uses [GitHub Issues](https://github.com/gaborage/go-bricks/issues) with structured templates. When you open a new issue, pick the template that fits and the form will guide you through the required fields:

- **Bug report** — something is broken or behaves incorrectly.
- **Enhancement** — internal framework improvement, refactor, or feature where you already know what should change.
- **Idea / feature request** — exploratory direction, external feature request, or anything where the path forward is unclear.

### Title format

Use `<package>: <verb-led summary>`. The `<package>` part should match a directory at the repo root (`scheduler`, `database`, `jose`, `messaging`, `cache`, ...).

Examples:

- `scheduler: extract setup helper for tracer-enabled tests`
- `jose: wire Header.Typ into observability or drop`
- `database: support multi-database transactions`

Maintainers will add `area/<package>` and `kind/*` labels during triage.

### No static priority

**Do not** prefix titles with `[P1]`/`[P2]`/`[P3]` or similar. Priority is **derived from usage signals** during triage — 👍 reactions on the issue body, references from PRs and other issues, demand from the [demo project](https://github.com/gaborage/go-bricks-demo-project) and external users — not assigned at filing time. Static priorities go stale; signal-based ranking does not.

### Search before filing

Search both **open and closed** issues before opening a new one. If you find a duplicate, comment on the existing issue (a fresh comment with new context bumps it back into view) instead of opening another.

## Build Commands

The project uses a Makefile for common tasks:

```bash
make help          # Show available commands
make build         # Build the project
make test          # Run unit tests only
make test-coverage # Run tests with coverage
make lint          # Run golangci-lint
make fmt           # Format Go code
make update        # Update deps and tidy modules
make clean         # Clean build cache
make check         # Run fmt, lint, test, alloc guards, and vuln scan
```

## Module Development

When adding new modules:

1. Implement the `Module` interface
2. Handle dependencies via `ModuleDeps`
3. Register routes and messaging handlers appropriately
4. Include proper shutdown logic
5. Add comprehensive tests

## Questions?

- Check existing documentation in README.md
- Review existing code for patterns and conventions
- Open an issue for questions about architecture or design decisions

Thank you for contributing to GoBricks!