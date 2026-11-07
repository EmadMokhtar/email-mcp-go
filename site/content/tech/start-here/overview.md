---
title: Architecture overview
description: The whole program on one page, with its parts, its two doors, its run modes and the rules that hold in all code.
weight: 10
code_areas:
  - cmd/email-mcp/main.go
  - cmd/email-mcp/serve.go
  - internal/app/app.go
  - internal/app/kek.go
  - internal/service/service.go
  - internal/service/principal.go
  - internal/mcpserver/server.go
  - internal/mcpserver/useronly_test.go
  - internal/store/store.go
  - internal/store/tenants.go
  - internal/domain/mail/backend.go
---

This page shows the whole program on one page: the parts, the two doors into
the use cases, the run modes, and the rules that every part follows. A use case
is one job of the program for a caller, such as "read one message" or "send
this mail". A door here is a way into the program: the code that a caller goes
through to reach the use cases. Read this page first if you want to change the
code and do not know it.

email-mcp is an MCP server. MCP (Model Context Protocol) is the open standard
that lets an AI (artificial intelligence) app call the tools of another
program. A tool is one action, such as `get_message`. The agent is the AI
program that calls the tools. Here the tools give the agent controlled access
to mail, through IMAP (Internet Message Access Protocol, for reading mail) and
SMTP (Simple Mail Transfer Protocol, for sending mail).

## One program, several commands

email-mcp is one Go program. The variable `commands` in
{{< src "cmd/email-mcp/main.go" >}} lists its commands, in the order that the
help shows them. `serve` is the command that an MCP client starts. An MCP
client is the AI app that talks to the server, such as Claude Code. The other
commands are for the person at the terminal: `setup` signs a browser in to the
web portal, and commands such as `account` and `token` work without a browser.

The web portal is the local web site where the person connects accounts, sets
what the agent may do, and answers the actions that wait. The user manual calls
it the web pages. See [The web pages](/manual/daily-use/web-pages/) for what the
person sees.

## The parts, from the outside in

{{< diagram "layers-and-doors" >}}

The program has these parts, from the outside in. They are roles, not the
import layers of the packages: the page
[Map of packages](/tech/start-here/package-map/) has those.

1. The transport carries the MCP messages. On stdio (standard input and
   output: the client starts the server and talks to it through two pipes) it
   is `serveStdio` in {{< src "cmd/email-mcp/serve.go" >}}. On local HTTP
   (HyperText Transfer Protocol) it is the HTTP server in the same file, with
   `limitRequests`, and the checks of the package `httpserve`. The page "Local
   HTTP mode" in the group "Ways in" goes deeper.
2. `mcpserver` holds the tools. A tool handler turns its arguments into one
   call of `service`, and turns the answer into a tool result. A tool handler
   is the function that runs one tool. It checks no permission itself. See
   [One tool call, step by step](/tech/start-here/tool-call/).
