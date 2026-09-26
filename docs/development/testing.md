# Testing

## Running Tests
```bash
# All tests
make test

# Specific package
go test ./internal/hivemind/ -v -run TestIdentity

# With verbose output
go test ./... -v
```

## Test Categories
- Unit tests: `*_test.go` files
- Integration tests: `*_test.go` with network
- Benchmark tests: `*_test.go` with Benchmark prefix

## Test Targets
```bash
# All tests
make test

# Specific packages
go test ./cmd/... -count=1
go test ./internal/... -count=1
```