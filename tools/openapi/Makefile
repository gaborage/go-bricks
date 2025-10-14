.PHONY: help build test lint fmt update clean install demo check update test-coverage validate-cli dev-deps release-dry-run

# Binary name
BINARY_NAME := go-bricks-openapi
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")

# Default target
help: ## Show this help message
	@echo "go-bricks-openapi tool commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

build: ## Build the CLI tool
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME) ./cmd/go-bricks-openapi

test: ## Run all tests
	go test -race ./cmd/... ./internal/commands/... ./internal/generator/... ./internal/analyzer/...

test-coverage: ## Run tests with coverage
	go test -race -coverprofile=coverage.out ./cmd/... ./internal/commands/... ./internal/generator/... ./internal/analyzer/...
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
	go install -ldflags "-X main.version=$(VERSION)" ./cmd/go-bricks-openapi

demo: build ## Run demo generation on demo project (requires cloning demo repo)
	./$(BINARY_NAME) doctor --project ../../
	@echo "To run demo generation:"
	@echo "1. Clone demo project: git clone https://github.com/gaborage/go-bricks-demo-project.git"
	@echo "2. Generate spec: ./$(BINARY_NAME) generate --project ../go-bricks-demo-project/openapi-demo --output demo-spec.yaml"

validate-cli: build ## Validate CLI commands work correctly
	./$(BINARY_NAME) version
	./$(BINARY_NAME) --help
	./$(BINARY_NAME) doctor --project ../../
	@echo "✓ CLI validation passed"

check: fmt lint test validate-cli ## Run fmt, lint, test, and CLI validation (pre-commit checks)

# Development helpers
dev-deps: ## Install development dependencies
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# Release helpers
release-dry-run: ## Test release build without publishing
	@echo "Testing release build for version: $(VERSION)"
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-linux-amd64 ./cmd/go-bricks-openapi
	GOOS=darwin GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-darwin-amd64 ./cmd/go-bricks-openapi
	GOOS=darwin GOARCH=arm64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-darwin-arm64 ./cmd/go-bricks-openapi
	GOOS=windows GOARCH=amd64 go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY_NAME)-windows-amd64.exe ./cmd/go-bricks-openapi
	@echo "✓ Release builds completed"
	@ls -la $(BINARY_NAME)-*
	@rm -f $(BINARY_NAME)-*