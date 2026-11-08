---
title: One tool call, step by step
description: The path of one get_message call from the MCP client to the IMAP server and back, with each check and each record on the way.
weight: 30
code_areas:
  - cmd/email-mcp/serve.go
  - internal/httpserve/httpserve.go
  - internal/app/app.go
  - internal/mcpserver/server.go
  - internal/mcpserver/capture.go
  - internal/mcpserver/read_tools.go
  - internal/mcpserver/shape.go
  - internal/mcpserver/errors.go
  - internal/service/service.go
  - internal/service/read.go
  - internal/service/backends.go
  - internal/service/calls.go
  - internal/service/write.go
  - internal/service/accounts.go
  - internal/service/send.go
  - internal/service/approvals.go
  - internal/policy/limiter.go
  - internal/provider/imapsmtp/backend.go
  - internal/provider/imapsmtp/fetch.go
  - internal/provider/imapsmtp/pool.go
---

This page follows one call of the tool `get_message` from the MCP (Model
Context Protocol) client to the mail server and back. It shows where the
permission is checked, where the call is recorded, where the text of the mail
is cleaned, and where errors turn into the texts that the agent sees. Read it
before you change a tool or a use case: a job of the program for a caller, such
as reading one message.

MCP is the open standard that lets an AI (artificial intelligence) app call
the tools of another program. The MCP client is that
app, and the agent is the AI program inside it. IMAP (Internet Message Access
Protocol) is the protocol for reading mail. `get_message` reads one message:
its headers, a page of its body and the list of its attachments.

## The steps

{{< diagram "tool-call" >}}

The numbers match the drawing. Step 1 has a part that runs only on HTTP
(HyperText Transfer Protocol).

1. **Transport.** On stdio (standard input and output: the client starts the
   server and talks to it through two pipes), `serveStdio` in
   {{< src "cmd/email-mcp/serve.go" >}} runs the server on an
   `mcp.IOTransport`. On HTTP only, two checks come first. `limitRequests`
   lets at most 8 requests run at once (`maxConcurrentRequests`), and a request
   over the limit waits up to 30 seconds (`requestQueueWait`) and then gets the
   status 503 (Service Unavailable). Then `httpserve.Handler` checks the `Host`
   header, the `Origin` header, the size of the body and the bearer token (a
   secret in the `Authorization` header), in this order. The body is at most
   15 MiB (`httpserve.MaxBodyBytes`; a MiB is 1,048,576 bytes). The page "Local
   HTTP mode" in the group "Ways in" explains these checks.
2. **Middleware of the SDK.** The SDK (software development kit) is the
   official Go library for MCP, `go-sdk`. A middleware is a function that wraps
   the handling of every request, so it runs before and after it. From the
   outside in:
   - `App.trackToolCalls` in {{< src "internal/app/app.go" >}} counts the tool
     call, so that a stop can wait for it. While the program stops, it refuses
     the call.
   - `capture` in {{< src "internal/mcpserver/capture.go" >}} puts a call trace
     in the context: a small record where `safe` writes the caller and the
     result. `capture` records the call when it ends (step 10).
   - The multi round-trip middleware of the SDK. Multi round-trip means that a
     tool can answer "I need input", and the client calls again with the
     answer. `mcp.NewServer` adds this middleware.
   - Then the SDK finds the tool by its name, and checks the arguments against
     the JSON (JavaScript Object Notation) schema of the tool: the
     description of the arguments that the tool accepts.

   Each call of `AddReceivingMiddleware` wraps the handler that is there, so
   the middleware that was added last runs first. `mcpserver.New` adds
   `capture`, and `App.MCPServer` adds `trackToolCalls` after it. The SDK adds
   its own middleware in `mcp.NewServer`, before both, so it runs inside both.
   A call to an unknown tool, or with arguments that do not match the schema,
   stops here. `safe` and the tool handler do not run for it. `capture`, which
   is also code of `mcpserver`, has already run, and it still records the
   call.
3. **`safe`.** The function `safe` in {{< src "internal/mcpserver/server.go" >}}
   wraps every tool handler. It asks `Options.Principal` for the principal: the
   caller of the request, with its tenant (the owner of a set of accounts), the
   scopes of its access token (the permissions that the token carries; an access
   token is the secret that a program sends on local HTTP), and the name of the
   client. Without a principal or a tenant, the answer is "not authorized", and
   the handler does not run. Then `safe` puts the principal in the context
   (`service.WithPrincipal`), runs the handler, turns a panic into "internal
   error", and turns an error into a tool result. Only `addTool` registers a
   tool, and it wraps the handler with `safe`. So no tool can skip it. The test
   `TestEveryToolRunsThroughSafe` calls every tool that the server lists, and
   fails for a tool that was added another way.
4. **The handler.** `getMessage` in
   {{< src "internal/mcpserver/read_tools.go" >}} checks the arguments that the
   schema cannot check, such as a negative `offset`. Then it calls
   `service.GetMessage` with the account id, the ref and the options. A ref is
   an opaque string that names a message. The agent passes it back unchanged.
