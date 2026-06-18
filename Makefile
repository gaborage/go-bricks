.PHONY: all help build test test-integration test-all test-coverage test-coverage-integration test-coverage-combined coverage-report lint fmt update clean check docker-check vuln sec release

# Package selection for testing (excludes tools directories)
PKGS := $(shell go list ./... | grep -vE '/(tools)(/|$$)')
INTEGRATION_PKGS :=
# Keep in sync with the other module's Makefile.
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION := v1.4.0
# Keep in sync with the other module's Makefile.
# renovate: datasource=go depName=github.com/securego/gosec/v2
GOSEC_VERSION := v2.27.1
# Keep in sync with the other module's Makefile and CI (ci-v2.yml golangci-lint-action version).
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION := v2.12.2
# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

all: build test test-integration ## Build and test the project

build: ## Build the project
	go build ./...

test: ## Run unit tests only
	go test -race $(PKGS)

test-integration: docker-check ## Run integration tests (requires Docker)
	@echo "Running integration tests with testcontainers..."
	go test -v -race -count=1 -tags=integration $(INTEGRATION_PKGS)

test-all: test test-integration ## Run all tests (unit + integration)

test-coverage: ## Run unit tests with coverage
	go test -race -cover -covermode=atomic -coverprofile=coverage.out $(PKGS)
	@go tool cover -func=coverage.out | tail -1

test-coverage-integration: docker-check ## Run integration tests with coverage (requires Docker)
	@echo "Running integration tests with coverage..."
	go test -v -race -count=1 -tags=integration -covermode=atomic -coverprofile=coverage-integration.out $(INTEGRATION_PKGS)
	@go tool cover -func=coverage-integration.out | tail -1

test-coverage-combined: docker-check ## Run combined unit and integration tests with coverage
	@echo "Running all tests (unit + integration) with coverage..."
	@go test -v -race -tags=integration -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	@echo ""
	@echo "=== Combined Coverage Summary ==="
	@go tool cover -func=coverage.out | tail -1
	@echo ""
	@echo "Generating function coverage report..."
	@go tool cover -func=coverage.out > coverage.func
	@echo "Coverage reports generated: coverage.out, coverage.func"
	@echo "Generate HTML report with: make coverage-report"

coverage-report: ## Generate HTML coverage report from coverage.out
	@if [ ! -f coverage.out ]; then echo "Error: coverage.out not found. Run 'make test-coverage-combined' first."; exit 1; fi
	@go tool cover -html=coverage.out -o coverage.html
	@echo "HTML coverage report generated: coverage.html"

docker-check: ## Check if Docker is available
	@docker info >/dev/null 2>&1 || (echo "Error: Docker is not running. Integration tests require Docker Desktop or Docker daemon." && echo "Install Docker: https://www.docker.com/products/docker-desktop" && exit 1)

lint: ## Run golangci-lint (pinned v2.12.2 + GOWORK=off, mirroring CI lint-framework's mechanism)
	GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) cache clean
	GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run --timeout=5m

fmt: ## Format Go code
	go fmt ./...

update: ## Update dependencies to latest versions
	go get -u ./...
	go mod tidy

clean: ## Clean build cache and test artifacts
	go clean -cache -testcache
	rm -f coverage.out coverage-integration.out coverage.html coverage.func
	rm -f *.test

check: fmt lint test vuln ## Run fmt, lint, test, and vuln scan (pre-commit checks)

vuln: ## Run govulncheck vulnerability scan (pinned; identical to CI)
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

sec: ## Run gosec security scanner (pinned; identical to CI)
	# gosec only accepts relative patterns — the previous $(PKGS) import paths
	# silently scanned 0 files (a no-op gate). This now scans ./... as a backstop to
	# golangci-lint's gosec (make lint), which is the fine-grained gate that honors the
	# codebase's //#nosec annotations. G103 (unsafe audit) and G104 (unchecked cleanup
	# Close errors) are excluded to match make lint's stance: the .golangci.yml
	# common-false-positives + std-error-handling presets already treat both classes as
	# non-issues, so gating them only here would diverge from the repo's gosec policy.
	go run github.com/securego/gosec/v2/cmd/gosec@$(GOSEC_VERSION) -exclude=G103,G104 ./...

release: ## Cut a signed release tag (usage: make release VERSION=v0.38.0). Run AFTER merging the release-please PR. Requires 1Password unlocked.
	@test -n "$(VERSION)" || { echo "Error: VERSION is required, e.g. 'make release VERSION=v0.38.0'"; exit 1; }
	@VERSION=$(VERSION) ./scripts/release.sh