3. `service` runs the use cases. A use case that a tool calls on the mail of
   one account finds the caller, finds the account in the tenant of the caller
   (the owner of a set of accounts), checks the permission (`authorize`), and
   only then calls the mail backend. The use cases of the person are guarded
   by their door, and some also call `authorize` (see "Two doors into the use
   cases" below). See
   [One tool call, step by step][permission].
4. Under `service` are the parts that it uses:
   - `policy` decides what the agent may do with an account, and limits how
     often it may send mail and move mail to the Trash. The page
     [Permissions](/tech/inside-the-server/permissions/) goes deeper.
   - `confirm` keeps the actions that wait for the "yes" of the person, such as
     a send. The page [The send flow](/tech/inside-the-server/send-flow/) goes
     deeper.
   - `outbox` queues the outgoing mail for its undo window (the time when a
     send can still be cancelled), and sends it from a background worker. The
     page [The send flow](/tech/inside-the-server/send-flow/) goes deeper.
   - `audit` keeps the activity log: what the agent did, and what the person
     did on a page. The page
     [Activity log and program log](/tech/inside-the-server/audit-and-logging/)
     goes deeper.
   - `store` keeps all data in one SQLite database (a database in one file).
     `secrets` encrypts the secrets in it. It reads the master key from the
     keychain of the operating system or from a file, and the master key
     protects the keys that the database stores. The page
     [Storage and secrets](/tech/inside-the-server/storage-and-secrets/) goes
     deeper.
   - The mail backend talks to the mail servers. The interface is
     `mail.Backend`, and `provider/imapsmtp` implements it. The page "The mail
     backend" goes deeper.

`web` serves only the person: it is the web portal. `cmd/email-mcp` serves both.
Its command `serve` holds the transport of the agent: `serveStdio`,
`localPrincipal`, `limitRequests` and the HTTP server, in `serve.go`. Its other
commands, such as `setup` and `account`, are for the person. The package `app`
builds all the parts and connects them. Its function `app.Open` does this.
`serve`, `setup` and every command that reads the data call it. The page
[Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/) goes
deeper.

## Two doors into the use cases

The agent and the person reach `service` through different doors:

- The agent uses the tools of `mcpserver`, through the transport that
  `email-mcp serve` runs.
- The person uses the web portal (`web`) in a browser, and the other commands
  of `cmd/email-mcp` at a terminal (a window where you type commands).

Some use cases are only for the person, for example `ApprovePending`. The list
`userOnlyCalls` in {{< src "internal/mcpserver/useronly_test.go" >}} names the
ones that a test guards, and it is the list to trust for them. Other use cases,
such as `ProbeAccount` and `QueuedSends`, are called only by the web portal or
the commands, and no test names them. In the drawing, the dashed box holds the
user-only use cases. The person's door reaches them, and the agent's door does
not.

Two tests in that file keep tools away from them.
`TestNoToolReachesTheUserOnlyUseCases` reads the Go files of `mcpserver`,
without the tests. It fails when a file names one of these calls, or imports
`web` or `tokens`. `TestNoToolIsNamedForAUserOnlyUseCase` lists the tools of
the server and fails when a tool name has a word such as `approve` or
`settings`.

The use cases of the person are guarded by their door: no tool calls them.
They read the principal (see below) and work only in its tenant. Some of them,
such as `UpdateSettings`, `RemoveAccount` and `ProbeAccount`, check no level.
Others also call `authorize` for the account of the action: `ApprovePending`
reaches it through `begin` and `authorizeWrite`, and `PendingDetail` calls it
for a send that waits, because it builds the message again.

The web portal reads the store itself for its sign-in sessions and for some
lists, such as the accounts. Every change of an account or a setting goes
through `service`, from the portal and from the commands.

## The caller and the tenant

A tenant is the owner of a set of accounts. A local install has one tenant, with
the id `local`: `store.LocalTenantID` in
{{< src "internal/store/tenants.go" >}}.

The principal is the caller of a request: the tenant, the scopes of the access
token, and the name of the client. An access token is a secret that a program
sends with each request on local HTTP. Scopes are the permissions that a token
carries, such as `email:read`. The type is `service.Principal` in
{{< src "internal/service/principal.go" >}}. A principal travels in the Go
context (`service.WithPrincipal`), and every use case reads it from there.

Each door makes its own principal:

- On stdio, `localPrincipal` in {{< src "cmd/email-mcp/serve.go" >}} gives the
  local tenant and no scope limit. Only the client that started the process can
  write to its standard input.
- On HTTP, `httpserve.Principal` reads the tenant and the scopes from the
  access token of the request.
- The web portal uses the tenant of the browser session, and the commands use
  the local tenant (`userContext` in `cmd/email-mcp/console.go`).

The tenant never comes from the arguments of a tool. The package comment of
{{< src "internal/store/store.go" >}} states the rule of the store: every query
for one tenant has a `tenant_id = ?` condition. So a caller cannot reach the
data of another tenant by guessing an id.

## Run modes

`serve` serves MCP on stdio by default, and on local HTTP with `--http`. It
calls `app.Open`, then `App.Run`, which starts the background work in
goroutines (functions that Go runs at the same time as the rest of the
program):

- the outbox worker, every second (`outboxTick` in
  {{< src "internal/app/app.go" >}});
- the daily jobs, at the start and then every 24 hours (`jobEvery`);
- the sweep that closes mail backends that nobody used for a while, every 5
  minutes (`backendSweepEvery`).

Before it starts them, `Run` cleans up after a program that stopped in the
middle of its work:

- `Outbox.RecoverInterrupted` marks a send that has been in the state
  `sending` for more than 15 minutes (`recoverAfter` in `outbox`) as failed.
  It never sends it again.
- `expireStaleApproved` ends the approved actions that have no result after
  their time is over.

The web portal does not start with `serve`. It starts the first time that a
link to it is needed (`web.Server.EnsureStarted`). `setup` serves the portal
until the person presses Ctrl-C (holds the Ctrl key and presses C).

An MCP client starts its own `serve` process. So two clients mean two
processes, and `setup` can run at the same time as both. They all use one data
directory and one database. These parts make that safe:

- A lock file guards the master key on the first start, so two processes do
  not each make a key (`loadKEK` in {{< src "internal/app/kek.go" >}}). The
  page [Storage and secrets](/tech/inside-the-server/storage-and-secrets/) goes
  deeper.
- The database runs in WAL mode (write-ahead log: a SQLite mode where readers
  and one writer work at the same time). See
  {{< src "internal/store/store.go" >}}.
- A send starts only after an atomic claim of its outbox item: one database
  statement that moves the item from `queued` to `sending` only when it is
  still queued (`store.ClaimOutbox`). Of two processes, one wins. The page
  [The send flow](/tech/inside-the-server/send-flow/) goes deeper.
- Browser sessions and access tokens are rows in the database. So a session
  that one process made works in every other process.
- The backend cache of each process (the cache of one mail backend for each
  account) compares a fingerprint of the account settings and the stored
  password on every call (`service.BackendCache`). The fingerprint is a hash
  that changes when the settings or the password change. When another process
  changes an account, the cache builds the backend again. The page "The mail
  backend" goes deeper.

## Rules that hold in all code

- The agent gets fixed error texts, not the text of an error, which can hold
  text from a mail server. The one exception is a reached limit
  (`*policy.RateLimitError`): its text is a fixed sentence with the kind of
  limit and the wait. See [One tool call, step by step][errors].
- The program log has ids, counts and fixed codes only: never a secret, an
  address, a subject or a body. The page
  [Activity log and program log](/tech/inside-the-server/audit-and-logging/)
  goes deeper.
- Text that third parties wrote, such as a mail body, is cleaned and marked as
  untrusted before the agent gets it. See
  [One tool call, step by step][cleaned].
- Mail is never deleted for good. A delete moves the mail to the Trash, and
  the comment of `mail.Backend` asks this of every implementation. The page
  "The mail backend" goes deeper.
- Every check fails closed: when a check cannot decide, the answer is "no".
  For example, `policy.Decide` refuses an action that it does not know, and it
  counts a damaged level of an account as level 0, no access
  (`account.MailOff`). The page "Security design" goes deeper.

## The mail backend boundary

`mail.Backend` in {{< src "internal/domain/mail/backend.go" >}} is the
provider-neutral interface: it is not tied to one mail provider, and it uses no
idea that belongs to one protocol. The use cases that read and change mail call
only this interface. The choice of the backend is in the backend cache of
`service`. Only IMAP and SMTP exist in the code. Other providers and a hosted
mode (one server that many people use) are planned in the design documents and
are not built. `PRODUCT.md` says the same for hosted mode.

## The design documents

The design documents under `docs/superpowers/specs/` are history. They record
why the program was built this way, but the code changed after them. Where a
design document and the code differ, the code is right. The main one is the
design spec of the rewrite:
{{< src "docs/superpowers/specs/2026-10-27-email-mcp-rewrite-design.md" >}}.

## Related pages

- [Map of packages](/tech/start-here/package-map/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [The web pages](/manual/daily-use/web-pages/) in the user manual
- [User manual](/manual/)

[errors]: /tech/start-here/tool-call/#errors-become-fixed-texts
[permission]: /tech/start-here/tool-call/#where-the-permission-is-checked
[cleaned]: /tech/start-here/tool-call/#where-text-from-the-mail-is-cleaned
