# Tech docs: design

This is the design for the tech docs, the third part of the docs site. It builds
on the [foundation](2026-11-05-docs-site-foundation-design.md). It follows the
decisions made there and does not reopen them.

The facts about the code were checked at commit `e254516` (release 0.6.0). Line
numbers in this spec come from that commit. The mark "(My call.)" means that the
author of this spec made the choice, and the owner can change it at review.
Terms are explained where they first appear. The end has a glossary.

## Intent

email-mcp is an MCP server. MCP (Model Context Protocol) is the open standard
that lets an AI app call tools of other programs. Here the tools give an agent
(the AI program) controlled access to mail through IMAP and SMTP, the standard
protocols for reading and for sending mail.

The intent of the tech docs: a developer who does not know the code can
understand how it works and why, and can change it with confidence.

Today the code has 27 packages under `internal/`, and one main program in
`cmd/email-mcp`. No page shows how they fit together. The README has two
maintainer sections ("Making a release" and "Development") and nothing about
structure. The only overview is the design spec of 2026-10-27. It is history,
and it differs from the code in places (see "Differences to resolve").

A developer who wants to fix a bug or add a feature must read code to find where
to start. The tech docs give that start: the picture, the reasons, and the place
in the code.

## Reader

The reader is an experienced backend developer.

- They know Go. They do not know this codebase.
- They come from the site menu, usually with one question. For example: "Where
  is the permission checked?" or "Why is there a copy of an HTML parser?"
- They read one or two pages, then go to the code.
- Some are contributors. Some review a pull request. The owner is one of them,
  months from now.

So each page must work alone. Its first paragraph says what it explains. It
names the files it covers, and it links to the code for detail.

## Goals

1. Explain how the parts fit together and why they are built this way.
2. Answer the questions of the page list below, each on one page.
3. Point to the code. Each page names the files and symbols it covers, and links
   to them at the version of the site.
4. Tell the author of a pull request which page to update (the code area map).
5. Stay true. Each fact comes from the code. Each number names its constant (see
   "Keeping facts true for each version").
6. Be safe to publish. Describe what the code does, never a weakness that is
   not fixed.
7. Use simple English, as the foundation asks.

## Non-goals

- A reference for every function, type or constant. `go doc` and the comments in
  the code do that job.
- A copy of the design spec, the plans or the rulings. The tech docs do not tell
  the history of decisions.
- Instructions for users. They belong in the manual.
- Pages for code that does not exist: calendar, contacts, Google, Microsoft and
  hosted mode. The overview has one sentence about them.
- A course on Go, MCP, IMAP or SQLite. A term is explained where it is used.
- Pages made from the code by a tool. People write the pages in the first
  version. (My call.)
- What the foundation already excludes: other versions, translations, search,
  analytics.

## Anti-goals

The work fails, even if the site builds, when:

- A page copies what the code already says: a list of fields, a description of
  functions one by one, a comment turned into prose. The docs explain how the
  parts fit and why. They link to the code for detail.
- A page states a fact that is not in the code, or a number with no source.
- A page, a table or a diagram describes a weakness that is not fixed, or gives
  attack steps.
- A diagram disagrees with the text next to it, or with the code.
- A page describes a feature, a check or a label that the release which
  publishes the page does not have.
- A page needs another page to be understood in its first paragraph.
- A link to the code leads to a file that does not exist.

## What the foundation already decides

These rules apply as written:

- The files are under `site/content/tech/`. The address is `/tech/`.
- The files follow `.github/markdown.instructions.md`: front matter (the block
  of settings at the top of a page file), no H1 in the body, lines of 80
  characters. See "Differences to resolve", item 12, for the front matter keys.
- The language stays as simple as the README: short sentences, plain verbs, a
  definition after each technical term.
- The tech docs have their own glossary page.
- Every fact comes from the code or from an existing document. If they
  disagree, the code wins.
- Public pages describe what the code does. They do not list weaknesses that are
  not fixed, and they give no attack steps.
- Diagrams are inline SVG (a picture made of shapes and text, written in the
  page) or images built before commit.
- A page works with no script. Nothing loads from another site.
- The site is published at each release. The footer shows the version.
- A pull request that changes behaviour must change a file under
  `site/content/tech/`, or carry the label `no-tech-docs-change`. The map in
  this spec tells the author which page.
- The tech docs are step 3 of the order of work: one pull request for each
  sidebar group, so that each one can be reviewed.

## Page list

### Sidebar

The sidebar has seven groups. Each group is a folder under `site/content/tech/`
with an `_index.md`. The title of the group is the title of its `_index.md`. The
`weight` values set the order. This is the same layout as the user manual, so
one sidebar template serves both parts. (My call.) The address of a page is its
path, for example `/tech/inside-the-server/send-flow/`. Code comments and the
pull request template will link to these addresses, so they must stay stable.

```text
site/content/tech/
  _index.md                       Tech docs (home)
  start-here/                     group 1
    _index.md
    overview.md
    package-map.md
    tool-call.md
  inside-the-server/              group 2
    _index.md
    send-flow.md
    permissions.md
    storage-and-secrets.md
    audit-and-logging.md
    startup-and-shutdown.md
  mail/                           group 3
    _index.md
    mail-backend.md
    mail-content.md
  ways-in/                        group 4
    _index.md
    command-line.md
    local-http.md
    web-portal.md
  security/                       group 5
    _index.md
    security-design.md
  work-on-the-code/               group 6
    _index.md
    local-development.md
    testing.md
    release-process.md
  reference/                      group 7
    _index.md
    glossary.md
```

| Group | Page | Title | Weight |
|---|---|---|---|
| Start here | `overview` | Architecture overview | 10 |
| Start here | `package-map` | Map of packages | 20 |
| Start here | `tool-call` | One tool call, step by step | 30 |
| Inside the server | `send-flow` | The send flow | 10 |
| Inside the server | `permissions` | Permissions | 20 |
| Inside the server | `storage-and-secrets` | Storage and secrets | 30 |
| Inside the server | `audit-and-logging` | Activity log and program log | 40 |
| Inside the server | `startup-and-shutdown` | Startup and shutdown | 50 |
| Mail | `mail-backend` | The mail backend | 10 |
| Mail | `mail-content` | Mail content | 20 |
| Ways in | `command-line` | The command line | 10 |
| Ways in | `local-http` | Local HTTP mode | 20 |
| Ways in | `web-portal` | The web portal | 30 |
| Security | `security-design` | Security design | 10 |
| Work on the code | `local-development` | Local development | 10 |
| Work on the code | `testing` | Testing | 20 |
| Work on the code | `release-process` | Making a release | 30 |
| Reference | `glossary` | Glossary | 10 |

The weights of the groups are: Start here 10, Inside the server 20, Mail 30,
Ways in 40, Security 50, Work on the code 60 and Reference 70. The weight of a
page is its weight inside its group.

That is 18 pages with content, 7 group pages and the home page: 26 files. A
group page has the title of the group and two or three sentences that say what
the group answers. The home page `_index.md` has the title "Tech docs", a short
introduction, and a table "If you want to know... read...", with one row for
each question in the page entries below. (My call.)

A page name is unique in the tech docs, as it is in the manual. This spec names
a page by its file name without `.md`.

### Changes to the expected list

The expected list had twelve pages. All twelve are kept. Six are added, because
the code has an area with its own rules that no other page can hold:

- `audit-and-logging`: the `audit` package and the log rules. Every other area
  follows these rules, and no other page would hold them.
- `startup-and-shutdown`: `internal/app` (1,432 lines of code, without tests).
  The order in which it builds and stops the parts is the reason that a send is
  never lost and never sent twice.
- `command-line`: `cmd/email-mcp` (2,969 lines), nine commands with shared
  prompts and rules.
- `mail-backend`: `domain/mail`, `provider/imapsmtp` (5,277 lines), `discovery`
  and `connect`. It is the one place where a new mail provider goes.
- `mail-content`: `internal/mime` (6,556 lines) and the copied HTML parser. It
  has its own rules for text from third parties.
- `glossary`: the foundation asks for it.

Not added, by design:

- A page for the libraries. A table in `package-map` is enough. (My call.)
- A page for `signing`, `inflight`, `errtype` or `version`. Each has fewer than
  210 lines. The pages that use them explain them.
- A page about the design history. One paragraph in `overview` is enough. (My
  call.)

### What every page has

The rules match the manual, so a reader meets the same shape in both parts.

- Front matter with `title`, `description` (one sentence, plain words) and
  `weight`. In addition, `code_areas`: the repository paths that the page
  covers, one per line. The manual spec offers `code_areas` as an option and
  does not show it. The tech docs use it on every page, and the template shows
  it as a list "Code covered by this page", with links to the code. (My call.)
  If the template cannot do this, the writer writes the list by hand with the
  shortcode `src`. A page that moves also gets `aliases`.
- The title is in the front matter. The body starts with an H2. Use H2 and H3
  only.
- A first paragraph that says what the page explains and who needs it.
- Sections named as short noun phrases or as the question the reader has.
- Define each technical term in brackets at its first use on each page. Expand
  each acronym at its first use on each page. Do not only link to the glossary.
  (My call: the same rule as the manual.)
- Fenced code blocks have a language. Each image has alt text.
- A closing list "Related pages", with at most five links.
- Links to other pages with Hugo's `relref`, so a wrong link fails the build.
  (My call.)
- A text form of every diagram: the table or the list next to it holds the same
  facts.
- No "last updated" line and no version number in the text. The site matches
  the release, and the footer names it.

A page that grows past about 1,500 words should be split. (My call.)

## Pages

Each entry gives the path, the purpose, the questions, what the page must cover
with its source, and the diagrams. "Source" lists code paths. "Design spec"
lists sections of the design spec of 2026-10-27, which is history: use it for
the why, and check each fact against the code. "Manual" gives the manual pages
to link to, by the paths of the manual spec. Those paths are stable (manual
spec, "What other parts rely on").

Rule for all pages: do not write how many tools or commands exist. The count
changes with each feature. Name the function that registers them instead.

Names. This part says "web portal", as `PRODUCT.md` and `DESIGN.md` do. The
manual says "web pages". The code comments say "web interface". The page
`web-portal` says this once and then keeps "web portal". The menu label of the
approval page is "Waiting for you" (`internal/web/templates/layout.html:52`).
The route and the code say "approvals". (My call.)

### `overview`: Architecture overview

Path: `site/content/tech/start-here/overview.md`

Purpose: show the whole program on one page: the layers, the two doors, the run
modes and the rules that hold everywhere.

Questions:

- What are the main parts, and what does each one do?
- Which code can an agent reach, and which code only a person can reach?
- Who is the caller, and what is a tenant (the owner of a set of accounts) in a
  local install?
