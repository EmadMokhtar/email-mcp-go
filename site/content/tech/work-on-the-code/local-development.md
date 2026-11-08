---
title: Local development
description: How to build, test and lint email-mcp, what CI runs, the rules of the code, how to add a tool, a command, a migration or a package, and how to build the docs site and the container image.
weight: 10
code_areas:
  - Makefile
  - .go-version
  - go.mod
  - .golangci.yml
  - .github/workflows/ci.yml
  - .github/dependabot.yml
  - .github/copilot-instructions.md
  - internal/mcpserver/server.go
  - internal/mcpserver/read_tools.go
  - internal/mcpserver/write_tools.go
  - internal/mcpserver/capture.go
  - internal/mcpserver/capture_test.go
  - internal/mcpserver/write_tools_test.go
  - internal/mcpserver/useronly_test.go
  - internal/service/accounts.go
  - internal/e2e/security_test.go
  - cmd/email-mcp/main.go
  - internal/store/migrations/
  - internal/e2e/screenshots_test.go
  - internal/testutil/chrometest/chrometest.go
  - site/layouts/_shortcodes/screenshot.html
  - test/sitecheck/main.go
  - Dockerfile
  - .dockerignore
---

This page explains how to build, test and change the code of email-mcp on your
own computer. It names the make targets, the Go version, the checks of CI
(continuous integration: the checks that GitHub runs on each push), the rules
of the code, and the steps to add a tool, a command, a migration or a package.
Read it before your first change.

## The make targets

Make is a tool that runs the build steps of a project. The steps are in the
{{< src "Makefile" >}}. These four commands build the program, run the tests,
run the linters (tools that find mistakes in code without running it) and look
for known vulnerabilities. The comment after each command says what it makes or
runs:

```sh
make build    # bin/email-mcp
make test     # go test -race ./...
make lint     # go vet, staticcheck and golangci-lint
make vuln     # govulncheck
```

