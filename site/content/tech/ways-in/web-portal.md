---
title: The web portal
description: How the web portal starts, how a person signs in and why the portal has its own host name, which checks run on each request, and how pages are built and added.
weight: 30
code_areas:
  - internal/web/server.go
  - internal/web/guard.go
  - internal/web/session.go
  - internal/web/links.go
  - internal/web/static.go
  - internal/web/accounts.go
  - internal/web/approvals.go
  - internal/web/routes_test.go
  - internal/web/static_test.go
  - internal/web/templates/
  - internal/web/static/
  - cmd/email-mcp/setup.go
  - internal/connect/connect.go
  - DESIGN.md
  - PRODUCT.md
---

This page explains the web portal: the local web site where the person connects
accounts, sets what the agent (the AI program that calls the tools) may do,
answers the actions that wait, and reads the activity. It covers how the portal
starts, how a person signs in, the checks of each request, how pages are built,
and how to add one. Read it before you change the package `web` or the command
`setup`.

The tech docs say "web portal", as `PRODUCT.md` and `DESIGN.md` do. The user
manual says "web pages", and the code comments say "web interface". The menu
calls the approvals page "Waiting for you", and the route and the code call it
`approvals`. The manual page [The web pages](/manual/daily-use/web-pages/) shows
what the person sees.

## When it starts and where it listens

`web.New` in {{< src "internal/web/server.go" >}} makes a `Server` that does
not listen yet. `EnsureStarted` starts it the first time that an address of the
portal is needed, and returns the base URL (the start of every address of the
portal), such as `http://email-mcp.localhost:53123`.

- `email-mcp setup` starts it, through `NewSetupURL`.
- `serve` starts it when a tool needs a link, because each method of `Links`
  calls `EnsureStarted` first. A `serve` process that makes no link does not
  listen.
- It listens on `127.0.0.1:0` (`listenAddr`): the IPv4 (Internet Protocol
  version 4) loopback address (an address that only this computer can reach),
  on a port that the system picks. A port is the number that names one service
  on an address.
- It listens once for each `Server`, and `app.Open` makes one `Server` for each
  `App`. So each process that serves the portal has its own port.
- When its listener fails, the server forgets its address. The next call of
  `EnsureStarted` starts a new listener, maybe on another port.
- `App.Close` shuts it down in step 5, after the drain of the outbox (the queue
  of outgoing mail), so the person can still reject an action during the drain.
  After `Shutdown`, `EnsureStarted` returns `ErrClosed`.

`EnsureStarted` gives the HTTP (HyperText Transfer Protocol) server its limits:
10 seconds to read the headers, 1 minute for the whole request, 2 minutes for
an open connection that waits for its next request, and 64 KiB of headers (a
KiB is 1,024 bytes).

## Signing in

{{< diagram "portal-sign-in" >}}

Every page needs a browser session that only the person can make, except
`/setup` and the static files. A session is the sign-in of one browser, kept in
the database. The person runs `email-mcp setup` in a terminal, and
{{< src "cmd/email-mcp/setup.go" >}} and
{{< src "internal/web/session.go" >}} take these steps:

1. `NewSetupURL` starts the server and makes a setup token: 32 random bytes
   (`randomBytes`). It stores the SHA-256 of the token (SHA is Secure Hash
   Algorithm; the hash is a short value from which the token cannot be read
   back), with an end 10 minutes later (`setupTokenTTL`), in the table
   `web_setup_tokens`. It returns the link
   `http://email-mcp.localhost:<port>/setup?token=<token>`. This link is the
   only address of the portal that holds a secret.
2. `setup` prints the link on standard output. It also prints the address of a
   help page on `127.0.0.1` (`SignInHelpURL`), which holds no token.
3. `writeSignInPage` writes a small page that sends the browser on to the link.
   The page is in a new folder that only this user can read, with the mode 0600
   (only the owner may read and write it). `openBrowser` gets the file address
   of that page, not the link, because other users of the computer can read the
   arguments of a process. The file goes after 1 minute (`signInPageLife`), or
   when `setup` stops.
4. The browser asks for `/setup` with the token. `handleSetup` swaps the token
   for a session: `store.UseWebSetupToken` deletes the row of the token and adds
   the session in one transaction (a group of database changes that happen all
   together or not at all), so a token works once. The session id is 32
   new random bytes, and the store keeps only its SHA-256.
