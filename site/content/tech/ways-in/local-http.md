---
title: Local HTTP mode
description: How serve --http checks a request before it reaches a tool, how its access tokens work, why the server is stateless, and why the question in the chat is a choice for each token.
weight: 20
code_areas:
  - internal/httpserve/httpserve.go
  - internal/tokens/tokens.go
  - internal/service/tokens.go
  - internal/service/principal.go
  - internal/mcpserver/elicit.go
  - cmd/email-mcp/serve.go
  - cmd/email-mcp/token.go
  - internal/httpserve/httpserve_test.go
---

This page explains local HTTP mode: the HTTP (HyperText Transfer Protocol)
server that `email-mcp serve --http` starts for an AI (artificial intelligence)
app that cannot start a program. It covers the checks that a request passes
before it reaches a tool, the access tokens, the stateless server, and the
choice that lets a token answer a confirmation in its own window. Read it
before you change the package `httpserve`, the HTTP part of `serve`, or the
tokens. After the checks, a tool call takes the same steps as on stdio
(standard input and output: the AI app starts the program and talks to it
through two pipes). See [One tool call, step by
step](/tech/start-here/tool-call/). The manual page [Local HTTP
mode](/manual/reference/local-http-mode/) has the steps for the person.

## Where it listens

`checkServeFlags` in {{< src "cmd/email-mcp/serve.go" >}} checks the flags
before anything is opened, and a bad one ends the command with the exit code 2.
`httpserve.CheckLoopbackAddr` refuses the address of `--http` unless it is a
loopback address (an address on this computer that no other computer can reach)
with a port: an IPv4 (Internet Protocol version 4) address in `127.0.0.0/8`,
`[::1]` or `localhost`. The address must be written exactly: brackets only
around an IPv6 (version 6) address, and an IP address in its usual short form.
So `0.0.0.0:8765` and `[127.0.0.1]:8765` are refused. Any program on this
computer can reach the address, so the token decides who may use the server.

`--allow-origin` adds a browser origin (the scheme, host and port of a web page,
such as `http://localhost:5173`) that may call the server. It needs `--http`.
Add only an origin that you trust. `httpserve.NormalizeOrigin` refuses `null`,
and an origin with a path, a query or a fragment. The MCP (Model Context
Protocol) endpoint is `/mcp` (`mcpPath`). When the server listens, it logs
"serving MCP over local HTTP" with the address and the path.

## The checks, in order

{{< diagram "http-checks" >}}

`serveHTTP` builds the handler from two layers. `limitRequests` in `serve.go` is
the outer one. `httpserve.Handler` in
{{< src "internal/httpserve/httpserve.go" >}} is the inner one. The SDK
(software development kit) is the official Go library for MCP, `go-sdk`. A
bearer token is a secret in the header `Authorization: Bearer <token>`. A MiB
(mebibyte) is 1,048,576 bytes. A request goes through these steps, in this
order:

| Step | Code | Refused with | Log code |
|---|---|---|---|
| 1. Concurrency limit | `limitRequests`: at most 8 requests at once (`maxConcurrentRequests`). A request over the limit waits up to 30 seconds (`requestQueueWait`) for a free place, before its body is read. | 503 (Service Unavailable), with `Retry-After: 1` | `too_many_requests` |
| 2. `Host` | `hostAllowed`: the connection came in on a loopback address, and the `Host` header is exactly `localhost:<port>`, `127.0.0.1:<port>` or `[::1]:<port>`, with the port of the connection (`LoopbackPort`). | 403 (Forbidden) | `bad_host` |
| 3. `Origin` | `originAllowed`: no `Origin` header, or exactly one whose value is in the list of `--allow-origin`. | 403 | `bad_origin` |
| 4. Stated body size | A `Content-Length` over 15 MiB (`MaxBodyBytes`) is refused at once. Any other body is cut at that limit while it is read (`http.MaxBytesReader`). | 413 (Request Entity Too Large) | `body_too_large` |
| 5. Bearer token | `auth.RequireBearerToken` of the SDK, with `verifier`, which calls `tokens.Manager.Verify`. | 401 (Unauthorized), or 500 with the fixed text "internal error" when the check itself fails | `invalid_token`, `token_check_failed` |
| 6. Read the body | `readBody` reads the whole body before the MCP handler runs. | 413 over the limit, 400 (Bad Request) when it cannot be read | `body_too_large`, `body_unreadable` |
| 7. MCP handler | `mcp.NewStreamableHTTPHandler` in stateless mode, with `MaxRequestBodyBytes` set to `MaxBodyBytes`. | The SDK answers. | none |

