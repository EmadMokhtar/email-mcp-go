---
title: Security design
description: The rules that keep the agent inside its limits, where the code enforces each one, and which test fails when one breaks.
weight: 10
code_areas:
  - internal/e2e/security_test.go
  - internal/e2e/harness_test.go
  - internal/mcpserver/server.go
  - internal/mcpserver/useronly_test.go
  - internal/mcpserver/elicit.go
  - internal/mcpserver/shape.go
  - internal/mcpserver/errors.go
  - internal/service/principal.go
  - internal/service/service.go
  - internal/service/write.go
  - internal/store/store.go
  - internal/policy/policy.go
  - internal/confirm/confirm.go
  - internal/signing/signing.go
  - internal/secrets/vault.go
  - internal/secrets/secret.go
  - internal/web/guard.go
  - internal/web/links.go
  - internal/httpserve/httpserve.go
  - internal/domain/account/account.go
  - internal/provider/imapsmtp/tlsdial.go
  - internal/domain/mail/backend.go
  - internal/mime/untrusted.go
---

This page shows the rules that hold the security design of email-mcp together,
where the code enforces each rule, and which test fails when a rule breaks. Read
it before you change code that checks a caller, a permission, a confirmation, a
secret or a connection, and when you review such a change.

The agent is the AI (artificial intelligence) program that calls the tools of
email-mcp. A tool is one action that the agent can call, such as `get_message`.
A use case is one job of the program for a caller, such as "send this mail".
The person is the human who owns the mail accounts.

## The model

