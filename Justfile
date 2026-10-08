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

# Fuzz the EMF output against the AWS spec
fuzz time="1m":
    go test ./emf -run '^$' -fuzz FuzzSpecCompliance -fuzztime {{time}} -fuzzminimizetime 100x

# Run benchmarks
bench count="6":
    go test ./emf -run '^$' -bench . -benchtime 200ms -count {{count}}

# Compare benchmarks with a git ref, and fail on a regression
bench-compare ref="main" rounds="10":
    go tool benchcompare -base {{ref}} -rounds {{rounds}}

# Run go vet
vet:
    go vet ./...

# Run golangci-lint
lint:
    golangci-lint run ./...

# Format code
fmt:
    go fmt ./...

# Scan dependencies for known vulnerabilities
scan:
    grype dir:. --name aws-embedded-metrics-golang --fail-on medium
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Tidy go.mod and go.sum
tidy:
    go mod tidy

# Run everything CI runs
check: build vet lint test
