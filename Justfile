# List available recipes
[private]
default:
    @just --list

# Build all packages
build:
    go build ./...

# Run tests
test:
    go test ./...

# Run tests with the race detector and coverage
test-race:
    go test -race -cover ./...

# Run go vet
vet:
    go vet ./...

# Run golangci-lint
lint:
    golangci-lint run ./...

# Format code
fmt:
    gofmt -w .

# Scan dependencies for known vulnerabilities
scan:
    grype dir:. --name aws-embedded-metrics-golang --fail-on medium
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Tidy go.mod and go.sum
tidy:
    go mod tidy

# Run everything CI runs
check: build vet lint test
