.PHONY: help build test lint fmt update clean check test-coverage

# Package selection for testing (excludes tools directories)
PKGS := $(shell go list ./... | grep -vE '/(tools)(/|$$)')

# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the project
	go build ./...

test: ## Run all tests
	go test -race $(PKGS)

test-coverage: ## Run tests with coverage
	go test -race -cover -covermode=atomic -coverprofile=coverage.out $(PKGS)

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