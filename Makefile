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
# templates stop the build when it is empty. The pattern takes the text up to
# the next quote ([^"]*). With ".*" it would also take everything up to the
# last quote of the line, when the manifest has a second entry on that line.
SITE_VERSION := $(shell sed -n 's/.*"\.": "\([^"]*\)".*/\1/p' .release-please-manifest.json)

# Where "make site" builds the table fixtures: the site plus the pages of
# test/sitecheck/testdata/tables. It is a folder of its own, so these pages
# never reach site/public, the folder that is published. Git ignores it.
SITE_TABLES_DIR := $(CURDIR)/site/public-tabletest

.PHONY: build test lint vuln screenshots site site-serve social-card

# build: compile a static binary into bin/
build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/email-mcp

# test: run all tests with the race detector
test:
	go test -race ./...

# lint: run go vet, staticcheck and golangci-lint (install golangci-lint v2 first).
# The interop test (build tag "interop") is checked too, but not run: it needs
# the Docker servers of test/interop/compose.yaml. The same goes for the tests
# with the build tag "screenshots": they need Chrome, and "make site" runs them.
# The tests with the build tag "sitebuild" need the table fixtures, and "make
# site" runs them too.
# One go vet has every tag and every package, so a new package with a tag is
# checked without a change here.
lint:
	go vet -tags interop,screenshots,sitebuild ./...
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) -tags interop,screenshots,sitebuild ./...
	golangci-lint run

# vuln: check dependencies and code for known vulnerabilities
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

# site/static/icon.svg: Hugo reads only the folder site/, so copy the icon of
# the portal there. The copy is not in git. The site uses it as the tab icon.
site/static/icon.svg: internal/web/static/icon.svg
	mkdir -p site/static
	cp internal/web/static/icon.svg site/static/icon.svg

# screenshots: make the images of the user manual. A Go test starts the whole
# program, and a headless Chrome saves its pages in site/assets/screenshots/. The
# images are not in git. The stamp file makes the test run only when the code
# changed, so a change of the text of the site does not start Chrome.
# "make -B screenshots" forces a new run. It needs Chrome: see
# internal/testutil/chrometest. The same run checks the layout of the pages in
# Chrome (internal/e2e/layout_test.go).
SCREENSHOT_INPUTS := $(shell find cmd internal -type f) go.mod go.sum
SCREENSHOT_STAMP := site/assets/screenshots/.done

$(SCREENSHOT_STAMP): $(SCREENSHOT_INPUTS)
	EMAIL_MCP_SCREENSHOT_DIR=$(CURDIR)/site/assets/screenshots go test \
		-tags screenshots -count=1 \
		-run 'TestScreenshots|TestClearShots|TestFindChrome|TestShotRefuses|TestConnectionsTableFits' \
		./internal/e2e/ ./internal/testutil/chrometest/
	touch $@

screenshots: $(SCREENSHOT_STAMP)

# site: build the docs site into site/public/. --cleanDestinationDir removes
# files of earlier builds that the site no longer makes. --panicOnWarning turns
# a Hugo warning into an error, so a warning cannot reach the published site.
# Then comes the site check (test/sitecheck). It reads the built pages and finds
# what Hugo cannot: a link that leaves the base path, a missing file or anchor, a
# script, a missing alt text, a table with no name or with the same name as
# another table of its page, a path in the source that does not exist.
#
# The next three steps test the table hook (layouts/_markup/render-table.html)
# on pages that are made for it. Hugo builds the site again, with
# --environment tabletest (site/config/tabletest/hugo.toml), into
# $(SITE_TABLES_DIR). That build has the real pages and the fixture pages of
# test/sitecheck/testdata/tables, and the site check reads it too. Then a test
# (build tag "sitebuild") requires the exact name, the escaping and the
# alignment of each fixture table.
#
# The last step opens every page in Chrome, at the width of a phone, and fails
# when a page scrolls sideways. It opens the fixture pages too, with the wide
# table.
site: screenshots site/static/icon.svg
	HUGO_PARAMS_VERSION=$(SITE_VERSION) $(HUGO) --source site --cleanDestinationDir --panicOnWarning --baseURL $(SITE_BASE_URL)
	go run ./test/sitecheck -public site/public -base $(SITE_BASE_PATH) -root .
	HUGO_PARAMS_VERSION=$(SITE_VERSION) $(HUGO) --source site --environment tabletest --destination $(SITE_TABLES_DIR) --cleanDestinationDir --panicOnWarning --baseURL $(SITE_BASE_URL)
	go run ./test/sitecheck -public $(SITE_TABLES_DIR) -base $(SITE_BASE_PATH) -root .
	SITE_TABLES_DIR=$(SITE_TABLES_DIR) go test -tags sitebuild -count=1 -run TestTableHook ./test/sitecheck/
	SITE_PUBLIC_DIR=$(CURDIR)/site/public SITE_TABLES_DIR=$(SITE_TABLES_DIR) SITE_BASE_PATH=$(SITE_BASE_PATH) go test -tags screenshots -count=1 -run TestNoSidewaysScroll ./test/sitecheck/

# site-serve: show the docs site on http://localhost:1313/ and rebuild it when
# a file changes.
site-serve: screenshots site/static/icon.svg
	HUGO_PARAMS_VERSION=$(SITE_VERSION) $(HUGO) server --source site

# social-card: make site/assets/social-card.png, the preview card of the
# landing page (its og:image), from site/assets/social-card.svg. The program
# test/socialcard opens the SVG in Chrome with no window and saves a picture of
# exactly 1200 by 630 pixels, at one pixel for each point. Chrome runs with a
# new empty profile, so your own Chrome profile is not touched. The PNG is in
# git, so a build of the site uses the card as it is and does not make it
# again: run this only after a change of the SVG. It also writes
# site/assets/social-card.png.sha256, the SHA-256 of the SVG that it used. A
# test in test/sitecheck fails when the SVG changed and that record did not.
#
# Chrome is found as the tests that use Chrome find it (chrometest.Find): the
# program that CHROME_PATH names when it is set, for example
# CHROME_PATH=google-chrome, or else the first of the usual places on Linux and
# macOS.
social-card:
	go run -tags screenshots ./test/socialcard -svg site/assets/social-card.svg -png site/assets/social-card.png
