.PHONY: all help build test test-integration test-all test-coverage test-coverage-integration test-coverage-combined coverage-report lint lint-md fmt update clean check docker-check vuln sec verify-mod mutate mutate-baseline release release-cli
# verify-mod mutates go.mod/go.sum/go.work.sum via `go mod tidy` — under `make
# -j check` that would race lint/test reading the same module files. Force
# check's prerequisites to run serially regardless of -j.
.NOTPARALLEL: check

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
# renovate: datasource=npm depName=markdownlint-cli2
MARKDOWNLINT_VERSION := 0.23.2
GREMLINS_CMD := go run github.com/go-gremlins/gremlins/cmd/gremlins@$(GREMLINS_VERSION)
# Hosted runners are 4-vCPU/16GB; 2 workers bounds peak memory (each worker keeps its
# own copy of the module tree).
MUTATE_BASELINE_WORKERS ?= 2
# Used only when the per-package coefficient cannot be computed. Generous on
# purpose: too small silently reports every mutant as TIMED OUT, which the
# advisory baseline would publish as a clean score (wiki/testing.md#timeout-ceiling).
MUTATE_FALLBACK_COEFFICIENT ?= 600
# `make mutate` runs on a developer's machine and holds the CPU busy for the whole
# run, which is what heat-soaks a laptop. MUTATE_WORKERS alone never bounded that:
# each worker shells out a `go test`, which compiles at `-p=GOMAXPROCS` and runs its
# binary at GOMAXPROCS, both defaulting to the machine's core count — so 2 workers
# admit far more than 2 cores' worth of work.
#
# MUTATE_CPU is the cap on test execution, which is where the sustained load is;
# build phases are a best-effort target, not a hard bound. mutatediff divides it
# by MUTATE_WORKERS and pins
# GOMAXPROCS plus GOFLAGS -p on every child process, which bounds test execution
# exactly and build fan-out approximately (compile processes nest one level, and
# at the defaults the overshoot can reach roughly 2x the budget during build
# phases, growing with MUTATE_CPU).
# Set MUTATE_CPU=0 to opt out and run at full speed.
MUTATE_CPU ?= 4
MUTATE_WORKERS ?= 2
# Pause after each mutated package so the chassis sheds heat before the next one.
# Any time.ParseDuration string; 0 disables. It does nothing inside a single long
# package — for those, speeding up the slowest tests is the lever.
MUTATE_COOLDOWN ?= 30s
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

# No globs on the command line: .markdownlint-cli2.jsonc owns both `globs` and
# `ignores`, so the file set has one definition that this target, CI, and an
# editor plugin all read. (A command-line glob would be ADDED to the configured
# ones, not substituted for them, and the ignores keep applying either way —
# it simply gives the file set a second home.)
# npx resolves and caches by package spec, so the version pin is the whole
# reproducibility story: `markdownlint-cli2` unpinned would silently follow upstream
# and a rule added in a later release would fail CI on a tree that lints clean here.
lint-md: ## Run markdownlint-cli2 on Markdown files (pinned; globs and ignores come from .markdownlint-cli2.jsonc)
	npx --yes markdownlint-cli2@$(MARKDOWNLINT_VERSION)

# `golangci-lint fmt`, not `go fmt`: .golangci.yml declares a formatters block
# (gofumpt + gci) and `golangci-lint run` reports its output as ordinary issues
# ("File is not properly formatted (gci)"). `go fmt` cannot fix either one, so
# with it here `make check` — which is `fmt lint ...` — would reformat and then
# fail lint anyway. Same pinned binary as the `lint` target so both agree on the
# rules. Note the two subcommands cover different file sets: `run` is
# package-scoped and never loads the //go:build integration files, while `fmt`
# walks the tree and does reach them (5 of them needed reformatting at adoption).
fmt: ## Format Go code (gofmt + gofumpt + gci, per .golangci.yml's formatters block)
	GOWORK=off go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) fmt

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
check: fmt lint lint-md test test-alloc vuln sec verify-mod ## Run fmt, lint, markdownlint, test, alloc guards, vuln scan, gosec, and mod-tidy verification (pre-commit checks; mirrors CI)

