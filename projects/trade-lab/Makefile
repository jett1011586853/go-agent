.PHONY: deps run test build clean

# Install dependencies
deps:
	go mod download
	go mod tidy

# Run development server
run:
	go run ./cmd/server

# Run tests
test:
	go test -v ./...

# Build binary
build:
	go build -o bin/trade-lab ./cmd/server

# Clean build artifacts
clean:
	rm -rf bin/

# Run with hot reload (requires air)
dev:
	air

# Format code
fmt:
	go fmt ./...

# Lint code (requires golangci-lint)
lint:
	golangci-lint run

# Generate test coverage
coverage:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