- What starts when I run `serve`, and what can run at the same time?
- Why can several processes use one database?

Must cover:

- One Go program with several commands (`commands` in
  `cmd/email-mcp/main.go:30`).
- The layers from the top, one sentence of job for each, with a link to the page
  that goes deeper: the transport (stdio or `httpserve`), `mcpserver` (the
  tools), `service` (the use cases), then `policy`, `confirm`, `outbox`,
  `audit`, `store` with `secrets`, and the mail backend.
- Two doors into `service`. The agent uses tools. The person uses the web portal
  and the commands. Name the use cases that only a person may call. The test
  that keeps tools away from them is `internal/mcpserver/useronly_test.go`. The
  portal reads the store itself for sessions and lists. Every change of an
  account or a setting goes through `service`.
- The principal (the caller of a request: tenant, token scopes and client name,
  `internal/service/principal.go`). Stdio builds it in `localPrincipal`
  (`cmd/email-mcp/serve.go:227`). HTTP builds it from the token
  (`httpserve.Principal`). The tenant never comes from tool arguments. A local
  install has one tenant, `local` (`store.LocalTenantID`). Each store query for
  a tenant has a `tenant_id` condition (`internal/store/store.go:1`).
- Run modes. An MCP client starts its own `serve` process, so several clients
  mean several processes on one data directory. List what makes this safe, each
  with a link: the lock for the first master key (`app/kek.go`), the database
  in WAL mode (write-ahead log, a SQLite mode where readers and one writer work
  together), the atomic claim in the outbox, sessions and tokens kept in the
  database, and the fingerprint check in the backend cache.
- The rules that hold in all code, one sentence each with a link: fixed error
  texts for the agent, logs with ids, counts and codes only, third-party text
  cleaned and marked, mail only moves to Trash, and every check fails closed
  (when a check cannot decide, the answer is "no").
- The provider-neutral boundary, `mail.Backend`. Only IMAP and SMTP exist
  today. One sentence: other providers and hosted mode are planned in the design
  documents and are not built (`PRODUCT.md` says the same for hosted mode).
- One paragraph on the design documents: they are history. Where they differ
  from the code, the code wins. Link to the rewrite design spec only. (My call:
  no link to the plans or to the rulings file.)

Source: `cmd/email-mcp/main.go`, `cmd/email-mcp/serve.go`,
`internal/app/app.go`, `internal/service/service.go`,
`internal/service/principal.go`, `internal/mcpserver/server.go`,
`internal/store/tenants.go`, `internal/domain/mail/backend.go`. Design spec:
sections 3 and 4.

Diagram: D1.

Manual (link with `relref`): `_index` (Start here) and `daily-use/web-pages`.

### `package-map`: Map of packages

Path: `site/content/tech/start-here/package-map.md`

Purpose: one row for each package: what it does, where it sits in the import
order, and where to put new code.

Questions:

- Where does this code live?
- What may a package import?
- Which package do I change for this feature?
- Which library does what, and why is `go-imap` pinned?

Must cover:

- A table of all 28 packages (27 under `internal/` and `cmd/email-mcp`) with:
  path, layer number, job in one line, and the main entry point (for example
  `app.Open`, `service.Service`, `mail.Backend`, `store.Store`). The writer
  reads `go doc` and writes the job in new words.
- The layer rule: a package imports only packages of lower layers. Go forbids
  import cycles. Show the command that finds the layers, so a reader can repeat
  it: `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...`.
- The layers as found (see "Dependency order" below) and the edges that a reader
  does not expect. State them as facts.
- Where new code goes: a decision list. New tool: `mcpserver` and `service`. New
  mail provider: a package under `internal/provider/` and the factory in
  `service/backends.go`. New page of the portal: `web`. New table: `store` and a
  migration. New command: `cmd/email-mcp`.
- The direct libraries (from `go.mod`): module, the packages that import it, and
  why it is used. Cover `go-sdk`, `go-imap/v2` (pinned to the exact version
  `v2.0.0-beta.8`; Dependabot ignores it, see `.github/dependabot.yml`),
  `go-smtp`, `go-sasl`, `go-message`, `bluemonday`, `jsonschema-go`,
  `modernc.org/sqlite` (pure Go, so the binary stays static), `go-keyring`,
  `x/net`, `x/term`, `x/sys` (the file lock on Windows,
  `internal/app/lock_windows.go`) and `testify` (tests only).
- One line on the copied parser, `internal/thirdparty/xhtml`: the only
  third-party code that lives in the repository. Link to `mail-content`.

Source: `go list` output, the package comments, `go.mod`,
`.github/dependabot.yml`, `internal/thirdparty/xhtml/README.md`.

Design spec (history): sections 4.2 and 5.

Diagram: D2.

Note for the builder: `.github/copilot-instructions.md` has a shorter copy of
the package list. Both lists must agree. This spec does not change that file.

Leave out: lists of functions for each package.

### `tool-call`: One tool call, step by step

Path: `site/content/tech/start-here/tool-call.md`

Purpose: follow one call of `get_message` from the MCP layer to the mail server
and back, and show where each check and each record happens.

Questions:

- What happens between the request of the client and the IMAP server?
- Where is the permission checked?
- Where is the call recorded in the activity log?
- Where is text from the mail cleaned?
- Where do errors become the texts that the agent sees?
- What differs between stdio and HTTP?

Must cover, in order (mark the steps that exist only on HTTP):

1. Transport. Stdio: `mcp.IOTransport` over stdin and stdout
   (`cmd/email-mcp/serve.go`, `serveStdio`). HTTP: the limit on requests at
   once (`limitRequests`) and the checks of `httpserve` run first (see
   `local-http`).
2. Middleware of the SDK, from the outside in: `App.trackToolCalls`
   (`internal/app/app.go`) counts the call and refuses it while the program
   stops; `capture` (`internal/mcpserver/capture.go`) records the call when it
   ends; then the SDK checks the tool name and the arguments against the schema.
   The SDK runs the middleware that was added last first, so the order is the
   reverse of the order of the `AddReceivingMiddleware` calls (SDK doc,
   `go-sdk/mcp/server.go`, "AddReceivingMiddleware"). The SDK's own multi
   round-trip middleware, added in `mcp.NewServer`, sits inside both.
3. `safe` (`internal/mcpserver/server.go:119`): finds the principal, puts it in
   the context, recovers from a panic, turns an error into a tool result. Only
   `addTool` registers tools, so no tool can skip it
   (`TestEveryToolRunsThroughSafe`).
4. The handler (`getMessage` in `read_tools.go`): turns arguments into a
   `service` call.
5. `service.GetMessage` → `mailBackend` → `authorize` (`service.go:255`):
   principal, account of the tenant, `policy.Decide`, then the reconnect check.
   The order is deliberate: a caller who may not read learns nothing about the
   connection.
6. `BackendCache.Mail` gets the backend of the account. It decrypts the
   password only when it builds a new backend.
7. `imapsmtp.Backend.GetMessage`: a pooled connection, read-only `EXAMINE`,
   `BODY.PEEK` fetch (reading does not mark mail as read). `mime` decodes and
   converts the body.
8. Back in `service`: `noteBackendError` marks the account when the server
   refused the stored login.
9. Back in `mcpserver`: `messageOut` cleans every string with
   `mime.CleanUntrusted` and wraps third-party text in `Untrusted`. The result
   has structured content and a text copy that starts with a fixed warning line.
10. `capture` writes the call entry with sealed arguments (see
    `audit-and-logging`).

Then one section "What changes for a write tool": the confirmation, the audit
entries of the use case, the outbox. Link to `send-flow`.

Source: `internal/mcpserver/server.go`, `capture.go`, `read_tools.go`,
`shape.go`, `errors.go`; `internal/service/service.go`, `read.go`,
`backends.go`; `internal/provider/imapsmtp/backend.go`, `fetch.go`, `pool.go`;
`internal/app/app.go`; `cmd/email-mcp/serve.go`;
`internal/httpserve/httpserve.go`.

Design spec (history): sections 4.3 and 6.

Diagram: D3.

Leave out: the internals of the IMAP pool (`mail-backend` has them).

### `send-flow`: The send flow

Path: `site/content/tech/inside-the-server/send-flow.md`

Purpose: explain how a send goes from the agent's request to the mail server,
including the confirmation, the outbox and the undo window.

Questions:

- What happens when an agent calls `send_message`?
- When is the person asked, and how?
- How does the person's "yes" bind to the exact message?
- What is the undo window, and who can cancel?
- What if the program stops, or the connection breaks, during a send?
- How is a double send prevented?

Must cover:

- The entry. `sendMessage` (`mcpserver/write_tools.go`) takes a new message or a
  `draft_ref`, exactly one. `Service.Send` (`service/send.go:186`) has the order
  of the steps. Cite its doc comment as the source and explain the why.
- When the person is asked: the account switch `ConfirmSends`; a recipient with
  a character outside printable ASCII; the new-recipient check when the account
  has it on (the audit `reason` values are `confirm_sends` and `new_recipient`).
- Two channels. In band (the question appears in the chat of the client): the
  first call returns an input request (a form with one box, `confirm`) and a
  `RequestState`; the second call carries the answer. Fallback: the result is
  `pending_approval` with a link, the person decides in the portal, and the
  agent polls `get_send_status`. Who gets which channel is in `local-http`.
- What the person sees: the preview (`confirm.Preview`, `sendPreview` in
  `elicit.go`). Each value is cleaned. A recipient whose address would show
  differently after cleaning is refused.
- How the answer binds to the message: the payload is a canonical JSON of the
  request (one fixed text form for the same request). Its SHA-256 hash (a
  fingerprint of the bytes) is in the `RequestState`. `signing` signs the state
  with HMAC-SHA-256 (a hash made with a secret key, so only this server can make
  it), and the state carries the tenant and an expiry. When the answer
  comes, the service rebuilds the message from the payload and compares it with
  the stored preview (`samePreview`). `Manager.Claim` moves the action from
  `pending` to `approved` in one database step, so two answers cannot both act.
  The sealed payload and preview are removed when the action ends.
- Delivery. `deliver` reserves the send in the limiter, then calls
  `Outbox.Enqueue` with the undo window: `UndoWindowAuto` when nobody confirmed,
  `UndoWindowConfirmed` after a confirmation. A window of 0 sends before the
  call returns. A longer window stores the message and a worker sends it when
  due.
- The outbox rules (`internal/outbox/outbox.go` doc comment): the claim is
  atomic; the network send runs outside any transaction; transient errors retry
  after 30 seconds, 2 minutes and 10 minutes; the fourth failure ends the item;
  a result that cannot be known is marked failed and never retried; an item
  stuck in `sending` is marked failed after 15 minutes. The sender
  (`internal/app/sender.go`) checks the level of the account again at each try,
  discards the draft after a successful send, and marks the account for
  reconnect when the server refuses the login.
