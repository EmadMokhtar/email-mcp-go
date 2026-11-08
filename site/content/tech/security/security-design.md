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
  - internal/service/
  - internal/store/store.go
  - internal/policy/policy.go
  - internal/confirm/confirm.go
  - internal/signing/signing.go
  - internal/secrets/vault.go
  - internal/web/guard.go
  - internal/web/links.go
  - internal/httpserve/httpserve.go
  - cmd/email-mcp/serve.go
  - internal/domain/account/account.go
  - internal/provider/imapsmtp/tlsdial.go
  - internal/provider/imapsmtp/search.go
  - internal/domain/mail/backend.go
  - internal/mime/untrusted.go
---

This page shows the main rules of the security design of email-mcp, where the
code enforces each rule, and which test fails when a rule breaks. Read it before
you change code that checks a caller, a permission, a confirmation, a secret or
a connection, and when you review such a change.

The agent is the AI (artificial intelligence) program that calls the tools of
email-mcp. A tool is one action that the agent can call, such as `get_message`.
The AI app is the program that runs the agent and talks to email-mcp, such as
Claude Code. A use case is one job of the program for a caller, such as "send
this mail". The person is the human who owns the mail accounts.

## The model

The agent is a caller with limited access. It reaches the use cases only
through the tools of `mcpserver` (the package that holds the tools), and
`service` (the package of the use cases) checks the permission of each call
before it touches the mail. The person has a second way in: the web portal (the
local web site of the person) and the commands at the terminal. Only that way
changes the settings of an account, makes an access token (a secret that a
program sends with each request to local HTTP, where HTTP is HyperText Transfer
Protocol), or approves an action on the
approval page (the page of the web portal where the person approves a waiting
send or move to the Trash). A "yes" that the person gives in the AI app comes
from the form of the app, and never from the arguments of a tool. So that "yes"
is only as good as the app that shows the form (see [Trust in the AI
app](/manual/daily-use/confirmations/#trust-in-the-ai-app)).

The model is for an agent that can only call the tools and fetch web addresses.
An agent that runs shell commands as the same operating system user, or that
controls the browser of the person, is outside it. The manual page
[Security](/manual/daily-use/security/) states the threat model and its limits
for users, and [Recommended Claude Code deny
rules](/manual/daily-use/deny-rules/) shows how to keep an agent away from the
shell. This page does not repeat them.

## Two channels into service

{{< diagram "two-channels" >}}

The agent's channel:

- **Stdio.** On stdio (standard input and output: the AI app starts the program
  and talks to it through two pipes; a pipe is a channel between two programs),
  the program reads its calls only from its standard input, and the AI app that
  started it holds the other end of that pipe. `localPrincipal` in
  `cmd/email-mcp` gives each call on stdio the local tenant (the owner of a set
  of accounts; a local install has one) with no scope limit. Scopes are the
  permissions that an access token carries.
- **Local HTTP.** Local HTTP is the server that `email-mcp serve --http` starts
  for programs on the same computer. `Handler` in
  {{< src "internal/httpserve/httpserve.go" >}} runs four checks, in this order.
  First the Host header (the name of the server that a request asks for). Then
  the Origin header (the web site that a browser request comes from). Then the
  size of the body. Last the access token. The page "Local HTTP mode" in the
  group "Ways in" goes deeper.
- **The tools.** `addTool` in {{< src "internal/mcpserver/server.go" >}} wraps
  each tool in `safe`, which asks for the caller first. A call with no caller,
  or with a caller that has no tenant, is refused with "not authorized", and the
  tool does not run.
- **The reach.** The tools call the use cases that read or change mail. They
  call none of the use cases that a test guards for the person (the list
  `userOnlyCalls`). `mcpserver` imports neither `web` (the package of the web
  portal) nor `tokens` (the package that makes access tokens), and the links
  that a tool returns carry no secret.

The person's channel:

- **The browser** goes to `web`, the web portal. `Server.serve` in
  {{< src "internal/web/guard.go" >}} first sets the security headers (headers
  on each answer that limit what a page may load and keep). Then it works in
  this order:
  1. It checks the Host header.
  2. It runs the cross-origin check (a check that refuses a form post that the
     browser marks as coming from another site).
  3. It answers `/setup`. That path takes only GET and HEAD, and it checks the
     name `email-mcp.localhost` itself. It takes the one-time link that
     `email-mcp setup` opens, and starts the session.
  4. It answers the static files (the style sheets and the script of the
     portal, which hold nothing private), with no name check and no session.
  5. For every other path, it checks the name `email-mcp.localhost`, then the
     session (the sign-in of one browser, kept in a cookie), then the CSRF
     token (cross-site request forgery token: a value in each form that proves
     that the form came from the portal) for a request that is not GET or HEAD.

  The page "The web portal" in the group "Ways in" goes deeper.
- **The terminal** runs the commands of `cmd/email-mcp` other than `serve`, such
  as `setup` and `token`. The ones that call a use case run as the local tenant.
- **The reach.** This channel calls the user-only use cases (the use cases that
  only the person may call), such as `UpdateSettings`, `ApprovePending` and
  `CreateToken`.

Both channels end in `service`. Each use case reads the principal (the caller of
a request: its tenant, the scopes of its token, and the name of its client) with
`PrincipalFrom` in {{< src "internal/service/principal.go" >}}. Each channel
makes its own principal, as [Architecture
overview](/tech/start-here/overview/#the-caller-and-the-tenant) explains. Under
`service`, both channels use the same parts: `policy`, `confirm`, `outbox`,
`audit`, `store` with `secrets`, and the mail backend.

## Rules and their tests

Each row names a rule, the code that enforces it, and the test that fails when
it breaks. The tests in {{< src "internal/e2e/security_test.go" >}} run the
whole program (see [The end-to-end tests](#the-end-to-end-tests)). The other
tests are named with their package.

The table uses a few terms. A level is a number from 0 to 5 that says what the
agent may do with the mail of an account. Level 5 has the name Delete: it moves
mail to the Trash and never deletes mail for good. The allow-list is the list of
browser origins that local HTTP accepts, and it is empty by default. The
activity log is the record of what the agent and the person did. TLS (Transport
Layer Security) encrypts a connection. A certificate is the signed proof of
identity that a server shows when a TLS connection starts, and a pin is the
fingerprint of a certificate that the person chose to trust. A loopback host is
this computer, such as `127.0.0.1`. To seal a value is to encrypt it, and a hash
is a fingerprint of bytes. A request state names an action that waits for the
person, a page token names the next page of a search, and a cursor marks where
the last check for arrived mail stopped. The agent passes all three back. A
form token is the CSRF token, and a certificate offer is the offer of the portal
to trust a certificate.

| Rule | Where it is enforced | Test that fails |
|---|---|---|
| The tenant comes from the principal, not from the arguments of a tool. Each query of `store` for one tenant has a `tenant_id = ?` condition. | `PrincipalFrom`; `authorize` in {{< src "internal/service/service.go" >}}; the queries of `store` (see the package comment of {{< src "internal/store/store.go" >}}) | `TestAccountOfOtherTenantNotFound` and `TestWriteOnAccountOfOtherTenantNotFound` in `service`; `TestGetAccountOtherTenantNotFound` in `store` |
| Each tool runs through `safe`, so no tool runs without a caller. | `addTool` and `safe` | `TestEveryToolRunsThroughSafe` in `mcpserver`. It calls each tool that the server lists, with a caller lookup that fails, and each tool must answer "not authorized". |
| A tool below its level is refused before the mail server gets a command. | `authorize`, which calls `policy.Decide` in {{< src "internal/policy/policy.go" >}} | `TestEveryWriteToolRefusedBelowItsLevel` |
| No tool calls a use case of the list `userOnlyCalls`: the use cases of the person that a test guards. | The code of `mcpserver`: a test checks that it names no call of the list `userOnlyCalls`, and that it imports neither `web` nor `tokens` | `TestNoToolReachesTheUserOnlyUseCases` and `TestNoToolIsNamedForAUserOnlyUseCase` in {{< src "internal/mcpserver/useronly_test.go" >}} |
| A link that the agent gets carries no secret. Opened without the session of the person, it shows one sentence and changes nothing. | `Links` in {{< src "internal/web/links.go" >}}; the session check of `Server.serve` | `TestLocalUILinksRevealNothingWithoutSession` |
| The web portal refuses a wrong Host header, a cross-origin form post, a request without a session (other than `/setup` and the static files), and a form post without the CSRF token of its session. | `hostAllowed`, `Server.serve` and `checkForm` | `TestLocalUIRefusesWrongHost`; `TestLocalUIRefusesCrossOriginPost`; `TestLocalUILinksRevealNothingWithoutSession`; `TestEveryPostRouteNeedsTheCSRFToken` in `web` |
| Local HTTP refuses a wrong Host header, and a request with an Origin header that is not on the allow-list, also with a valid token. It refuses a request without a valid token. | `Handler` | `TestLocalUIRefusesWrongHost` and `TestLocalUIRefusesCrossOriginPost`, which test the endpoint too; `TestHTTPRejectsMissingToken` and `TestHTTPCheckOrder` in `httpserve` |
| A "yes" acts once, in time, for the exact request that the person was asked about, in the channel where it was asked, and for the tenant, the account and the kind of action that asked. | `Manager.Claim` and `HashPayload` in {{< src "internal/confirm/confirm.go" >}}; `claimAnswer`, which checks the account and the kind with `sameAction`, and `samePreview`, in `service`; `accepted` in {{< src "internal/mcpserver/elicit.go" >}} | `TestConfirmationCannotBeSkipped` |
| The password of an account, the payload and preview (the exact request and its summary) of a waiting action, the message of a queued send, and the recipients and arguments of an activity log entry are sealed in the database. Access tokens, setup tokens and session ids are stored as hashes. | `Vault` in {{< src "internal/secrets/vault.go" >}}, which seals a value with a key of the tenant, and the sealed and hashed columns of `store` | `TestNoPlaintextSecretsInDB` |
| The program log gets fixed codes, ids, counts and names such as tool names and Go type names, not a secret, a mail address, a subject or a body. | `safe` in {{< src "internal/mcpserver/server.go" >}}, which logs the name of the tool, a fixed code and the Go types of an error (`errtype.Chain`); `secrets.Secret`, which prints `[redacted]`; the log rules of each package | `TestNoSecretsInLogs` |
| A connection to a remote mail server uses TLS 1.2 or newer (`tls.VersionTLS12` in `newPinnedTLSConfig`) and checks the certificate, or its pin. Plain text is allowed only to a loopback host, on a local install. | `MailServer.Validate` in {{< src "internal/domain/account/account.go" >}}; the secure dialer in {{< src "internal/provider/imapsmtp/tlsdial.go" >}} | `TestPlainTextToRemoteRefused`; `TestTLSCertificateChecksEnforced` |
| `mail.Backend` has no method that deletes mail for good, and its comment asks each implementation to keep it so. `move_messages` refuses a Trash folder that email-mcp recognizes, at every level. `trash_messages` needs level 5, and `discard_draft` moves a draft to the Trash at level 3. | The comment of `mail.Backend` in {{< src "internal/domain/mail/backend.go" >}}; in `service`: `Trash`, which needs `policy.ActTrash` (level 5), `DiscardDraft`, and `Move`, which refuses such a folder with `ErrMoveToTrash` | `TestTrashMoveBlockedBelowLevel5` checks the refusals of `move_messages`, `trash_messages` and `discard_draft`. No single test covers the rule of `mail.Backend` for all providers, so check it when you review a change to `mail.Backend` or to a provider. |
| The text that a sender wrote (the name, the subject, the body and the names of the attachments) is cleaned of characters that a reader cannot see, and it is put inside an `Untrusted` wrapper. An address stays outside the wrapper only when it has the plain form that `mime.IsPlainAddress` accepts. | `CleanUntrusted` in {{< src "internal/mime/untrusted.go" >}}; `messageOut` and the `Untrusted` wrapper in {{< src "internal/mcpserver/shape.go" >}} | `TestCleanUntrustedRemovesInvisible` and `TestCleanUntrustedRemovesWholeRanges` in `mime`; `TestUntrustedCleaned` and `TestSearchResultMarksUntrusted` in `mcpserver` |
| The agent gets a text that email-mcp wrote for each known error, and "internal error" for any other. | `toolError`, `permissionFailure`, `reauthFailure` and `rateLimitFailure` in {{< src "internal/mcpserver/errors.go" >}} | `TestToolErrorMapping` and `TestUnknownErrorIsInternalError` in `mcpserver` |
| Request states, page tokens, cursors, form tokens and certificate offers are signed (see [Signed values](#signed-values)). | `Signer` in {{< src "internal/signing/signing.go" >}} and its callers | `TestOpenTamperedInvalid` and `TestOpenWrongPurposeInvalid` in `signing`; `TestListNewMessagesRejectsAForgedCursor` in `mcpserver` |

The pages that own each area give the details: [The send
flow](/tech/inside-the-server/send-flow/#how-the-yes-is-tied-to-the-message) for
the "yes", [Permissions](/tech/inside-the-server/permissions/#levels) for the
levels, [Storage and
secrets](/tech/inside-the-server/storage-and-secrets/#sealed-hashed-or-plain)
for what is sealed, [Activity log and program
log](/tech/inside-the-server/audit-and-logging/#the-program-log) for the log,
[One tool call, step by
step](/tech/start-here/tool-call/#errors-become-fixed-texts) for the error
texts, [The mail backend](/tech/mail/mail-backend/#the-secure-dialer) for TLS
and the pins, and [Mail content](/tech/mail/mail-content/) for the cleaning.

Two rows need a note. For the row about errors: a few kinds of error add a
field that `service` or `policy` wrote for the agent. [One tool call, step by
step](/tech/start-here/tool-call/#errors-become-fixed-texts) lists them.

For the cleaning row, note a limit: in HTML (HyperText Markup Language) mail,
email-mcp cannot always tell what a reader sees (see [Known
limitations](/manual/reference/known-limitations/)). When it matters, open the
mail yourself.

## Failing closed

The checks below fail closed: when one cannot decide, the answer is no. For
`policy.Decide`, see the section of Permissions [How a decision is
made](/tech/inside-the-server/permissions/#how-a-decision-is-made). The other
checks that fail closed are these:

- `safe` refuses a caller without a tenant before the tool runs, and
  `PrincipalFrom` counts such a principal as no principal.
- The zero value (the value that Go gives a field that nobody set) of
  `Principal.InBandConfirm` is false. So a caller that nobody thought about
  cannot answer a confirmation in the AI app, and gets the approval page.
- A `Signer` that was not made with `New` has no key, and it opens no token
  (`TestZeroSignerFailsClosed` in `signing`).
- When the link of a permission error cannot be built, the action is still
  refused, with an empty link.

## Signed values

`Signer` makes a small token that the server gives to the agent or to a
browser, and gets back later. The token is the JSON (JavaScript Object Notation)
of a purpose, an expiry and a value, in base64url (a text form of bytes that is
safe in a web address), with an HMAC-SHA-256 signature. HMAC (hash-based message
authentication code) with SHA-256 (a hash with a 256-bit result) makes a hash
with a secret key. The key comes from the server key with the label
`signing/v1` (see [Storage and
secrets](/tech/inside-the-server/storage-and-secrets/#keys)).

- Anyone who holds a token can read it, so no secret goes into a token.
- `Open` accepts a token only for the purpose that it was made for. `signing`
  names four purposes: `PurposeCursor`, `PurposePage`, `PurposeRequestState`
  and `PurposeCSRF`. `web` adds `cert_offer` for the offer to trust a
  certificate. In `provider/imapsmtp`, `seal` and `open` in
  {{< src "internal/provider/imapsmtp/search.go" >}} add the tenant and the
  account (`TokenScope`) to the purpose of a page token and a cursor, so a token
  of one
  account fails for another account (see [The mail
  backend](/tech/mail/mail-backend/#refs-folders-and-tags)).
- A token that is changed, made without the key, made for another purpose or
  not well formed gives `ErrInvalid`, and the caller cannot tell these cases
  apart. A genuine token whose time has passed gives `ErrExpired`.
- `Open` reads at most 64 KiB (kibibytes: 65,536 bytes; `maxTokenLen`), and
  `New` needs a key of at least 32 bytes (`minKeyLen`).
- A signer keeps no record of the tokens that it made. A use that must happen
  once keeps its own record. For a "yes" in the AI app, `confirm` claims the
  action: one database statement moves it from waiting to approved, and only
  one caller succeeds.

The other values that the agent passes back, such as a message ref, an
attachment id and a send id, are not signed. They give no right, because every
call checks the tenant and the permission first (see [The mail
backend](/tech/mail/mail-backend/#refs-folders-and-tags)).

## The end-to-end tests

{{< src "internal/e2e/security_test.go" >}} holds the tests of the security
checklist of the design spec. Each test opens the whole program with `app.Open`,
as the command `serve` does. Then it calls the program as an agent and as a
browser would: with an MCP (Model Context Protocol) client on an in-memory
connection, through the local HTTP endpoint, and through the web portal. The
helpers are in {{< src "internal/e2e/harness_test.go" >}}. The mail servers are
the test servers `imaptest` and `smtptest` on the loopback address, so no test
of this package uses the network outside the computer.

- The test secrets are long and unusual strings, so a match in a log or in a
  file cannot be chance.
- Most tests of a refusal check the text or the status that the caller gets, so
  a failure shows which check refused.
- The test of the levels compares, before and after each refused call, the
  commands that reached the IMAP (Internet Message Access Protocol) server, the
  connections and messages of the SMTP (Simple Mail Transfer Protocol) server,
  the pending actions (the actions that wait for the person) and the outbox.

## When you change security code

Security code is code that checks a caller, a permission, a confirmation, a
secret or a connection, or that cleans text from a third party.

1. Run `make test`. It runs `go test -race ./...`, so the end-to-end tests run
   too. The flag `-race` turns on the race detector (a check that finds two
   goroutines, functions that Go runs at the same time, that use the same
   memory without a lock).
2. Add a test for the rule that you add or change: in `security_test.go` for a
   rule of the whole program, or next to the test of the package that the table
   names.
3. To add a tool: add it with `addTool`. In `security_test.go`, add its row to
   `requiredLevel`, and add its arguments to the map `args` of
   `TestEveryWriteToolRefusedBelowItsLevel`. That test fails for a tool without
   a row or without arguments. A tool that can change something needs a level
   above 0. The test allows level 0 only for a read-only tool, and for
   `request_permission_change` and `connect_account`, which return a link and
   change nothing.
4. To add a use case that only the person may call: add its name to
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
