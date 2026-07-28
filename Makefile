.PHONY: all help build test test-integration test-all test-coverage test-coverage-integration test-coverage-combined coverage-report lint fmt update clean check docker-check vuln sec mutate mutate-baseline release release-cli

# Package selection for testing (excludes tools directories)
PKGS := $(shell go list ./... | grep -vE '/(tools)(/|$$)')
INTEGRATION_PKGS :=
# Keep in sync with the other module's Makefile.
# renovate: datasource=go depName=golang.org/x/vuln
GOVULNCHECK_VERSION := v1.6.0
# Keep in sync with the other module's Makefile.
# renovate: datasource=go depName=github.com/securego/gosec/v2
GOSEC_VERSION := v2.28.0
# Keep in sync with the other module's Makefile and CI (ci-v2.yml golangci-lint-action version).
# renovate: datasource=go depName=github.com/golangci/golangci-lint/v2
GOLANGCI_LINT_VERSION := v2.12.2
# renovate: datasource=go depName=github.com/go-gremlins/gremlins
GREMLINS_VERSION := v0.5.1
GREMLINS_CMD := go run github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)
# Hosted runners are 4-vCPU/16GB; 2 workers halves peak memory vs the local default.
MUTATE_BASELINE_WORKERS ?= 2
# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*## "}; {printf "  %-18s %s\n", $$1, $$2}'

all: build test test-integration ## Build and test the project

build: ## Build the project
	go build ./...

test: ## Run unit tests only
	go test -race $(PKGS)

test-alloc: ## Enforce ADR-026 alloc-stability guards WITHOUT -race (the detector inflates testing.AllocsPerRun counts; see server/alloc_guard_*_test.go)
	go test ./server/ -run 'AllocsStable' -count=1

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

lint: ## Run golangci-lint (pinned + GOWORK=off, mirroring CI; LINT_CLEAN=1 wipes the result cache first)
	@if [ "$(LINT_CLEAN)" = "1" ]; then \
		GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) cache clean; \
	fi
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

# `sec` is not redundant with `lint`: golangci-lint's gosec runs under the
# common-false-positives preset, so classes like G304 are suppressed there and
# reported only by the standalone scanner CI runs. Leaving it out of `check` meant
# a clean local run could still fail the security-framework job.
check: fmt lint test test-alloc vuln sec ## Run fmt, lint, test, alloc guards, vuln scan, and gosec (pre-commit checks; mirrors CI)

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

mutate: ## Diff-scoped mutation gate: mutants on changed lines vs origin/main must die (see wiki/testing.md#mutation-gate)
	go run ./scripts/mutatediff -engine "$(GREMLINS_CMD)"

# One gremlins process per package: a single full-repo process with 4 workers
# exhausted a 4-vCPU/16GB hosted runner ~25 min in (runner shutdown signal).
# Per-package processes bound memory, let partial results survive an eviction,
# and the merge prefixes file_name with the package dir (gremlins emits
# basenames, which are ambiguous repo-wide). scripts/ excluded: gremlins
# misverdicts that nested package main (wiki/testing.md#mutation-gate).
mutate-baseline: ## Full-repo mutation baseline, one engine process per package (advisory; consumed by the nightly workflow)
	@rm -rf .gremlins-reports gremlins-report.json && mkdir -p .gremlins-reports
	@i=0; for dir in $$(go list -f '{{.Dir}}' ./... | sed -e "s|^$$(pwd)/||" -e "s|^$$(pwd)$$|.|" | grep -v '^scripts/' | sort -u); do \
		i=$$((i+1)); \
		echo "== mutating ./$$dir"; \
		out=".gremlins-reports/$$i-$$(echo "$$dir" | tr / -).json"; \
		$(GREMLINS_CMD) unleash --workers $(MUTATE_BASELINE_WORKERS) --output "$$out" "./$$dir" \
			|| echo "WARN: gremlins exited non-zero for ./$$dir (advisory)"; \
		if [ -f "$$out" ]; then \
			jq --arg d "$$dir" '.files = ((.files // []) | map(.file_name = ($$d + "/" + .file_name)))' "$$out" > "$$out.tmp" && mv "$$out.tmp" "$$out" \
				|| { echo "WARN: ./$$dir produced an unparsable report, dropped (advisory)"; rm -f "$$out" "$$out.tmp"; }; \
		fi; \
	done
	@if ls .gremlins-reports/*.json >/dev/null 2>&1; then \
		jq -s '(map(.mutants_killed // 0) | add) as $$k \
			| (map(.mutants_lived // 0) | add) as $$l \
			| (map(.mutants_not_covered // 0) | add) as $$n \
			| { \
				mutants_killed: $$k, \
				mutants_lived: $$l, \
				mutants_not_covered: $$n, \
				test_efficacy: (if ($$k + $$l) > 0 then ($$k * 100 / ($$k + $$l)) else 0 end), \
				mutations_coverage: (if ($$k + $$l + $$n) > 0 then (($$k + $$l) * 100 / ($$k + $$l + $$n)) else 0 end), \
				files: (map(.files // []) | add) \
			}' .gremlins-reports/*.json > gremlins-report.json; \
		jq -r '"baseline: killed=\(.mutants_killed) lived=\(.mutants_lived) not_covered=\(.mutants_not_covered) efficacy=\(.test_efficacy | floor)%"' gremlins-report.json; \
	else \
		echo "WARN: no package reports produced — skipping merge (advisory)"; \
	fi

release: ## Cut a signed release tag (usage: make release VERSION=v0.38.0). Run AFTER merging the release-please PR. Requires 1Password unlocked.
	@test -n "$(VERSION)" || { echo "Error: VERSION is required, e.g. 'make release VERSION=v0.38.0'"; exit 1; }
	@VERSION=$(VERSION) ./scripts/release.sh

release-cli: ## Cut a signed go-bricks-migrate CLI tag (usage: make release-cli VERSION=v0.53.0). Run AFTER the framework tag is on the module proxy. Requires 1Password unlocked.
	@test -n "$(VERSION)" || { echo "Error: VERSION is required, e.g. 'make release-cli VERSION=v0.53.0'"; exit 1; }
	@VERSION="$(VERSION)" ./scripts/release-cli.sh
