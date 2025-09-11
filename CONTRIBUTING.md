# Contributing to GoBricks

Thank you for your interest in contributing to GoBricks! This document provides guidelines for contributing to this enterprise-grade Go framework.

## Getting Started

1. Fork the repository
2. Clone your fork: `git clone https://github.com/your-username/go-bricks.git`
3. Install dependencies: `go mod download`
4. Run tests to ensure everything works: `make test`

## Development Workflow

### Prerequisites
- Go 1.25 or later
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
   This runs formatting, linting, and tests.

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

### Commit Messages

Use clear, descriptive commit messages:
- Start with a verb in imperative mood
- Keep the first line under 72 characters
- Add details in the body if needed

Examples:
```
feat: Add support for Oracle connection pooling

fix: Fix race condition in logger context handling

docs: Update README with new configuration options
```

## Submitting Changes

1. Push your branch to your fork
2. Create a pull request with:
   - Clear description of what the change does
   - Reference any related issues
   - Include test results if applicable

3. Ensure all CI checks pass
4. Address any review feedback

## Reporting Issues

- Use GitHub Issues to report bugs or suggest features
- Include Go version, OS, and relevant configuration
- Provide minimal reproduction steps for bugs
- Search existing issues before creating new ones

## Build Commands

The project uses a Makefile for common tasks:

```bash
make help          # Show available commands
make build         # Build the project
make test          # Run all tests
make test-coverage # Run tests with coverage
make lint          # Run golangci-lint
make fmt           # Format Go code
make tidy          # Tidy Go modules
make clean         # Clean build cache
make check         # Run fmt, lint, and test
```

## Module Development

When adding new modules:

1. Implement the `Module` interface
2. Handle dependencies via `ModuleDeps`
3. Register routes and messaging handlers appropriately
4. Include proper shutdown logic
5. Add comprehensive tests

## Questions?

- Check existing documentation in README.md and CLAUDE.md
- Review existing code for patterns and conventions
- Open an issue for questions about architecture or design decisions

Thank you for contributing to GoBricks!