After step 7, the request is a tool call as on stdio: the middleware (functions
that wrap the handling of every request: `trackToolCalls`, `capture` and the
multi round-trip middleware of the SDK, which lets a tool answer "I need input";
see [Stateless mode](#stateless-mode)), `safe`, the tool handler and `service`.

Why this order:

- The `Host` check comes before the token. It stops a web page that uses DNS
  rebinding (DNS is the Domain Name System; in DNS rebinding, a name of the
  attacker points to `127.0.0.1`). Such a page gets 403 before any token is
  looked at. `TestHTTPCheckOrder` in
  {{< src "internal/httpserve/httpserve_test.go" >}} checks the order.
- A request with an `Origin` header comes from a browser, and the list of
  `--allow-origin` is empty by default. Programs send no `Origin`. The handler
  sends no CORS headers (CORS, cross-origin resource sharing, is the set of
  headers that lets a page read the answers of another origin).
- `limitRequests` waits before the body is read, so a waiting request holds no
  body. `readBody` runs after the token check, so it does not run for a caller
  without a token.

Each refusal has a fixed answer, the standard text of its status, such as
"Forbidden". It never repeats a value of the request. The log gets the fixed
code of the table, and never a token, the `Authorization` header or a body. A
request without the `Authorization` header, or with one of another form, gets
the SDK's own 401 and is not logged.

## Tokens

An access token is the secret that the AI app sends with each request. Its
rules are in {{< src "internal/tokens/tokens.go" >}}:

- A token is `emcp_` (`prefix`) and 52 characters: 32 random bytes
  (`randomBytes`) in lower-case base32 (the letters `a` to `z` and the digits
  `2` to `7`), without padding. The prefix makes a token easy to find, for
  example for a tool that looks for secrets in code.
- The store keeps only the SHA-256 of the token (SHA is Secure Hash Algorithm;
  the hash is a short value from which the token cannot be read back). The
  plain token exists only in the answer of `Create`, so the person copies it
  then. The page [Storage and
  secrets](/tech/inside-the-server/storage-and-secrets/) has the table
  `api_tokens`.
- A token has an explicit list of known scopes. A scope is a permission that a
  token carries, such as `email:read`. `Create` refuses a token with no scope,
  or with one that `policy` does not know, and stores the list sorted, with each
  scope once. What the scopes allow is on
  [Permissions](/tech/inside-the-server/permissions/#token-scopes).
- A token has an end date. `Create` takes a lifetime from 1 minute
  (`MinLifetime`) to 3,650 days (`MaxLifetime`), and 90 days
  (`DefaultLifetime`) when it gets none. A stored token with no end date works
  until it is revoked, so the person should make a token to replace it and
  revoke the old one.
- A token may answer a confirmation in its own window only when the person
  chose that when the token was made (`InBandConfirm`). See [Who gets which
  channel](#who-gets-which-channel).
- A name is 1 to 128 bytes (`maxNameBytes`) of UTF-8 (Unicode Transformation
  Format, 8-bit) text (the usual way to write text as bytes), without a control
  or format character, which could change how a terminal shows the list.

`Manager.Verify` checks a token. It refuses a token of the wrong form before
any lookup (`wellFormed`). Each token has one spelling, so upper case, padding
and line breaks are refused. Then it looks up the SHA-256, and compares the
stored hash again in constant time (a comparison whose time does not depend on
the bytes). It refuses a revoked token, an expired token and a token with no
scope. Every refusal is the same error, `ErrInvalidToken`, so the answer does
not say why. `TestHTTPBadTokensGetTheSameAnswer` checks that a malformed, an
unknown and a revoked token get the same answer. A valid token notes its last
use at most once a minute (`useGap`). The manager keeps no cache, so a revoke
counts at the next check.

`verifier` in `httpserve` turns the result into the `TokenInfo` of the SDK: the
tenant (the owner of a set of accounts) as `UserID`, the scopes, and the id and
the in-band choice of the token in `Extra`. These values come from the token
check, not from the request. `httpserve.Principal` then makes the principal
(the caller of a request) for the tool call, of the type `service.Principal` in
{{< src "internal/service/principal.go" >}}:

- A call without a `TokenInfo` or without a `UserID` is refused with
  `ErrNoToken`.
- The scopes are a copy of the list and are never nil, so a token never means
  "no limit". A nil list (no limit) belongs to the callers without a token:
  stdio, the web portal and the commands.
- A missing or odd value in `Extra` means no token id and no in-band
  confirmation.
- The client name comes from the request. It is not trusted, and the activity
  log cleans it.

The person makes a token with `token create` in
{{< src "cmd/email-mcp/token.go" >}}, or on the Connections page of the [web
portal](/tech/ways-in/web-portal/). Both call `Service.CreateToken` in
{{< src "internal/service/tokens.go" >}}, which writes the activity log entry
`token_created`, or `token_created_in_band` for a token that may answer in its
own window. `Service.RevokeToken` writes `token_revoked`. No tool reaches these
use cases. `token list` and the Connections page show the tokens with
`Manager.List`, which never returns a token or its hash. Only the command sets
a lifetime (`--expires-in`). The page uses the default.

## Stateless mode

`httpserve.Handler` makes the MCP handler with `Stateless: true`. The server
keeps no session (no state that lasts from one request to the next): each
request carries its token, and the token decides the caller. `serveHTTP` builds
one MCP server for all requests, because the SDK asks for the server more than
once in one request.

What this changes:

- The server cannot send a request to the client. So it does not send
  `elicitation/create` (the request that asks the client to show a form) over
  HTTP. `serveHTTP` sets `InBandLegacyElicitation` to false.
- A client that speaks the protocol version `2026-07-28` or newer can still get
  the question, inside the result. This is MRTR (multi round-trip request): the
  tool answers "I need input", and the client calls again with the answer.
- A client of an older protocol version gets the approval link for every
  confirmation. The approval link leads to the page of the web portal where the
  person approves or rejects the action.

The design of the program chose this over a server that keeps sessions for
older clients, and accepted that those clients get the approval link.

## Who gets which channel

`canElicitInBand` in {{< src "internal/mcpserver/elicit.go" >}} decides whether
the client can show the confirmation form in this call (in band). It needs a
caller that may confirm in band (`Principal.InBandConfirm`), form elicitation
in the capabilities of the client (what the client says it can do), and a way
to ask (`canAsk`): the protocol version `2026-07-28` (`mrtrProtocolVersion`) or
newer, or `InBandLegacyElicitation`. Form elicitation is a way for a server to
ask the person a question through a form in the AI app. A protocol version is a
date that names a version of the MCP rules.

| Caller | Client with form elicitation, protocol `2026-07-28` or newer | Client with form elicitation, older protocol | Client without form elicitation |
|---|---|---|---|
| stdio (`localPrincipal`: `InBandConfirm` is true) | In band, as an input request in the result | In band, with `elicitation/create` in the same session | Approval link |
| HTTP, with a token made with `--in-band-confirm` or the box "Let this app ask me in its own window" | In band, as an input request in the result | Approval link | Approval link |
| HTTP, with any other token | Approval link | Approval link | Approval link |

The answer of an in-band form is only as good as the client: the client decides
whether a person saw the form. A client that holds a token with this choice can
answer the form by itself. So for a token, `InBandConfirm` is false by default,
and the choice is the person's, made when the token is made. Make a token with
this choice only for an app that shows you the form. On stdio, the AI app that
started the process holds the other end of the pipes, so the answer comes from
that app. A program that runs as the same user is outside what email-mcp can
control. The page [The send flow](/tech/inside-the-server/send-flow/#in-band)
shows the two calls of an in-band question.

The table is for confirmations. `canOpenLink` uses the same rule for the
protocol to let the client open the link of `connect_account` or
`request_permission_change` for the person. That link holds no secret.

## Limits and timeouts

| Name | Value | What it bounds |
|---|---|---|
| `maxConcurrentRequests` (`serve.go`) | 8 | Requests that run at once. A request can hold its body twice: `httpserve` reads it, and the SDK reads it again. |
| `requestQueueWait` (`serve.go`) | 30 seconds | The wait of a request over the limit, before 503 |
| `MaxBodyBytes` (`httpserve.go`) | 15 MiB | The body of a request. A message may have 10 MiB of attachments (`mime.MaxAttachmentBytes`), and base64 (the text form of bytes in a mail) makes them a third larger. |
| `httpMaxHeaderBytes` (`serve.go`) | 64 KiB | The headers of a request. A KiB is 1,024 bytes. |
| `httpReadHeaderTimeout` (`serve.go`) | 10 seconds | Reading the headers |
| `httpReadTimeout` (`serve.go`) | 1 minute | Reading the whole request |
| `httpIdleTimeout` (`serve.go`) | 2 minutes | An open connection that waits for its next request |
| `httpPrepareMargin` (`serve.go`) | 5 minutes | The checks of a tool call before its send, and the calls to the mail server of a reply or a forward |
| `httpWriteTimeout` (`serve.go`) | 19 minutes 30 seconds | Writing the answer, counted from the end of the request headers |
| `DefaultLifetime`, `MinLifetime`, `MaxLifetime` (`tokens.go`) | 90 days, 1 minute, 3,650 days | The lifetime of a token |
| `useGap` (`tokens.go`) | 1 minute | The shortest time between two writes of the last use of a token |
| `maxNameBytes` (`tokens.go`) | 128 bytes | The name of a token |

`httpWriteTimeout` is `requestQueueWait + httpReadTimeout + outbox.SendDeadline
+ httpPrepareMargin`: 30 seconds, 1 minute, 13 minutes and 5 minutes. Before
the answer, a request may wait for a place, read its body, prepare the message,
and send it with no undo window (the time when a send can still be cancelled)
for up to `outbox.SendDeadline`. A client that never gets the answer of a send
that went out could send it again. So the answer must still go out.

## How the server stops

When `serve` gets Ctrl-C (hold the Ctrl key and press C) or SIGTERM (the signal
that asks a program to stop), `serveHTTP` calls `App.BeginStop`, stops taking
connections, and waits for the requests that run. The wait is within the stop
budget (`stopBudget`: one time limit for the whole stop, `app.StopTimeout`).
Then `serve` calls `App.Close` with the same budget. The page [Startup and
shutdown](/tech/inside-the-server/startup-and-shutdown/#stdio-and-http) gives
each step and what happens to a request that does not end in time.

## Related pages

- [One tool call, step by step](/tech/start-here/tool-call/)
- [Permissions](/tech/inside-the-server/permissions/)
- [The send flow](/tech/inside-the-server/send-flow/)
- [The command line](/tech/ways-in/command-line/)
- [Local HTTP mode](/manual/reference/local-http-mode/) in the user manual
