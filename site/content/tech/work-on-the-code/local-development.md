---
title: Local development
description: How to build, test and lint email-mcp, what CI runs, the rules of the code, how to add a tool, a command, a migration or a package, which docs page to update, and how to build the docs site and the container image.
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
  - internal/mcpserver/read_tools_test.go
  - internal/mcpserver/write_tools_test.go
  - internal/mcpserver/useronly_test.go
  - internal/service/accounts.go
  - internal/e2e/security_test.go
  - internal/e2e/capture_test.go
  - cmd/email-mcp/main.go
  - cmd/email-mcp/serve_test.go
  - internal/store/migrations/
  - internal/e2e/screenshots_test.go
  - internal/testutil/chrometest/chrometest.go
  - site/layouts/_shortcodes/screenshot.html
  - test/sitecheck/main.go
  - test/docscheck/main.go
  - .github/workflows/docs-check.yml
  - .github/pull_request_template.md
  - Dockerfile
  - .dockerignore
---

This page explains how to build, test and change the code of email-mcp on your
own computer. It names the make targets, the Go version, the checks of CI
(continuous integration: the checks that GitHub runs on each push), the rules
of the code, the steps to add a tool, a command, a migration or a package, and
which page of the docs to update. Read it before your first change.

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
unset), so `go` and golangci-lint (a program that runs many linters at once)
both use the same Go.

