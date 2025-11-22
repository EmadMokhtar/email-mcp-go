# Makefile for email-mcp-go

# Variables
BINARY_NAME=email-mcp
BINARY_PATH=./bin/$(BINARY_NAME)
MAIN_PATH=./cmd/email-mcp
GO=go
GOFLAGS=-v
LDFLAGS=-ldflags "-s -w"

# Docker variables
DOCKER_IMAGE=email-mcp-go
DOCKER_TAG=latest
DOCKER_REGISTRY?=
DOCKER_FULL_IMAGE=$(if $(DOCKER_REGISTRY),$(DOCKER_REGISTRY)/,)$(DOCKER_IMAGE):$(DOCKER_TAG)

# Go related variables
GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GOFILES=$(wildcard *.go)

# Color output
RED=\033[0;31m
GREEN=\033[0;32m
YELLOW=\033[0;33m
NC=\033[0m # No Color

.PHONY: all build run test clean install uninstall fmt vet lint deps tidy help dev \
	docker/build docker/run docker/run-http docker/push docker/pull docker/clean docker/test

## all: Default target - build the application
all: build

## help: Show this help message
help:
	@echo "Available targets:"
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## build: Build the binary
build:
	@echo "$(GREEN)Building $(BINARY_NAME)...$(NC)"
	@mkdir -p $(GOBIN)
	@$(GO) build $(GOFLAGS) $(LDFLAGS) -o $(BINARY_PATH) $(MAIN_PATH)
	@echo "$(GREEN)Build complete: $(BINARY_PATH)$(NC)"

## run: Build and run the MCP server in stdio mode
run/stdio: build
	@echo "$(GREEN)Running $(BINARY_NAME)...$(NC)"
	@$(BINARY_PATH)

## run: Build and run the MCP server in HTTP mode
run/http: build
	@echo "$(GREEN)Running $(BINARY_NAME)...$(NC)"
	@$(BINARY_PATH) -http -addr localhost:8080

## dev: Run the application without building binary (using go run)
dev:
	@echo "$(GREEN)Running in development mode...$(NC)"
	@$(GO) run $(MAIN_PATH)/main.go

## test: Run all tests
test:
	@echo "$(GREEN)Running tests...$(NC)"
	@$(GO) test -v ./... -cover

## test-coverage: Run tests with coverage report
test-coverage:
	@echo "$(GREEN)Running tests with coverage...$(NC)"
	@$(GO) test -v ./... -coverprofile=coverage.out -covermode=atomic
	@$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Coverage report generated: coverage.html$(NC)"

## test-race: Run tests with race detector
test-race:
	@echo "$(GREEN)Running tests with race detector...$(NC)"
	@$(GO) test -v -race ./...

## bench: Run benchmarks
bench:
	@echo "$(GREEN)Running benchmarks...$(NC)"
	@$(GO) test -bench=. -benchmem ./...

## clean: Remove build artifacts and cache
clean:
	@echo "$(YELLOW)Cleaning...$(NC)"
	@rm -rf $(GOBIN)
	@rm -f coverage.out coverage.html
	@$(GO) clean -cache -testcache -modcache
	@echo "$(GREEN)Clean complete$(NC)"

## install: Install the binary to GOPATH/bin
install:
	@echo "$(GREEN)Installing $(BINARY_NAME)...$(NC)"
	@$(GO) install $(LDFLAGS) $(MAIN_PATH)
	@echo "$(GREEN)Installation complete$(NC)"

## uninstall: Remove the binary from GOPATH/bin
uninstall:
	@echo "$(YELLOW)Uninstalling $(BINARY_NAME)...$(NC)"
	@rm -f $(GOPATH)/bin/$(BINARY_NAME)
	@echo "$(GREEN)Uninstall complete$(NC)"

## fmt: Format Go source code
fmt:
	@echo "$(GREEN)Formatting code...$(NC)"
	@$(GO) fmt ./...
	@echo "$(GREEN)Format complete$(NC)"

## vet: Run go vet
vet:
	@echo "$(GREEN)Running go vet...$(NC)"
	@$(GO) vet ./...
	@echo "$(GREEN)Vet complete$(NC)"

## lint: Run golangci-lint (requires golangci-lint to be installed)
lint:
	@echo "$(GREEN)Running linter...$(NC)"
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "$(RED)golangci-lint not installed. Install with: brew install golangci-lint$(NC)"; \
	fi

## deps: Download dependencies
deps:
	@echo "$(GREEN)Downloading dependencies...$(NC)"
	@$(GO) mod download
	@echo "$(GREEN)Dependencies downloaded$(NC)"

## tidy: Tidy and verify dependencies
tidy:
	@echo "$(GREEN)Tidying dependencies...$(NC)"
	@$(GO) mod tidy
	@$(GO) mod verify
	@echo "$(GREEN)Dependencies tidied$(NC)"

## verify: Verify dependencies
verify:
	@echo "$(GREEN)Verifying dependencies...$(NC)"
	@$(GO) mod verify
	@echo "$(GREEN)Verification complete$(NC)"

## check: Run fmt, vet, and test
check: fmt vet test
	@echo "$(GREEN)All checks passed$(NC)"

## build-all: Build for multiple platforms
build-all:
	@echo "$(GREEN)Building for multiple platforms...$(NC)"
	@mkdir -p $(GOBIN)
	GOOS=darwin GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(GOBIN)/$(BINARY_NAME)-darwin-amd64 $(MAIN_PATH)
	GOOS=darwin GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(GOBIN)/$(BINARY_NAME)-darwin-arm64 $(MAIN_PATH)
	GOOS=linux GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(GOBIN)/$(BINARY_NAME)-linux-amd64 $(MAIN_PATH)
	GOOS=linux GOARCH=arm64 $(GO) build $(LDFLAGS) -o $(GOBIN)/$(BINARY_NAME)-linux-arm64 $(MAIN_PATH)
	GOOS=windows GOARCH=amd64 $(GO) build $(LDFLAGS) -o $(GOBIN)/$(BINARY_NAME)-windows-amd64.exe $(MAIN_PATH)
	@echo "$(GREEN)Multi-platform build complete$(NC)"

## update-deps: Update all dependencies to latest versions
update-deps:
	@echo "$(GREEN)Updating dependencies...$(NC)"
	@$(GO) get -u ./...
	@$(GO) mod tidy
	@echo "$(GREEN)Dependencies updated$(NC)"

## info: Display build info
info:
	@echo "$(GREEN)Build Information:$(NC)"
	@echo "  Binary Name: $(BINARY_NAME)"
	@echo "  Binary Path: $(BINARY_PATH)"
	@echo "  Main Path: $(MAIN_PATH)"
	@echo "  Go Version: $(shell $(GO) version)"
	@echo "  GOPATH: $(GOPATH)"
	@echo "  GOBIN: $(GOBIN)"

## install-claude: Install and configure for Claude Desktop
install-claude:
	@echo "$(GREEN)Installing Email MCP for Claude Desktop...$(NC)"
	@./install-claude.sh
	@echo "$(GREEN)Installation complete! Restart Claude Desktop to use the email MCP server.$(NC)"

## docker/build: Build the Docker image
docker/build:
	@echo "$(GREEN)Building Docker image $(DOCKER_FULL_IMAGE)...$(NC)"
	@docker build -t $(DOCKER_FULL_IMAGE) .
	@echo "$(GREEN)Docker image built successfully$(NC)"

## docker/run: Run the Docker container in stdio mode
docker/run:
	@echo "$(GREEN)Running Docker container in stdio mode...$(NC)"
	@docker run --rm -it --env-file .env $(DOCKER_FULL_IMAGE)

## docker/run-http: Run the Docker container in HTTP mode
docker/run-http:
	@echo "$(GREEN)Running Docker container in HTTP mode on port 8080...$(NC)"
	@docker run --rm -it -p 8080:8080 --env-file .env $(DOCKER_FULL_IMAGE) /app/email-mcp -http -addr 0.0.0.0:8080

## docker/push: Push the Docker image to the registry
docker/push:
	@echo "$(GREEN)Pushing Docker image $(DOCKER_FULL_IMAGE) to registry...$(NC)"
	@docker push $(DOCKER_FULL_IMAGE)

## docker/pull: Pull the Docker image from the registry
docker/pull:
	@echo "$(GREEN)Pulling Docker image $(DOCKER_FULL_IMAGE) from registry...$(NC)"
	@docker pull $(DOCKER_FULL_IMAGE)

## docker/clean: Remove Docker images and containers
docker/clean:
	@echo "$(YELLOW)Cleaning Docker images and containers...$(NC)"
	@docker compose down -v 2>/dev/null || true
	@docker rmi $(DOCKER_FULL_IMAGE) 2>/dev/null || true
	@echo "$(GREEN)Docker cleanup complete$(NC)"

## docker/test: Run the Docker test script
docker/test:
	@echo "$(GREEN)Running Docker tests...$(NC)"
	@./test_docker.sh