- Cancel. The agent has `cancel_send` (needs level 4). The person can cancel on
  the Activity page and with `email-mcp outbox cancel`, at any level. Once a
  send is running, cancel is too late (`outbox.ErrTooLate`).
- Stop: a short pointer to `startup-and-shutdown` (the drain).
- Trash uses the same confirmation with kind `trash` (level 5, the switch
  `ConfirmDeletes`, a daily limit, a preview of up to 20 messages).
- Status: `SendResult.SendID` is the id of the pending action first and of the
  outbox item after, so `get_send_status` follows one send to its end.

Source: `internal/service/send.go`, `write.go`; `internal/confirm/confirm.go`;
`internal/mcpserver/elicit.go`, `write_tools.go`; `internal/outbox/outbox.go`;
`internal/app/sender.go`; `internal/signing/signing.go`;
`cmd/email-mcp/outbox.go`. Design spec: sections 7.5 to 7.7 (history).

Diagrams: D4, D5, D6.

Manual (link with `relref`): `daily-use/confirmations`,
`daily-use/app-confirmation` and `daily-use/activity-and-undo`.

Leave out: how a message is built (`mail-content`).

### `permissions`: Permissions

Path: `site/content/tech/inside-the-server/permissions.md`

Purpose: explain levels, switches, token scopes and limits, who can change them,
and where each is enforced.

Questions:

- What can an agent do at each level?
- Who changes a level, and how?
- What do token scopes change?
- What are the limits, and when do they apply?
- What is full autonomy, and what does it need?
- What can never be done, at any level?

Must cover (heading "Levels" is a stable anchor, see "Keeping facts true"):

- The levels 0 to 5 and the actions that each allows (`account.Level`,
  `policy.actions`). Only mail has levels today. `account.Level` and the
  `Areas` map can hold more areas.
- `policy.Decide` (`policy.go:160`): the level that applies is the lower of the
  account level and the highest level that the scopes allow. A level outside 0
  to 5 counts as Off. An unknown action is refused. Only a send and a move to
  Trash can need confirmation.
- Who sets the level: only the person, on the settings page
  (`Service.UpdateSettings`). No tool changes a level.
  `request_permission_change` returns a link with the area and the level in the
  query. The page shows the request and applies it only when the person presses
  its own button (`POST /accounts/{id}/level`).
- Full autonomy and the risk acceptance: `NeedsRiskAcceptance`
  (`service/accounts.go:260`) has three cases. `AutonomyAcceptedAt` records the
  time. The warning text lives in `web/settings.go`; do not copy it.
- A new account: level Send, both confirmations on, default limits
  (`account.NewIMAPAccount`, `DefaultLimits`).
- Token scopes (`policy.KnownScopes`): they add up to the highest level they
  name. When the token lowers the level, the error has no settings link,
  because the settings cannot help (`PermissionError.TokenLimited`).
- Limits (`policy.Limiter`): the windows are fixed, in UTC. A limit of 0 allows
  nothing. A refusal says when to retry (`RateLimitError`). A send counts when
  it reaches the outbox, so a declined send uses no quota. `ErrLimit` is for a
  request that no wait can fix.
- What no level allows: permanent delete (`mail.Backend` has none); a move into
  Trash or a folder inside it (`ErrMoveToTrash`, use `trash_messages`); a file
  path as an attachment (tools take base64 data only).
- Where the text for the agent is made (`mcpserver/errors.go`) and when it holds
  a link.

Source: `internal/policy/policy.go`, `limiter.go`;
`internal/domain/account/account.go`; `internal/service/service.go`,
`accounts.go`, `write.go`; `internal/mcpserver/write_tools.go`, `errors.go`;
`internal/web/settings.go`; `internal/tokens/tokens.go`.

Design spec (history): sections 7.1 to 7.4 and 7.6.

Diagram: none. Use two tables: levels with actions, and limits with defaults.

Manual (link with `relref`): `daily-use/permission-levels` and
`daily-use/full-autonomy-and-limits`.

### `storage-and-secrets`: Storage and secrets

Path: `site/content/tech/inside-the-server/storage-and-secrets.md`

Purpose: show where data lives, which fields are encrypted, and how the keys
work.

Questions:

- Where are the files, and who may read them?
- Which fields are encrypted, hashed or plain?
- What is the master key, and where does it live?
- How does the first start make the key, and what if two programs start at once?
- How do I rotate the master key?
- How long is data kept?
- How do I add a migration?

Must cover:

- The data directory and its default by platform (`app.defaultDataDir`; the flag
  `--data-dir` and `EMAIL_MCP_DATA_DIR`). Modes 0700 for the directory and 0600
  for the file.
- How the database opens
  (`store.Open`): WAL, a busy timeout, foreign keys, `secure_delete`,
  `synchronous(FULL)`, and `_txlock=immediate`; why each one matters when
  several processes share the file (`store/store.go`, the comment on `newDB`).
- Migrations: embedded SQL files named `0001_name.sql`; they run in order; a
  database written by a newer program is refused (`ErrNewerSchema`). Do not edit
  a migration that a release has published. The warning about rebuilding a table
  that other tables reference with `ON DELETE CASCADE` (`store/store.go` package
  comment).
- A table of tables, grouped by purpose (keys; accounts and their secrets;
  pending actions; outbox; audit; counters and known recipients; tokens and web
  sessions).
- A table "sealed, hashed or plain": which field is which. Sealed: the account
  password, the payload and the preview of a pending action, the outbox
  message, the recipients and the arguments of an audit entry. Hashed: access
  tokens, setup tokens, session ids (SHA-256), known recipients (HMAC with a
  derived key). Plain: the rest.
- Envelope encryption (`secrets.Vault`): a data key (DEK) for each tenant seals
  the fields with AES-256-GCM; the associated data binds a value to its tenant,
  account and field (`secrets.AAD`); a master key (KEK) wraps the data keys; the
  version of the key is stored next to each sealed value. The server key signs
  tokens and makes the recipient hashes through derived keys (`signing/v1`,
  `known-recipients/v1`).
- Master key sources: `keychain` or `file:<absolute path>`, chosen by
  `EMAIL_MCP_KEK`. The keychain entry is `email-mcp` / `master-key`. A key file
  must have private permissions.
- First start (`app.loadKEK`): a lock file, then the key is made only when the
  database has no keys yet. If the database has keys and the key is missing,
  the start is refused. Explain why.
- Rotation (`App.RotateKEK`, the command `key rotate`): all stored keys are
  wrapped again in one transaction; the old key is never deleted.
- `secrets.Secret`: prints `[redacted]` in `fmt` and in `slog`. Only `Reveal`
  gives the value.
- Retention and clean-up: ended pending actions after 30 days, audit entries
  after 90 days, a checkpoint of the WAL every day (`App.dailyJobs`).

Source: `internal/store/*.go`, `internal/store/migrations/`,
`internal/secrets/*.go`, `internal/app/app.go`,
`internal/app/kek.go`, `cmd/email-mcp/key.go`.

Design spec (history): sections 9.1 to 9.4.

Diagrams: D7. D8 is optional.

Manual (link with `relref`): `reference/data-folder` and `reference/master-key`.

### `audit-and-logging`: Activity log and program log

Path: `site/content/tech/inside-the-server/audit-and-logging.md`

Purpose: explain the two logs, who reads each one, and the rules for writing to
them.

Questions:

- What does the activity log record, and where do the entries come from?
- What is never recorded?
- How long are entries kept?
- What goes in the program log, and where does it go?
- How do I add a log line safely?

Must cover:

- Activity log: two kinds of entry. A call entry is one `tools/call` request,
  whatever its outcome. An action entry is what a use case did or what the user
  did on a page. One send makes one call entry and one or more action entries.
- Where entries come from: `capture` in `mcpserver` for calls;
  `service.record`, `finish` and `recordOutcome` for actions.
- What is sealed: recipients and arguments. What is dropped: `body`, `html_body`
  and `data_base64` (`omittedArguments` in `capture.go`). What a call that fails
  the schema keeps: names and sizes only.
- How `Client` (a name from the MCP client) is cleaned and why a bad name never
  hides an action. `Detail` is a short code.
- Retention (90 days), and removal with the account (`ON DELETE CASCADE`).
- The program log: `slog`, text, to standard error. Standard output carries the
  protocol on stdio, so nothing else may write there. Logs hold ids, counts and
  fixed codes. `errtype.Chain` logs the Go type names of an error and never its
  text. `secrets.Secret` redacts values. The SDK logger is not set, because it
  logs error texts. `httpserve.ErrorLog` turns `net/http` messages into codes.
- A short checklist for a new log line.

Source: `internal/audit/audit.go`, `internal/mcpserver/capture.go`,
`internal/service/calls.go`, `write.go`, `internal/errtype/errtype.go`,
`internal/secrets/secret.go`, `internal/app/app.go`,
`internal/httpserve/httpserve.go`, `internal/web/activity.go`.

Design spec (history): sections 7.6 and 9.4.

Diagram: none. One table of the entry fields and how each is stored.

Manual (link with `relref`): `daily-use/activity-and-undo`.

### `startup-and-shutdown`: Startup and shutdown

Path: `site/content/tech/inside-the-server/startup-and-shutdown.md`

Purpose: explain the order in which the program builds, runs and stops, and why
the order matters.

Questions:

- What does `app.Open` build, and in what order?
- What background work runs?
- What happens when I press Ctrl-C, or the client closes stdin?
- How long can a stop take, and why?
- What happens at the next start after a crash?
- Why does a short command not wait for the queue?

Must cover:

- `Open`'s six steps (doc comment, `app.go:250`). The backend factory comes
  before the outbox because the sender uses it. The web server comes before the
  service because the service needs its links; `Attach` closes the loop.
- `Run`: recover sends that were interrupted, expire stale approvals, then three
  loops: the outbox worker, the daily jobs and the sweep of idle backends.
- `BeginStop` and `Close` (steps 1 to 6 in the doc comment). Why steps 2 and 3
  do not stop at the end of the context: a send that has started must write its
  result, or it would end as "delivery unknown". Why the web server stays up
  during the drain: the person can still reject an action that waits.
- `StopTimeout`: how it is made from its parts (see the fact sheet).
- A second signal ends the program at once (`cmd/email-mcp/main.go`). A send cut
  off then is never sent again; the next start marks it "delivery unknown".
- `CloseNow` for short commands: no drain, because the queue belongs to the
  program that serves.