If your shell sets `GOROOT` to another Go install, run make as
`env -u GOROOT make test`. `GOROOT` is the variable that names the folder of a
Go install. `env -u GOROOT` runs the command without that variable (`-u`:
unset), so `go` and golangci-lint both use the same Go. The interop test runs
the mail scenario against real Dovecot and Postfix servers in Docker. See
[The interop test](/tech/work-on-the-code/testing/#the-interop-test).

| Target | What it runs |
|---|---|
| `build` | `CGO_ENABLED=0 go build -trimpath -o bin/email-mcp ./cmd/email-mcp`. `CGO_ENABLED=0` means no C compiler, so the binary is static: it needs no system library to run. `-trimpath` removes the file paths of your computer from the binary. The build sets no version, so `email-mcp version` prints `email-mcp dev`. |
| `test` | `go test -race ./...`: every test of the module, with the race detector (a check that finds two goroutines that use the same memory at the same time). |
| `lint` | `go vet` and staticcheck with the build tags `interop`, `screenshots` and `sitebuild`, then `golangci-lint run`. The tags make the linters read the tests that `go test ./...` leaves out. Install golangci-lint v2 first. |
| `vuln` | govulncheck, which checks the code and the dependencies for known vulnerabilities. |
| `screenshots` | The images of the user manual. See [The docs site](#the-docs-site). |
| `site` | The images, then the docs site and its checks. |
| `site-serve` | The images, then a preview of the site that rebuilds when a file changes. |
| `social-card` | `site/assets/social-card.png`, the preview picture of the landing page, from its SVG. Run it only after a change of the SVG. |

The Makefile runs staticcheck and govulncheck with `go run tool@version`, so
there is nothing to install: Go downloads and builds the tool the first time.
The versions are the variables `STATICCHECK_VERSION` and `GOVULNCHECK_VERSION`
of the Makefile. The workflows pin the same versions, and a comment in each
file says to keep them in step.

The settings of golangci-lint are in {{< src ".golangci.yml" >}}. It runs the
standard set of linters (errcheck, govet, ineffassign, staticcheck and unused)
and gosec, a linter for security mistakes, and it checks the format with gofmt.
It reads the same three build tags. It skips `internal/thirdparty`, the copied
HTML parser (see [Map of packages](/tech/start-here/package-map/)).

## The Go version

The `go` line of {{< src "go.mod" >}} (`go 1.26.0`) is the oldest Go that can
build the module. {{< src ".go-version" >}} names the Go version that the
project works with. The workflows do not read it: they install the newest
stable Go (`go-version: stable` of `actions/setup-go`). The Dockerfile pins its
Go image by digest (a hash of the exact image).

## What CI runs

{{< src ".github/workflows/ci.yml" >}} runs on every push and on every pull
request. It has five jobs:

| Job | What it runs |
|---|---|
| `vet and test` | `go vet` with the three build tags, then `go test -race ./...` |
| `lint` | staticcheck with the three build tags, then golangci-lint, which the job builds with its own Go |
| `goreleaser check` | `goreleaser check`, which reads `.goreleaser.yaml`. A bad key then fails the pull request, and not the release after the tag exists. |
| `govulncheck` | govulncheck |
| `build site` | `make site`, then it uploads the images as a workflow artifact (files that a run keeps for download) for 7 days, also when `make site` failed |

The rules of every workflow:

- The token of the workflow may only read the repository
  (`permissions: contents: read`).
- Each action is pinned by its full commit SHA (the hash of one commit), with
  the release tag in a comment after it.
- The checkout does not keep the token in its git settings
  (`persist-credentials: false`).
- No step puts a `${{ }}` value into a `run:` script. A value goes through
  `env:` and is used as a shell variable in quotes.

The interop test runs in its own workflow. The page
[Testing](/tech/work-on-the-code/testing/#what-ci-and-the-release-run) lists
what each workflow runs.

## Dependabot

Dependabot is the GitHub service that opens pull requests to update
dependencies. Its settings are in {{< src ".github/dependabot.yml" >}}:

- It looks once a week, and it opens one pull request for each ecosystem (a
  kind of dependency): the Go modules, the actions, the images of the
  Dockerfiles, and the image of the interop compose file.
- A new version must be 7 days old before Dependabot offers it (the cooldown,
  `default-days: 7`). Security updates do not wait.
- The pull requests of the Go modules start with `fix(deps):`. The Go modules
  are inside the released program, and release-please counts a `fix` as a
  change that needs a patch release. So a security fix in a module ships in the
  next release. Without the prefix, Dependabot would write `build(deps):`,
  which makes no release.
- It does not update `go-imap/v2`, which is pinned to one beta version on
  purpose, or chromedp and `go-json-experiment/json`, which only the tests use.
  With the `fix` prefix, each update of a test module would propose a release
  of a program that did not change.

The comment at the top of the file lists what Dependabot does not see, so a
person updates it by hand: the BuildKit and Syft scanner images of the release,
the tool versions that `go run` uses, the Hugo version in `.hugo-version`, the
cosign and Syft versions that `release.yml` gives to two installer actions, the
golangci-lint version of `ci.yml`, chromedp, and the release-please version in
the `$schema` address of `release-please-config.json`.

## Rules of the code

The rules come from {{< src ".github/copilot-instructions.md" >}} and from the
code:

- Format the code with `gofmt`. The lint fails when a file is not formatted.
- Return errors, and do not panic. Wrap an error with
  `fmt.Errorf("...: %w", err)`, so the caller can still find the cause.
- Each exported name has a comment that starts with the name. A comment says
  why, because the code already says what.
- Tests are table-driven (one test runs the same check over a table of cases),
  with `github.com/stretchr/testify/require`.
- A message id in the tool API is an IMAP UID (the number of a message in its
  folder, which stays the same when other messages are removed), never a
  sequence number (the position of a message, which changes). Address messages
  with UID sets in each IMAP command, never with sequence sets.
- Fetch with `BODY.PEEK` (a fetch that does not mark the message as read),
  unless the purpose is to mark it as read.
- Log no mail content and no secret. The page
  [Activity log and program log][log-rules] has the rules for a new log line.

The design documents under `docs/superpowers/specs/` are history. The page
[Architecture overview](/tech/start-here/overview/#the-design-documents) says
how to read them.

## Commit and pull request titles

Each commit message and each pull request title is a Conventional Commit
(a format for commit messages: `type(scope): description`), such as
`fix(mcpserver): point agents to html_body in the create_draft description`.
A squash merge makes the title of the pull request the commit message on
`main`. release-please reads these messages to choose the next version and to
write the changelog (the list of changes of each release). So a `fix:` or a
`feat:` makes a release, and a `docs:`, `ci:` or `test:` alone does not. The
page [Making a release][bump] says which type raises which number.

## Add code

The page [Map of packages](/tech/start-here/package-map/#where-to-add-code)
says which package gets which kind of code. These lists name the files and the
tests to change.

### Add a tool

A tool is one action that an agent can call, such as `get_message`.

1. **The use case.** Add a method to `service.Service`. It checks the
   permission first, with the `policy.Action` that it needs. See [One tool
   call, step by step][permission].
2. **The input.** Add an input struct in `mcpserver`. The `json` tag of a field
   is the name of the argument, and the `jsonschema` tag is its description.
   The SDK builds the schema of the tool from the struct.
3. **The handler.** Add it in `registerReadTools` in
   {{< src "internal/mcpserver/read_tools.go" >}} or `registerWriteTools` in
   {{< src "internal/mcpserver/write_tools.go" >}}, with `addTool`. Give it a
   description that names the level it needs, and its annotations (hints for
   the client): `readOnly()`, or `writeHints(destructive, idempotent,
   openWorld)`. `addTool` in {{< src "internal/mcpserver/server.go" >}} is the
   only way to add a tool, because it wraps the handler with `safe`, which
   finds the caller first. `TestEveryToolRunsThroughSafe` fails for a tool that
   was added another way.
4. **The activity log.** Each argument name goes in `omittedArguments` in
   {{< src "internal/mcpserver/capture.go" >}} (the log never keeps it: the
   content of a message) or in `keptArguments` in
   {{< src "internal/mcpserver/capture_test.go" >}}. Decide on purpose.
   `TestEveryArgumentOfEveryToolIsKeptOrOmitted` fails for an argument that is
   in neither. Add the name of the tool to `auditTools` in
   {{< src "internal/service/accounts.go" >}}, so the Activity page can filter
   by it.
5. **The lists of the tests.** Add the tool and its hints to `wantTools` in
   {{< src "internal/mcpserver/write_tools_test.go" >}}, and change the number
   of tools that `TestToolsListFullSet` expects. Add its level and arguments to
   `requiredLevel` and `args` in {{< src "internal/e2e/security_test.go" >}}.
   These tests fail for a tool that the server lists and they do not know.
6. **Only for the person?** A use case that only the person may call gets no
   tool. Add it to `userOnlyCalls` in
   {{< src "internal/mcpserver/useronly_test.go" >}}, and the test fails when
   the code of `mcpserver` calls it.

Then update the manual page
[What the agent can do](/manual/daily-use/what-the-agent-can-do/).

### Add a command

1. Add an entry to the list `commands` in
   {{< src "cmd/email-mcp/main.go" >}}: the name, one line for the help, and its
   function.
2. Write the function in a file of `cmd/email-mcp`, with its own
   `flag.FlagSet` (the set of flags of one command).
3. Test it through `runWith`, with a fake `system`, so the test needs no
   terminal and opens no browser (see
   [Testing](/tech/work-on-the-code/testing/#clocks-and-fakes)).
4. Update the manual page [Commands](/manual/reference/commands/).

The tech docs page "The command line" explains how the commands open the
program and handle passwords and exit codes.

### Add a migration

A migration is a numbered SQL file in {{< src "internal/store/migrations/" >}}
that changes the structure of the database. Add the file with the next number,
and its methods in a file of the package `store`. The page
[Storage and secrets](/tech/inside-the-server/storage-and-secrets/#migrations)
has the rules. Read them first: a migration that rebuilds a table can delete
the stored passwords.

### Add a package

1. Put it in the layer that its imports allow, and add its row to
   [Map of packages](/tech/start-here/package-map/#all-packages).
2. Write or change the tech docs page that explains it, and add its path to the
   `code_areas` of that page (the list of the code that a page covers).
3. A file with a build tag that is not in use yet needs the tag in `go vet`
   and staticcheck in the Makefile and in `ci.yml`, and in `build-tags` of
   `.golangci.yml`. Otherwise no linter reads the file. A tag that is in use
   needs no change, because each of them reads every package.

## The docs site

The docs site is built with Hugo, a program that makes web pages from Markdown
files. Its files are under `site/`. Three targets build it:

- `make screenshots` makes the images of the user manual in
  `site/assets/screenshots/`. The images are not in git.
- `make site` makes the images, builds the site into `site/public/`, and checks
  it.
- `make site-serve` makes the images and shows the site on
  `http://localhost:1313/`. It builds the pages again when a file changes.

`make site` runs these steps:

1. Hugo builds the site. `--panicOnWarning` turns a Hugo warning into an error,
   so a warning cannot reach the published site.
2. The site check {{< src "test/sitecheck/main.go" >}} reads the built pages
   and the Markdown files. The comment at its top lists each rule. It fails,
   for example, on a link that leaves the site or points to a missing page or
   anchor, a script or a file from another site, an image with no alt text, a
   table with no name, a page with no `code_areas`, a path in `code_areas` or in
   a code link that does not exist, an image that no page uses, an H1 or a
   heading below H3, a code block with no language, and a line of more than 80
   characters outside a code block and a table.
3. Hugo builds the site again with the test pages of the table hook, the check
   reads that build too, and a test checks each test table.
4. A test opens every page in Chrome at the width of a phone, and fails when a
   page scrolls sideways.

The links to the code on a page that you build go to the branch `main`. The
publish workflow sets them to the release tag (see
[Making a release][publish]).

`make screenshots` needs Chrome, and so does the last step of `make site`. The
images come from `TestScreenshots` in
{{< src "internal/e2e/screenshots_test.go" >}}: it starts the whole program
with sample data, and a headless Chrome (Chrome with no window) opens each page
and saves it in light and in dark. `Find` in
{{< src "internal/testutil/chrometest/chrometest.go" >}} finds Chrome: the
program that `CHROME_PATH` names when it is set, or else the first of a list of
the usual places on Linux and macOS. One Chrome may run for at most 2 minutes
(`timeLimit`), so a page that never loads fails the test.

The target runs only when a file in `cmd/` or `internal/`, `go.mod` or `go.sum`
changed, because make compares them with the stamp file
`site/assets/screenshots/.done`. So a change of the text of the site does not
start Chrome. This command makes the images again even when nothing changed
(`-B`: treat every target as out of date):

```sh
make -B screenshots
```

### Add or change a shot

A shot is one screen of the program, saved as `<name>-light.png` and
`<name>-dark.png`.

1. In `TestScreenshots`, bring the program to the state that the screen needs
   through the real paths: connect the sample account (`h.connect`), and call
   tools with the MCP client. Use only addresses on `example.com`.
2. Open the page with `s.open(link, want...)`, or click to it with
   `s.click(selector, ready, want...)`. `want` are texts that the page must
   show, so a renamed button fails the test. Choose the height of the window
   with `s.viewport`: `shotHeight` (800 pixels) or `shotHeightShort` (420). The
   width is `shotWidth` (1280).
3. Call `s.shot("name")`. It fails when the page shows an access token or one
   of the values in `forbidden`, such as the password and the sign-in link.
4. Take no shot while a form is busy: its busy sign never ends, and the shot
   waits until the time limit. For a result that comes from the outbox (the
   queue of sends), use `s.reloadUntil`.
5. Put the image on the manual page with the shortcode of
   {{< src "site/layouts/_shortcodes/screenshot.html" >}}. Its alt text says
   what the screen shows. Each step in the text must work without the image.
6. Run `make -B screenshots`, then `make site`.

This is the shortcode on a page:

```text
{{</* screenshot name="waiting" alt="The approval page with the send" */>}}
```

To change a shot, change its steps or its `want` texts. To remove one, remove
its `s.shot` call and its shortcode. The site check fails when a page uses a
shot that does not exist, or when an image is not used. `TestScreenshots`
removes the old images at the start, so the image of a removed shot does not
stay.

## Build and try the container image

The image is for tests and for building on, not for daily use. The manual page
[Containers](/manual/reference/containers/) explains why it does not suit use
on your own computer.

### Build it yourself

These commands build the image from the {{< src "Dockerfile" >}} in the
current folder and name it `email-mcp:dev` (`-t`: the name of the image). Then
they run the program in it once to print its version (`--rm`: delete the
container when it stops). It prints `email-mcp dev`, because the build argument
`VERSION` of the Dockerfile is `dev` by default:

```sh
docker build -t email-mcp:dev .
docker run --rm email-mcp:dev version
```

The Dockerfile compiles the binary from the source code by default. The
release uses the same Dockerfile with `--build-arg BINARY_FROM=release`: then
it copies the binary that GoReleaser built, instead of compiling a new one (see
[The container image][image]).
The build does not send the files that {{< src ".dockerignore" >}} lists to
the builder, such as `.git/`, `docs/`, `site/` and the Markdown files.

### Try it

This starts the server inside the container with a key file in the home
folder of the container. It serves MCP over stdio, so it reads MCP messages
from standard input (`-i`) and waits for them. `-e` sets an environment
variable (a named setting that a program reads from its surroundings) in the
container: `EMAIL_MCP_KEK` says where the master key is. It has no account, so
it is only a smoke test (a short test that only shows that the program starts):

```sh
docker run --rm -i -e EMAIL_MCP_KEK=file:/home/nonroot/kek.key email-mcp:dev
```

The data (the database and the key file) is lost when the container stops,
because it is not on a volume.

## Related pages

- [Testing](/tech/work-on-the-code/testing/)
- [Making a release](/tech/work-on-the-code/release-process/)
- [Map of packages](/tech/start-here/package-map/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [Containers](/manual/reference/containers/) in the user manual

[log-rules]: /tech/inside-the-server/audit-and-logging/#add-a-log-line
[bump]: /tech/work-on-the-code/release-process/#how-the-version-goes-up
[permission]: /tech/start-here/tool-call/#where-the-permission-is-checked
[publish]: /tech/work-on-the-code/release-process/#how-the-site-is-published
[image]: /tech/work-on-the-code/release-process/#the-container-image
