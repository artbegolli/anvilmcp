.PHONY: help build run test clean lint fmt vet docker-build docker-run

help:
	@echo "anvilmcp - Anvil MCP Server"
	@echo ""
	@echo "Targets:"
	@echo "  build         - Build the binary"
	@echo "  run           - Run the server in development mode"
	@echo "  test          - Run tests"
	@echo "  coverage      - Run tests with coverage"
	@echo "  clean         - Clean build artifacts"
	@echo "  fmt           - Format code"
	@echo "  vet           - Run go vet"
	@echo "  lint          - Run golangci-lint (if installed)"
	@echo "  docker-build  - Build Docker image"
	@echo "  docker-run    - Run Docker container (see docs/DOCKER.md)"

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

docker-build: test
	docker build -t anvilmcp:latest -t anvilmcp:$(shell git rev-parse --short HEAD) .
	@echo "Docker image built successfully: anvilmcp:latest"
	@echo "Run with: make docker-run"

docker-run:
	docker run -it --rm \
		-p 8080:8080 \
		-v $(PWD)/workspace:/home/anvilmcp/workspace \
		-v $(PWD)/.config/mcp-session:/home/anvilmcp/.config/mcp-session \
		anvilmcp:latest

.DEFAULT_GOAL := help