| Target | What it runs |
|---|---|
| `build` | `CGO_ENABLED=0 go build -trimpath -o bin/email-mcp ./cmd/email-mcp`. `CGO_ENABLED=0` means no C compiler, so the binary is static: it needs no system library to run. `-trimpath` removes the file paths of your computer from the binary. The build sets no version, so `email-mcp version` prints `email-mcp dev`. |
| `test` | `go test -race ./...`: every test that needs no build tag, with the race detector (a check that finds two goroutines, functions that run at the same time, that use the same memory without a lock). |
| `lint` | `go vet` and staticcheck (a linter for Go) with the build tags `interop`, `screenshots` and `sitebuild`, then `golangci-lint run`. The tags make the linters read the files that `go test ./...` leaves out. Install golangci-lint v2 first. |
| `vuln` | govulncheck, which checks the code and the dependencies for known vulnerabilities. |
| `screenshots` | The images of the user manual. See [The docs site](#the-docs-site). |
| `site` | The images, then the docs site and its checks. |
| `site-serve` | The images, then a preview of the site that rebuilds when a file changes. |
| `social-card` | `site/assets/social-card.png`, the preview picture of the landing page, from its SVG file (Scalable Vector Graphics: a picture made of shapes). Run it only after a change of the SVG. |

No target runs the interop test, because it needs Docker (a tool that runs
programs in containers). See
[The interop test](/tech/work-on-the-code/testing/#the-interop-test).

The Makefile runs staticcheck and govulncheck with `go run tool@version`, so
there is nothing to install: Go downloads and builds that version of the tool
the first time. The versions are the variables `STATICCHECK_VERSION` and
`GOVULNCHECK_VERSION` of the Makefile. The workflows pin (fix to one exact
version) the same versions, and a comment in each file says to keep them in
step.

The settings of golangci-lint are in {{< src ".golangci.yml" >}}. It runs the
standard set of linters (errcheck, govet, ineffassign, staticcheck and unused)
and gosec, a linter for security mistakes, and it checks the format with gofmt.
It reads the same three build tags. It skips `internal/thirdparty`, the copied
HTML (HyperText Markup Language) parser (see
[Map of packages](/tech/start-here/package-map/)).

## The Go version

The `go` line of {{< src "go.mod" >}} (`go 1.26.0`) is the oldest Go that can
build the module. {{< src ".go-version" >}} holds the Go version of the
project, for tools that choose the Go version of a folder, such as goenv. The
workflows do not read it: they install the newest stable Go
(`go-version: stable` of `actions/setup-go`). The Dockerfile pins its Go image
by digest (a hash of the exact image), and its comment says to use the latest
stable release.

## What CI runs

{{< src ".github/workflows/ci.yml" >}} runs on every pull request and on every
push to `main`. It has five jobs:

| Job | What it runs |
|---|---|
| `vet and test` | `go vet` with the three build tags, then `go test -race ./...` |
| `lint` | staticcheck with the three build tags, then golangci-lint, which the job builds with its own Go |
| `goreleaser check` | `goreleaser check`, which reads `.goreleaser.yaml`, the settings of GoReleaser (the tool that builds the release). A bad key then fails the pull request, and not the release after the tag exists. |
| `govulncheck` | govulncheck |
| `build site` | `make site`, then it uploads the images as a workflow artifact (files that a run keeps for download) for 7 days, also when `make site` failed |

The workflows of the repository follow these rules. Keep them when you add or
change a workflow:

- Set `permissions: contents: read` at the top, so the token that GitHub gives
  the run (`GITHUB_TOKEN`) may only read the repository by default. Give a job
  that needs more its own block, with only what it needs, as the job `release`
  of `release.yml` and the job `deploy` of `pages.yml` have.
- Pin each action by its full commit SHA (Secure Hash Algorithm: here, the hash
  that names one commit), with the release tag in a comment after it.
- Set `persist-credentials: false` on the checkout, so it does not keep the
  token in its git settings.
- Put no `${{ }}` value into a `run:` script. Pass a value through `env:`, and
  use it as a shell variable in quotes.

The interop test runs in its own workflow. The page
[Testing](/tech/work-on-the-code/testing/#what-ci-and-the-release-run) lists
what each workflow runs. The docs check also runs in its own workflow (see
[Which page do I update?](#which-page-do-i-update)).

## Dependabot

Dependabot is the GitHub service that opens pull requests to update
dependencies. Its settings are in {{< src ".github/dependabot.yml" >}}:

- It looks once a week, and it opens one pull request for each ecosystem (a
  kind of dependency): the Go modules, the actions, the images of the
  Dockerfiles, and the image of the interop compose file.
- A version must be 7 days old before Dependabot offers it (the cooldown,
  `default-days: 7`). Security updates do not wait.
- The pull requests of the Go modules start with `fix(deps):`. The Go modules
  are inside the released program, and release-please (the tool that proposes
  each release) counts a `fix` as a change that needs a patch release. So a
  security fix in a module leads to a proposed release, which a maintainer
  then merges. Without the prefix, Dependabot would write `build(deps):`,
  which makes no release.
- It does not update `go-imap/v2`, which is pinned to one beta version on
  purpose, or chromedp and `go-json-experiment/json`, which only the tests use.
  With the `fix` prefix, each update of a test module would propose a release
  of a program that did not change. A fix in a pinned module does not arrive by
  itself: watch the releases of that module, and read the result of
  govulncheck.

The comment at the top of the file lists what Dependabot does not see, so a
person updates it by hand: the images of BuildKit (the build engine of Docker)
and of the Syft scanner (it lists the modules inside an image) of the release,
the tool versions that `go run` uses, the version of Hugo (the program that
builds the docs site) in `.hugo-version`, the versions of cosign (a tool that
signs files) and Syft that `release.yml` gives to two installer actions, the
golangci-lint version of `ci.yml`, chromedp, and the release-please version in
the `$schema` address of `release-please-config.json`.

## Rules of the code

The rules come from {{< src ".github/copilot-instructions.md" >}} and from the
code:

- Format the code with `gofmt`. The lint fails when a file is not formatted.
- Return errors, and do not panic. Wrap an error with
  `fmt.Errorf("...: %w", err)`, so the caller can still find the cause.
- Give each exported name a comment that starts with the name. Make a comment
  say why, because the code already says what.
- Write table-driven tests (one test runs the same check over a table of
  cases), with `github.com/stretchr/testify/require`.
- A message id in the tool API (application programming interface: here, the
  tools and their arguments) is an IMAP UID (IMAP: Internet Message Access
  Protocol, for reading mail; a UID is the unique id of a message in its
  folder, which stays the same when other messages are removed), never a
  sequence number (the position of a message, which changes). Address messages
  with UID sets in each IMAP command, never with sequence sets.
- Fetch with `BODY.PEEK` (a fetch that does not mark the message as read),
  unless the purpose is to mark it as read.
- Log no mail content and no secret. The page
  [Activity log and program log][log-rules] has the rules for a log line.

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

A tool is one action that an agent can call, such as `get_message`. The agent
is the AI (artificial intelligence) program that calls the tools.

1. **The use case.** Add a method to `service.Service`. It checks the
   permission first, with the `policy.Action` that it needs. See [One tool
   call, step by step][permission].
2. **The input.** Add an input struct in `mcpserver`. The `json` tag of a field
   is the name of the argument, and the `jsonschema` tag is its description.
   The SDK (software development kit: the Go library for MCP, Model Context
   Protocol) builds the schema of the tool from the struct.
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
   {{< src "internal/service/accounts.go" >}}, with a name constant as the
   other tools have, so the Activity page can filter by it.
5. **The lists of the tests.** These tests fail for a tool that the server
   lists and they do not know:
   - `wantTools` and the number of tools in `TestToolsListFullSet`, in
     {{< src "internal/mcpserver/write_tools_test.go" >}};
   - for a read tool, the list `want` of `TestToolsListStableAndAnnotated`, in
     {{< src "internal/mcpserver/read_tools_test.go" >}};
   - the number of tools in the serve test of
     {{< src "cmd/email-mcp/serve_test.go" >}};
   - `requiredLevel`, and the map `args` inside
     `TestEveryWriteToolRefusedBelowItsLevel`, in
     {{< src "internal/e2e/security_test.go" >}};
   - a call of the tool in `TestEveryToolCallIsInTheActivityLog`, in
     {{< src "internal/e2e/capture_test.go" >}}.
6. **Only for the person?** A use case that only the person may call gets no
   tool. Add it to `userOnlyCalls` in
   {{< src "internal/mcpserver/useronly_test.go" >}}, and the test fails when
   the code of `mcpserver` calls it. A tool name must not contain `settings`,
   `remove`, `approve`, `reject`, `token`, `autonomy` or `approval`:
   `TestNoToolIsNamedForAUserOnlyUseCase` in the same file fails for such a
   name.

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

### Add a migration

A migration is a numbered SQL (Structured Query Language) file in
{{< src "internal/store/migrations/" >}} that changes the structure of the
database. Add the file with the next number, and its methods in a file of the
package `store`. The page
[Storage and secrets](/tech/inside-the-server/storage-and-secrets/#migrations)
has the rules. Read them first: a migration that rebuilds a table can delete
the stored passwords.

### Add a package

1. Put it in the layer that its imports allow, and add its row to
   [Map of packages](/tech/start-here/package-map/#all-packages) and to the
   maps of [Which page do I update?](#which-page-do-i-update).
2. Write or change the tech docs page that explains it, and add its path to the
   `code_areas` of that page (the list of the code that a page covers).
3. A file with a build tag that is not one of `interop`, `screenshots` and
   `sitebuild` needs that tag in `go vet` and staticcheck in the Makefile and in
   `ci.yml`, and in `build-tags` of `.golangci.yml`. Otherwise no linter reads
   the file. A file with one of the three tags needs no change, because each
   linter reads every package with them.

## Which page do I update?

A change of the code can make a page of the docs wrong. The site has two kinds
of docs: the user manual, for the person who uses email-mcp, and the tech docs,
for the people who change the code. The two maps below say which pages to read
for each part of the code. The docs check, a check on each pull request, fails
when a change of the program leaves out one of the two kinds.

### The docs check

The workflow {{< src ".github/workflows/docs-check.yml" >}} (a workflow is a
file of steps that GitHub runs) has the job `docs check`. The job runs when a
pull request is opened or reopened, when it gets a new commit, and when a label
is added or removed. A label is a short name that a person attaches to a pull
request. The job gives the list of the files that the pull request changes to
{{< src "test/docscheck/main.go" >}}. A moved file counts with its old path and
its new path, so a file that moves out of `internal/` still counts.

First, the check looks for a change of behaviour (of what the program does). A
change counts as behaviour when it touches:

- a Go file in `cmd/` or `internal/` that is not a test file (a file whose name
  ends in `_test.go`);
- any file in `internal/web/templates/`, `internal/web/static/` or
  `internal/store/migrations/`. The program embeds files of these folders
  (builds them into the binary). The check counts every file in them, whatever
  its type.

Files in `internal/testutil/`, `internal/e2e/` and `internal/thirdparty/` do
not count. No other file counts either, such as the Makefile, a workflow or a
page of the site.

Then, if behaviour changed, the pull request must do both of these:

- change a file under `site/content/manual/`, or carry the label
  `no-manual-change`;
- change a file under `site/content/tech/`, or carry the label
  `no-tech-docs-change`.

The landing page (`site/content/_index.md`) is in neither folder, so it does
not count as docs. When a part is missing, the job fails. Its message names the
missing part and the label that would clear it.

The check sees folders, not pages: a change of any file under
`site/content/manual/` clears the manual part. So the check cannot tell you
which page to change. The maps below do that.

The check also fails when it cannot check, with a message that starts with
`docscheck:`. This happens, for example, when it reads no changed file. The job
fails too when `gh` (the GitHub command line tool) cannot read the pull request
from GitHub. It also happens when the pull request changes more than 3000
files, because the GitHub API (here, the web service that lists the files)
lists no more. In that case, check the docs by hand.

The job reads the labels from GitHub when it runs, not from the event that
started it. So "Re-run jobs" on an old run uses the labels that the pull
request has when the job runs again.

You can run the check on your own computer, from the root of the repository.
`origin/main` is your copy of the branch `main` of GitHub, so run
`git fetch origin` first to bring it up to date. The command sees only commits,
so commit your change first. With no commit, the check reads no file and
cannot run. The command lists the files that your commits change since your
branch left `origin/main`. The `|` (a
pipe) gives that list to the check as its standard input (the text that a
program reads). `--name-only` prints only the paths. `--no-renames` lists a
moved file as two paths, the old one and the new one, as the job does.
`origin/main...` (three dots) compares your last commit with the commit where
your branch left `origin/main`. `go run ./test/docscheck` builds the check and
runs it:

```sh
git diff --name-only --no-renames origin/main... | go run ./test/docscheck
```

It prints nothing and exits with 0 (the code that means success) when nothing
is missing. A missing part ends with the line `exit status 1`, and a check that
could not run ends with `exit status 2`. In both cases the `go run` command
itself exits with 1, so read the message.

The check reads the labels from the variable `PR_LABELS`, a JSON (JavaScript
Object Notation) array of names. This command runs the same check as if the
pull request had the label `no-manual-change`. The variable is set for `go run`
only, so it comes after the `|`:

```sh
git diff --name-only --no-renames origin/main... |
  PR_LABELS='["no-manual-change"]' go run ./test/docscheck
```

### The two labels

Add a label only after you read the pages of the maps below, and none of them
needs a change:

- `no-manual-change`: the person who uses email-mcp sees no change in what it
  does, and no step of the manual changes. A change that alters only the look
  of a web page can carry it too: `make site` makes the images of the manual
  again when the code changed, so the images show the changed look.
- `no-tech-docs-change`: no sentence of a tech docs page becomes wrong.

There are two labels, not one. One label would clear both parts. Then a pull
request that updated the tech docs could skip the manual with it.

In this repository, only people with write access can add a label. If you have
no write access, say in the pull request why a label fits, and a maintainer
adds it.

The pull request template {{< src ".github/pull_request_template.md" >}} adds
two checkboxes, one for each kind of docs, and a link to this section. GitHub
fills it in when you open a pull request on github.com. A pull request that a
tool opens with its own text does not get it. The docs check runs in both
cases.

### Map of the tech docs

Each row names a code area, its main page, and other pages that may need a
change. Each tech docs page also lists the code that it covers, at its end,
under "Code covered by this page". The docs check does not count
`internal/e2e`, `internal/testutil` and `internal/thirdparty`, but they are in
the map so that you can find their page. Two pages are named Local HTTP mode,
one in each part of the docs. HTTP (HyperText Transfer Protocol: the protocol
of web servers) is one of the two ways that an AI app talks to the server.

| Code area | Main tech page | Also check |
|---|---|---|
| `cmd/email-mcp` | [The command line][t-cli] | [Startup and shutdown][t-startup], [Local HTTP mode][t-http] |
| `internal/app` | [Startup and shutdown][t-startup] | [Storage and secrets][t-storage], [The send flow][t-send] |
| `internal/audit` | [Activity log and program log][t-audit] | [Storage and secrets][t-storage] |
| `internal/confirm` | [The send flow][t-send] | [Security design][t-security] |
| `internal/connect` | [Connecting an account][t-connect] | [The command line][t-cli], [The web portal][t-portal] |
| `internal/discovery` | [Connecting an account][t-connect] | |
| `internal/domain/account` | [Permissions][t-perm] | [Storage and secrets][t-storage] |
| `internal/domain/mail` | [The mail backend][t-backend] | [One tool call, step by step][t-call] |
| `internal/e2e` | [Testing][t-testing] | [Security design][t-security], this page |
| `internal/errtype` | [Activity log and program log][t-audit] | |
| `internal/httpserve` | [Local HTTP mode][t-http] | [One tool call, step by step][t-call] |
| `internal/inflight` | [Startup and shutdown][t-startup] | |
| `internal/mcpserver` | [One tool call, step by step][t-call] | [The send flow][t-send], [Activity log and program log][t-audit], this page |
| `internal/mime` | [Mail content][t-content] | [Building mail][t-building], [Security design][t-security] |
| `internal/outbox` | [The send flow][t-send] | [Startup and shutdown][t-startup] |
| `internal/policy` | [Permissions][t-perm] | |
| `internal/provider/imapsmtp` | [The mail backend][t-backend] | [One tool call, step by step][t-call] |
| `internal/secrets` | [Storage and secrets][t-storage] | [Security design][t-security] |
| `internal/service` | [One tool call, step by step][t-call] | [The send flow][t-send], [Permissions][t-perm] |
| `internal/signing` | [Security design][t-security] | [The send flow][t-send], [The web portal][t-portal] |
| `internal/store` | [Storage and secrets][t-storage] | |
| `internal/testutil/chrometest` | [Testing][t-testing] | this page |
| `internal/testutil/imaptest` | [Testing][t-testing] | |
| `internal/testutil/smtptest` | [Testing][t-testing] | |
| `internal/testutil/webtest` | [Testing][t-testing] | |
| `internal/thirdparty/xhtml` | [Mail content][t-content] | [Map of packages][t-packages] |
| `internal/tokens` | [Local HTTP mode][t-http] | [Permissions][t-perm] |
| `internal/version` | [Making a release][t-release] | |
| `internal/web` | [The web portal][t-portal] | [Permissions][t-perm], [The send flow][t-send] |

`internal/service` is large, so several pages explain it. Choose the page by
the file:

- `send.go`, `write.go`, `queue.go` and `approvals.go`: [The send
  flow][t-send];
- `service.go`, `read.go` and `principal.go`: [One tool call, step by
  step][t-call];
- `calls.go`: [Activity log and program log][t-audit];
- `tokens.go`: [Local HTTP mode][t-http];
- `accounts.go`: [Permissions][t-perm] for the settings and the risk
  acceptance, and [Connecting an account][t-connect] for connecting an account;
- `reconnect.go`: [Connecting an account][t-connect];
- `backends.go`: [The mail backend][t-backend];
- `links.go`: [Permissions][t-perm].

`internal/mime` has two pages. Choose the page by the file:

- `build.go`, `body.go`, `recipients.go`, `draft.go` and `address.go`:
  [Building mail][t-building];
- the other files: [Mail content][t-content].

These files and folders are not Go packages. The docs check counts the files of
the first three rows. It does not count the other rows.

| File or folder | Main tech page | Also check |
|---|---|---|
| `internal/store/migrations/` | [Storage and secrets][t-storage] | this page |
| `internal/web/templates/`, `internal/web/static/` | [The web portal][t-portal] | |
| `internal/web/templates/approval.html` | [The web portal][t-portal] | [The send flow][t-send] |
| `Makefile` | this page | [Testing][t-testing] |
| `.golangci.yml`, `.go-version`, `.github/copilot-instructions.md` | this page | |
| `go.mod`, `.github/dependabot.yml` | this page | [Map of packages][t-packages] |
| `.github/workflows/ci.yml` | this page | [Testing][t-testing] |
| `.github/workflows/docs-check.yml`, `test/docscheck/`, `.github/pull_request_template.md` | this page | |
| `test/sitecheck/` | this page | [Testing][t-testing] |
| `.github/workflows/interop.yml`, `test/interop/` | [Testing][t-testing] | |
| `.github/workflows/release-please.yml`, `.github/workflows/pages.yml` | [Making a release][t-release] | |
| `.github/workflows/release.yml` | [Making a release][t-release] | [Testing][t-testing] |
| `.goreleaser.yaml`, `release-please-config.json`, `.release-please-manifest.json`, `server.json`, `CHANGELOG.md` | [Making a release][t-release] | |
| `Dockerfile` | [Making a release][t-release] | this page |
| `.dockerignore` | this page | |
| `DESIGN.md`, `PRODUCT.md` | [The web portal][t-portal] | |

The landing page of the site has a drawing of the approval page, in
`site/layouts/_partials/approval-picture.html`. A change of
`internal/web/templates/approval.html` may need the same change in the drawing.
The test `TestLandingPictureLabelsMatchPortal` in
`internal/web/landing_labels_test.go` compares a fixed list of labels of the
approval page and of the drawing, and fails until both say the same.

A new package gets a row in this map in the same pull request, and a row in
[Map of packages](/tech/start-here/package-map/#all-packages). It also gets a
row in the map of the user manual below, or a place in its list of areas with
no manual page. A new file in `internal/service` or `internal/mime` gets a line
in the list of its package above.

### Map of the user manual

Each row names a part of the code and the manual pages that may need a change.
The manual pages also list their code in `code_areas`, but they do not show the
list.

| Code | Manual pages |
|---|---|
| `cmd/email-mcp/main.go` (the list of commands, the help) | [Commands][m-commands], [Install email-mcp][m-install] |
| `cmd/email-mcp/serve.go` | [Commands][m-commands], [Local HTTP mode][m-http], [Containers][m-containers], [Connect an AI app][m-apps] and its five app pages |
| `cmd/email-mcp/setup.go` | [Connect your first account][m-connect], [The web pages][m-web], [Commands][m-commands] |
| `cmd/email-mcp/account.go`, `connect.go` | [Connect your first account][m-connect], [Manage accounts][m-manage], [Commands][m-commands] |
| `cmd/email-mcp/reconnect.go` | [Connect your first account][m-connect], [Manage accounts][m-manage], [Commands][m-commands], [Known limitations][m-limits] |
| `cmd/email-mcp/token.go` | [Local HTTP mode][m-http], [Commands][m-commands] |
| `cmd/email-mcp/outbox.go` | [Activity and undo][m-activity], [Commands][m-commands] |
| `cmd/email-mcp/importenv.go` | [Moving from the old email-mcp][m-move], [Commands][m-commands] |
| `cmd/email-mcp/key.go` | [The master key][m-key], [Commands][m-commands] |
| `cmd/email-mcp/doctor.go` | [Your first use][m-first], [Troubleshooting][m-trouble], [The master key][m-key], [Commands][m-commands] |
| `cmd/email-mcp/console.go` (the flags that all commands share) | [Commands][m-commands], [Where the data lives][m-data] |
| `internal/mcpserver/read_tools.go`, `write_tools.go` | [What the agent can do][m-agent], [Permission levels][m-levels] |
| `internal/mcpserver/shape.go` | [What the agent can do][m-agent], [Known limitations][m-limits] |
| `internal/mcpserver/server.go` (`Instructions`, the text that the server sends to the agent) | [What the agent can do][m-agent] |
| `internal/mcpserver/elicit.go` | [Confirmations][m-confirm], [How each AI app confirms][m-app-confirm], [Your first use][m-first], [Connect an AI app][m-apps] and its five app pages |
| `internal/mcpserver/errors.go` | [Troubleshooting][m-trouble] |
| `internal/mcpserver/capture.go` | [Activity and undo][m-activity] |
| `internal/policy/policy.go` | [Permission levels][m-levels], [Local HTTP mode][m-http] |
| `internal/policy/limiter.go` | [Full autonomy, limits and settings][m-autonomy] |
| `internal/domain/account` | [Permission levels][m-levels], [Full autonomy, limits and settings][m-autonomy], [Your first use][m-first] |
| `internal/tokens` | [Local HTTP mode][m-http] |
| `internal/httpserve` | [Local HTTP mode][m-http], [How each AI app confirms][m-app-confirm] |
| `internal/confirm` | [Confirmations][m-confirm], [How each AI app confirms][m-app-confirm] |
| `internal/outbox` | [Full autonomy, limits and settings][m-autonomy], [Activity and undo][m-activity], [Known limitations][m-limits] |
| `internal/audit` (the activity log) | [Activity and undo][m-activity] |
| `internal/web/server.go` (the routes), `templates/layout.html` (the menu) | [The web pages][m-web] |
| `internal/web/session.go`, `guard.go`, `links.go` | [Connect your first account][m-connect], [The web pages][m-web], [Security][m-security] |
| `internal/web/accounts.go` | [Connect your first account][m-connect], [The web pages][m-web] |
| `internal/web/templates/connect.html` | [Connect your first account][m-connect] |
| `internal/web/templates/accounts.html` | [Manage accounts][m-manage], [The web pages][m-web], [Connect your first account][m-connect], [Permission levels][m-levels] |
| `internal/web/settings.go`, `templates/settings.html` | [Permission levels][m-levels], [Full autonomy, limits and settings][m-autonomy], [Manage accounts][m-manage]; `settings.go` also [Your first use][m-first] |
| `internal/web/reconnect.go`, `templates/reconnect.html` | [Manage accounts][m-manage] |
| `internal/web/approvals.go`, `templates/approval.html`, `templates/approvals.html` | [Confirmations][m-confirm], [The web pages][m-web]; `approvals.go` and `approval.html` also [Your first use][m-first] |
| `internal/web/activity.go`, `templates/activity.html` | [Activity and undo][m-activity] |
| `internal/web/connections.go`, `templates/connections.html` | [Local HTTP mode][m-http], [The web pages][m-web] |
| `internal/web/templates/no_session.html`, `use_name.html` | [Connect your first account][m-connect], [Troubleshooting][m-trouble], [The web pages][m-web] |
| `internal/mime` | [What the agent can do][m-agent], [Security][m-security], [Known limitations][m-limits] |
| `internal/provider/imapsmtp` | [What the agent can do][m-agent], [Connect your first account][m-connect], [Troubleshooting][m-trouble] |
| `internal/discovery` | [Connect your first account][m-connect], [Manage accounts][m-manage] |
| `internal/connect` | [Connect your first account][m-connect], [Manage accounts][m-manage], [Troubleshooting][m-trouble] |
| `internal/secrets` | [The master key][m-key], [Where the data lives][m-data], [Recommended Claude Code deny rules][m-deny], [Troubleshooting][m-trouble] |
| `internal/app/kek.go` | [The master key][m-key], [Where the data lives][m-data], [Containers][m-containers] |
| `internal/app/app.go` | [Where the data lives][m-data], [Activity and undo][m-activity], [Recommended Claude Code deny rules][m-deny] |
| `internal/store` | [Where the data lives][m-data], [Troubleshooting][m-trouble] |
| `internal/version` | [Install email-mcp][m-install] |
| `.goreleaser.yaml` | [Install email-mcp][m-install], [Verifying a release][m-verify] |
| `.github/workflows/release.yml` | [Verifying a release][m-verify] |
| `Dockerfile` | [Containers][m-containers] |

These areas have no manual page: `internal/service`, `internal/domain/mail`,
`internal/signing`, `internal/inflight`, `internal/errtype`,
`internal/thirdparty`, `internal/e2e` and `internal/testutil`. A change in
`internal/service` reaches the person through a tool or a web page, so use the
row of that tool or page. A file with no row, such as a file of
`internal/web/static/` or another file of `internal/app`, has no manual page of
its own. If a person can see its change, update the page that shows that screen
or that behaviour.

### Find the pages that name a path

The maps name the pages to read first. A search finds the pages that contain a
path, in their `code_areas` (the list of code in the front matter, the block of
settings at the top of the file) or in their text. Run it from the root of the
repository. `grep -rl` prints the name of each file that holds the text (`-r`:
in every folder below, `-l`: only the names of the files):

```sh
grep -rl 'internal/outbox/' site/content
```

A page may name a file by its short name only, such as `outbox.go`, so search
for the short name too.

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
   and the Markdown files. It fails on each rule that the comment at its top
   lists, for example a broken link or anchor, an image with no alt text, a
   path in `code_areas` that does not exist, or a line of more than 80
   characters outside a code block and a table.
3. Hugo builds the site again with the test pages of the table hook (the
   template that turns a Markdown table into HTML). The check reads that build
   too, and a test checks each test table.
4. A test opens every page in Chrome at the width of a phone, and fails when a
   page scrolls sideways.

The links to the code on a page that you build go to the branch `main`. On a
tag, the publish workflow points them at the tag, and on any other run at the
commit (see [Making a release][publish]).

`make screenshots` needs Chrome, and so does the last step of `make site`. The
images come from `TestScreenshots` in
{{< src "internal/e2e/screenshots_test.go" >}}: it starts the whole program
with sample data, and a headless Chrome (Chrome with no window) opens each page
and saves it in light and in dark. `Find` in
{{< src "internal/testutil/chrometest/chrometest.go" >}} finds Chrome: the
program that `CHROME_PATH` names when it is set, or else the first of a list of
the usual places on Linux and macOS. One Chrome may run for at most 2 minutes
(`timeLimit`), so a page that never loads fails the test.

Make runs the tests when the stamp file `site/assets/screenshots/.done` is
missing, or when a file in `cmd/` or `internal/`, `go.mod` or `go.sum` is newer
than it. So a change of the text of the site does not make the images again.
The last step of `make site` still opens Chrome. This command makes the images
again even when nothing changed (`-B`: treat every target as out of date):

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

A long alt text does not fit on one line of 80 characters. Write it between
back quotes (`` ` ``) over several lines: the shortcode joins the lines into
one.

To change a shot, change its steps or its `want` texts. To remove one, remove
its `s.shot` call and its shortcode. The Hugo build fails when a page uses a
shot that has no images, because the shortcode stops it. The site check fails
when an image is not used. `TestScreenshots`
removes the old images at the start, so the image of a removed shot does not
stay.

## Build and try the container image

A container is a way to run a program in its own closed space, and the image
is the package that holds the program. The image is for tests and for building
on, not for daily use. The manual page
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
release builds its image from the same Dockerfile with the binary that
GoReleaser built (see [The container image][image]). The build does not send
the files that {{< src ".dockerignore" >}} lists to the builder, such as
`.git/`, `docs/`, `site/` and the Markdown files in the root of the repository.

### Try it

This starts the server inside the container with a key file in the home
folder of the container. It serves MCP over stdio (standard input and output),
so it reads MCP messages from standard input (`-i`) and waits for them. `-e`
sets an environment variable (a named setting that a program reads from its
surroundings) in the container: `EMAIL_MCP_KEK` says where the master key (the
key that protects the other keys) is. It has no account, so it is only a smoke
test (a short test that only shows that the program starts):

```sh
docker run --rm -i -e EMAIL_MCP_KEK=file:/home/nonroot/kek.key email-mcp:dev
```

The data (the database and the key file) is lost when the container stops,
because it is not on a volume (a folder that Docker keeps outside the
container). Do not add a real mail account to this container. If you keep its
data on a volume, keep the key file in a different place from the database.

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
[t-packages]: /tech/start-here/package-map/
[t-call]: /tech/start-here/tool-call/
[t-send]: /tech/inside-the-server/send-flow/
[t-perm]: /tech/inside-the-server/permissions/
[t-storage]: /tech/inside-the-server/storage-and-secrets/
[t-audit]: /tech/inside-the-server/audit-and-logging/
[t-startup]: /tech/inside-the-server/startup-and-shutdown/
[t-backend]: /tech/mail/mail-backend/
[t-connect]: /tech/mail/connecting-an-account/
[t-content]: /tech/mail/mail-content/
[t-building]: /tech/mail/building-mail/
[t-cli]: /tech/ways-in/command-line/
[t-http]: /tech/ways-in/local-http/
[t-portal]: /tech/ways-in/web-portal/
[t-security]: /tech/security/security-design/
[t-testing]: /tech/work-on-the-code/testing/
[t-release]: /tech/work-on-the-code/release-process/
[m-install]: /manual/getting-started/install/
[m-connect]: /manual/getting-started/connect-account/
[m-apps]: /manual/getting-started/connect-ai-app/
[m-first]: /manual/getting-started/first-use/
[m-agent]: /manual/daily-use/what-the-agent-can-do/
[m-web]: /manual/daily-use/web-pages/
[m-levels]: /manual/daily-use/permission-levels/
[m-confirm]: /manual/daily-use/confirmations/
[m-app-confirm]: /manual/daily-use/app-confirmation/
[m-autonomy]: /manual/daily-use/full-autonomy-and-limits/
[m-activity]: /manual/daily-use/activity-and-undo/
[m-security]: /manual/daily-use/security/
[m-deny]: /manual/daily-use/deny-rules/
[m-manage]: /manual/daily-use/manage-accounts/
[m-trouble]: /manual/daily-use/troubleshooting/
[m-commands]: /manual/reference/commands/
[m-key]: /manual/reference/master-key/
[m-http]: /manual/reference/local-http-mode/
[m-containers]: /manual/reference/containers/
[m-data]: /manual/reference/data-folder/
[m-limits]: /manual/reference/known-limitations/
[m-move]: /manual/reference/move-from-old-version/
[m-verify]: /manual/reference/verify-a-release/
