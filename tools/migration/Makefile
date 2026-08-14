.PHONY: help build test lint fmt update clean install check test-coverage validate-cli dev-deps release-dry-run vuln sec verify-mod
# verify-mod mutates go.mod/go.sum via `go mod tidy` — under `make -j check`
# that would race lint/test reading the same module files. Force check's
# prerequisites to run serially regardless of -j.
.NOTPARALLEL: check

BINARY_NAME := go-bricks-migrate
VERSION := $(shell git describe --tags --match "tools/migration/v*" --always --dirty 2>/dev/null | sed "s|^tools/migration/||" | grep . || echo "dev")
# Keep in sync with the other module's Makefile.
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION := v1.6.0
# Keep in sync with the other module's Makefile.
# renovate: datasource=go depName=github.com/securego/gosec/v2
GOSEC_VERSION := v2.28.0
# Keep in sync with the other module's Makefile and CI (ci-v2.yml golangci-lint-action version).
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION := v2.12.2

help: ## Show this help message
	@echo "go-bricks-migrate tool commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

build: ## Build the CLI tool
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME) ./cmd/go-bricks-migrate

test: ## Run all tests
	go test -race ./cmd/... ./internal/...

test-coverage: ## Run tests with coverage
	go test -race -coverprofile=coverage.out ./cmd/... ./internal/...
	go tool cover -html=coverage.out -o coverage.html

lint: ## Run golangci-lint (pinned; identical to CI)
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

# Mirrors the root Makefile: golangci-lint resolves ../../.golangci.yml from
# here, so this module is held to the same formatters block (gofumpt + gci) and
# `go fmt` cannot fix those findings. See the root Makefile's `fmt` for the full
# rationale.
fmt: ## Format Go code (gofmt + gofumpt + gci, per the repo-root .golangci.yml)
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt

update: ## Update dependencies to latest versions
	go get -u ./...
	go mod tidy

clean: ## Clean build cache and binaries
	go clean -cache -testcache
	rm -f $(BINARY_NAME) coverage.out coverage.html

install: build ## Install the CLI tool locally
	go install -ldflags "-X main.version=$(VERSION)" ./cmd/go-bricks-migrate

validate-cli: build ## Validate CLI commands work correctly
	./$(BINARY_NAME) version
	./$(BINARY_NAME) --help
	@echo "✓ CLI validation passed"

check: fmt lint test validate-cli vuln sec verify-mod ## Run fmt, lint, test, CLI validation, vuln scan, gosec, and mod-tidy verification (pre-commit checks)

verify-mod: ## Verify go.mod/go.sum are tidy (mirrors CI)
	go mod tidy
	git diff --exit-code go.mod go.sum

dev-deps: ## Install development dependencies
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

# GOWORK=off for the reason spelled out on the root Makefile's `vuln` target:
# go.work is one directory up, and in workspace mode its `go` line — not this
# module's `toolchain` directive — picks the toolchain, so the scan silently
# reports an older stdlib than the CI job it is supposed to mirror. Package scope
# is unaffected (3 packages either way).
vuln: ## Run govulncheck vulnerability scan (pinned + GOWORK=off, mirroring CI)
	GOWORK=off go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

sec: ## Run gosec security scanner (pinned; identical to CI)
	go run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) ./cmd/... ./internal/...

release-dry-run: ## Test release build without publishing
	@echo "Testing release build for version: $(VERSION)"
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-linux-amd64 ./cmd/go-bricks-migrate
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-darwin-amd64 ./cmd/go-bricks-migrate
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-darwin-arm64 ./cmd/go-bricks-migrate
	GOOS=windows GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-windows-amd64.exe ./cmd/go-bricks-migrate
	@echo "✓ Release builds completed"
	@ls -la $(BINARY_NAME)-*
	@rm -f $(BINARY_NAME)-*
