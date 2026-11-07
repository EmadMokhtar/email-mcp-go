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
  - internal/provider/imapsmtp/backend.go
  - internal/provider/imapsmtp/fetch.go
  - internal/provider/imapsmtp/pool.go
---

This page follows one call of the tool `get_message` from the MCP client to
the mail server and back. It shows where the permission is checked, where the
call is recorded, where the text of the mail is cleaned, and where errors turn
into the texts that the agent sees. Read it before you change a tool or a use
case.

MCP (Model Context Protocol) is the open standard that lets an AI (artificial
intelligence) app call the tools of another program. The MCP client is that
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
     in the context, and records the call when it ends (step 10).
   - The multi round-trip middleware of the SDK. Multi round-trip means that a
     tool can answer "I need input", and the client calls again with the
     answer. `mcp.NewServer` adds this middleware.
   - Then the SDK finds the tool by its name, and checks the arguments against
     the JSON (JavaScript Object Notation) schema of the tool.

   Each call of `AddReceivingMiddleware` wraps the handler that is there, so
   the middleware that was added last runs first. `mcpserver.New` adds
   `capture`, and `App.MCPServer` adds `trackToolCalls` after it. The SDK adds
   its own middleware in `mcp.NewServer`, before both, so it runs inside both.
   A call to an unknown tool, or with arguments that do not match the schema,
   stops here. No code of `mcpserver` runs for it, but `capture` still records
   it.
3. **`safe`.** The function `safe` in
   {{< src "internal/mcpserver/server.go" >}} wraps every tool handler. It asks
   `Options.Principal` for the principal: the caller of the request, with its
   tenant (the owner of a set of accounts), the scopes of its token, and the
   name of the client. Without a principal or a tenant, the answer is "not
   authorized", and the handler does not run. Then `safe` puts the principal in
   the context (`service.WithPrincipal`), runs the handler, turns a panic into
   "internal error", and turns an error into a tool result. Only `addTool`
   registers a tool, and it wraps the handler with `safe`. So no tool can skip
   it. The test `TestEveryToolRunsThroughSafe` calls every tool that the
   server lists, and fails for a tool that was added another way.
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
6. **The backend cache.** `mailBackend` gets the backend of the account from
   `BackendCache.Mail` in {{< src "internal/service/backends.go" >}}. The cache
   has one slot for each account, so calls for one account take turns. On every
   call it reads the sealed (encrypted) password from the store and makes a
   fingerprint: a SHA-256 (Secure Hash Algorithm with a 256-bit result) hash of
   the server settings, the key version and the sealed password. When the
   fingerprint is the one of the cached backend, it returns that backend.
   Otherwise it decrypts the password and builds the backend again with
   `imapsmtp.New`. It decrypts the password only then. The old backend closes
   after 2 minutes (`retireAfter`), because a call that started on it may still
   use it.
7. **The IMAP backend.** `Backend.GetMessage` in
   {{< src "internal/provider/imapsmtp/fetch.go" >}} takes a logged-in
   connection from the pool of the account
   ({{< src "internal/provider/imapsmtp/pool.go" >}}). It opens the folder
   read-only with the IMAP command `EXAMINE`. It fetches with `BODY.PEEK`, the
   IMAP way to read a message without marking it as read. The package `mime`
   decodes the body. It turns HTML (HyperText Markup Language, the language of
   web pages) into text without the parts that a reader cannot see
   (`mime.ReadableText`). One page of the body has 20,000 characters
   by default (`defaultBodyChars`) and 100,000 at most (`maxBodyChars`). The
   page "The mail backend" in the group "Mail" explains the pool.
8. **Back in `service`.** `noteBackendError` looks at the error of the
   backend. When it matches `imapsmtp.ErrAuthFailed` (the server refused the
   stored login), it marks the account as one that must be reconnected. The
   next call then stops in `authorize`, before it reaches the server.
9. **Back in `mcpserver`.** `messageOut` in
   {{< src "internal/mcpserver/shape.go" >}} shapes the result. See "Where text
   from the mail is cleaned" below.
10. **The call entry.** When the call ends, `capture` writes one call entry in
    the activity log. See "Where the call is recorded" below.

## Where the permission is checked

In `service`, never in `mcpserver`. Every use case that works on one account
starts with `authorize` (step 5), and `authorize` runs before any call to a
mail backend. A tool
handler checks no permission itself. So a tool that you add gets the check from
its use case. The page "Permissions" in the group "Inside the server" explains
`policy.Decide`.

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
of the error itself never reaches the agent, because it can hold text from the
mail server or the name of an account. The only parts that come from an error
are fields that the service filled for the agent: the reason and the link of a
`*service.PermissionError`, and the link of a `*service.ReauthError`.

An error with no known text becomes "internal error". The program log then gets
the name of the tool, a fixed code and the Go types of the error
(`errtype.Chain`), never its text. The SDK turns the failure into a tool result
with `isError` set.

## Where the call is recorded

`capture` records every `tools/call` request that reaches it, also a call that
the SDK refused before a handler ran. It calls `service.RecordToolCall` in
{{< src "internal/service/calls.go" >}}, which writes a call entry with the
`audit` package. A call that `trackToolCalls` refuses while the program stops
never reaches `capture`, so it has no entry.

The arguments are sealed with the key of the tenant. The arguments `body`,
`html_body` and `data_base64` are never kept (`omittedArguments`). A failure to
write the entry does not change the answer of the call. A call without a
principal has no entry either, because the table needs a tenant, and the
program log gets a fixed code instead. The page "Activity log and program log"
in the group "Inside the server" explains the log.

## What differs between stdio and HTTP

| Part | stdio | HTTP |
|---|---|---|
| Transport | `mcp.IOTransport` in `serveStdio` | `limitRequests`, the checks of `httpserve.Handler`, then the SDK handler in stateless mode (the server keeps nothing between two requests) |
| Principal | `localPrincipal`: the tenant `local`, no scope limit | `httpserve.Principal`: the tenant and the scopes of the token. The scopes are a list that is never nil, so a token never means "no limit". |
| A question in the chat | The client may show the confirmation form (`InBandConfirm` is true) | Only with a token made with `--in-band-confirm`. Other callers get a link to the approval page. |

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
- A send goes to the `outbox`, which waits for the undo window (the time when
  the send can still be cancelled) and then delivers it. A send with no undo
  window runs inside its tool call, and that is why `trackToolCalls` counts the
  call.

The page "The send flow" in the group "Inside the server" explains this path.

## Related pages

- [Architecture overview](/tech/start-here/overview/)
- [Map of packages](/tech/start-here/package-map/)
- [What the agent can do](/manual/daily-use/what-the-agent-can-do/) in the
  user manual
- [Confirmations](/manual/daily-use/confirmations/) in the user manual
