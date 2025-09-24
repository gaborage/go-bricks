.PHONY: help build test lint fmt tidy clean

# Default target
help: ## Show this help message
	@echo "Available targets:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the project
	go build ./...

test: ## Run all tests
	go test -race $(shell go list ./... | grep -vE '/(examples|testing|tools)(/|$$)')

test-coverage: ## Run tests with coverage
	go test -race -cover -covermode=atomic -coverprofile=coverage.out $(PKGS)

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format Go code
	go fmt ./...

tidy: ## Tidy Go modules
	go mod tidy

clean: ## Clean build cache
	go clean -cache -testcache

check: fmt lint test ## Run fmt, lint, and test (pre-commit checks)