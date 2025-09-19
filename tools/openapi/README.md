# Go-Bricks OpenAPI Generator

Static analysis-based OpenAPI 3.0.1 specification generator for go-bricks services.

## Quick Start

```bash
# Install the tool
go install ./tools/openapi/cmd/go-bricks-openapi

# Generate OpenAPI spec
go-bricks-openapi generate -project . -output docs/openapi.yaml

# Check tool health and compatibility
go-bricks-openapi doctor -project .
```

## Commands

- `generate` - Generate OpenAPI specification from go-bricks service
- `doctor` - Check compatibility and environment health
- `version` - Show tool version

## Requirements

- Go 1.21+
- go-bricks v1.2.0+ (when available)