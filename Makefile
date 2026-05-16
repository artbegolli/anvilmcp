.PHONY: help build run test clean lint fmt vet

help:
	@echo "anvilmcp - Anvil MCP Server"
	@echo ""
	@echo "Targets:"
	@echo "  build       - Build the binary"
	@echo "  run         - Run the server in development mode"
	@echo "  test        - Run tests"
	@echo "  coverage    - Run tests with coverage"
	@echo "  clean       - Clean build artifacts"
	@echo "  fmt         - Format code"
	@echo "  vet         - Run go vet"
	@echo "  lint        - Run golangci-lint (if installed)"

build:
	go build -o ./bin/anvilmcp ./cmd/anvilmcp

run: build
	ANVILMCP_DEV=1 ./bin/anvilmcp serve

test:
	go test -v -race ./...

coverage:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

clean:
	rm -rf ./bin coverage.out coverage.html

fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	@which golangci-lint > /dev/null || (echo "golangci-lint not installed; skipping" && exit 0)
	golangci-lint run

deps:
	go mod tidy
	go mod download

.DEFAULT_GOAL := help