- Stdio versus HTTP: how `serve` stops each one (`serveStdio`, `serveHTTP`, the
  stop budget).
- The lock for the first key and the shared database (link to `overview`).

Source: `internal/app/app.go`, `kek.go`, `lock_unix.go`, `lock_windows.go`,
`sender.go`; `internal/inflight/inflight.go`; `cmd/email-mcp/serve.go`,
`main.go`, `setup.go`; `internal/outbox/outbox.go` (`Drain`,
`RecoverInterrupted`).

Design spec (history): section 7.7.

Diagram: D9.

### `mail-backend`: The mail backend

Path: `site/content/tech/mail/mail-backend.md`

Purpose: explain the provider-neutral interface and the IMAP and SMTP backend,
and where a new provider would go.

Questions:

- What must a provider implement?
- How does the backend keep connections, and how does it stay safe?
- How are messages, folders and tags addressed?
- How does a send reach the SMTP server?
- How does an account get connected and tested?
- What would I change to add a provider?

Must cover:

- `mail.Backend`: neutral types, opaque `Ref` values, batch calls that check
  every ref before they change anything, `ErrPartial`, and the rule that no
  method deletes mail for good.
- `BackendFactory` and `BackendCache`: one backend for each account in its own
  slot; a fingerprint of the server settings and the sealed password, compared
  on each call, so a change made by another process is seen; a replaced backend
  closes after a grace time; idle backends are swept.
- `imapsmtp`: the pool (`pool.go`: two slots, forced close of the network
  connection, retry once for calls that are safe to repeat, never for `APPEND`);
  the secure dialer (`tlsdial.go`: TLS 1.2 or newer, `STARTTLS` without
  fallback, certificate pin by SHA-256 of the leaf, the rule for when a pin may
  be offered); refs (`ref.go`: mailbox, UIDVALIDITY and UID); folder aliases
  from `SPECIAL-USE`; tags as IMAP keywords when the inbox allows them; `Move`
  with `MOVE`, or `COPY` and a `UID EXPUNGE` of the same UIDs, never a plain
  `EXPUNGE`; cursors; drafts with `APPEND`.
- Send (`smtp.go`): a new SMTP connection for each message, `AUTH PLAIN` only,
  all-or-nothing for recipients, a Sent copy that cannot fail the send, and
  `ErrDeliveryUnknown` when nobody can tell if the server took the message.
- Connecting an account: `discovery` (four sources, only the domain is sent),
  `Service.CreateIMAPAccount` (settings checked, the probe must pass for IMAP
  and SMTP, the account and sealed password saved in one transaction), the
  shared `connect` package (fixed texts, certificate decision) used by the web
  portal and the commands, `needs_reauth` and `ReconnectIMAPAccount`.
- Adding a provider: start from the packages that import `imapsmtp` today
  (`go list` shows them): `service`, `mcpserver`, `connect`, `web`, `app`,
  `cmd/email-mcp`. State this as a starting list, not as a recipe that was
  tested.

Source: `internal/domain/mail/*.go`, `internal/provider/imapsmtp/*.go`,
`internal/service/backends.go`, `accounts.go`, `reconnect.go`,
`internal/discovery/discovery.go`, `internal/connect/connect.go`,
`internal/domain/account/account.go`.

Design spec (history): sections 4.5, 8.4, 8.5 and 13.1.

Diagram: D10.

Manual (link with `relref`): `getting-started/connect-account`,
`daily-use/manage-accounts` and `daily-use/troubleshooting`.

### `mail-content`: Mail content

Path: `site/content/tech/mail/mail-content.md`

Purpose: explain how text from third parties is made safe for an agent, how
outgoing mail is built so that the preview equals the message, and why the
repository holds a copy of an HTML parser.

Questions:

- What is removed from incoming text, and why?
- How is HTML turned into text?
- What limits stop a hostile message from using all the memory?
- How is an outgoing message built, and what does it refuse?
- Why is there a copy of `golang.org/x/net/html`?
- How do I change the hidden-text rules safely?

Must cover:

- `CleanUntrusted`: what categories it removes and why it keeps the zero-width
  joiners (scripts such as Persian need them).
- `HTMLToText`: the groups of things it leaves out (hidden by attribute, by
  style, by inherited size or visibility, comments). Link to the long comment in
  `html.go` for the full list. The principle: a hiding rule on an element wins,
  and when a style cannot be worked out, the answer fails closed.
- Limits: HTML part size, the work budget of the parser, the output cap, the
  nesting depth. Name the constants in the fact sheet.
- The copied parser: why a size limit is not enough (see the README of
  `internal/thirdparty/xhtml`), what the patch does (a work budget, marked with
  `// email-mcp:`), how to update it.
- Building mail (`Build`, `FromDraft`): reply and reply-all rules, forward,
  header injection refused, quoted local parts and domain literals refused,
  hidden characters refused in subject, text and display names, HTML reduced to
  text, headings, lists, tables and `http`, `https` and `mailto` links, Bcc kept
  out of the headers, the ASCII form (punycode) shown for a non-ASCII domain.
- The link to the send flow: the text part of a message made from HTML is what
  the preview shows.

Source: `internal/mime/*.go`, `internal/thirdparty/xhtml/README.md`,
`internal/provider/imapsmtp/fetch.go`, `internal/mcpserver/shape.go`.

Design spec (history): sections 6.6 and 10.2.

Diagram: D11.

Manual (link with `relref`): `daily-use/what-the-agent-can-do`.

### `command-line`: The command line

Path: `site/content/tech/ways-in/command-line.md`

Purpose: explain how the commands are built and what each one opens.

Questions:

- How are commands registered and tested?
- Which commands open the app, and how does each one close it?
- How do commands handle passwords, prompts and exit codes?
- What do the web portal and the commands share?
- How do I add a command?

Must cover:

- `commands` and `run` (`main.go`): one `flag.FlagSet` for each command; exit
  codes 0, 1 and 2; a bad flag gets a fixed text, never the text that was typed
  (`parseOnce`).
- A table: command → file → what it opens (`serve`: `Open`, `Run`, `Close`;
  `setup`: the same plus the portal; `account`, `token`, `outbox`: `Open` and
  `CloseNow`; `doctor`: `ExistingKeyOnly`; `key rotate`: `RotateKEK`;
  `import-env`: a strict parser and the connect flow).
- `system` (`console.go`): the terminal, the browser and discovery are replaced
  in tests, so no test needs a terminal.
- Rules: standard output gets what the user asked for; standard error gets
  errors and the log; a password is never a flag; it comes from the terminal
  without echo or from the first line of standard input.
- The shared connect flow (`connect.go` and `internal/connect`): the same rules
  as the portal for certificates and plain connections.
- How to add a command (a list).

Source: `cmd/email-mcp/*.go`, `internal/connect/connect.go`,
`internal/app/app.go`.

Design spec (history): section 4.1.

Diagram: none. One table.

Manual (link with `relref`): `reference/commands`.

### `local-http`: Local HTTP mode

Path: `site/content/tech/ways-in/local-http.md`

Purpose: explain how `serve --http` checks a request, and why in-band
confirmation is a choice that the user makes for each token.

Questions:

- Which checks run before a request reaches a tool, and in what order?
- How do tokens work?
- Why is the server stateless, and what does that change?
- Who gets the in-band question, and who gets the approval link?
- How are size, time and concurrency bounded?
- How does the server stop?

Must cover:

- The address must be a loopback address (`httpserve.CheckLoopbackAddr`).
- The order: limit on concurrent requests (`limitRequests`), `Host`, `Origin`,
  body size, bearer token, then the SDK handler in stateless mode. Why the
  order: a DNS rebinding page gets 403 before any token is looked at.
