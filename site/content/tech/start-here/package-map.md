---
title: Map of packages
description: One row for each Go package, its layer in the import order, and the place to add code for a feature.
weight: 20
code_areas:
  - cmd/email-mcp/
  - internal/
  - go.mod
  - .github/dependabot.yml
  - internal/thirdparty/xhtml/README.md
---

This page has one row for each Go package of the program email-mcp and of its
test helpers: what it does, where it sits in the import order, and where to add
code. Use it to find the code of a feature, and to check what a package may
import.

A package is a folder of Go files that build together. A layer is the place of
a package in the import order. On this page, a short name such as `app` means
the package `internal/app`. `cmd/email-mcp` is the one package of the program
outside `internal/`. Go does not let code outside this module import a package
under `internal/`.

The module has two more packages, under `test/`. `test/sitecheck` checks the
built docs site, and `test/interop/gencerts` makes the certificates of the
interop test (the test against real mail servers). They are tools of the
repository and not part of the program, so this page leaves them out.

## The layer rule

A package imports only packages of lower layers. The layer number of a package
is the longest path of imports from it down to a package with no imports of
this module. Go refuses an import cycle (two packages that import each other,
directly or through others), so every package has one number.

This command prints each package under `cmd/` and `internal/`, with the
packages that it imports:

```sh
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./cmd/... ./internal/...
```

`-f` gives the template of each output line: the path of the package, then the
paths that it imports, with a space between them. The command reads the code
that a normal build uses, without the test files. Run it again after you
change an import, and compare the output with the layers below.

{{< diagram "dependency-layers" >}}

The layers, from the top:

- Layer 9: `cmd/email-mcp`.
- Layer 8: `app`.
- Layer 7: `web`.
- Layer 6: `httpserve`.
- Layer 5: `connect`, `mcpserver` and `tokens`.
- Layer 4: `service`.
- Layer 3: `audit`, `confirm`, `outbox` and `provider/imapsmtp`.
- Layer 2: `policy` and `secrets`.
- Layer 1: `discovery`, `mime` and `store`.
- Layer 0, with no imports of this module: `domain/account`, `domain/mail`,
  `errtype`, `inflight`, `signing`, `thirdparty/xhtml` and `version`.
- Tests only: `testutil/imaptest`, `testutil/smtptest`, `testutil/webtest`,
  `testutil/chrometest` and `e2e`. They import no package of this module
  outside their tests. `testutil/chrometest` builds only with the build tag
  `screenshots` (a word that a Go file asks for before it is built), so the
  command above does not list it. `e2e` has test files only.

## All packages

The table has the packages that the command above prints, and
`testutil/chrometest`, which needs the build tag. The main entry point is the
name to read first in `go doc`.

MCP (Model Context Protocol) is the open standard that lets an AI (artificial
intelligence) app call the tools of another program. IMAP (Internet Message
Access Protocol) is the protocol for reading mail. SMTP (Simple Mail Transfer
Protocol) is the protocol for sending mail. HTTP (HyperText Transfer Protocol)
is the protocol of the web. HTML (HyperText Markup Language) is the language
of web pages, and of the body of many mails.