5. **The permission.** `service.GetMessage` in
   {{< src "internal/service/read.go" >}} calls `mailBackend`, which calls
   `authorize` in {{< src "internal/service/service.go" >}}. `authorize` does
   these checks, in this order:
   - It reads the principal from the context (`service.PrincipalFrom`). With
     none, the error is `ErrNoPrincipal`.
   - It asks the store for the account in the tenant of the principal. When
     the tenant has no such account, the error is `ErrAccountNotFound`.
   - It calls `policy.Decide` with the account, the action `policy.ActRead`
     and the scopes. A refusal is a `*PermissionError`, with a reason, and a
     link to the settings page of the account when there is one. There is no
     link when the scopes of the token lowered the level, or when the link
     cannot be built.
   - When the account must be reconnected, the error is a `*ReauthError`.

   The order is on purpose. A caller that may not read gets the permission
   error first, so it learns nothing about the connection of the account.
6. **The backend cache.** The backend of an account is the object that talks
   to its mail servers. `mailBackend` gets the backend of the account from
   `BackendCache.Mail` in {{< src "internal/service/backends.go" >}}. The cache
   has one slot for each account. A call holds the slot only while it gets or
   builds the backend (`mailOn`), so two calls for one account wait for each
   other only then. After that, they can use the same backend at the same time.
   On every call the cache reads the sealed (encrypted) password from the store
   and makes a fingerprint: a SHA-256 (Secure Hash Algorithm with a 256-bit
   result) hash of the server settings, the key version and the sealed password.
   When the fingerprint is the one of the cached backend, it returns that
   backend. Otherwise it decrypts the password and builds the backend again with
   `imapsmtp.New`. It decrypts the password only then. The old backend closes
   after 2 minutes (`retireAfter`), because a call that started on it may still
   use it.
7. **The IMAP backend.** `Backend.GetMessage` in
   {{< src "internal/provider/imapsmtp/fetch.go" >}} takes a logged-in
   connection from the pool of the account: the small set of open connections
   that its calls share ({{< src "internal/provider/imapsmtp/pool.go" >}}). It
   opens the folder read-only with the IMAP command `EXAMINE`. It fetches with
   `BODY.PEEK`, the IMAP way to read a message without marking it as read. The
   package `mime` decodes the body. It turns HTML (HyperText Markup Language,
   the language of web pages) into text without the parts that a reader cannot
   see (`mime.ReadableText`). One page of the body has 20,000 characters by
   default (`defaultBodyChars`) and 100,000 at most (`maxBodyChars`). The page
   "The mail backend" in the group "Mail" explains the pool.
8. **Back in `service`.** `noteBackendError` looks at the error of the
   backend. When it matches `imapsmtp.ErrAuthFailed` (the server refused the
   stored login), it marks the account as one that must be reconnected. The
   next call then stops in `authorize`, before it reaches the server.
9. **Back in `mcpserver`.** `messageOut` in
   {{< src "internal/mcpserver/shape.go" >}} shapes the result. See "Where text
   from the mail is cleaned" below.
10. **The call entry.** When the call ends, `capture` writes one call entry in
    the activity log: the log of the tool calls of the agent and of the actions
    of the use cases, also those that the person did on a page. The person reads
    it in the web portal (the local web site of the person). See "Where the call
    is recorded" below.

## Where the permission is checked

In `service`, never in `mcpserver`. A tool handler checks no permission
itself.

- The use cases that a tool calls to read or change the mail of one account
  call `authorize` (step 5) before they read the account or its mail. Some,
  such as `Send`, first check that the program is not stopping
  (`beginAction`). The reads call it through `mailBackend`.
  The writes, `Send`, `Trash` and `CancelSend` among them, call it through
  `authorizeWrite` in `write.go`, which `begin` calls. `SendStatus` calls it
  itself. So `authorize` runs before any of these use cases calls a mail
  backend.
- `ListAccounts` has no account id. It lists the accounts of the tenant of the
  caller. It asks the backend of an account for its capabilities (what the
  backend supports, such as tags, and the most refs in one batch call) only when
  `policy.Decide` allows reading and the account needs no reconnect
  (`worthAsking` in `read.go`).
- `PermissionLink` and `ConnectLink` change nothing and return a link.
  `PermissionLink` checks that the account is in the tenant of the caller.
- No tool calls the use cases of the person (see
  [Architecture overview][doors]). They work only in the tenant of the caller.
  Some, such as `UpdateSettings`, `RemoveAccount` and `ProbeAccount` in
  `accounts.go`, check no level. Others also call `authorize` for the account
  of the action: `ApprovePending` through `begin` and `authorizeWrite`, and
  `PendingDetail` for a send that waits (`sendDetail` in `approvals.go`).

