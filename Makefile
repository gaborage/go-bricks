.PHONY: all help build test test-integration test-all test-coverage test-coverage-integration test-coverage-combined coverage-report lint fmt update clean check docker-check

# Package selection for testing (excludes tools directories)
PKGS := $(shell go list ./... | grep -vE '/(tools)(/|$$)')
INTEGRATION_PKGS := ./database/mongodb/...
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
	@echo "Running unit tests with coverage..."
	@go test -race -covermode=atomic -coverprofile=coverage-unit.out $(PKGS)
	@echo "Running integration tests with coverage..."
	@go test -v -race -count=1 -tags=integration -covermode=atomic -coverprofile=coverage-integration.out $(INTEGRATION_PKGS)
	@echo "Generating combined coverage report..."
	@go test -tags=integration -coverprofile=coverage.out ./...
	@echo "\n=== Combined Coverage Summary ==="
	@go tool cover -func=coverage.out | tail -1
	@echo "=== Function Coverage Report (coverage.func) ==="
	@go tool cover -func=coverage.out > coverage.func
	@echo "Coverage reports generated: coverage.out, coverage.func"

coverage-report: ## Generate HTML coverage report from coverage.out
	@if [ ! -f coverage.out ]; then echo "Error: coverage.out not found. Run 'make test-coverage-combined' first."; exit 1; fi
	@go tool cover -html=coverage.out -o coverage.html
	@echo "HTML coverage report generated: coverage.html"

docker-check: ## Check if Docker is available
	@docker info >/dev/null 2>&1 || (echo "Error: Docker is not running. Integration tests require Docker Desktop or Docker daemon." && echo "Install Docker: https://www.docker.com/products/docker-desktop" && exit 1)

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format Go code
	go fmt ./...

update: ## Update dependencies to latest versions
	go get -u ./...
	go mod tidy

clean: ## Clean build cache
	go clean -cache -testcache

check: fmt lint test ## Run fmt, lint, and test (pre-commit checks)