| Package | Layer | Job | Main entry point |
|---|---|---|---|
| {{< src "cmd/email-mcp/" >}} | 9 | The program and its commands. | `commands`, `runServe` |
| {{< src "internal/app/" >}} | 8 | Builds all parts and connects them. Runs the background work, and stops the parts in an order that never loses a send. | `app.Open`, `app.App` |
| {{< src "internal/web/" >}} | 7 | The web portal of the person: sign-in, accounts, settings, approvals, activity and connections. | `web.New`, `web.Server` |
| {{< src "internal/httpserve/" >}} | 6 | MCP over local, stateless HTTP, with the checks of each request. | `httpserve.Handler`, `httpserve.Principal` |
| {{< src "internal/connect/" >}} | 5 | The parts of connecting an account that the portal and the commands share: fixed error texts, and the decision to offer a certificate pin (to trust one certificate by its fingerprint). | `connect.ErrorText`, `connect.CheckCertificate` |
| {{< src "internal/mcpserver/" >}} | 5 | The tools of the agent, and the shape of their results and errors. | `mcpserver.New` |
| {{< src "internal/tokens/" >}} | 5 | Makes and checks the access tokens of local HTTP. The store keeps only a hash of each token. | `tokens.Manager` |
| {{< src "internal/service/" >}} | 4 | The use cases. Finds the caller and the account, checks the permission, and only then calls the mail backend. | `service.Service`, `service.Principal` |
| {{< src "internal/audit/" >}} | 3 | The activity log: one entry for each tool call, and one for each action of a use case. | `audit.Log` |
| {{< src "internal/confirm/" >}} | 3 | The actions that wait for the "yes" of the person, bound to the exact request. | `confirm.Manager` |
| {{< src "internal/outbox/" >}} | 3 | The queue of outgoing mail with its undo window, and the worker that sends it at most once. | `outbox.Outbox` |
| {{< src "internal/provider/imapsmtp/" >}} | 3 | The IMAP and SMTP backend: secure connections, a small pool of IMAP connections, reading, drafts and sending. | `imapsmtp.New`, `imapsmtp.Backend` |
| {{< src "internal/policy/" >}} | 2 | Decides what the agent may do with an account, and limits sends and moves to the Trash. | `policy.Decide`, `policy.Limiter` |
| {{< src "internal/secrets/" >}} | 2 | Encryption: a `Secret` type that never prints its value, sealed (encrypted) values, the vault (the part that seals and opens values with the keys in the database), and the sources of the master key. | `secrets.Vault`, `secrets.Secret` |
| {{< src "internal/discovery/" >}} | 1 | Suggests IMAP and SMTP settings from an email address. | `discovery.Finder` |
| {{< src "internal/mime/" >}} | 1 | Mail content: cleans text from third parties, turns HTML into text, and builds outgoing mail. | `mime.CleanUntrusted`, `mime.Build` |
| {{< src "internal/store/" >}} | 1 | The one package that talks to the SQLite database, with its migrations. | `store.Open`, `store.Store` |
| {{< src "internal/domain/account/" >}} | 0 | Plain types: tenants, accounts, areas, permission levels, server settings and limits. | `account.Account` |
| {{< src "internal/domain/mail/" >}} | 0 | The provider-neutral mail types and the interface of a mail backend. | `mail.Backend` |
| {{< src "internal/errtype/" >}} | 0 | Names the Go types of an error chain, so the log never holds the text of an error. | `errtype.Chain` |
| {{< src "internal/inflight/" >}} | 0 | Counts the work that runs, so a stop can refuse more work and wait. | `inflight.Tracker` |
| {{< src "internal/signing/" >}} | 0 | Small signed tokens that the agent passes back: page tokens, cursors and request states. | `signing.Signer` |
| {{< src "internal/thirdparty/xhtml/" >}} | 0 | A copy of the HTML parser of `golang.org/x/net`, with a limit on its work. | `xhtml.ParseWithLimit` |
| {{< src "internal/version/" >}} | 0 | The version of the build. | `version.Version` |
| {{< src "internal/testutil/imaptest/" >}} | tests | A small in-memory IMAP server for tests. | `imaptest.New` |
| {{< src "internal/testutil/smtptest/" >}} | tests | A small SMTP server for tests. | `smtptest.New` |
| {{< src "internal/testutil/webtest/" >}} | tests | Lets a test reach the web portal by its name, `email-mcp.localhost`. | `webtest.Transport` |
| {{< src "internal/testutil/chrometest/" >}} | tests | Starts a headless Chrome (Chrome with no window) for the screenshot tests. | `chrometest.Start` |
| {{< src "internal/e2e/" >}} | tests | End-to-end tests: each one opens the whole program with `app.Open`. | the test files |

## Imports that you may not expect

These are facts of the code. The design documents do not show them.

1. `service` imports `provider/imapsmtp`. The backend cache builds an
   `*imapsmtp.Backend`. The probe of an account and errors such as
   `imapsmtp.ErrAuthFailed` come from there too. So the use cases talk to
   `mail.Backend`, but the choice of the backend is in `service`
   ({{< src "internal/service/backends.go" >}}).
2. `mcpserver` imports `imapsmtp`, `outbox`, `audit`, `confirm`, `policy`,
   `signing` and `mime`. It uses their types, their error values and their
   pure functions (functions that change no state). It does not import `store`.
   It must not import `web` or `tokens`: the test in
   {{< src "internal/mcpserver/useronly_test.go" >}} fails if it does.
3. `web` reads the store itself for its sign-in sessions, its setup tokens and
   the account list. Every change of an account, a setting or an approval goes
   through `service`.
4. `web` imports `httpserve` for the shared check of the port of a loopback
   address (`httpserve.LoopbackPort`), the limit of a request body
   (`httpserve.MaxBodyBytes`) and the log of the HTTP server
   (`httpserve.ErrorLog`).
5. `tokens` imports `service`. `tokens.Manager` implements
   `service.TokenMaker`, and its check of a token returns a
   `service.Principal`. `httpserve` imports `tokens`.
6. `secrets` and `policy` import `store`. The vault keeps its keys with methods
   of the store, and the limiter keeps its counters there.