5. `handleSetup` sets the cookie (a value that the browser keeps and sends back
   with each request) `emcp_session` (`cookieName`) and redirects to
   `/` with 303 (See Other), so the token leaves the address bar and the history
   entry of the page.

A missing, malformed, used, expired or unknown token gets the no-session page,
the same for every cause. HEAD (a request for the headers only) on `/setup` uses
up the token too. It is the one place where a safe method, GET or HEAD, changes
state.

The cookie is `HttpOnly` (a script of a page cannot read it) and `SameSite=Lax`
(from another site, the browser sends it only with a top-level GET, such as a
click on a link). It has no `Domain`. It is not `Secure`, because the server
speaks plain HTTP on the loopback address.

A session lives at most 24 hours (`sessionTTL`), and ends after 2 hours without
a request (`sessionIdleTTL`). The last use is written at most once a minute
(`sessionTouchStep`), so the idle end can come up to a minute early. Sessions
are rows of the table `web_sessions`, and the server reads them on each
request. So a session that one process made works in every other process, also
on another port. The person ends sessions in three ways:

- the button **Sign out** in the menu (`POST /signout`) ends the session of this
  browser;
- the Connections page lists the signed-in browsers, and its **Sign out** button
  ends one of them (`POST /sessions/{id}/revoke`);
- `email-mcp setup --revoke-sessions` (`RevokeSessions`) deletes every session
  and every unused setup token of the local tenant (the owner of the accounts
  of a local install), in every process.

### Why the portal has its own host name

A browser sends a cookie to every port of a host. So a cookie of `127.0.0.1`
would go to every other web service on this computer. The portal uses the name
`email-mcp.localhost` (`uiHost`) in the setup link and in each link of `Links`,
and only this name gets a cookie. A name under `localhost` always means this
computer (RFC 6761: the Request for Comments, or standard, that keeps these
names for it). Chrome, Edge and Firefox answer it without DNS (the Domain Name
System). No other program uses this name, so the cookie stays with the portal.

A request on `127.0.0.1` or `localhost` gets a page with 403 that says how to
reach the name, with a line for the hosts file (the file of the computer that
maps names to addresses). `useName` makes this page. No setup token is used
there, no cookie is set, and no session works there, also not with a good
cookie.

## The checks, in order

{{< diagram "portal-checks" >}}

The method `serve` in {{< src "internal/web/guard.go" >}} runs these checks on
every request, in this order. The package comment of `server.go` lists them too.

| Step | Code | Refused with | Log code |
|---|---|---|---|
| 1. Headers | `setSecurityHeaders` sets the security headers of every answer. | none | none |
| 2. `Host` | `hostAllowed`: the connection came in on a loopback address, and the `Host` header is exactly `email-mcp.localhost:<port>`, `127.0.0.1:<port>` or `localhost:<port>`, with the port of the connection. | 403 (Forbidden) | `bad_host` |
| 3. Cross-origin | `http.CrossOriginProtection` refuses a request that the browser marks as cross-origin, unless it is GET, HEAD or OPTIONS. | 403 | `cross_origin` |
| 4. `/setup` and `/static/` | The guard answers them itself, never through the routes. They take only GET and HEAD, and need no session. `/setup` on another name gets the page of step 5. | 405 (Method Not Allowed) for another method; 404 for an address under `/static/` that is not a file | `bad_method`, `no_static` |
| 5. Name | `onUIHost`: a request on `127.0.0.1` or `localhost` gets the page with the name. | 403 | `use_name` |
| 6. Session | `sessionOf`: a valid `emcp_session` cookie. | 401 (Unauthorized), with the no-session page | `no_session` |
| 7. Form token | `checkForm`, for each method that is not GET or HEAD: a form body of at most 15 MiB (`httpserve.MaxBodyBytes`; a MiB is 1,048,576 bytes), with the CSRF token of the session. | 403; 413 for a larger body; 400 for a form that cannot be read | `bad_csrf`, `body_too_large`, `bad_form` |
| 8. Route | The mux of `net/http` (the router that picks the handler of a path) finds the route in `routes`. | 404 or 405 when no route matches | none |

CSRF is cross-site request forgery: another site makes the browser send a
request to the portal. The CSRF token of a session (`csrfToken`) is the SHA-256
of the session id, signed for the purpose `csrf` with `signing.Signer`, which
signs values with a key of the program. It is valid for as long as a session
lives. A form carries it in the hidden field `csrf_token`. It must be in the
url-encoded body, never in the address, and it must belong to the session of the
request. `csrfValid` refuses a token of another session, a changed token, an
expired token, and a token made for another purpose.

