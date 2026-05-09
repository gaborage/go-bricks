.PHONY: help build test lint fmt update clean install check test-coverage validate-cli dev-deps release-dry-run

BINARY_NAME := go-bricks-migrate
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

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

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format Go code
	go fmt ./...

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

check: fmt lint test validate-cli ## Run fmt, lint, test, and CLI validation (pre-commit checks)

dev-deps: ## Install development dependencies
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

release-dry-run: ## Test release build without publishing
	@echo "Testing release build for version: $(VERSION)"
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-linux-amd64 ./cmd/go-bricks-migrate
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-darwin-amd64 ./cmd/go-bricks-migrate
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-darwin-arm64 ./cmd/go-bricks-migrate
	GOOS=windows GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-windows-amd64.exe ./cmd/go-bricks-migrate
	@echo "✓ Release builds completed"
	@ls -la $(BINARY_NAME)-*
	@rm -f $(BINARY_NAME)-*