7. `connect` imports `service` and `imapsmtp`. Both `web` and `cmd/email-mcp`
   use it, so the portal and the commands give the same texts.
8. Only `app` imports both `mcpserver` and `web`. `cmd/email-mcp` reaches the
   web portal through `app`.

## Where to add code

- A tool: a handler in `mcpserver` (`read_tools.go` or `write_tools.go`),
  added with `addTool`, and its use case in `service`. A use case that only the
  person may call must not get a tool. See [Architecture overview][doors].
- A mail provider: a package under `internal/provider/` that implements
  `mail.Backend`, and the backend factory in
  {{< src "internal/service/backends.go" >}}.
- A page of the web portal: `web`, with its template in
  {{< src "internal/web/templates/" >}}.
- A table: a migration file in {{< src "internal/store/migrations/" >}} and its
  methods in a file of `store`. A migration is a numbered SQL (Structured
  Query Language) file that changes the structure of the database, and each
  one runs once, in order. Read the
  warning about tables with `ON DELETE CASCADE` in the package comment of
  `store` first.
- A command: `cmd/email-mcp`, with an entry in the list `commands` in
  {{< src "cmd/email-mcp/main.go" >}}.

A package that you add needs a row on this page and a place in the layers.

## Libraries

These are the direct dependencies in {{< src "go.mod" >}}. "Imported by" names
the packages of the program. Tests import more.

| Module | Imported by | Why |
|---|---|---|
| `github.com/modelcontextprotocol/go-sdk` | `cmd/email-mcp`, `app`, `httpserve`, `mcpserver` | The official Go SDK (software development kit) for MCP: the server, the stdio and HTTP transports, typed tool handlers and the check of a bearer token. |
| `github.com/emersion/go-imap/v2` | `provider/imapsmtp`, `testutil/imaptest` | The IMAP client, and the IMAP server of the tests. |
| `github.com/emersion/go-smtp` | `provider/imapsmtp`, `testutil/smtptest` | The SMTP client, and the SMTP server of the tests. |
| `github.com/emersion/go-sasl` | `provider/imapsmtp`, `testutil/smtptest` | The SMTP login with `AUTH PLAIN`, the SMTP command that sends the user name and the password. |
| `github.com/emersion/go-message` | `mime` | Reads and writes the parts of a message. |
| `github.com/microcosm-cc/bluemonday` | `mime` | Cleans HTML (`mime.SanitizeHTML`). |
| `github.com/google/jsonschema-go` | `mcpserver` | Builds JSON (JavaScript Object Notation) schemas: the output of the send and Trash tools, whose first call may return a question and no output, and the confirmation form. |
| `modernc.org/sqlite` | `store` | SQLite written in pure Go. So the program builds with `CGO_ENABLED=0` (no C compiler), and the binary stays static. |
| `github.com/zalando/go-keyring` | `secrets` | Keeps the master key in the keychain of the operating system. |
| `golang.org/x/net` | `discovery`, `mime`, `thirdparty/xhtml` | International domain names, the public suffix list, and the list of known HTML names. |
| `golang.org/x/term` | `cmd/email-mcp` | Reads a password at the terminal without showing it. |
| `golang.org/x/sys` | `app` | The file lock on Windows only ({{< src "internal/app/lock_windows.go" >}}). |
| `github.com/stretchr/testify` | tests only | The checks of the tests (`require`). |
| `github.com/chromedp/chromedp`, `github.com/chromedp/cdproto` | tests only | Drives Chrome for the screenshots of the docs site. |

Two of them are not updated by Dependabot, the GitHub service that opens pull
requests to update dependencies. Its settings are in
{{< src ".github/dependabot.yml" >}}:

- `go-imap/v2` is a beta version. The project pins it to exactly
  `v2.0.0-beta.8`, and it changes the version only on purpose. Only
  `provider/imapsmtp` and the test server import it, so the rest of the code
  depends on `mail.Backend` and not on this library.
- `chromedp` is only in the tests. An update would propose a release of a
  program that did not change.

## The copied HTML parser

`internal/thirdparty/xhtml` is the only third-party code that lives in the
repository. It is the HTML parser of `golang.org/x/net`, with a limit on the
work of the parser. Only `mime` imports it. Its
{{< src "internal/thirdparty/xhtml/README.md" >}} lists each change and the
steps to update it. The page "Mail content" in the group "Mail" explains why
the limit is needed.

## Related pages

- [Architecture overview](/tech/start-here/overview/)
- [One tool call, step by step](/tech/start-here/tool-call/)

[doors]: /tech/start-here/overview/#two-doors-into-the-use-cases