verify-mod: ## Verify go.mod/go.sum are tidy and go.work.sum is settled (mirrors CI)
	go mod tidy
	git diff --exit-code go.mod go.sum go.work.sum

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
	go run ./scripts/mutatediff -engine "$(GREMLINS_CMD)" -workers "$(MUTATE_WORKERS)" -cpu "$(MUTATE_CPU)" -cooldown "$(MUTATE_COOLDOWN)"

# One gremlins process per package: a single full-repo process with 4 workers
# exhausted a 4-vCPU/16GB hosted runner ~25 min in (runner shutdown signal).
# Per-package processes bound memory, let partial results survive an eviction,
# and the merge prefixes file_name with the package dir (gremlins emits paths
# relative to the package it was pointed at, which are ambiguous repo-wide).
# scripts/ excluded: gremlins misverdicts that nested package main
# (wiki/testing.md#mutation-gate).
#
# The per-package timeout coefficient is not optional: gremlins derives each
# mutant's ceiling from a CACHE-SERVED replay of the package's tests, while every
# mutant run is a cache miss that pays the real suite. Left at the default the
# ceiling lands under what one mutant costs and every mutant reports TIMED OUT —
# which this job would publish as a clean score. See scripts/mutatediff/timeout.go.
mutate-baseline: ## Full-repo mutation baseline, one engine process per package (advisory; consumed by the nightly workflow)
	@rm -rf .gremlins-reports gremlins-report.json && mkdir -p .gremlins-reports
	@go list ./... > /dev/null   # fail fast on a broken tree — inside the loop pipeline a go list failure would vanish into sort's exit status
	@i=0; for dir in $$(go list -f '{{.Dir}}' ./... | sed -e "s|^$$(pwd)/||" -e "s|^$$(pwd)$$|.|" | grep -v '^scripts/' | sort -u); do \
		i=$$((i+1)); \
		echo "== mutating ./$$dir"; \
		out=".gremlins-reports/$$i-$$(echo "$$dir" | tr / -).json"; \
		coeff=$$(go run ./scripts/mutatediff -coefficient "./$$dir") \
			|| { echo "ERROR: coefficient measurement for ./$$dir was canceled or could not run — stopping the baseline"; exit 1; }; \
		case "$$coeff" in ''|*[!0-9]*) echo "WARN: no coefficient for ./$$dir, falling back to $(MUTATE_FALLBACK_COEFFICIENT)"; coeff=$(MUTATE_FALLBACK_COEFFICIENT);; esac; \
		$(GREMLINS_CMD) unleash --workers $(MUTATE_BASELINE_WORKERS) --timeout-coefficient "$$coeff" --output "$$out" "./$$dir" \
			|| echo "WARN: gremlins exited non-zero for ./$$dir (advisory)"; \
		if [ -f "$$out" ]; then \
			jq --arg d "$$dir" '.files = ((.files // []) | map(.file_name = ($$d + "/" + .file_name)))' "$$out" > "$$out.tmp" && mv "$$out.tmp" "$$out" \
				|| { echo "WARN: ./$$dir produced an unparsable report, dropped (advisory)"; rm -f "$$out" "$$out.tmp"; }; \
		fi; \
	done
# The merge lives in Go (scripts/mutatediff -merge), not inline jq: make's
# backslash continuations land literal backslashes inside a single-quoted jq
# program, which cost the first sharded run its entire report (jq compile
# error swallowed by the advisory guard, 0-byte artifact uploaded).
	@go run ./scripts/mutatediff -merge .gremlins-reports -out gremlins-report.json

release: ## Cut a signed release tag (usage: make release VERSION=v0.38.0). Run AFTER merging the release-please PR. Requires 1Password unlocked.
	@test -n "$(VERSION)" || { echo "Error: VERSION is required, e.g. 'make release VERSION=v0.38.0'"; exit 1; }
	@VERSION=$(VERSION) ./scripts/release.sh

release-cli: ## Cut a signed go-bricks-migrate CLI tag (usage: make release-cli VERSION=v0.53.0). Run AFTER the framework tag is on the module proxy. Requires 1Password unlocked.
	@test -n "$(VERSION)" || { echo "Error: VERSION is required, e.g. 'make release-cli VERSION=v0.53.0'"; exit 1; }
	@VERSION="$(VERSION)" ./scripts/release-cli.sh