Why this order:

- The `Host` check stops a web page that uses DNS rebinding (a name of the
  attacker that points to `127.0.0.1`). Such a page sends its own name in the
  `Host` header, so it gets 403 before the session is looked at.
- `checkForm` reads the body only after the session check, so a browser without
  a session cannot make the server read a body.
- A refused request gets a fixed text, and repeats nothing of the request. The
  log gets the route pattern, the status and a fixed code, and never a token, a
  cookie, a session id, a CSRF token, a path or a query string.

### A GET only reads

The variable `routes` in `server.go` lists each route: a method and a path,
such as `GET /accounts` or `POST /approvals/{id}/approve`, with its handler. A
GET route only reads. Every change is a POST of a
url-encoded form (the usual body of an HTML form: pairs of a name and a value)
with the CSRF token. The setup link, outside the routes, is the one exception.
Because the cookie is `SameSite=Lax`, a link from another site can open a GET
page, and that is safe because no GET route changes anything. Three tests in
{{< src "internal/web/routes_test.go" >}} go through the list:

- `TestEveryRouteNamesItsMethod`: each pattern starts with GET or POST.
- `TestEveryPostRouteNeedsTheCSRFToken`: each POST route answers 403 to a
  missing token, a wrong token, the token of another session, and a token in the
  address, and nothing changes.
- `TestNoGetRouteChangesState`: each GET route, also with HEAD, changes
  nothing, even when its address holds a full form and a valid token.

## Pages, styles and the script

The pages are `html/template` files in `templates/`, embedded (built into the
program file) with `go:embed`. `html/template` is the package of the Go library
that escapes each value for its place in the page, so a text is shown as text
and is not read as HTML (HyperText Markup Language). `parsePages` reads
`layout.html`, and then each other file as a copy of the layout with that file
added. A page is named by its file name without `.html`. `render` runs the
template into a buffer first, so a template error gives a clean 500 and not half
a page. A page with a session gets its CSRF token and, for the menu, the number
of actions that wait.

The static files are `app.css`, `app.js` and `icon.svg` in `static/`, embedded
too. `loadStatic` in {{< src "internal/web/static.go" >}} gives each one the
address `/static/<first 16 hex (base 16) digits of its SHA-256>/<name>`, so a
changed file gets a new address. A static file is the one answer that a browser
may keep, for one year (`staticCacheControl`). Every other answer has
`Cache-Control: no-store`. `handleStatic` answers only the exact address of a
file, with the fixed `Content-Type` of `staticTypes`.

`setSecurityHeaders` sets four headers on every answer: the
Content-Security-Policy (a header that tells the browser which sources a page
may load), `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`
and `Cache-Control: no-store`. The policy is the constant
`contentSecurityPolicy`:

```text
default-src 'self'; script-src 'self'; form-action 'self';
frame-ancestors 'none'; base-uri 'none'
```

A page loads only from its own origin, sends forms only to its own origin, and
cannot be put in a frame. No inline script and no inline style run, so
`TestTemplatesHaveNoInlineStyleOrScript` in
{{< src "internal/web/static_test.go" >}} fails on one in a template.

The one script, `static/app.js`, only shows that a form is working: the pressed
button shows its busy text, and the other buttons of its group stop working.
Every form works without it, because the pages are plain HTML forms that the
browser posts. `TestScriptOnlyShowsThatAFormWorks` fails when the script sends a
request of its own, moves the page, or writes HTML.

The visual rules of the portal are in {{< src "DESIGN.md" >}}: the colours, the
type, the spacing and the parts of a page. {{< src "PRODUCT.md" >}} says who
uses the portal, what it is for, and its principles. Read both before you
change how a page looks.

## How the portal reaches the service

`app.Open` makes the web server first, then the service with the `Links` of the
server, and then calls `Server.Attach`, because the service needs the links.
`Links` in {{< src "internal/web/links.go" >}} implements `service.Links`:
`Connect`, `Settings` and `Approval` return the addresses that a tool gives to
the agent. Each one calls `EnsureStarted`, and escapes an id as one part of the
path. No link holds a token or a session, so a link works only in a browser
that the person signed in.

A page that calls the service uses `userCall` in
{{< src "internal/web/accounts.go" >}}. It gives the call the principal (the
caller of a request) of the person: the tenant of the session, no scope limit,
and the client name `web`. A scope is a permission that an access token
carries, and the person has no token. Each change of an account, a setting or a
token goes through `service`, and so does each answer on the approvals page and
each cancel on the activity page.

