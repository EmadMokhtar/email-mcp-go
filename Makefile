# Makefile for email-mcp-go
#
# If your shell sets GOROOT to a different Go install, start make without it
# so that go and golangci-lint both use the right toolchain, for example:
#   env -u GOROOT make test

BINARY := bin/email-mcp

# Versions of the tools that "go run" downloads and builds on first use.
# Keep them in sync with .github/workflows/ci.yml (and govulncheck also with
# .github/workflows/release.yml).
STATICCHECK_VERSION := v0.8.2
GOVULNCHECK_VERSION := v1.9.0

.PHONY: build test lint vuln

# build: compile a static binary into bin/
build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/email-mcp

# test: run all tests with the race detector
test:
	go test -race ./...

# lint: run go vet, staticcheck and golangci-lint (install golangci-lint v2 first).
# The interop test (build tag "interop") is checked too, but not run: it needs
# the Docker servers of test/interop/compose.yaml.
lint:
	go vet ./...
	go vet -tags interop ./test/interop/
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -tags interop ./...
	golangci-lint run

# vuln: check dependencies and code for known vulnerabilities
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