The agent is a caller with a short reach. It reaches the use cases only through
the tools of `mcpserver`, and `service` checks the permission of each call
before it touches the mail. The person has a second way in: the web portal (the
local web site of the person) and the commands at the terminal. Only that way
changes the settings of an account, makes an access token, or approves an action
on the approval page. A "yes" in the chat comes from the form of the AI app, and
never from the arguments of a tool. So that "yes" is only as good as the app
that shows the form (see [Trust in the AI
app](/manual/daily-use/confirmations/#trust-in-the-ai-app)).

The model is for an agent that can only call the tools. The manual page
[Security](/manual/daily-use/security/) states the threat model and its limits
for users, and [Recommended Claude Code deny
rules](/manual/daily-use/deny-rules/) shows how to keep an agent away from the
shell. This page does not repeat them.

## Two channels into service

{{< diagram "two-channels" >}}

The agent's channel:

- **The transport.** On stdio (standard input and output: the AI app starts the
  program and talks to it through two pipes), only the app that started the
  process can write to its standard input. On local HTTP (HyperText Transfer
  Protocol), `Handler` in {{< src "internal/httpserve/httpserve.go" >}} checks
  the Host header (the name of the server that a request asks for), the Origin
  header (the web site that a browser request comes from), the size of the body
  and the access token (a secret that a program sends with each request), in
  this order. The page "Local HTTP mode" in the group "Ways in" goes deeper.
- **The tools.** `addTool` in {{< src "internal/mcpserver/server.go" >}} wraps
  each tool in `safe`, which asks for the caller first. A call with no caller,
  or with a caller that has no tenant (the owner of a set of accounts), is
  refused with "not authorized", and the tool does not run.
- **The reach.** The tools call the use cases that read or change mail. They do
  not call the user-only use cases, and `mcpserver` imports neither `web` nor
  `tokens`. The links that a tool returns carry no secret.

The person's channel:

- **The browser** goes to `web`, the web portal. `Server.serve` in
  {{< src "internal/web/guard.go" >}} runs the checks of every request: the
  security headers, the Host header, the cross-origin check (a check that a form
  post comes from a page of the portal itself), the name `email-mcp.localhost`,
  the session, and the CSRF token (cross-site request forgery token: a value in
  each form that proves that the form came from the portal) for a request that
  is not GET or HEAD. The session starts with the one-time link that
  `email-mcp setup` opens. The page "The web portal" in the group "Ways in" goes
  deeper.
- **The terminal** runs the other commands of `cmd/email-mcp`, as the local
  tenant.
- **The reach.** This is the channel that calls the user-only use cases, such
  as `UpdateSettings`, `ApprovePending` and `CreateToken`.

Both channels end in `service`. Each use case reads the principal (the caller of
a request: its tenant, the scopes of its token, and the name of its client) with
`PrincipalFrom` in {{< src "internal/service/principal.go" >}}. Scopes are the
permissions that an access token carries. Each channel makes its own principal,
as [Architecture overview](/tech/start-here/overview/#the-caller-and-the-tenant)
explains. Under `service`, both channels use the same parts: `policy`,
`confirm`, `outbox`, `audit`, `store` with `secrets`, and the mail backend.

## Rules and their tests

Each row names a rule, the code that enforces it, and the test that fails when
it breaks. The tests in {{< src "internal/e2e/security_test.go" >}} drive the
whole program (see [The end-to-end tests](#the-end-to-end-tests)). The other
tests are named with their package.

The table uses a few terms. A level is a number from 0 to 5 that says what the
agent may do with the mail of an account. Level 5 has the name Delete: it moves
mail to the Trash and never deletes mail for good. TLS (Transport Layer
Security) encrypts a connection. A certificate is the signed proof of identity
that a server shows when a TLS connection starts, and a pin is the fingerprint
of a certificate that the person chose to trust. A loopback host is this
computer, such as `127.0.0.1`. To seal a value is to encrypt it, and a hash is a
fingerprint of bytes.

| Rule | Where it is enforced | Test that fails |
|---|---|---|
| The tenant comes from the principal. No tool has a tenant argument, and each query of `store` for one tenant has a `tenant_id = ?` condition. | `PrincipalFrom`; `authorize` in {{< src "internal/service/service.go" >}}; the queries of `store` (see the package comment of {{< src "internal/store/store.go" >}}) | `TestAccountOfOtherTenantNotFound` and `TestWriteOnAccountOfOtherTenantNotFound` in `service`; `TestGetAccountOtherTenantNotFound` in `store` |
| Each tool runs through `safe`, so no tool runs without a caller. | `addTool` and `safe` | `TestEveryToolRunsThroughSafe` in `mcpserver`. It calls each tool that the server lists, with a caller lookup that fails, and each tool must answer "not authorized". |
| A tool below its level is refused before the mail server gets a command. | `authorize`, which calls `policy.Decide` in {{< src "internal/policy/policy.go" >}} | `TestEveryWriteToolRefusedBelowItsLevel`. Each tool that the server lists needs a row in its map `requiredLevel`, so a new tool fails the test until its level is there. |
| No tool reaches a use case that only the person may call. | The code of `mcpserver`: it names no call of the list `userOnlyCalls`, and it imports neither `web` nor `tokens` | `TestNoToolReachesTheUserOnlyUseCases` and `TestNoToolIsNamedForAUserOnlyUseCase` in {{< src "internal/mcpserver/useronly_test.go" >}} |
| A link that the agent gets carries no secret. Opened without the session of the person, it shows one sentence and changes nothing. | `Links` in {{< src "internal/web/links.go" >}}; the session check of `Server.serve` | `TestLocalUILinksRevealNothingWithoutSession` |
| The web portal refuses a wrong Host header, a cross-origin form post, and a form post without the CSRF token of its session. | `hostAllowed`, `Server.serve` and `checkForm` | `TestLocalUIRefusesWrongHost`; `TestLocalUIRefusesCrossOriginPost`; `TestEveryPostRouteNeedsTheCSRFToken` in `web` |
| Local HTTP refuses a wrong Host header and a browser origin that is not on the allow-list, also with a valid token. It refuses a request without a valid token. | `Handler` | `TestLocalUIRefusesWrongHost` and `TestLocalUIRefusesCrossOriginPost`, which test the endpoint too; `TestHTTPRejectsMissingToken` and `TestHTTPCheckOrder` in `httpserve` |
| A "yes" acts once, in time, for the exact message that the person saw, in the channel where it was asked, and for the tenant, the account and the kind of action that asked. | `Manager.Claim` and `HashPayload` in {{< src "internal/confirm/confirm.go" >}}; `samePreview` in `service`; `accepted` in {{< src "internal/mcpserver/elicit.go" >}} | `TestConfirmationCannotBeSkipped` |
| The password of an account and the data of a message are sealed in the database. Tokens are stored as hashes. | `Vault` in {{< src "internal/secrets/vault.go" >}}, and the methods of `store` | `TestNoPlaintextSecretsInDB`. It reads the raw bytes of the database files while sends wait and after they go out. |
| The program log gets ids, counts and fixed codes, not a secret, an address, a subject or a body. | The log rules of each package; `Secret` in {{< src "internal/secrets/secret.go" >}}, which prints `[redacted]` | `TestNoSecretsInLogs`. It captures each log line of a full scenario, at Debug level. |
| A connection to a remote mail server uses TLS 1.2 or newer and checks the certificate, or its pin. Plain text is allowed only to a loopback host, on a local install. | `MailServer.Validate` in {{< src "internal/domain/account/account.go" >}}; the secure dialer in {{< src "internal/provider/imapsmtp/tlsdial.go" >}} | `TestPlainTextToRemoteRefused`; `TestTLSCertificateChecksEnforced` |
| No method deletes mail for good. Below level 5, no tool moves mail to the Trash, except `discard_draft` for a draft, at level 3. | The comment of `mail.Backend` in {{< src "internal/domain/mail/backend.go" >}}; `Move` in {{< src "internal/service/write.go" >}}, which refuses a Trash folder with `ErrMoveToTrash` | `TestTrashMoveBlockedBelowLevel5` |
| Text from a third party is cleaned of characters that a reader cannot see, and it is marked as untrusted. | `CleanUntrusted` in {{< src "internal/mime/untrusted.go" >}}; `messageOut` and the `Untrusted` wrapper in {{< src "internal/mcpserver/shape.go" >}} | The tests of `CleanUntrusted` in `mime`; `TestUntrustedCleaned` and `TestSearchResultMarksUntrusted` in `mcpserver` |
| The agent gets a fixed text for each known error, and "internal error" for any other. | `toolError` in {{< src "internal/mcpserver/errors.go" >}} | `TestToolErrorMapping` and `TestUnknownErrorIsInternalError` in `mcpserver` |
| Request states, page tokens, cursors and form tokens are signed. | `Signer` in {{< src "internal/signing/signing.go" >}} | `TestOpenTamperedInvalid` and `TestOpenWrongPurposeInvalid` in `signing`; `TestListNewMessagesRejectsAForgedCursor` in `mcpserver` |

The pages that own each area give the details: [The send
flow](/tech/inside-the-server/send-flow/#how-the-yes-is-tied-to-the-message) for
the "yes", [Permissions](/tech/inside-the-server/permissions/#levels) for the
levels, [Storage and
secrets](/tech/inside-the-server/storage-and-secrets/#sealed-hashed-or-plain)
for what is sealed, [The mail
backend](/tech/mail/mail-backend/#the-secure-dialer) for TLS and the pins, and
[Mail content](/tech/mail/mail-content/) for the cleaning. Two rows need a note:

- A reached limit (`*policy.RateLimitError`) gets the text of its error. That
  text is a fixed sentence with the kind of limit and the wait.
- In HTML (HyperText Markup Language) mail, email-mcp cannot always tell what a
  reader sees (see [Known limitations](/manual/reference/known-limitations/)).
  When it matters, open the mail yourself.

## Failing closed

The checks below fail closed: when one cannot decide, the answer is no.

- `policy.Decide` refuses an action that it does not know, and it counts a level
  outside 0 to 5 as level 0, Off (`account.MailOff`).
- `safe` refuses a caller without a tenant before the tool runs, and
  `PrincipalFrom` counts such a principal as no principal.
- The zero value of `Principal.InBandConfirm` is false. So a caller that nobody
  thought about cannot answer a confirmation in the chat, and gets the approval
  page.
- A `Signer` that was not made with `New` has no key, and it opens no token
  (`TestZeroSignerFailsClosed` in `signing`).
- When the link of a permission error cannot be built, the refusal stands, with
  an empty link.

## Signed values

`Signer` makes a small token that the server gives to the agent or to a
browser, and gets back later. The token is the JSON (JavaScript Object Notation)
of a purpose, an expiry and a value, in base64url (a text form of bytes that is
safe in a web address), with an HMAC-SHA-256 signature (a hash made with a
secret key). The key comes from the server key with the label `signing/v1` (see
[Storage and secrets](/tech/inside-the-server/storage-and-secrets/#keys)).

- Anyone who holds a token can read it, so a token holds no secret.
- Nobody can change a token, or make one, without the key.
- `Open` accepts a token only for its own purpose: `cursor`, `page`,
  `request_state` or `csrf`.
- A token that was changed, made with another key, made for another purpose or
  is not well formed gives `ErrInvalid`. A caller cannot tell these cases
  apart. A genuine token whose time has passed gives `ErrExpired`.
- `Open` reads at most 64 KiB (`maxTokenLen`), and `New` needs a key of at least
  32 bytes (`minKeyLen`).
- A signer keeps no record of the tokens that it made. A use that must happen
  once keeps its own record: the claim of `confirm` does this for a "yes".

A message ref (the value that names one message) is not signed. It gives no
right, because every call checks the permission first (see [The mail
backend](/tech/mail/mail-backend/#refs-folders-and-tags)).

## The end-to-end tests

{{< src "internal/e2e/security_test.go" >}} holds the tests of the security
checklist of the design spec. Each test opens the whole program with `app.Open`,
as the command `serve` does. Then it drives the program as an agent and as a
browser would: with an MCP (Model Context Protocol) client on an in-memory
connection, through the local HTTP endpoint, and through the web portal. The
helpers are in {{< src "internal/e2e/harness_test.go" >}}. The mail servers are
the test servers `imaptest` and `smtptest` on the loopback address, so no test
uses the network outside the computer.

- The test secrets are long and unusual strings, so a match in a log or in a
  file cannot be chance.
- A test of a refusal checks the exact text that the agent gets, so a failure
  shows which check refused.
- The test of the levels compares, before and after each refused call, the
  commands that reached the IMAP (Internet Message Access Protocol) server, the
  connections and messages of the SMTP (Simple Mail Transfer Protocol) server,
  the pending actions and the outbox.

## When you change security code

Security code is code that checks a caller, a permission, a confirmation, a
secret or a connection, or that cleans text from a third party.

1. Run `make test`. It runs `go test -race ./...`, so the end-to-end tests run
   too. The flag `-race` turns on the race detector (a check that finds two
   goroutines, functions that Go runs at the same time, that use the same
   memory without a lock).
2. Add a test for the new rule, or for the new case of a rule: in
   `security_test.go` for a rule of the whole program, or next to the test of
   the package that the table names.
3. For a new tool: add it with `addTool`, and add its row to `requiredLevel` in
   `security_test.go`. A tool that can change something needs a level above 0.
   The test allows level 0 only for a read-only tool, and for
   `request_permission_change` and `connect_account`, which return a link and
   change nothing.
4. For a new use case that only the person may call: add its name to
   `userOnlyCalls` in `useronly_test.go`.
5. Update the table of this page. Then check the other pages that list the code
   that you changed under "Code covered by this page".

## Related pages

- [Security](/manual/daily-use/security/) in the user manual
- [Recommended Claude Code deny rules](/manual/daily-use/deny-rules/) in the
  user manual
- [The send flow](/tech/inside-the-server/send-flow/)
- [Permissions](/tech/inside-the-server/permissions/)
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
