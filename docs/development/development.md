# Development Setup

## Prerequisites
- Go 1.21+
- Make
- Git

## Setup
```bash
git clone https://github.com/ManicDevs/hivemind
cd hivemind
make build-all
```

## Running Tests
```bash
# All tests
make test

# Specific package
go test ./internal/hivemind/ -v

# With race detector (requires gcc)
# go test -race ./...
```

## Building
```bash
# Development build
make build-all

# Release build (with anti-RE hardening)
make build-release
```

## Code Style
```bash
# Format
gofmt -w cmd/ internal/

# Lint
go vet ./...
golangci-lint run
```