The portal also reads some data itself:

- the accounts, with `store.ListAccounts` and `store.GetAccount`;
- the setup tokens and the sessions, with the calls of the store for the tables
  `web_*` (signing out one browser deletes its row there);
- the access tokens, with `tokens.Manager.List`;
- the activity log, with `audit.Log.List`;
- the suggested server settings of an address, with `discovery.Finder.Mail`.

The pages that connect an account use the rules of
{{< src "internal/connect/connect.go" >}}, as the commands do. The page
[Connecting an account](/tech/mail/connecting-an-account/#the-shared-package)
explains them. An offer to trust a certificate (the signed proof of identity
that a mail server shows) is a signed token that lasts 10 minutes
(`certOfferTTL`).

## The approval page

The approvals page lists the actions that wait for the person, and shows one of
them with its Approve and Reject buttons. In
{{< src "internal/web/approvals.go" >}}, only an action of the mode `approval`
is shown or answered. An action that waits for an answer in the chat of the AI
app is not shown, and the service refuses to approve or reject it here, so one
message cannot be approved twice. Approve and Reject call
`Service.ApprovePending` and `Service.RejectPending` only from a POST with the
CSRF token.

For a send, `Service.PendingDetail` builds the whole message again from the
stored request before the page shows it. The page has the Approve button only
when it shows all of the message (`CanApprove`). When the message cannot be
built now, for example while the mail server does not answer for the original
of a reply, the page shows the start that was stored when the agent asked, and
only Reject. The service refuses Approve in that case too, because it builds the
message in the same way. Every text of the message comes cleaned from the
service, and the HTML of a message never goes into a page. The page [The send
flow](/tech/inside-the-server/send-flow/#what-the-person-sees) explains what
the person sees.

## Add a page

1. Write the template in `templates/`. Keep its name, the file name without
   `.html`, in a constant such as `pageConnections`.
2. Write the handler, a method of `Server`. Call the service through
   `userCall`. Answer with `render`, `message` or `fail`, with fixed texts.
3. Add the route to `routes` in `server.go`, with its method. A GET route only
   reads. A change is a POST, and its form holds
   `{{template "csrf_field" .CSRFToken}}`.
4. If the path has an `{id}`, add its case to `routeFixture.path` in
   `routes_test.go`. Until then, the route tests fail on the route.
5. Give a page with the menu its section in `sectionOf`.
   `TestEveryMenuPageHasASection` checks it.
6. Put no inline style or script in the template. Styles go in
   `static/app.css`, after the rules of `DESIGN.md`.
7. Run the tests of `internal/web`, with the three route tests.
8. Update [The web pages](/manual/daily-use/web-pages/) in the user manual. A
   change to `approval.html` also needs the picture in
   `site/layouts/_partials/approval-picture.html` and the label test in
   `internal/web`.

## Limits and lifetimes

| Name | Value | What it bounds |
|---|---|---|
| `setupTokenTTL` (`session.go`) | 10 minutes | A setup link |
| `sessionTTL` (`session.go`) | 24 hours | A session, in use or not, and its CSRF token |
| `sessionIdleTTL` (`session.go`) | 2 hours | A session without a request |
| `sessionTouchStep` (`session.go`) | 1 minute | The shortest time between two writes of the last use of a session |
| `signInPageLife` (`setup.go`) | 1 minute | The file with the setup link |
| `certOfferTTL` (`accounts.go`) | 10 minutes | An offer to trust a certificate |
| `approvalOpenTimeout` (`approvals.go`) | 3 seconds | The wait of the approvals list for its first action. After it, the action shows what was stored, and only Reject. |
| `httpserve.MaxBodyBytes` (`guard.go`) | 15 MiB | The form body of a POST |
| `staticCacheControl` (`static.go`) | 1 year | How long a browser may keep a static file |
| The server of `EnsureStarted` (`server.go`) | 10 seconds, 1 minute, 2 minutes, 64 KiB | Reading the headers, reading the request, an open connection with no request, the headers |

## Related pages

- [The send flow](/tech/inside-the-server/send-flow/)
- [The command line](/tech/ways-in/command-line/)
- [Connecting an account](/tech/mail/connecting-an-account/)
- [The web pages](/manual/daily-use/web-pages/) in the user manual
- [Connect your first account](/manual/getting-started/connect-account/) in
  the user manual
