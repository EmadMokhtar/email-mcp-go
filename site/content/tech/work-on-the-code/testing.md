---
title: Testing
description: The kinds of test of email-mcp, what each one proves, how to run them, and how to write a test for a new feature.
weight: 20
code_areas:
  - internal/testutil/imaptest/imaptest.go
  - internal/testutil/smtptest/smtptest.go
  - internal/testutil/webtest/webtest.go
  - internal/testutil/chrometest/chrometest.go
  - internal/e2e/harness_test.go
  - internal/e2e/security_test.go
  - internal/e2e/capture_test.go
  - internal/e2e/screenshots_test.go
  - internal/e2e/layout_test.go
  - internal/web/routes_test.go
  - internal/mcpserver/useronly_test.go
  - internal/mcpserver/read_tools_test.go
  - internal/mime/fuzz_test.go
  - internal/mime/race_on_test.go
  - internal/mime/race_off_test.go
  - internal/app/app.go
  - internal/service/service.go
  - cmd/email-mcp/console.go
  - test/interop/
  - test/sitecheck/
  - .github/workflows/ci.yml
  - .github/workflows/interop.yml
  - .github/workflows/release.yml
  - Makefile
---

This page shows the kinds of test of email-mcp, what each one proves and how to
run it. It also says how to test code that talks to a mail server or to the web
portal (the pages that email-mcp shows in the browser). Read it before you write
the tests of a new feature.

## The kinds of test

| Kind | Where | How it runs | What it proves |
|---|---|---|---|
| Unit tests | the `_test.go` files next to the code | `go test -race ./...` (`make test`) | One package does what its comments say, often with fake parts around it. |
| End-to-end tests | `internal/e2e` | `go test -race ./...` | The whole program keeps its security rules, seen from the outside. |
| Screenshots and layout | `internal/e2e`, build tag `screenshots` | `make screenshots` | Each page of the manual exists and shows the expected texts. The images of the manual come from here. |
| Site tests | `test/sitecheck` | `go test ./...`, and `make site` with the build tags | The checker of the docs site finds what it must find. |
| Interop test | `test/interop`, build tag `interop` | by hand or in `interop.yml` | The mail code works with real Dovecot and Postfix servers. |
| Fuzz targets | `Fuzz*` functions in `internal/mime`, `internal/signing` and `internal/thirdparty/xhtml` | `go test`, with their seed inputs only | The checks of each target hold for its seed inputs. |

A build tag is a word that a Go file asks for before it is built, in a line
such as `//go:build interop`. `go test ./...` does not build such a file, so
those tests need the tag on the command line.

## Unit tests

A unit test sits next to the code that it tests, in the same package. The tests
are table-driven: one test runs the same check over a table of cases. They use
`github.com/stretchr/testify/require`, which stops the test at the first check
that fails.

Run one package with the race detector (a check that finds two goroutines that
use the same memory at the same time) and with no cached result (`-count=1`):

```sh
go test -race -count=1 ./internal/outbox/
```

## Test servers

Code that talks to a mail server or to the web portal is tested against small
servers in `internal/testutil`. Each one starts in the test, listens on
127.0.0.1 with a random port, and stops when the test ends.

- **`imaptest`** in {{< src "internal/testutil/imaptest/imaptest.go" >}} is an
  IMAP server (IMAP: Internet Message Access Protocol, for reading mail) that
  keeps its mail in memory. It is built on the server of `go-imap`. `New` starts
  it, with TLS (Transport Layer Security: the encryption of a connection) from
  the first byte, with STARTTLS (a plain connection that turns into TLS), or
  plain (`WithMode`). Options change its answers, for example the capabilities
  that it offers (`WithCaps`) or an `APPEND` that fails (`WithAppendError`).
  Methods such as `Commands`, `FetchedSections` and `Unseen` let a test read
  what the server saw.
- **`smtptest`** in {{< src "internal/testutil/smtptest/smtptest.go" >}} is an
  SMTP server (SMTP: Simple Mail Transfer Protocol, for sending mail). It needs
  `AUTH PLAIN` before it takes a message, and it keeps every message that it
  accepts (`Messages`). Options refuse a recipient (`RejectRcpt`), refuse or
  lose a message (`WithDataHook`, `ErrDropConnection`) or slow it down
  (`WithDelay`).
- **`webtest`** in {{< src "internal/testutil/webtest/webtest.go" >}} lets a
  test reach the web portal by its own name, `email-mcp.localhost`. The name
  lookup of a test may not know that name, for example on a CI runner (the
  computer that runs the checks of GitHub). So `Transport` connects to
  127.0.0.1 and keeps the name in the address and in the `Host` header, as a
  browser does.

Both mail servers make a new self-signed certificate (a certificate that no
authority signed) each time a test starts one. A test reads `CertSHA256` to
check a pinned certificate (one whose fingerprint the person chose to trust),
or `CertPool` to check the normal verification. These packages are for tests
only.