- Stateless mode (`Stateless: true`): the server keeps no session, so it cannot
  call back to the client. It never sends `elicitation/create` over HTTP. A
  client that speaks protocol `2026-07-28` or newer can still get the question,
  inside the result (MRTR, multi round-trip request: the tool answers "I need
  input" and the client calls again with the answer).
- Tokens (`internal/tokens`): prefix, random bytes, SHA-256 stored, explicit
  scopes, end date, the flag `--in-band-confirm`. How `httpserve.Principal`
  reads them. A wrong token is refused with one fixed answer for all causes.
- The table "who gets which channel": stdio, HTTP with a token made with
  `--in-band-confirm`, HTTP with any other token; each against the protocol
  version and the elicitation support of the client. Source: `canElicitInBand`
  and `canAsk` (`mcpserver/elicit.go:91` and `:118`).
- Limits and timeouts of `serve.go`, and the formula of `httpWriteTimeout`.
- Stop: `serveHTTP` and the stop budget.

Source: `internal/httpserve/httpserve.go`, `internal/tokens/tokens.go`,
`cmd/email-mcp/serve.go`, `token.go`, `internal/mcpserver/elicit.go`,
`internal/service/principal.go`.

Design spec (history): sections 6.1 and 7.5.

Diagram: D12. The channel table is a table, not a diagram.

Manual (link with `relref`): `reference/local-http-mode`.

### `web-portal`: The web portal

Path: `site/content/tech/ways-in/web-portal.md`

Purpose: explain how the portal starts, how a person signs in, how a request is
checked, and how pages are built.

Questions:

- When does the portal start, and where does it listen?
- How does a person sign in, and why does the portal have its own host name?
- Which checks run on a request, and in what order?
- How are pages, styles and the one script served?
- How does the portal reach the service?
- How do I add a page?

Must cover:

- Lazy start: `EnsureStarted` on the first link; `127.0.0.1` with a random port;
  one server for each process; it stops last in `Close`.
- Sign in: `email-mcp setup` makes a one-time token (stored hashed, 10 minutes,
  single use) and opens the link through a private file. `/setup` swaps the
  token for a cookie and redirects, so the token leaves the address bar. The
  session lives in the database (so it works in every process), for at most 24
  hours and 2 hours idle. Sign out and `--revoke-sessions`.
- The host name `email-mcp.localhost`: why a cookie for `127.0.0.1` would go to
  every local port, and so only this name gets a cookie.
- The checks in `guard.go`, in order: security headers, `Host`, cross-origin
  protection, the name, the session, the CSRF token (CSRF is cross-site request
  forgery: another site makes the browser send a request; the token is a signed
  value tied to the session). A GET only reads. The routes list and the tests
  that read it.
- Pages: `html/template` in `templates/`, embedded; one stylesheet and one
  script in `static/`; the Content-Security-Policy (a header that tells the
  browser which sources a page may load); why a page works without the script.
- How the portal reaches the service: `Attach`, the `Links` type that implements
  `service.Links`, and which calls the portal makes directly on the store.
- The approval page: the whole message is built again before it is shown, and an
  action that cannot be shown in full cannot be approved.
- How to add a route (the three route tests).
- Where the visual rules live: `DESIGN.md` and `PRODUCT.md`.

Source: `internal/web/*.go`, `internal/web/templates/`, `internal/web/static/`,
`cmd/email-mcp/setup.go`, `internal/connect/connect.go`, `DESIGN.md`,
`PRODUCT.md`.

Design spec (history): sections 8.1 to 8.3.

Diagrams: D13, D14.

Manual (link with `relref`): `daily-use/web-pages` and
`getting-started/connect-account`.

### `security-design`: Security design

Path: `site/content/tech/security/security-design.md`

Purpose: show the rules that hold the design together, where each one is
enforced, and which test checks it.

Questions:

- What protects the person from an agent that follows bad instructions?
- Where is each rule enforced?
- Which test fails if a rule breaks?
- What must I check when I change security code?

Must cover:

- The model in one paragraph: the agent is a caller with limited reach (tools
  only). The person has the only door to settings and approvals. Link to the
  manual page that states the threat model and its limits for users. Do not
  restate them here. (My call.)
- A table of rules with three columns: the rule, where it is enforced, and
  which test checks it. The page uses a table; this spec uses a list because the
  rows are long. Start from these rules:
  - The tenant comes from the principal, never from an argument. Enforced in
    `service.authorize` and in the `tenant_id` conditions of `store`. Checked by
    the tests of `service` and `store`.
  - Every tool runs through `safe`. Enforced by `mcpserver.addTool`. Checked by
    `TestEveryToolRunsThroughSafe`.
  - A level is checked before any backend call. Enforced in `service` and
    `policy`. Checked by `TestEveryWriteToolRefusedBelowItsLevel` in
    `internal/e2e/security_test.go`.
  - Tools cannot reach the use cases that only a person may call. Enforced by
    the imports and calls of `mcpserver`. Checked by `useronly_test.go`.
  - Links carry no secret. Enforced in `web.Links` and `web/guard.go`. Checked
    by `TestLocalUILinksRevealNothingWithoutSession`.
  - The portal checks Host, Origin, session and CSRF token. Enforced in
    `web/guard.go`. Checked by `TestLocalUIRefusesWrongHost`,
    `TestLocalUIRefusesCrossOriginPost` and
    `TestEveryPostRouteNeedsTheCSRFToken`.
  - A confirmation binds to the exact message. Enforced in `confirm`, `service`
    and `mcpserver/elicit.go`. Checked by `TestConfirmationCannotBeSkipped`.
  - Secrets are sealed and never logged. Enforced in `secrets` and by the
    `Secret` type. Checked by `TestNoPlaintextSecretsInDB` and
    `TestNoSecretsInLogs`.
  - TLS 1.2 or newer, certificate pins, no plain text to a remote host.
    Enforced in `imapsmtp/tlsdial.go` and `account.MailServer.Validate`. Checked
    by `TestTLSCertificateChecksEnforced` and `TestPlainTextToRemoteRefused`.
  - Mail only moves to Trash. Enforced by the `mail.Backend` contract and
    `service.Move`. Checked by `TestTrashMoveBlockedBelowLevel5`.
  - Third-party text is cleaned and marked. Enforced in `mime` and
    `mcpserver/shape.go`. Checked by the tests of `mime`.
  - Errors for the agent are fixed texts. Enforced in `mcpserver/errors.go`.
    Checked by the tests of `mcpserver`.
  - Values that the agent passes back are signed. Enforced in `signing` and
    `imapsmtp`. Checked by the tests of `signing`.

  The writer checks each test name before it goes into the page. The list above
  is the minimum. A name that no longer exists must be fixed, or the rule left
  out.
- Two channels into `service` (D15).
- A checklist for a change to security code: run `make test`; add a test to the
  matching file; update this table; check the map for other pages.

Source: the files in the table; `internal/e2e/security_test.go`;
`internal/e2e/harness_test.go`.

Design spec (history): section 10.

Diagram: D15.

Manual (link with `relref`): `daily-use/security` and `daily-use/deny-rules`.

Leave out: any weakness that is not fixed, and any attack step. Say what the
code does.

### `local-development`: Local development

Path: `site/content/tech/work-on-the-code/local-development.md`

Purpose: how to build, test and change the code, and how to keep the docs true.

Questions:

- How do I build, test and lint?
- Which Go version do I need?
- What does CI run?
- How do I add a tool, a command, a migration or a package?
- Which conventions does the code follow?

Must cover (this moves "Development" from the README, and adds to it):

- The make targets and what each one runs (`Makefile`): `build` (static binary,
  `-trimpath`), `test` (`go test -race ./...`), `lint`, `vuln`. The note about
  `GOROOT`. The pinned tool versions live in the Makefile and the workflows.
- Go version: `.go-version` and the `go` line of `go.mod`.
- CI (`.github/workflows/ci.yml`): the four jobs; actions pinned by commit; read
  only token. The interop workflow is on `testing`.
- Dependabot: weekly, grouped, cooldown, the `fix(deps)` prefix and why.
- Conventions (from `.github/copilot-instructions.md` and the code): wrapped
  errors, comments that explain why, table-driven tests with `testify/require`,
  UIDs and `BODY.PEEK`, no content in logs.
- Four recipes, each a short list with the files and the tests to change: add a
  tool (`addTool`, input struct, one use case, annotations, the audit argument
  rules in `capture.go`), add a command, add a migration, add a package.
- Commit and pull request titles: Conventional Commits, because release-please
  reads them (link to `release-process`).
- `make screenshots`, `make site` and `make site-serve` (from the foundation).
  `make screenshots` needs Chrome. Add a recipe: how to add or change a shot.
- A section "Build and try the container image": the content of `DOCKER.md`
  lines 56 to 79 ("Build it yourself" and "Try it"). The user manual spec asks
  the tech docs to take these lines, because only a developer with a copy of the
  source needs them. Add the sentence that the image is for tests and for
  building on. The manual page `reference/containers` explains why a container
  does not suit daily use.
- Not in this pull request: the docs check, the labels and the pull request
  template. They come with the cutover (step 6), and the text about them comes
  with that pull request.

Source: `Makefile`, `.go-version`, `go.mod`, `.golangci.yml`,
`.github/workflows/ci.yml`, `.github/dependabot.yml`,
`.github/copilot-instructions.md`, `Dockerfile`, `README.md` lines 1059 to
1074, `DOCKER.md` lines 56 to 79 (the manual spec hands these over).

Design spec (history): section 15.2.

Diagram: none.

### `testing`: Testing

Path: `site/content/tech/work-on-the-code/testing.md`

Purpose: show the kinds of test, how to run them, and how to write a test for a
new feature.

Questions:

- Which kinds of test exist, and what does each one prove?
- How do I test code that talks to IMAP, SMTP or the portal?
- What is in `internal/e2e`?
- What are the build tags, and what does the interop test need?
- What does CI run?

Must cover:

- Unit tests next to the code; table-driven; `testify/require`.
- Fake servers: `imaptest` (in-memory IMAP server built on go-imap),
  `smtptest`, `webtest` (reach the portal by its own name).
- The build tag `screenshots` in `internal/e2e`: the test that makes the images
  of the manual with headless Chrome. It runs in the site build, not in
  `go test ./...` (see the foundation, "Screenshots").
- `internal/e2e`: the whole program through `app.Open`, a go-sdk client on an
  in-memory connection, the local HTTP endpoint and the portal. It holds the
  security checklist tests. Name the tests in `security_test.go`.
- Tests that read the code itself: the routes tests in `web`, the
  user-only test and `TestEveryToolRunsThroughSafe` in `mcpserver`.
- Fuzz targets (`Fuzz*` in `mime` and `signing`): they run as normal tests with
  their seed inputs in `go test`. State only what the workflows run. To fuzz for
  longer, run `go test -fuzz`.
- The race detector: `-race` and the `race_on_test.go` and `race_off_test.go`
  files.
- Test clocks and injection points: `app.Config.Now`, `service.Deps.Probe`, the
  `system` struct of the commands.
- Interop (`test/interop`, tag `interop`): Dovecot and Postfix in Docker; how to
  run it (`gencerts`, `docker compose`, `-count=1` and why); when it runs
  (`interop.yml`: every night, every `v*` tag, by hand).
- What CI runs and what the release workflow runs.

Source: `Makefile`, `.github/workflows/ci.yml`, `interop.yml`, `release.yml`,
`internal/e2e/*.go`, `internal/testutil/*`, `test/interop/`.

Design spec (history): section 15.

Diagram: D16 (optional).

### `release-process`: Making a release

Path: `site/content/tech/work-on-the-code/release-process.md`

Purpose: how a release is made, step by step, and what to do when it fails. This
moves "Making a release" from the README.

Questions:

- What must be set up once?
- What do I do for each release?
- How does the version go up?
- What do I do when a release fails?
- How can I try the build without publishing?
- How is the site published?

Must cover:

- Keep the content of README lines 932 to 1057 (one-time setup, each release,
  versions below 1.0.0, failures, local trial, MCP Registry). Check each
  statement against `release.yml`, `release-please.yml`, `.goreleaser.yaml` and
  `release-please-config.json` before it moves. Reword for the simple-language
  rule only where needed.
- What the release makes: archives for five platforms, an SBOM for each, a
  checksums file with a cosign signature, a Homebrew cask, an image on GHCR,
  and build attestations. The manual holds the user's side (install, verify).
- The version string: `-ldflags -X .../internal/version.Version`.
- A section "The container image": the content of `DOCKER.md` lines 35 to 47
  (the base images, the pins by digest, the Dockerfile without a `# syntax=`
  line, the pinned builder and scanner images). The user manual spec asks the
  tech docs to take these lines. Add how the release builds the image: the same
  Dockerfile with `--build-arg BINARY_FROM=release`, so the image holds the
  binary that GoReleaser built (`Dockerfile`, `.goreleaser.yaml`).
- A section "How the site is published" from `.github/workflows/pages.yml` and
  the foundation: `release.yml` calls it as the last job, after the release job
  succeeds; `workflow_dispatch` is for the first launch and urgent fixes. Add
  two owner steps to the one-time list, because a release cannot deploy the site
  without them: set the Pages source to "GitHub Actions", and allow tags that
  match `v*` to deploy to the `github-pages` environment (foundation, "Steps
  only the owner can do"). That section of the foundation gives the time of each
  owner step. The docs check and its labels come with the cutover.
- Stable anchors: the headings "Once, before the first release", "For each
  release", "When the release fails" and "Try the build on your computer". Code
  comments will link to them (see "Keeping facts true").

Source: `.github/workflows/release.yml`, `release-please.yml`, `pages.yml`,
`.goreleaser.yaml`, `release-please-config.json`,
`.release-please-manifest.json`, `server.json`, `CHANGELOG.md`, `Dockerfile`,
`internal/version/version.go`, `README.md` lines 932 to 1057,
`DOCKER.md` lines 35 to 47 (the manual spec hands these over).

Design spec (history): section 16.

Diagram: D17.

Manual (link with `relref`): `reference/verify-a-release` and
`getting-started/install`.

### `glossary`: Glossary

Path: `site/content/tech/reference/glossary.md`

Purpose: one line for each term that the tech docs use.

Must cover: every term that a page defines in place, so a reader who skips to a
page still finds it. Start from the glossary at the end of this spec, and add
the terms of the pages. Sort in alphabetical order. A term links to the page
that explains it.

Source: the other pages.

## Diagrams

### Rules

- Format: inline SVG, made before commit. A shortcode `diagram` inserts the file
  (for example from `site/assets/diagrams/`). Inline SVG, not `<img>`, because
  only inline SVG can use the CSS custom properties of the site for light and
  dark. (My call.)
- Colours come from classes in the stylesheet, which use the tokens of
  `DESIGN.md` (ground, pane, ink, soft, line, go, warn, bad). No colour value
  inside an SVG. (My call.)
- Each SVG has `role="img"`, a `<title>` and a `<desc>`. Text in the drawing is
  real text, in the font of the page. No web fonts.
- Each SVG has a `viewBox` and no fixed size. Wide diagrams sit in a box that
  scrolls sideways on a narrow screen. The page itself never scrolls sideways.
  (My call.)
- A diagram never carries a fact that the text does not have. The text next to
  it (a list, a table) has the same facts.
- A tool may draw the SVG, but its output must be changed to use classes. Keep
  the source of the drawing next to the SVG if a tool made it. Which tool is the
  builder's choice. Nothing is installed to build the site.
- A diagram names packages and functions exactly as the code does.

### List

"Required" means the page is not complete without it.

- **D1 `layers-and-doors`** (`overview`, required). The agent on the left and
  the person on the right. The agent goes through stdio or HTTP to
  `mcpserver`. The person goes through the browser to `web`, and through the
  terminal to the commands. Both reach `service`. Under `service`:
  `policy`, `confirm`, `outbox`, `audit`, `store` with `secrets`, and the mail
  backend, which reaches the mail servers. A dotted line marks what only the
  person's door can call.
- **D2 `dependency-layers`** (`package-map`, required). Ten rows, layer 0 at the
  bottom. Each row lists its packages. Test-only packages are in a separate
  strip. The caption states the rule. The writer makes the data from `go list`.
- **D3 `tool-call`** (`tool-call`, required). A sequence from the client to the
  IMAP server and back, with these lanes: client, transport, SDK middleware,
  `mcpserver`, `service`, backend cache, `imapsmtp`, IMAP server, audit log.
  Mark the HTTP-only steps. Mark where the audit entry is written.
- **D4 `send-decision`** (`send-flow`, required). A flow from `Send` to the
  outbox: permission, build, limits, "needs a question?" with its three reasons,
  the two channels, the answer, the rebuild and compare, the claim, `deliver`.
  Show both undo windows.
- **D5 `send-two-calls`** (`send-flow`, required). A sequence for the in-band
  question: first call, input request with `RequestState`, the form shown to the
  person, the answer, second call, result.
- **D6 `send-states`** (`send-flow`, required). Two state diagrams. A pending
  action: `pending`, `approved`, `rejected`, `expired`, `done`. An outbox item:
  `queued`, `sending`, `sent`, `failed`, `cancelled`, with the transient retry
  and the permanent failure. `outbox.go` has a text sketch to start from.
- **D7 `key-hierarchy`** (`storage-and-secrets`, required). The master key
  (keychain or file) wraps the tenant data keys and the server key, which are
  stored in the database. The data key seals the fields. The server key gives
  the signing key and the recipient key.
- **D8 `tables`** (`storage-and-secrets`, optional). The tables and their
  foreign keys, drawn from the migrations.
- **D9 `open-and-close`** (`startup-and-shutdown`, required). Two vertical lists
  side by side: the six steps of `Open`, and the six steps of `Close`. Mark the
  steps that wait, and the budget.
- **D10 `backend-connections`** (`mail-backend`, required). `BackendCache`, one
  slot for each account, the fingerprint, `Backend`, the pool with two slots,
  the secure dialer, the IMAP server. A second path for send: its own SMTP
  connection.
- **D11 `mail-pipelines`** (`mail-content`, required). Two pipelines. Incoming:
  fetch, decode, parse with the work budget, hidden-text rules, clean, wrap as
  untrusted. Outgoing: compose, build, clean the HTML, refuse hidden characters,
  preview of the text part.
- **D12 `http-checks`** (`local-http`, required). The pipeline of a request:
  concurrency limit, `Host`, `Origin`, body size, token, stateless SDK handler,
  middleware, tool. Show the status code of each refusal.
- **D13 `portal-checks`** (`web-portal`, required). The pipeline of the portal:
  headers, `Host`, cross-origin, name, session, CSRF, route. Show the status
  code of each refusal.
- **D14 `portal-sign-in`** (`web-portal`, required). A sequence: `setup` makes
  the token, opens the link through a file, the browser requests `/setup`, the
  cookie is set, the redirect. Show that the token is stored only as a hash.
- **D15 `two-channels`** (`security-design`, required). The agent channel and
  the person channel into `service`, with what each can and cannot reach.
- **D16 `e2e-harness`** (`testing`, optional). `app.Open`, a client on an
  in-memory connection, the HTTP endpoint, a browser client, and the fake
  servers.
- **D17 `release-flow`** (`release-process`, required). From a merge to `main`,
  through the release pull request, the tag, the checks, the approval of the
  environment, the publish, and the site. Show the three failure cases and what
  to do in each.

Pages with no diagram, and why: `permissions` (two tables are clearer),
`audit-and-logging` (one table), `command-line` (one table), `local-development`
(lists), `glossary`, and the index.

## Dependency order

### How it was checked

The order comes from the imports of non-test code:

```text
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./cmd/... ./internal/...
```

A layer number is the longest path to a package with no internal imports. Go
forbids import cycles, so the numbers are well defined.

### Layers as found

Each line lists the packages of a layer and the internal packages they import.

- **Layer 0** (no internal imports): `domain/account`, `domain/mail`, `errtype`,
  `inflight`, `signing`, `version`, `thirdparty/xhtml`.
- **Layer 1**: `store` (account); `discovery` (account); `mime` (mail, xhtml).
- **Layer 2**: `policy` (account, store); `secrets` (account, store).
- **Layer 3**: `audit` (account, secrets, store); `confirm` (account, errtype,
  secrets, signing, store); `outbox` (account, mail, errtype, secrets, store);
  `provider/imapsmtp` (account, mail, mime, secrets, signing).
- **Layer 4**: `service` (account, mail, audit, confirm, errtype, inflight,
  mime, outbox, policy, imapsmtp, secrets, signing, store).
- **Layer 5**: `connect` (account, imapsmtp, service); `mcpserver` (account,
  mail, audit, confirm, errtype, mime, outbox, policy, imapsmtp, service,
  signing); `tokens` (account, policy, service, store).
- **Layer 6**: `httpserve` (account, errtype, policy, service, tokens).
- **Layer 7**: `web` (almost everything below: audit, confirm, connect,
  discovery, account, mail, errtype, httpserve, mime, outbox, policy, imapsmtp,
  secrets, service, signing, store, tokens).
- **Layer 8**: `app` (audit, confirm, discovery, account, mail, errtype,
  inflight, mcpserver, outbox, policy, imapsmtp, secrets, service, signing,
  store, tokens, version, web).
- **Layer 9**: `cmd/email-mcp` (app, connect, discovery, account, httpserve,
  mcpserver, outbox, policy, imapsmtp, secrets, service, store, tokens,
  version).
- **Test support** (no internal imports in non-test code): `testutil/imaptest`,
  `testutil/smtptest`, `testutil/webtest`, `e2e`. Their tests import many
  packages.

The writer repeats the command when writing, because the code changes.

### What differs from the design documents

The design spec does not give an order of packages. Section 4.3 shows the flow
`mcpserver` → `service` → `policy`, `store`, `secrets`, the backend, `outbox`
and `audit`. The code agrees for the tool path. These facts are not in the
design:

1. `service` imports `provider/imapsmtp` directly. The backend cache builds an
   `*imapsmtp.Backend`; the probe, the certificate error types and
   `ErrAuthFailed` come from there too (`service/backends.go:17`,
   `service.go:33`, `accounts.go:21`, `reconnect.go:22`). Use cases talk to
   `mail.Backend`, but the choice of the concrete backend is in `service`.
2. `mcpserver` imports `imapsmtp`, `outbox`, `audit`, `confirm`, `policy`,
   `signing` and `mime`. It uses their types, error values and pure functions.
   It does not call the store or a backend. It must not import `web` or `tokens`
   (`useronly_test.go`).
3. `web` reads the store itself for sessions, setup tokens, account lists, the
   token list and the audit list. Every change of an account, a setting or an
   approval goes through `service`.
4. `web` imports `httpserve` for the shared host check, the body limit and the
   error-log wrapper.
5. `tokens` imports `service`. It implements `service.TokenMaker` and returns
   `service.Principal`. `httpserve` imports `tokens`.
6. `secrets` and `policy` import `store`. The vault uses store methods for keys,
   and the limiter uses counters.
7. `connect` imports `service` and `imapsmtp`. Both `web` and `cmd/email-mcp`
   use it.
8. Only `app` imports both `mcpserver` and `web`. `cmd/email-mcp` reaches the
   portal through `app`.

Design sections 4.2 and 4.5 also list packages and interfaces that do not exist.
See "Differences to resolve", items 8 and 9.

## Code area to page map

A pull request author uses this table. The first page is the main page for the
area. The second column lists other pages that may need a change. The docs check
of the foundation ignores `internal/e2e`, `internal/testutil` and
`internal/thirdparty`, but they are in the map so that a reader can find their
page.

| Code area | Main page | Also check |
|---|---|---|
| `cmd/email-mcp` | command-line | startup-and-shutdown, local-http |
| `internal/app` | startup-and-shutdown | storage-and-secrets, send-flow |
| `internal/audit` | audit-and-logging | storage-and-secrets |
| `internal/confirm` | send-flow | security-design |
| `internal/connect` | mail-backend | command-line, web-portal |
| `internal/discovery` | mail-backend | |
| `internal/domain/account` | permissions | storage-and-secrets |
| `internal/domain/mail` | mail-backend | tool-call |
| `internal/e2e` | testing | security-design |
| `internal/errtype` | audit-and-logging | |
| `internal/httpserve` | local-http | tool-call |
| `internal/inflight` | startup-and-shutdown | |
| `internal/mcpserver` | tool-call | send-flow, audit-and-logging |
| `internal/mime` | mail-content | mail-backend |
| `internal/outbox` | send-flow | startup-and-shutdown |
| `internal/policy` | permissions | |
| `internal/provider/imapsmtp` | mail-backend | tool-call |
| `internal/secrets` | storage-and-secrets | security-design |
| `internal/service` | tool-call | send-flow, permissions |
| `internal/signing` | security-design | send-flow, web-portal |
| `internal/store` | storage-and-secrets | |
| `internal/testutil/imaptest` | testing | |
| `internal/testutil/smtptest` | testing | |
| `internal/testutil/webtest` | testing | web-portal |
| `internal/thirdparty/xhtml` | mail-content | package-map |
| `internal/tokens` | local-http | permissions |
| `internal/version` | release-process | |
| `internal/web` | web-portal | permissions, send-flow |

Areas that are not Go packages (the docs check does not see them, because it
counts Go files only):

| Code area | Main page | Also check |
|---|---|---|
| `internal/store/migrations/` | storage-and-secrets | local-development |
| `internal/web/templates/`, `internal/web/static/` | web-portal | |
| `internal/web/templates/approval.html` | web-portal | send-flow |
| `Makefile`, `.golangci.yml`, `.go-version`, `go.mod` | local-development | |
| `.github/workflows/ci.yml` | local-development | testing |
| `.github/workflows/interop.yml`, `test/interop/` | testing | |
| `.github/workflows/release*.yml` | release-process | |
| `.github/workflows/pages.yml` | release-process | |
| `.goreleaser.yaml`, `release-please-config.json` | release-process | |
| `Dockerfile` | release-process | local-development |
| `server.json` | release-process | |
| `DESIGN.md`, `PRODUCT.md` | web-portal | |

A change to `internal/web/templates/approval.html` also needs the picture in
`site/layouts/_partials/approval-picture.html` and the label test in
`internal/web`. Both come from the landing page spec ("Keeping the page true"),
which asks this map to say so.

Rules for the map:

- A new package gets a row in the same pull request, and a line in
  `package-map`.
- `service` has several pages because it is large (5,313 lines). The author
  picks the page by the file:
  - `send.go`, `write.go`, `queue.go` and `approvals.go`: `send-flow`;
  - `service.go`, `read.go` and `principal.go`: `tool-call`;
  - `calls.go`: `audit-and-logging`;
  - `tokens.go`: `local-http`;
  - `accounts.go`: `permissions` for the settings and the risk acceptance,
    `mail-backend` for connecting an account;
  - `reconnect.go` and `backends.go`: `mail-backend`.
- The map is part of `local-development` after the cutover, as a short section
  "Which page do I update?". The map of the manual spec joins it there, and the
  pull request template links to that section. (My call.)

## Keeping facts true for each version

The site is published at each release, from the tag (foundation). A page is
therefore true for the release that published it. Between releases, `main` can
be ahead of the site. These rules keep each page true to its release.

1. **Links to code point at the built version.** The shortcode `src` makes the
   link `https://github.com/EmadMokhtar/email-mcp-go/blob/<ref>/<path>`, and for
   a folder `tree/<ref>/<path>`. The repository address is a site parameter, not
   text in a page. (My call.)
   - `<ref>` is the tag when the build runs for a release (`v0.6.0`). At a
     release, the text and the code at the tag are the same commit, so every
     link is exact.
   - When the build runs by hand (`workflow_dispatch`) on a branch, `<ref>` is
     the commit SHA of the build. The links then match the text.
   - For `make site-serve`, `<ref>` is `main`.
   - The workflow gives the ref to Hugo. The builder chooses how: an
     environment variable from `pages.yml`, or the tag name made from
     `.release-please-manifest.json`, which the footer already reads. If the
     build cannot know the ref, it fails. It does not guess `main`. (My call.)
2. **No line numbers in pages.** They become wrong when code moves. A page names
   the file and the function or constant, and the reader searches. (This spec
   uses line numbers only because it is a note about one commit.)
3. **A number names its constant.** Write the number with the constant and file
   in the same sentence, for example "A request state is valid for 10 minutes
   (`confirm.InBandLifetime`)". The fact sheet below lists the numbers that the
   pages need. (My call.)
4. **No time words.** Do not write "currently", "now", "new", "recently", "since
   version", "soon", "will" or "planned". The one exception is the sentence in
   `overview` about unbuilt providers and hosted mode. Do not write the version
   of the program in the text. Write a version only when it is the fact, such as
   the MCP protocol version `2026-07-28`. This is a rule for review, not an
   automatic check. It also applies to text moved from the README.
5. **Describe only what the publishing release has.** The docs check, the two
   labels and the pull request template come with the cutover (step 6 of the
   foundation). Text about them comes in that pull request, not in this one.
   `make site` and `make site-serve` are in step 1, so this pull request may
   describe them.
6. **Source lists are checked.** Every path in `code_areas` and in a `src` link
   must exist in the checkout. A check fails the build if one does not. The
   builder decides how (a small script in `make site`, or a template that tests
   for the file). If it takes too much work, the review checklist below covers
   it. (My call.)
7. **Stable addresses and anchors.** A page file name is part of the public
   address. If a page must move, keep the old address with `aliases` in front
   matter. Code comments will point to these headings, so do not rename them:
   - `release-process`: "Once, before the first release", "For each release",
     "When the release fails", "Try the build on your computer";
   - `permissions`: "Levels" (the comment in `internal/web/settings.go:343`
     points to the levels).
8. **Review checklist** for any pull request that touches `site/content/tech/`:
   - Each number names its constant.
   - Each code link uses `src`; there are no line numbers.
   - `code_areas` is up to date.
   - Each diagram and its text agree.
   - New terms are in the glossary.
   - No time words, no weakness, no attack step.

### Fact sheet

These facts are read from the code at commit `e254516`. The writer must read the
constant again at writing time. Do not copy a number without its name.

- Default limits (`account.DefaultLimits`): 20 sends an hour, 200 a day, 50
  recipients in a message, 500 moves to Trash a day, an undo window of 30
  seconds when nobody confirms and 0 after a confirmation. A window is 0 to 300
  seconds (`maxUndoWindow`).
- A new account: level Send (4), both confirmations on
  (`account.NewIMAPAccount`).
- Lifetime of an action: 10 minutes in band (`confirm.InBandLifetime`), 24 hours
  on the approval page (`confirm.ApprovalLifetime`). A preview keeps 500
  characters of the body (`maxBodyStart`) and up to 20 message lines for Trash
  (`maxMessages`).
- Message size: a send payload is at most 15 MiB
  (`maxSendPayloadBytes`), a draft at most 25 MiB (`maxDraftBytes`), attachments
  10 MiB (`mime.MaxAttachmentBytes`).
- Outbox (`internal/outbox/outbox.go`): `SendDeadline` 13 minutes;
  `recoverAfter` 15 minutes; `retryDelays` 30 seconds, 2 minutes, 10 minutes;
  `minSendBudget` 30 seconds; at most four sends in a pass, one for each
  account. The worker ticks every second (`outboxTick`).
- Stop (`internal/app/app.go`): `drainWait` 5 minutes; `StopTimeout` is
  `drainWait + outbox.SendDeadline + 2 minutes`.
- Retention (`app.go`): ended pending actions 30 days (`pendingRetention`);
  audit entries 90 days (`auditRetention`); daily jobs every 24 hours and at the
  start.
- Backend cache: a replaced backend closes after 2 minutes (`retireAfter`); a
  backend unused for 30 minutes is closed by a sweep every 5 minutes
  (`backendIdle`, `backendSweepEvery`).
- Web (`internal/web/session.go`): setup token 10 minutes, session 24 hours,
  idle 2 hours. Cookie `emcp_session`. Host name `email-mcp.localhost`
  (`web/server.go:92`). It listens on `127.0.0.1:0`.
- Tokens (`internal/tokens/tokens.go`): prefix `emcp_`, 32 random bytes, default
  life 90 days, from 1 minute to 3,650 days.
- HTTP: body at most 15 MiB (`httpserve.MaxBodyBytes`); at most 8 requests at
  once and a wait of 30 seconds, then 503 (`cmd/email-mcp/serve.go:64`, `:67`).
- IMAP and SMTP (`provider/imapsmtp/config.go`): pool size 2, dial 15 seconds,
  command 60 seconds, idle 5 minutes. TLS 1.2 minimum (`tlsdial.go`). A batch
  call takes at most 500 refs. A page of a body is 20,000 characters by default
  and 100,000 at most. An HTML part over 4 MiB is not converted (`fetch.go`). A
  search page has at most 100 results (`mcpserver/read_tools.go`).
- Mail content (`internal/mime`): parser work budget 16,000,000 (`maxHTMLCost`;
  a node and an attribute cost 16 each); output cap 16 MiB; nesting depth 512.
- Discovery (`internal/discovery/discovery.go`): four sources, at most 1 MiB for
  a response, at most 3 redirects, 10 seconds in all.
- MCP: input requests in a result from protocol `2026-07-28`
  (`mrtrProtocolVersion`, `mcpserver/elicit.go`).
- Tool versions: pinned in `Makefile`, `ci.yml`, `release.yml` and
  `.goreleaser.yaml`. Name the file, not the number.

## Differences to resolve

The code wins in every case. Line numbers: design spec = the file
`2026-10-27-email-mcp-rewrite-design.md`; rulings = `2026-10-27-m0a-rulings.md`.

1. **Tool list.** Design line 319 lists `get_thread`. The code registers no such
   tool (`internal/mcpserver/read_tools.go:165`). Do not write about it.
2. **Message refs.** Design lines 231 to 234 say IMAP refs use `OBJECTID`
   (`EMAILID`) when the server has it. The code builds refs from mailbox,
   UIDVALIDITY and UID only (`internal/provider/imapsmtp/ref.go:22`). No Go file
   mentions `OBJECTID`.
3. **Web technology.** Design lines 147, 510 and 561 say htmx. The code uses
   plain forms and one script, `static/app.js` (`internal/web/guard.go:22`,
   `internal/web/static.go:3`). Rulings lines 151 and 222 record the change.
4. **Web address.** Rulings line 154 says every link uses
   `http://127.0.0.1`. The code uses `email-mcp.localhost`
   (`internal/web/server.go:92`, `internal/web/links.go`). The design agrees
   with the code. `127.0.0.1` serves only the help page (`SignInHelpURL`).
5. **Master key sources.** Design lines 641 to 646 list `keychain`, `file`,
   `passphrase` and `vault-transit`. The code has `keychain` and `file`
   (`internal/app/kek.go:21`, `internal/secrets/kek.go`). No Go file mentions
   Argon2 or a Vault provider.
6. **SMTP login.** Design line 822 says `AUTH PLAIN` or `LOGIN`. The code uses
   `AUTH PLAIN` only and fails if the server does not offer it
   (`internal/provider/imapsmtp/smtp.go:539`). IMAP uses its `LOGIN` command
   (`tlsdial.go:203`).
7. **Commands.** Design lines 107 to 118 do not list `outbox` or
   `account reconnect`, and list `serve --hosted`. The code has the first two
   and not the third (`cmd/email-mcp/main.go:30`, `account.go:30`,
   `outbox.go:21`).
8. **Backend interface.** Design lines 182 to 200 sketch `MailBackend`. The code
   is `mail.Backend` with `Capabilities(ctx)`, `GetRaw`, a reset flag in
   `Changes`, `Close`, and `Send` with a `*mail.RawMessage`; it has no
   `GetThread` (`internal/domain/mail/backend.go:44`). The calendar and contacts
   interfaces do not exist.
9. **Package list.** Design lines 129 to 150 list eight packages that do not
   exist (`domain/calendar`, `domain/contacts`, `provider/dav`,
   `provider/google`, `provider/microsoft`, `freebusy`, `authz`, `netguard`) and
   lack thirteen that do (`app`, `confirm`, `connect`, `domain/account`,
   `httpserve`, `inflight`, `errtype`, `signing`, `tokens`, `version`,
   `thirdparty/xhtml`, `testutil`, `e2e`). Use `go list`.
10. **Tables.** Design lines 611 to 624 list `account_calendars`. The code has
    no such table. It has `server_keys`, `known_recipients` and
    `web_setup_tokens` that the design lacks, and columns added later: `mode` on
    `pending_actions` (migration 0009), idle fields on `web_sessions` (0012),
    `in_band_confirm`, `expires_at` and `last_used_at` on `api_tokens` (0013).
    Use `internal/store/migrations/`.
11. **Test layers.** Design lines 1064 to 1100 include layers for later
    milestones: provider fakes, an MCP conformance suite, DAV servers and
    cross-tenant tests. The `testing` page describes only what the repository
    and its workflows have.
12. **Front matter and line length.** `.github/markdown.instructions.md` is the
    file that the foundation names. It lists front matter keys for another
    system (lines 43 to 52: `post_title`, `author1`, `microsoft_alias`,
    `featured_image`, `categories` from `/categories.txt`, and others). It says
    400 characters at line 22 and 80 at line 36. Its `applyTo` is `docs/**/*.md`
    (line 3), so it does not match `site/content`. Hugo needs its own keys. The
    manual spec makes the same call (its item 15): `title`, `description` and
    `weight`. This spec adds the key `code_areas` and `aliases`, and uses 80
    characters. (My call.) The foundation pull request should state the keys, so
    all three parts agree.
13. **Comments that name the README.** The foundation lists the places. Two have
    a more exact place in the tech docs: the three comments about "Making a
    release" go to `release-process`, and `internal/web/settings.go:343` ("The
    levels are those of the README") goes to `permissions`, heading "Levels".
14. **Names for the web side.** `PRODUCT.md` and `DESIGN.md` say "portal". The
    code comments say "web interface". The manual says "web pages" (manual spec,
    "Made in this document"). The menu says "Waiting for you" for the page that
    the route and the code call "approvals" (`layout.html:52`). The tech docs
    use "web portal" and say the other names once, on `web-portal`.
15. **Lines of other documents that move here.** The manual spec hands these to
    the tech docs: README lines 932 to 1074 ("Making a release" and
    "Development") and `DOCKER.md` lines 35 to 47, 56 to 65 and 67 to 79. The
    entries `release-process` and `local-development` take them. One detail:
    "Development" ends at line 1074. Line 1075 is empty.

## Left to the builder

- `assets/` or `static/` for the SVG files, as the foundation allows.
- For the builder of step 1: the Hugo templates for the sidebar and the groups,
  the display and the check of `code_areas`, the shortcodes `src` and `diagram`,
  and how the build knows `<ref>` (an environment variable or the manifest).
  This spec gives what they must do, and step 1 of the foundation delivers them.
- The tool for drawing the SVG, and where the drawing sources live.
- The words of every page, inside the limits of the entries above. The entries
  say what to cover and where the facts are. They do not give the text.
- The package table and the layers: recompute them with `go list` when writing.
- The exact words of the home page table and of the seven group pages.
- Each link to a manual page, with `relref`. The paths come from the manual
  spec. If the manual pull request changes a path, the build fails, and the
  builder fixes the link.
- The glossary entries. Start from the list below.
- Whether `D8` and `D16` are worth drawing.
- The order of writing. A good order is `package-map`, `overview`, `tool-call`,
  `send-flow`, then the rest, and `glossary` last. (My call.)
- The `aliases` of a page, if a page is renamed during review.

### Done when

- All 26 files of the sidebar table exist (18 pages, 7 group pages and the
  home page), and the build passes with no warning.
- Each page has `code_areas`, and each path in it exists.
- Each row of the map has a page, and each page has at least one row.
- Each required diagram exists, has a title, a description and a text form.
- The layers in `package-map` match `go list` at the commit of the pull request.
- Every line range that the manual spec hands over is in the tech docs: README
  932 to 1074, and `DOCKER.md` 35 to 47, 56 to 65 and 67 to 79.
- No page has a line over 80 characters, an H1, or a time word.
- A reader can answer these questions in two clicks from the tech docs home:
  1. Where is the permission of a tool call checked?
  2. What happens between `send_message` and the mail leaving?
  3. How does the person's "yes" bind to the exact message?
  4. Where are secrets stored, and which key protects them?
  5. Why can an agent not open the settings page?
  6. Which packages do I change to add a tool, a command or a migration?
  7. How does `serve --http` check a request?
  8. What does the program do on Ctrl-C during a send?
  9. How do I make a release, and what do I do when it fails?
  10. Which page do I update when I change `internal/outbox`?

## Glossary

- **AAD (associated data):** extra text that must match when a sealed value is
  opened. It is not encrypted. Here it ties a value to its tenant, account and
  field.
- **Agent:** the AI program that calls the tools.
- **Alias (Hugo):** a front matter key that keeps an old address working after a
  page moves.
- **Anchor:** the part of an address after `#`. It points to a heading.
- **Approval page:** the page of the web portal where a person approves or
  rejects an action that waits.
- **Atomic claim:** one database statement that changes a row only if it still
  has the expected state, so only one caller wins.
- **Attestation:** a signed statement that says how and where a file was built.
- **Bearer token:** a secret sent in the `Authorization` header. Whoever holds
  it may use it.
- **BODY.PEEK:** the IMAP way to read a message without marking it as read.
- **Conventional Commits:** the rules for commit messages and pull request
  titles that tools such as release-please read.
- **cosign:** a tool that signs release files so a user can check their origin.
- **CSP (Content-Security-Policy):** a header that tells the browser which
  sources a page may load.
- **CSRF (cross-site request forgery):** another website makes your browser send
  a request to a site where you are signed in.
- **Cursor:** a signed value that marks a position, for example for
  `list_new_messages`.
- **Dependabot:** the GitHub service that opens pull requests to update
  dependencies.
- **DEK (data encryption key):** the key that seals the fields of one tenant.
- **DNS rebinding:** a web page uses a name that first points to a safe address
  and later to `127.0.0.1`, to reach a local server.
- **Dovecot, Postfix:** mail servers. The interop test runs against them in
  Docker.
- **Elicitation:** a request from the server to the client for input from the
  person.
- **Envelope encryption:** data keys seal the data. A master key wraps the data
  keys.
- **Fail closed:** when a check cannot decide, the answer is "no".
- **Front matter:** the block of settings at the top of a page file.
- **Fuzz test:** a test that feeds many generated inputs to a function to find
  crashes. Without `-fuzz`, Go runs only the seed inputs.
- **GoReleaser:** the tool that builds and publishes the release files.
- **Harness:** the program that runs the agent and starts MCP servers, such as
  Claude Code.
- **HMAC-SHA-256:** a hash made with a secret key. Only a holder of the key can
  make or check it.
- **Hugo:** the tool that builds the site from the files.
- **IMAP, SMTP:** the standard protocols for reading mail and for sending mail.
- **In band:** the question to the person appears in the chat of the client.
- **Interop test:** a test that runs the program against real mail servers.
- **KEK (key encryption key, the master key):** the key that wraps the data
  keys. It lives in the keychain or in a file.
- **MCP (Model Context Protocol):** the open standard that lets an AI app call
  tools of other programs.
- **Migration:** a numbered SQL file that changes the database structure. Each
  one runs once, in order.
- **MRTR (multi round-trip request):** a tool answers "I need input", and the
  client calls again with the answer.
- **Outbox:** the queue where a send waits for its undo window and for delivery.
- **Principal:** the caller of a request: tenant, token scopes, client name.
- **Race detector:** the Go tool (`-race`) that finds unsafe access to shared
  memory while tests run.
- **Ref:** an opaque string that names a message. The agent passes it back
  unchanged.
- **release-please:** the tool that reads commits, keeps a release pull request
  open, and makes the tag.
- **Request state:** a signed value that names a pending action and the hash of
  its payload.
- **SBOM (software bill of materials):** the list of every module inside a
  build.
- **SDK (software development kit):** here, the official Go library for MCP,
  `go-sdk`.
- **relref:** a Hugo function that makes a link to another page, and fails the
  build if the page does not exist.
- **Shortcode:** a Hugo tag that inserts a snippet into a page.
- **SPECIAL-USE:** the IMAP feature that marks folders such as Sent and Trash,
  so aliases such as `@trash` can find them.
- **Stateless:** the server keeps nothing between two requests.
- **STARTTLS:** a way to upgrade a plain connection to TLS.
- **stdio:** the client starts the server and talks to it over standard input
  and output.
- **SVG (scalable vector graphics):** a picture format made of shapes and text.
- **Tenant:** the owner of a set of accounts. A local install has one.
- **UID, UIDVALIDITY:** the number of a message in an IMAP folder, and the
  number that tells whether those numbers were reset.
- **Web portal:** the local web site where the person connects accounts, sets
  levels and answers actions that wait. The manual calls it the web pages.
- **Undo window:** the time a queued send waits, so it can be cancelled.
- **Vendored:** a copy of third-party code kept inside the repository.
- **WAL (write-ahead log):** a SQLite mode where changes go to a log file first,
  so readers and one writer work together.