So a tool that you add gets the check from its use case. That use case must
call `authorize`, or, for a write, `authorizeWrite` (alone or through
`begin`). The page [Permissions](/tech/inside-the-server/permissions/) in the
group "Inside the server" explains `policy.Decide`.

## Where text from the mail is cleaned

The text of a mail comes from a third party, so it can hold hidden
instructions for the agent. `messageOut` cleans every string that a sender
controls with `mime.CleanUntrusted`. It removes characters that a reader cannot
see, and control characters. Then it puts the text that a sender wrote (the
name, the subject, the body and the names of the attachments) inside an
`Untrusted` wrapper, with the field `"trust": "untrusted"`. An address stays
outside the wrapper only when it has the plain form that `mime.IsPlainAddress`
accepts.

The backend cleans text too, while it decodes the message: a text part goes
through `cleanText`, and the name of an attachment through `partFilename`, in
{{< src "internal/provider/imapsmtp/fetch.go" >}}. Both use
`mime.CleanUntrusted`.

The result has two forms. The structured content is the JSON object. The text
copy is for clients that do not read structured content. `textCopy` makes it,
and its first line is always the fixed warning `untrustedBanner`. When the HTML
of the message may hide text, a second fixed line says so
(`hiddenContentNotice`). The page "Mail content" in the group "Mail" explains
the cleaning.

## Errors become fixed texts

An error that a use case returns goes back up to `safe`. There `toolError` in
{{< src "internal/mcpserver/errors.go" >}} matches it with `errors.Is` and
`errors.As`, never by its text, and picks a fixed text for the agent. The text
of the error itself does not reach the agent, because it can hold text from
the mail server or the name of an account. There is one exception: for a
`*policy.RateLimitError`, `rateLimitFailure` sends the text of the error. That
text is a fixed sentence that `policy` builds from the kind of limit and the
wait, and it names no account. The other parts that come from an error are
fields that the service filled for the agent: the reason and the link of a
`*service.PermissionError`, and the link of a `*service.ReauthError`.

An error with no known text becomes "internal error". The program log (the log
that the program writes to standard error) then gets the name of the tool, a
fixed code and the Go types of the error (`errtype.Chain`), never its text. The
SDK turns the failure into a tool result with `isError` set.

## Where the call is recorded

`capture` records every `tools/call` request that reaches it, also a call that
the SDK refused before a handler ran. It calls `service.RecordToolCall` in
{{< src "internal/service/calls.go" >}}, which writes a call entry with the
`audit` package. A call that `trackToolCalls` refuses while the program stops
never reaches `capture`, so it has no entry.

The arguments are sealed with the key of the tenant. The arguments `body`,
`html_body` and `data_base64` are never kept (`omittedArguments`). The page
[Activity log and program log](/tech/inside-the-server/audit-and-logging/) in
the group "Inside the server" explains the log and the rules for writing to
it.

## What differs between stdio and HTTP

| Part | stdio | HTTP |
|---|---|---|
| Transport | `mcp.IOTransport` in `serveStdio` | `limitRequests`, the checks of `httpserve.Handler`, then the SDK handler in stateless mode (the server keeps nothing between two requests) |
| Principal | `localPrincipal`: the tenant `local`, no scope limit | `httpserve.Principal`: the tenant and the scopes of the token. The scopes are a list that is never nil, so a token never means "no limit". |
| A question in the chat | The client may show the confirmation form, where the person answers yes or no (`InBandConfirm` is true) | Only with a token that may confirm in band: one made with `--in-band-confirm`, or with the box "Let this app ask me in its own window" in the web portal. Other callers get a link to the approval page: the page of the web portal where the person approves or rejects an action. |

Steps 2 to 10 are the same code for both. `serve` builds one server for each
transport with `App.MCPServer`, with a different `mcpserver.Options`.

## What changes for a write tool

A write tool, such as `send_message`, follows the same steps, with three
additions:

- The use case decides whether the person must confirm the action.
  `policy.Decide` gives the setting of the account, and a send has more
  reasons. Then `confirm` stores the exact request, and the tool asks the
  question in the chat of the client, or returns `pending_approval` with a link
  to the approval page of the web portal.
- The use case writes its own action entries in the activity log. So a call
  that changes something has a call entry and one or more action entries.
- A send goes to the `outbox`, the queue of sends, which waits for the undo
  window (the time when the send can still be cancelled) and then delivers it.
  A send with no undo window runs inside its tool call, and that is why
  `trackToolCalls` counts the call.

The page [The send flow](/tech/inside-the-server/send-flow/) in the group
"Inside the server" explains this path.

## Related pages

- [Architecture overview](/tech/start-here/overview/)
- [Map of packages](/tech/start-here/package-map/)
- [What the agent can do](/manual/daily-use/what-the-agent-can-do/) in the
  user manual
- [Confirmations](/manual/daily-use/confirmations/) in the user manual

[doors]: /tech/start-here/overview/#two-doors-into-the-use-cases