## The end-to-end tests

An end-to-end test drives the whole program from the outside. The package
`internal/e2e` has test files only. `newHarness` in
{{< src "internal/e2e/harness_test.go" >}} opens the program with `app.Open`,
as `serve` opens it: a new data folder, a master key file outside it, and a log
at the Debug level in a buffer, so the test sees every line of every part. The
test then drives the program in three ways:

- as an agent on stdio, with a client of the go-sdk (the official Go library
  for MCP, Model Context Protocol) on an in-memory connection (`client`);
- as an agent on local HTTP, through the real HTTP handler with a token
  (`httpEndpoint`, `httpClient`);
- as a person in a browser, through the web portal, with a session cookie and
  the CSRF token of a form (`signIn`; CSRF, cross-site request forgery, is an
  attack where another site sends a form in the name of the person).

The mail servers are `imaptest` and `smtptest` (`newMailServers`). No test
uses the network outside the computer. `harnessOptions` can open a hosted
install (one that is not local) or set the clock of the program.

{{< src "internal/e2e/security_test.go" >}} holds the tests of the security
checklist of the design. The comment above each test says what it checks:

| Test | What it requires |
|---|---|
| `TestNoSecretsInLogs` | The log of a full scenario holds no test secret and no token. |
| `TestNoPlaintextSecretsInDB` | The files of the database hold no password, message, address of a third party or token in plain text, also while sends wait. |
| `TestEveryWriteToolRefusedBelowItsLevel` | Each tool is refused at each level below the one it needs, and the call reaches no mail server and stores no action. |
| `TestConfirmationCannotBeSkipped` | A confirmation acts once, for the exact request that the person saw, in time, and only in the channel where it was asked. |
| `TestTrashMoveBlockedBelowLevel5` | Below level 5, no tool and no approval moves mail to the Trash, by any path. |
| `TestPlainTextToRemoteRefused` | A connection without TLS is allowed only to a mail server on this computer, on a local install. |
| `TestTLSCertificateChecksEnforced` | A certificate that differs from the pin, that no trusted authority signed, or that is for another host name is refused, and no login reaches the server. |
| `TestLocalUILinksRevealNothingWithoutSession` | A link of the portal, opened without the session of the person, shows nothing and changes nothing. |
| `TestLocalUIRefusesWrongHost` | A request with a wrong `Host` header gets 403, on the portal and on the HTTP endpoint. |
| `TestLocalUIRefusesCrossOriginPost` | A form post from another origin gets 403, even with the session cookie and a valid CSRF token. |

`TestEveryToolCallIsInTheActivityLog` in
{{< src "internal/e2e/capture_test.go" >}} requires an entry in the activity
log for every tool call, however it ends, and reads the log on the Activity
page.

The tests with the build tag `screenshots` are in the same package. They make
the images of the manual ({{< src "internal/e2e/screenshots_test.go" >}}) and
check the layout of the Connections page in Chrome
({{< src "internal/e2e/layout_test.go" >}}). They need Chrome, so they run in
`make screenshots` and not in `go test ./...`. `Start` in
{{< src "internal/testutil/chrometest/chrometest.go" >}} starts Chrome with no
window. It turns off the sandbox of Chrome (the walls that Chrome puts around
a page) only when the variable `CI` is set, because the runners of CI do not
allow it. The page [Local development][shot] says how to add a shot.

## Tests that check every item of a list

Some tests read a list that the code keeps, and check each item. A new route
or a new tool is then checked with no new test:

- The routes tests in {{< src "internal/web/routes_test.go" >}} go over the
  table `routes` of the portal. Each route names GET or POST, each POST route
  refuses a request without the CSRF token, and no GET route changes anything.
- `TestEveryToolRunsThroughSafe` in
  {{< src "internal/mcpserver/read_tools_test.go" >}} calls each tool that the
  server lists, with a caller that is refused. A tool that was not added with
  `addTool` answers in another way, and the test fails.
- The user-only tests in {{< src "internal/mcpserver/useronly_test.go" >}}
  parse the Go files of `mcpserver` (not its tests). They fail when the code
  names a use case that only the person may call, such as `ApprovePending`, or
  imports `web` or `tokens`.

## Clocks and fakes

The code has a few points where a test puts in its own part:

- `app.Config.Now` in {{< src "internal/app/app.go" >}} is the clock of the
  limiter, the confirm manager, the outbox, the audit log, the tokens, the web
  server and the service. A test moves it forward to let a request state expire
  without waiting. Nil means `time.Now`.
- `service.Deps.Probe` in {{< src "internal/service/service.go" >}} tests the
  settings and the password of a new account before it is saved. Nil means
  `imapsmtp.Probe`, and a test puts in its own function.
