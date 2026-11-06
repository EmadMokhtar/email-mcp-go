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

# The docs site. Hugo builds the folder site/. Its version is in .hugo-version
# (like .go-version), so a local build and the CI build use the same Hugo. "go
# run" downloads and builds Hugo on first use, which takes about a minute.
# CGO_ENABLED=0 builds the standard Hugo, which needs no C compiler. The site
# uses none of the features of the extended Hugo.
HUGO_VERSION := $(shell cat .hugo-version)
HUGO ?= CGO_ENABLED=0 go run github.com/gohugoio/hugo@v$(HUGO_VERSION)

# Where the built site is served. SITE_BASE_URL is the full address, and
# SITE_BASE_PATH is the path part of the same address. Keep the two in step.
# GitHub Pages serves a project site under /email-mcp-go/. A custom domain
# serves it at /.
SITE_BASE_URL ?= http://localhost:1313/email-mcp-go/
SITE_BASE_PATH ?= /email-mcp-go/

# The released version, for the footer of the site. Release-please keeps it in
# the manifest. Setting SITE_VERSION on the command line replaces it. The
# templates stop the build when it is empty.
SITE_VERSION := $(shell sed -n 's/.*"\.": "\(.*\)".*/\1/p' .release-please-manifest.json)

.PHONY: build test lint vuln site site-serve

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

# site/static/icon.svg: Hugo reads only the folder site/, so copy the icon of
# the portal there. The copy is not in git. The site uses it as the tab icon.
site/static/icon.svg: internal/web/static/icon.svg
	mkdir -p site/static
	cp internal/web/static/icon.svg site/static/icon.svg

# site: build the docs site into site/public/. --cleanDestinationDir removes
# files of earlier builds that the site no longer makes. --panicOnWarning turns
# a Hugo warning into an error, so a warning cannot reach the published site.
site: site/static/icon.svg
	HUGO_PARAMS_VERSION=$(SITE_VERSION) $(HUGO) --source site --cleanDestinationDir --panicOnWarning --baseURL $(SITE_BASE_URL)

# site-serve: show the docs site on http://localhost:1313/ and rebuild it when
# a file changes.
site-serve: site/static/icon.svg
	HUGO_PARAMS_VERSION=$(SITE_VERSION) $(HUGO) server --source site