- The struct `system` in {{< src "cmd/email-mcp/console.go" >}} holds the
  terminal check, the password prompt, the browser and the discovery of server
  settings. The command tests call `runWith` with a fake `system`, so they need
  no terminal, open no browser and use no network.

## The race detector

`make test`, CI and the release run the tests with `-race`. The race detector
makes the code many times slower. So a few packages have two files,
`race_on_test.go` (build tag `race`) and `race_off_test.go` (build tag
`!race`), that set the constant `raceEnabled`. Go sets the tag `race` by
itself under `-race`. A test that measures time skips that check, or skips
itself, when `raceEnabled` is true. The tests of the work limits of
`internal/mime` also check memory, which holds with the detector too. See
{{< src "internal/mime/race_on_test.go" >}} and
{{< src "internal/mime/race_off_test.go" >}}.

## Fuzz targets

A fuzz target is a test that Go can run with many generated inputs. The
targets are the `Fuzz*` functions of `internal/mime` (for example in
{{< src "internal/mime/fuzz_test.go" >}}), `internal/signing` and
`internal/thirdparty/xhtml`. Each one adds its seed inputs with `f.Add`.
`go test` runs each target with these seeds only, as a normal test. No workflow
generates more inputs.

To fuzz for longer, run one target in its package. This command runs no normal
test (`-run='^$'`), generates inputs for `FuzzHTMLToText` (`-fuzz`) and stops
after 5 minutes (`-fuzztime`):

```sh
go test -run='^$' -fuzz='^FuzzHTMLToText$' -fuzztime=5m ./internal/mime/
```

Go keeps an input that fails in `testdata/fuzz/` of the package. Then each
`go test` runs it too.

## Build tags

| Tag | Files | What they need | Who runs them |
|---|---|---|---|
| `interop` | `test/interop` | Dovecot and Postfix in Docker | `interop.yml`, or you by hand |
| `screenshots` | the shots and the layout test in `internal/e2e`, `internal/testutil/chrometest`, the phone test of `test/sitecheck`, `test/socialcard` | Chrome | `make screenshots`, `make site` and `make social-card` |
| `sitebuild` | the table tests of {{< src "test/sitecheck/" >}} | a built site with the test tables | `make site` |

`go vet`, staticcheck and golangci-lint read the files of all three tags, so a
change that breaks one of these files fails CI, also where its test does not
run.

### The interop test

The interop test in {{< src "test/interop/" >}} runs the mail scenario against
real servers: Dovecot for IMAP and Postfix for SMTP submission, in Docker. It
drives the provider code of `internal/provider/imapsmtp` step by step (the
probe, the folders, search, tags, moves, a draft, a send, a move to the Trash),
and then the whole program through `app.Open` and an MCP client. A step that
fails stops the scenario.

These commands make the test certificates, start the servers and wait until
they are healthy (`-d`: in the background, `--wait`), run the test, and stop
the servers and delete their data (`-v`: the volumes):

```sh
go run ./test/interop/gencerts test/interop/certs
docker compose -f test/interop/compose.yaml up -d --wait
go test -count=1 -tags interop ./test/interop/ -v
docker compose -f test/interop/compose.yaml down -v
```

`gencerts` makes a new test authority and a server certificate for each run.
It keeps the key of the authority only in memory, so nothing else can be signed
with it. The servers publish their ports on 127.0.0.1 only. `-count=1` makes Go
run the test every time: without it, Go can print a cached PASS also when the
servers are down, because the test cache does not see the network. Without the
servers, the test fails with a message that names the command to run. `-v`
prints each step as it runs.

`interop.yml` runs it every night, for every tag that starts with `v`, and by
hand (`workflow_dispatch`). It adds `-race`, prints the logs of the servers,
and stops them also when the test failed.

## What CI and the release run

| Workflow | When | What it runs |
|---|---|---|
| {{< src ".github/workflows/ci.yml" >}} | each push and each pull request | `go vet` with the three tags, `go test -race ./...`, staticcheck and golangci-lint with the tags, `goreleaser check`, govulncheck, and `make site`, which runs the screenshot, layout and site tests |
| {{< src ".github/workflows/interop.yml" >}} | each night, each `v*` tag, by hand | the interop test with `-race -count=1` |
| {{< src ".github/workflows/release.yml" >}} | each `v*` tag | `go vet ./...` without tags, `go test -race ./...`, govulncheck and the version check of `server.json`, before it builds anything |

The {{< src "Makefile" >}} runs the same commands on your computer: `make test`,
`make lint`, `make vuln` and `make site`.

## Related pages

- [Local development](/tech/work-on-the-code/local-development/)
- [Making a release](/tech/work-on-the-code/release-process/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [Map of packages](/tech/start-here/package-map/)

[shot]: /tech/work-on-the-code/local-development/#add-or-change-a-shot
