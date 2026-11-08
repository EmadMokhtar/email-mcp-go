---
title: Glossary
description: One short definition for each term of the tech docs, with a link to the page that explains it.
weight: 10
---

This page gives one short definition for each term that the tech docs use, with
a link to the page that explains it. It is for a reader who meets a term on a
page and wants its meaning. Each page of the tech docs also defines its terms
where it uses them, so that a page can be read alone.

The list holds ideas, short names, units and tools, in alphabetical order. A
name from the code, such as a function, a type or a constant, is in the list
only when the pages use it as a term: the pages and the comments in the code
explain the others. Each short name is written out in its own entry. A term with
two or more meanings gives each one.

## A

- **AAD (associated data):** extra text that sealing binds to a value. It is not
  stored and not encrypted, and opening needs the same text. Here it names the
  tenant, the account and the field, so a sealed value does not open under
  another tenant, account or field. See [Storage and secrets][ssk].
- **Access token:** the secret that a program sends with each request on local
  HTTP mode. It starts with `emcp_` and carries scopes. The database keeps only
  its hash. See [Local HTTP mode][lht].
- **Action entry:** an entry of the activity log for what a use case did, or for
  what the person did on a page. See [Activity log and program log][all].
- **Activity log:** the table of the database that records the tool calls of the
  agent and the actions of the use cases. The person reads it on the Activity
  page of the web portal. The code calls it the audit log. See [Activity log and
  program log][all].
- **AES-256-GCM (Advanced Encryption Standard with a 256-bit key, in
  Galois/Counter Mode):** the cipher that seals values. It also detects a change
  to the sealed bytes. See [Storage and secrets][ssk].
- **Agent:** the AI program that calls the tools of email-mcp. See [Architecture
  overview][ov].
- **AI (artificial intelligence) app:** the program that runs the agent and
  talks to email-mcp, such as Claude Code. It is the MCP client. See [Security
  design][sdm].
- **Allow-list (of origins):** the list of browser origins that local HTTP mode
  accepts. It is empty by default, and the flag `--allow-origin` adds one. See
  [Local HTTP mode][lhl].
- **Anchor:** the part of a web address after `#`. It points to a heading of a
  page. The site check fails on a link to an anchor that does not exist. See
  [Local development][lds].
- **`App`:** what `app.Open` builds: all the parts of the program, connected.
  Each command that needs them opens the `App` and closes it. See [The command
  line][clo].
- **APPEND:** the IMAP command that stores a message in a folder. See [The mail
  backend][mbc].
- **Approval link:** a link to the approval page for one action that waits. A
  tool gives it to the agent, so that the person can open it. See [Local HTTP
  mode][lhs].
- **Approval page:** the page of the web portal where the person approves or
  rejects an action that waits. The menu calls it "Waiting for you", and the
  route and the code call it `approvals`. See [The web portal][wpa].
- **Area:** a part of an account that has its own level. Mail is the only area
  with actions. See [Permissions][pel].
- **ASCII (American Standard Code for Information Interchange):** the basic set
  of characters: English letters, digits and common signs. See [Building
  mail][bmf].
- **Atomic claim:** one database statement that changes a row only when the row
  still has the expected state (a compare-and-set), so only one caller succeeds.
  The outbox claims a send this way, and `confirm` claims a "yes". See [The send
  flow][sfo].
- **Attestation (build provenance):** a signed record that says which workflow
  built which file from which commit. See [Making a release][rpm].
- **Authority (certificate authority):** a body that signs certificates, so that
  a client can trust them. See [The mail backend][mbd].
- **AUTH PLAIN:** the SMTP command that sends the user name and the password in
  one step. It is the only SMTP login that email-mcp uses. See [The mail
  backend][mbs].

## B

- **Backend (mail backend):** the part of the program that talks to the mail
  servers of one account. It implements the interface `mail.Backend`. See [The
  mail backend][mb].
- **Backend cache:** the cache of one mail backend for each account, in each
  process. It builds another backend when the server settings, the key version
  or the sealed password of the account change. See [The mail backend][mbo].
- **Backend factory:** the code that builds the backend of an account
  (`BackendFactory`). The backend cache is the default factory. See [The mail
  backend][mbo].
- **Bare CR:** a carriage return with no line feed after it. See [Building
  mail][bmd].
- **Base32:** a text form of bytes. Access tokens use it in lower case, with the
  letters `a` to `z` and the digits `2` to `7`, without padding. See [Local HTTP
  mode][lht].
- **Base64:** a text form of bytes made of letters, digits and a few signs. A
  key file holds the master key as base64 text, and a mail can use base64 as its
  transfer encoding. See [Storage and secrets][ssm].
- **Base64url:** a text form of bytes that is safe in a web address. Refs and
  signed tokens use it. See [Security design][sds].
- **Base URL:** the start of every address of the web portal. See [The web
  portal][wps].
- **Bcc (blind copy):** a recipient that the other recipients do not see. The
  message that goes out has no `Bcc` header. A saved draft keeps it. See
  [Building mail][bmr].
- **Bearer token:** a secret in the header `Authorization: Bearer <token>`. See
  [Local HTTP mode][lhc].
- **bluemonday:** the library that cleans HTML (`mime.SanitizeHTML`). See [Map
  of packages][pmb].
- **BODY.PEEK:** the IMAP way to read a message without marking it as read. See
  [One tool call, step by step][tcs].
- **BuildKit:** the build engine of Docker. See [Making a release][rpi].
- **Build tag:** a word that a Go file asks for before it is built, in a line
  such as `//go:build interop`. See [Testing][tet].
- **Buildx:** the Docker tool that builds an image for several platforms. See
  [Making a release][rpw].

## C

- **Call entry:** an entry of the activity log for one `tools/call` request of
  the agent. See [Activity log and program log][all].
- **Capabilities:** what one side says it can do. A client says, for example,
  that it can show a form. The confirmation form appears in band only when the
  caller may also confirm in band. The capabilities of a backend say what it
  supports, such as tags, and the most refs in one batch call. See [Local HTTP
  mode][lhw] and [One tool call, step by step][tcp].
- **Certificate:** the signed proof of identity that a server shows when a
  connection starts. See [The mail backend][mbd].
- **Certificate offer:** the offer of the web portal to trust (pin) a
  certificate. It is a signed token with a short life (`certOfferTTL`). See [The
  web portal][wpr].
- **Changelog:** the list of changes of each release. See [Making a
  release][rpe].
- **Channel:** a word with three meanings on these pages.
  1. A Go value that passes values between goroutines. See [The mail
     backend][mbo].
  2. One of the two routes into the use cases: the channel of the agent (the
     tools) and the channel of the person (the web portal and the commands). See
     [Security design][sdc].
  3. The way a question reaches the person: in band, or the approval page. The
     send flow calls it the mode of the action: see Mode. See [Local HTTP
     mode][lhw].
- **Charset:** the table that maps bytes to characters. See [Mail content][mcp].
- **CHECK condition:** a rule that a table of the database keeps itself. For
  example, a pending action that waits has its payload and its preview, and one
  that has ended has neither. See [Storage and secrets][sst].
- **Checkpoint:** a step that copies the write-ahead log into the database file.
  See [Storage and secrets][sso].
- **CI (continuous integration):** the checks that GitHub runs on each pull
  request and on each push to `main`. A CI runner is a computer that runs them.
  See [Local development][ldc] and [Testing][tes].
- **`code_areas`:** the list of the code that a page covers, in the settings at
  the top of a page file. The page shows it as "Code covered by this page". See
  [Local development][lda].
- **Command:** a word after the name of the program, such as `serve` or
  `account add`. See [The command line][cl].
- **Compare-and-set:** a change of a row that happens only if the row still has
  the expected value. See Atomic claim.
- **Confirmation form:** the form where the person answers yes or no in the chat
  of the AI app. See [The send flow][sfi].
- **Confirmation switches:** the two settings of an account that ask the person
  before a send (`ConfirmSends`) and before a move to the Trash
  (`ConfirmDeletes`). See [Permissions][ped].
- **Connection pool:** the small set of open, logged-in IMAP connections of one
  account, which its calls share. See [The mail backend][mbc].
- **Constant time:** a comparison whose time does not depend on which bytes
  differ. See [The mail backend][mbd].
- **Container:** a way to run a program in its own closed space. See [Local
  development][k].
- **Content-Security-Policy:** a header that tells the browser which sources a
  page may load. With the policy of the web portal, a page loads only from its
  own origin, sends forms only to its own origin, and cannot be put in a frame.
  See [The web portal][wpp].
- **Context (Go):** the Go value that carries the deadline and the cancel of a
  request. See [The mail backend][mbo].
- **Conventional Commits:** a format for commit messages and pull request
  titles: `type(scope): description`. release-please reads it. See [Local
  development][ldt].
- **Cookie:** a value that the browser keeps and sends back with each request.
  The web portal keeps the session of a browser in one. See [The web
  portal][wpi].
- **Copied parser:** `internal/thirdparty/xhtml`, a copy of the HTML parser of
  `golang.org/x/net` with a limit on its work. It is the only third-party code
  that lives in the repository. See [Mail content][mcc].
- **CORS (cross-origin resource sharing):** the set of headers that lets a page
  read the answers of another origin. See [Local HTTP mode][lhc].
- **cosign:** a tool that signs files. The release signs its checksums with it.
  See [Making a release][rpm].
- **Cost (of the HTML parser):** the work of a parse. Each node, and each
  attribute with its bytes, costs work (`xhtml.NodeCost`, `xhtml.AttrCost`), and
  a parse stops when it goes over the budget (`maxHTMLCost`). See [Mail
  content][mcl].
- **Credentials version:** a number of an account that goes up by one at each
  reconnect, so a call from before the reconnect cannot undo it. See [Connecting
  an account][car].
- **CR, LF, NUL (carriage return, line feed, the zero byte):** control
  characters. A header value that the caller gives is refused when it has one,
  because a line break would let the value add a header. A value from the
  original message is cleaned, and an address from it is refused. See [Building
  mail][bmf].
- **Cross-origin check:** the check of the web portal that refuses a request
  that the browser marks as coming from another site, unless it is GET, HEAD or
  OPTIONS (`http.CrossOriginProtection`). See [The web portal][wpc].
- **CSRF (cross-site request forgery):** another site makes the browser send a
  request to a site where the person is signed in. See [The web portal][wpc].
- **CSRF token (form token):** the value that each form of the web portal sends
  with a change. It is the SHA-256 of the session id, signed by the server. See
  [The web portal][wpc].
- **CSS (Cascading Style Sheets):** the language of styles. A small CSS reader
  finds the parts of a mail that a mail client hides. See [Mail content][mch].
- **Ctrl-C:** the keys that stop a program in a terminal: hold the Ctrl key and
  press C. The first one starts a clean stop, and a second one ends the program
  at once. See [Startup and shutdown][g].
- **Cursor:** a signed value that marks a position in a folder: where the last
  check for arrived mail stopped. The caller passes it back unchanged. See [The
  mail backend][mbr].

## D

- **Daily jobs:** the housekeeping that runs when `Run` starts, and then every
  24 hours (`jobEvery`). It expires the stale approved actions (those that
  nothing finishes), deletes old rows, and runs a checkpoint of the write-ahead
  log. See [Storage and secrets][ssh] and [Startup and shutdown][sr].
- **Data folder:** the folder of the database and the other files of email-mcp.
  The flag `--data-dir` or the variable `EMAIL_MCP_DATA_DIR` chooses another
  one, and the flag has priority. See [Storage and secrets][ssw].
- **DEK (data encryption key):** the random key of one tenant, with a version,
  that seals its values. The database keeps it only wrapped by the master key.
  See [Storage and secrets][ssk].
- **Delivery unknown:** the text of a send that may or may not have reached the
  mail server. The outbox item is marked `failed` and is never sent again,
  because the server may have the message. See [Startup and shutdown][sr].
- **Dependabot:** the GitHub service that opens pull requests to update
  dependencies. See [Local development][ldd].
- **Digest:** a hash that names one exact container image. See [Making a
  release][rpi].
- **Discovery:** the part that suggests the mail server settings of an address.
  See [Connecting an account][caf].
- **DNS (Domain Name System):** the system of the internet that maps names to
  addresses. See [Connecting an account][caf].
- **DNS rebinding:** a web page uses a name of the attacker that points to
  `127.0.0.1`, to reach a server on this computer. The check of the Host header
  is for this case. See [Local HTTP mode][lhc].
- **Docker:** a tool that runs programs in containers. See [Local
  development][ldm].
- **Door:** a way into the program: the code that a caller goes through to reach
  the use cases. See [Architecture overview][ov].
- **Dovecot:** an IMAP server. The interop test runs it in Docker. See
  [Testing][tei].
- **Draft:** a message that is saved but not sent. The agent can send a saved
  draft by its `draft_ref`, and the draft goes out as it was saved. See [The
  send flow][sfs].
- **Drain:** the step of a stop that sends the queued messages that are due, or
  that become due within `drainWait`. The others stay queued for the next start.
  See [Startup and shutdown][suc].
- **DSN (data source name):** the connection string of the database. The
  settings of SQLite are in it, so every connection of the pool gets them. See
  [Storage and secrets][sso].

## E

- **Echo:** showing the characters that a person types. A password is read
  without echo. See [The command line][clp].
- **Elicitation (form elicitation):** a way for a server to ask the person a
  question through a form in the AI app. See [Local HTTP mode][lhw].
- **Embedded:** built into the program file, with `go:embed`. The pages, the
  styles and the script of the web portal are embedded. See [The web
  portal][wpp].
- **End-to-end test:** a test that drives the whole program from the outside.
  See [Testing][tee].
- **Envelope (of a mail):** the list of addresses that the mail goes to. See
  [The send flow][sfw].
- **Envelope encryption:** a key wraps (encrypts) other keys, and those keys
  seal the data. Here the master key wraps the data keys and the server key. See
  [Storage and secrets][ssk].
- **`.env` file:** a text file of `NAME=VALUE` lines, which the old email-mcp
  read for its settings. See [The command line][clo].
- **Environment (GitHub):** a set of rules and secrets of the repository that a
  job names. GitHub gives the secrets only to a job that names it, and only when
  the rules allow the run. See [Making a release][rpo].
- **Environment variable:** a named setting that a program reads from its
  surroundings, such as `EMAIL_MCP_KEK`. See [Local development][ldy].
- **Error chain:** an error and the errors that it wraps. See [Map of
  packages][pma].
- **Exit code:** the number that a program gives back when it ends: 0 (`exitOK`)
  when the command did its job, 1 (`exitError`) when it failed, and 2
  (`exitUsage`) for a wrong use. See [The command line][cle].
- **EXPUNGE:** the IMAP command that removes the messages that are marked
  `\Deleted`. The only one that email-mcp sends is `UID EXPUNGE` of the messages
  that the fallback of `Move` has just copied. See [The mail backend][mbx].

## F

- **Fail closed:** when a check cannot decide, the answer is "no". The security
  design lists the checks that work this way. See [Security design][sdf].
- **File mode:** the number that says who may read and write a file. The mode
  0700 of a folder and the mode 0600 of a file let only the owner use them. See
  [Storage and secrets][ssw].
- **Fingerprint:** a hash that names one thing, such as one certificate (see
  Hash). The pin of a certificate is its SHA-256 fingerprint. The backend cache
  keeps a fingerprint of the server settings, the key version and the sealed
  password of each account. See [The mail backend][mbd].
- **Flag:** an option after a command, such as `--data-dir`. See [The command
  line][cl].
- **Folder alias:** a name such as `@inbox`, `@sent` or `@trash` that names a
  well-known folder without knowing its name at the provider. See [The mail
  backend][mbp].
- **Foreign key:** a column that must match a row of another table. SQLite
  checks the foreign keys in every connection. See [Storage and secrets][sso].
- **Full autonomy:** both confirmation switches of an account are off, so they
  do not ask before a send or a move to the Trash. Other checks can still ask,
  such as the check of an address outside ASCII and the new-recipient check, and
  the level must allow the action. A save that turns it on needs the risk
  acceptance. See [Permissions][pa] and [The send flow][sfw].
- **Fuzz target:** a test that Go can run with many generated inputs. Each one
  has seed inputs (the inputs that the test gives by hand). See [Testing][tef].

## G

- **GHCR (GitHub Container Registry):** the registry where a release publishes
  the container image. See [Making a release][rpm].
- **GitHub Actions:** the automation service of GitHub. See [Making a
  release][rp].
- **GitHub App:** an account for a program, with the permissions that its owner
  gives it. See [Making a release][rpo].
- **`GITHUB_TOKEN`:** the token that GitHub gives a workflow run. Each workflow
  lets it only read the repository by default. See [Local development][ldc].
- **golangci-lint:** a program that runs many linters at once. See [Local
  development][ldm].
- **GoReleaser:** a tool that builds and publishes Go programs. It makes the
  release. See [Making a release][rpm].
- **Goroutine:** a function that Go runs at the same time as the rest of the
  program. See [Architecture overview][ovm].
- **gosec:** a linter for security mistakes. See [Local development][ldm].
- **govulncheck:** a check of the code and the dependencies for known
  vulnerabilities. See [Local development][ldm].

## H

- **Handshake:** the first exchange of a connection, which sets up the
  encryption. See [The mail backend][mbs].
- **Harness (of the end-to-end tests):** the test code that starts and drives
  the program. See [Testing][tee].
- **Hash:** a short value computed from bytes. It is one-way: the bytes are not
  meant to be read back from it. See [The send flow][sfy].
- **Headless Chrome:** Chrome with no window. The screenshots of the docs site
  and some tests use it. See [Local development][lds].
- **Hex (base 16):** writing bytes with 16 digits, `0` to `9` and `a` to `f`.
  The address of a static file of the web portal has the first 16 hex digits of
  its SHA-256. A pin is written in hex too. See [The web portal][wpp].
- **Hidden characters:** characters that a reader cannot see, such as the zero
  width space and the direction marks. `CleanUntrusted` removes the ones on its
  list from text of a third party. It keeps a joiner (an invisible character
  that joins two letters or two emoji) where the joiner changes the shape of a
  word or an emoji. A message that the agent writes is refused when its subject,
  its text body or a display name has one of the removed characters. A draft
  that the person saved goes out as it was saved. See [Mail content][mcr] and
  [Building mail][bmf].
- **Hidden text:** the parts of an HTML mail that a mail client would not show,
  because of their element, an attribute or a style. email-mcp leaves out the
  hidden parts that its rules find, and the result says how many. A style that
  it cannot work out sets a warning (`hidden_content_suspected`). It cannot
  always tell what a reader sees, so read the mail itself when it matters. See
  [Mail content][mch].
- **HMAC (hash-based message authentication code):** a hash made with a secret
  key. email-mcp uses HMAC-SHA-256 for keyed hashes and for signed values. See
  [Storage and secrets][sss].
- **Homebrew:** a package manager for macOS and Linux. A release that is not a
  pre-release updates its cask (install recipe) in the tap
  `EmadMokhtar/homebrew-tap` (a repository of recipes). See [Making a
  release][rpm].
- **Hosted mode:** one server that many people use. No command runs the program
  this way. Some end-to-end tests open a hosted install (one that is not local).
  See [Architecture overview][ovb].
- **Host header:** the line of a request that names the server that the request
  asks for. Each local server accepts only its own names. See [Local HTTP
  mode][lhc].
- **Hosts file:** the file of the computer that maps names to addresses. See
  [The web portal][wph].
- **HTML (HyperText Markup Language):** the language of web pages, and of the
  body of many mails. See [Map of packages][pma].
- **HTTP (HyperText Transfer Protocol):** the protocol of the web. The program
  has two HTTP servers: local HTTP mode and the web portal. See [Map of
  packages][pma].
- **HttpOnly:** a mark of a cookie: a script of a page cannot read the cookie.
  See [The web portal][wpi].
- **Hugo:** a program that makes web pages from Markdown files. It builds the
  docs site. See [Local development][lds].

## I

- **IMAP (Internet Message Access Protocol):** the protocol for reading mail.
  See [The mail backend][mb].
- **Import cycle:** two packages that import each other, directly or through
  others. See [Map of packages][pml].
- **In band:** the question to the person appears in the chat of the AI app, as
  a confirmation form. The other way is the approval page. These are the two
  modes of an action that waits: see Mode. See [The send flow][sfi].
- **In-band confirm:** the choice, made when a token is made, that lets the
  token answer a confirmation in its own window (`--in-band-confirm`). Make a
  token with it only for an app that shows you the form. See [Local HTTP
  mode][lht].
- **Interop test:** the test that runs the mail scenario against real mail
  servers, Dovecot and Postfix, in Docker. See [Testing][tei].
- **IPv4, IPv6 (Internet Protocol version 4 and version 6):** the two kinds of
  internet address. See [Local HTTP mode][lhl].
- **ISP (internet service provider):** a company that gives access to the
  internet. Discovery reads the ISP database of Thunderbird. See [Connecting an
  account][caf].

## J

- **JSON (JavaScript Object Notation):** a text format for data. See [The mail
  backend][mbr].
- **JSON schema:** a description of the shape that a JSON value must have. Each
  tool has one for its arguments. See [One tool call, step by step][tcs].

## K

- **KEK (key encryption key):** the master key. It wraps the data keys and the
  server key, so the database holds only wrapped keys. See [Storage and
  secrets][ssk].
- **Keychain:** the password store of the operating system. It is the default
  place of the master key. See [Storage and secrets][ssm].
- **Key file:** a file that holds the master key as base64 text. email-mcp
  refuses a key file that the group or other users can access, on systems that
  have file modes. See [Storage and secrets][ssm].
- **Key version:** the number of a data key. Each sealed value has its key
  version next to it. See [Storage and secrets][sss].
- **KiB (kibibyte):** 1,024 bytes. See [Mail content][mcl].
- **Known recipients:** the addresses that an account has sent to, kept as keyed
  hashes for the new-recipient check. See [The send flow][sfw].

## L

- **Label (of a host name):** one part of a name between dots, such as `example`
  in `imap.example.com`. See [Connecting an account][caf].
- **Layer:** the place of a package in the import order. A package imports only
  packages of lower layers. See [Map of packages][pml].
- **Leaf certificate:** the certificate of the server itself, the first one in
  the chain. See [The mail backend][mbd].
- **Level:** a number from 0 to 5 (`account.Level`) that says what the agent may
  do with the mail of an account: Off, Read, Organize, Draft, Send and Delete. A
  level includes every level under it. Level 5, Delete, moves mail to the Trash
  and never deletes mail for good. See [Permissions][pel].
- **Limiter:** the part that counts the sends and the moves to the Trash, and
  checks them against the limits of the account. See [Permissions][pli].
- **Limits:** the settings of an account in `account.Limits`: sends in an hour
  and in a day, recipients in a message, moves to the Trash in a day, the undo
  windows, and the new-recipient check. See [Permissions][pli].
- **Linter:** a tool that finds mistakes in code without running it. See [Local
  development][ldm].
- **LIST-STATUS:** an IMAP extension that sends the counts of messages with the
  list of folders. See [The mail backend][mbr].
- **`localhost` names:** a name under `localhost` is meant for this computer, so
  Chrome, Edge and Firefox reach it without DNS. The web portal uses
  `email-mcp.localhost`. See [The web portal][wph].
- **Local HTTP mode:** the HTTP server that `email-mcp serve --http` starts, for
  an AI app that cannot start a program. It listens only on a loopback address,
  and each request needs an access token. See [Local HTTP mode][lh].
- **Local tenant:** the one tenant of a local install, with the id `local`. See
  [Architecture overview][ovt].
- **Lock file:** a file that one program locks while it looks for the master key
  and makes it. So two programs that start at once on a first install do not
  make two keys. See [Storage and secrets][ssf].
- **LOGIN:** the IMAP command that sends the user name and the password. See
  [The mail backend][mbd].
- **Loopback address:** an address of this computer that no other computer can
  reach, such as `127.0.0.1`. See [Local HTTP mode][lhl].

## M

- **Make:** a tool that runs the build steps of a project. See [Local
  development][ldm].
- **Marker:** the field and the fixed text that mark text of a third party as
  untrusted in a tool result. It is text that the agent reads, and it does not
  force the agent. See [Mail content][mc].
- **Master key:** the key that protects the other keys. See KEK.
- **MCP (Model Context Protocol):** the open standard that lets an AI app call
  the tools of another program. See [Architecture overview][ov].
- **MCPB (MCP Bundle):** a kind of package that the MCP Registry supports. See
  [Making a release][rpr].
- **MCP client:** the name that MCP gives to the AI app: the program that talks
  to the server, such as Claude Code. See AI app.
- **MCP Registry:** the official registry of MCP servers. `server.json`
  describes email-mcp there, and lists no package. See [Making a release][rpr].
- **Message-ID:** the header that names one message. See [The mail
  backend][mbx].
- **MiB (mebibyte):** 1,048,576 bytes, or 1,024 KiB. See [Mail content][mcp].
- **Middleware:** a function that wraps the handling of every request, so it
  runs before and after it. See [One tool call, step by step][tcs].
- **Migration:** a numbered SQL file that changes the structure of the database.
  Each file runs in its own transaction. See [Storage and secrets][ssg].
- **MIME (Multipurpose Internet Mail Extensions):** the format that gives a mail
  its parts, such as the text, the HTML and the attachments. See [Mail
  content][mc].
- **Mode:** a word with three meanings on these pages.
  1. The mode of an action that waits: `in_band` (in the chat) or `approval` (on
     the approval page). It never changes. Local HTTP mode calls it the channel.
     See [The send flow][sft].
  2. The file mode. See File mode.
  3. The security mode of a mail server: `tls`, `starttls` or `plain`. See [The
     mail backend][mbd].
- **Module:** the Go module of this repository,
  `github.com/EmadMokhtar/email-mcp-go`. See [Map of packages][pm].
- **MRTR (multi round-trip request):** a tool answers "I need input", and the
  client calls again with the answer. See [Local HTTP mode][lhs].
- **multipart/alternative:** the part of a mail that holds the same message in
  more than one form, such as text and HTML. A client shows the last form that
  it can show. See [Mail content][mcb].
- **Mutex:** a lock. See [The mail backend][mbo].
- **MX (mail exchange) record:** the DNS record that names the mail server of a
  domain. See [Connecting an account][caf].

## N

- **`needs_reauth`:** the status of an account whose mail server refused the
  stored login. A passing account test (`ProbeAccount`) or a reconnect sets the
  status back to `ok`. See [Connecting an account][car].
- **New-recipient check:** a setting of an account that asks the person before a
  send to an address that the account never sent to. See [The send flow][sfw].

## O

- **OCI (Open Container Initiative):** the format of container images. See
  [Making a release][rpr].
- **ON DELETE CASCADE:** an SQL rule: when a row is deleted, the rows of other
  tables that point to it are deleted too. So everything of a removed account
  goes. See [Map of packages][pmw].
- **Opaque value:** a string that the backend makes, and that the caller passes
  back unchanged without reading it: a ref, a cursor or a page token. See [The
  mail backend][mbp].
- **Origin (browser origin):** the scheme, the host and the port of a web page,
  such as `http://localhost:5173`. See [Local HTTP mode][lhl].
- **Outbox:** the queue of outgoing mail. A send waits there for its undo
  window, and a worker delivers it at most once. See [The send flow][sfo].

## P

- **Package:** a folder of Go files that build together. See [Map of
  packages][pm].
- **Page token:** a signed value that names the next page of a search. See [The
  mail backend][mbr].
- **Payload:** the exact request of an action that waits, as JSON in which the
  same request always gives the same bytes. Its hash ties the "yes" of the
  person to the message. See [The send flow][sfy].
- **Pending action:** an action that waits for the person: a send or a move to
  the Trash. Its states are `pending`, `approved`, `rejected`, `expired` and
  `done`. See [The send flow][sfo].
- **PERMANENTFLAGS:** the IMAP answer that lists the flags that a client may
  keep. The backend reads it for the inbox, to know whether the account supports
  tags. See [The mail backend][mbr].
- **Person:** the human who owns the mail accounts. See [Security design][sdm].
- **Pin:** the SHA-256 fingerprint of the leaf certificate of a mail server, in
  lower-case hex, that the person chose to trust. A pin is saved only for the
  server (protocol, host and port) that showed the certificate. See [The mail
  backend][mbd] and [The command line][cls].
- **Pinned (version):** fixed to one exact version. The workflows pin the
  actions by commit SHA and most of their tools. They do not pin the Go version.
  See [Making a release][rpw].
- **Pipe:** a channel between two programs. On stdio, the client and the server
  talk through two pipes. See [Security design][sdc].
- **Plain connection:** a connection with no encryption. email-mcp allows it
  only to a server on this computer, and only when the person chose it. See [The
  mail backend][mbd].
- **Port:** the number that names one service on an address. See [The web
  portal][wps].
- **Postfix:** an SMTP server that takes mail from a mail program. The interop
  test runs it in Docker. See [Testing][tei].
- **Pre-release:** a version with a pre-release part, such as `v0.2.0-rc.1`. See
  [Making a release][rpw].
- **Preview:** what the person sees before they say yes. For a send, it shows
  the start of the text part of the built message, which is the start of the
  text that goes out. See [Building mail][bmp].
- **Principal:** the caller of a request: the tenant, the scopes of the access
  token, and the name of the client. See [Architecture overview][ovt].
- **Probe:** the test of the settings and the password of an account. It logs in
  to the IMAP server and to the SMTP server, and sends no message. See
  [Connecting an account][cas].
- **Program log:** the text that the program writes to standard error, for the
  person who runs it. It holds ids, counts and fixed codes, never a secret, an
  address, a subject or a body. See [Activity log and program log][alp].
- **Prompt injection:** words in a mail that try to give instructions to the
  agent, often hidden from the person who reads the mail. See [Mail
  content][mc].
- **Protocol version:** a date that names a version of the MCP rules, such as
  `2026-07-28`. See [Local HTTP mode][lhw].
- **Provider:** the kind of service behind a mail account. The code has one: a
  generic server that speaks IMAP and SMTP (`account.ProviderIMAP`). See [The
  mail backend][mb].
- **Public suffix list:** the list of domain endings, such as `co.uk`, under
  which anyone can register a name. See [Map of packages][pmb].
- **Punycode:** the ASCII form of a domain name that has characters outside
  ASCII. It starts with `xn--`. See [Building mail][bml].
- **Pure function:** a function that changes no state. See [Map of
  packages][pmi].

## R

- **Race detector:** the Go check (`-race`) that finds two goroutines that use
  the same memory without a lock. It makes the code many times slower. See
  [Testing][ter].
- **Redirect:** an answer that sends the browser on to another address, such as
  303 (See Other). See [The web portal][wpi].
- **Ref:** an opaque string that names one message, made from the folder name,
  the UIDVALIDITY of the folder and the UID of the message. A ref gives no
  right: every call checks the permission first. See [The mail backend][mbr].
- **Registrable domain:** the domain that a person can register, such as
  `example.co.uk`. See [Connecting an account][caf].
- **release-please:** the tool that reads the Conventional Commits on `main` and
  keeps one release pull request open. See [Making a release][rp].
- **Request state:** a signed token that names an action that waits for the
  person, with the hash of its request. See [The send flow][sfy].
- **Retired backend:** the backend of an account that another backend replaced,
  after a change of the server settings, the key version or the sealed password.
  It closes after `retireAfter`. See [The mail backend][mbo].
- **RFC (Request for Comments):** a numbered document that defines an internet
  standard. RFC 5322, for example, defines the format of a mail. See [Mail
  content][mcp] and [Building mail][bmo].
- **Risk acceptance:** the box that the person must tick before a save that
  turns `ConfirmSends` off, that lets the agent start to send without the send
  confirmation, or that ends with both confirmation switches off. Without it,
  the save is refused. See [Permissions][pa].
- **Root certificates:** the list of certificate authorities that the system
  trusts. See [The mail backend][mbd].
- **Route:** a method and a path of the web portal, such as `GET /accounts`,
  with its handler. A GET route only reads. The setup link, outside the routes,
  is the one exception. See [The web portal][wpg].
- **Router (mux):** the part that picks the handler of a path, such as
  `http.ServeMux` of the Go library. See [The web portal][wpc].
- **Ruleset:** a set of rules that GitHub applies to branches or tags. See
  [Making a release][rpo].

## S

- **SameSite=Lax:** a mark of a cookie: from another site, the browser sends the
  cookie only with a top-level GET, such as a click on a link. So a GET route of
  the web portal must only read. See [The web portal][wpi] and [A GET only
  reads][wpg].
- **SBOM (software bill of materials):** the list of every module inside a
  binary. See [Making a release][rpm].
- **Scope:** a permission that an access token carries, such as `email:read`.
  Scopes add up to the highest level that they name. See [Permissions][pes].
- **SDK (software development kit):** here, the official Go library for MCP,
  `go-sdk`. See [One tool call, step by step][tcs].
- **Seal:** to encrypt a value. Each sealed value has its key version next to
  it. See [Storage and secrets][sss].
- **Self-signed certificate:** a certificate that no authority signed. See
  [Testing][tes].
- **Sequence number:** the position of a message in an IMAP folder, which
  changes. The code never uses it to name a message. See [Local
  development][ldr].
- **Server key:** one random key of the program, wrapped by the master key. The
  code uses only keys derived from it, one for each use, and never the raw key.
  See [Storage and secrets][ssk].
- **Session:** a word with two meanings on these pages.
  1. The sign-in of one browser to the web portal, kept in the database. See
     [The web portal][wpi].
  2. State that lasts from one request to the next. Local HTTP mode keeps no
     session: see Stateless. See [Local HTTP mode][lhs].
- **Setup link:** the one-time sign-in link of the web portal. It holds a setup
  token, and the first browser that opens it gets the session. See [The web
  portal][wpi].
- **Setup token:** the one-time secret in a setup link. The database keeps only
  its hash. See [The web portal][wpi].
- **SHA (Secure Hash Algorithm):** a family of hashes. SHA-256 has a 256-bit
  result. A commit SHA is the hash that names one commit. See [Storage and
  secrets][sss] and [Local development][ldc].
- **Shortcode:** a Hugo tag that inserts a snippet into a page, such as `src` or
  `screenshot`. See [Local development][ldo].
- **Shot:** one screen of the program for the docs site, saved as a light and a
  dark image. See [Local development][ldo].
- **Signed token (signed value):** a value that the server made and can check.
  It holds a purpose, an expiry or none, and a value, in base64url, with an
  HMAC-SHA-256 signature. Cursors, page tokens, request states, CSRF tokens and
  certificate offers are signed tokens. See [Security design][sds].
- **Sigstore:** a public service that gives short-lived certificates for
  signing, so that cosign needs no long-lived signing key. See [Making a
  release][rpm].
- **SIGTERM:** the signal that asks a program to stop. As with Ctrl-C, the first
  signal starts a clean stop, and a second one ends the program at once. See
  [Startup and shutdown][g].
- **Site check:** `test/sitecheck`, which reads the built pages and the Markdown
  files of the docs site, and fails on a broken rule, such as a broken link. See
  [Local development][lds].
- **Smoke test:** a short test that only shows that the program starts. See
  [Local development][ldy].
- **SMTP (Simple Mail Transfer Protocol):** the protocol for sending mail. See
  [The mail backend][mbs].
- **SMTP transaction:** the exchange for one message: the sender (`MAIL FROM`),
  one `RCPT TO` for each recipient, and the data. See [The mail backend][mbs].
- **SMTPUTF8:** the SMTP extension that allows addresses with characters outside
  ASCII. See [The mail backend][mbs].
- **Snapshot (of GoReleaser):** a build with a test version, not from a tag. See
  [Making a release][rpt].
- **SPECIAL-USE:** the IMAP feature that marks folders such as Sent and Trash,
  so that folder aliases such as `@trash` can find them. See [The mail
  backend][mbr].
- **SQL (Structured Query Language):** the language of the database. See
  [Storage and secrets][ssg].
- **SQLite:** a database engine that keeps the whole database in one file. See
  [Storage and secrets][ssw].
- **Squash merge:** a merge that makes the title of the pull request the commit
  message on `main`. See [Local development][ldt].
- **SRV (service) record:** a DNS record that names the host and the port of a
  service. See [Connecting an account][caf].
- **Stamp file:** the file `site/assets/screenshots/.done`. Make runs the
  screenshot tests again when it is missing, or when the code is newer. See
  [Local development][lds].
- **Standard error, standard output:** the stream where a program writes
  messages about itself, and the stream for its answer. See [The command
  line][cl].
- **STARTTLS:** the command that upgrades a plain connection to TLS. With the
  security mode `starttls`, there is no fall back to plain text. See [The mail
  backend][mbd].
- **Stateless:** the server keeps nothing between two requests. Local HTTP mode
  is stateless. See [Local HTTP mode][lhs].
- **Static binary:** a binary that needs no system library to run. The build
  sets `CGO_ENABLED=0` (no C compiler) for it. See [Local development][ldm].
- **staticcheck:** a linter for Go. `make lint` runs it with the build tags of
  the tests. See [Local development][ldm].
- **stdin:** the standard input of a program. See [Tech docs](/tech/).
- **stdio (standard input and output):** the client starts the server and talks
  to it through two pipes. See [Architecture overview][ovp].
- **Stop budget:** one time limit for the whole stop of the program
  (`app.StopTimeout`). See [Startup and shutdown][suc] and [Local HTTP
  mode][lhp].
- **STORE:** the IMAP command that changes the flags of messages. With
  `.SILENT`, the server does not send the flags back. See [The mail
  backend][mbx].
- **Structured content:** the JSON object of a tool result. The text copy of the
  result is for clients that do not read it. See [One tool call, step by
  step][tcc].
- **SVG (Scalable Vector Graphics):** a picture made of shapes. The preview
  picture of the landing page is made from an SVG file. See [Local
  development][ldm].
- **Syft:** a tool that lists the modules of a program. A release makes its
  SBOMs with it. See [Making a release][rpm].

## T

- **Table-driven test:** one test runs the same check over a table of cases. See
  [Testing][teu].
- **Table hook:** the template of the docs site that turns a Markdown table into
  HTML. See [Local development][lds].
- **Tag:** a word with two meanings on these pages.
  1. In git, a name that marks one commit, such as `vX.Y.Z`. See [Making a
     release][rp].
  2. In mail, a label of a message, made from an IMAP keyword (a flag without a
     backslash). See [The mail backend][mbr].
- **Tenant:** the owner of a set of accounts. A local install has one: see Local
  tenant. See [Architecture overview][ovt].
- **Terminal:** a window where you type commands. See [The command line][clr].
- **Third party:** a person who wrote text that email-mcp passes on and who is
  not the user: the sender of a mail. See [Mail content][mc].
- **TLS (Transport Layer Security):** the protocol that encrypts a connection.
  email-mcp needs version 1.2 or newer (`tls.VersionTLS12`). See [The mail
  backend][mbd].
- **Tokenizer:** the first step of an HTML parser, which cuts the text into tags
  and words. See [Mail content][mcc].
- **Tool:** one action that the agent can call, such as `get_message`. See
  [Architecture overview][ov].
- **Tool handler:** the function that runs one tool. See [Architecture
  overview][ovp].
- **Transaction:** a group of database steps that all happen or none do. See
  [Storage and secrets][ssg].
- **Transfer encoding:** the way that a part of a mail writes its bytes as plain
  text: base64 or quoted-printable. See [Mail content][mcp].
- **Transport:** the way that the MCP messages travel: stdio or local HTTP. See
  [Map of packages][pma].

## U

- **U+FFFD:** the replacement character. Bytes that are not valid UTF-8 become
  U+FFFD, which a reader sees. See [Mail content][mcp].
- **UID (unique identifier):** a word with two meanings on these pages.
  1. In IMAP, the number that a folder gives to a message. It stays the same
     when other messages are removed. See [The mail backend][mbr].
  2. In an operating system, the number of a user. See [Making a release][rpi].
- **UIDPLUS:** an IMAP extension that makes the server report the UIDs of the
  messages that it copies (`COPYUID`) or stores (`APPENDUID`). See [The mail
  backend][mbx].
- **UIDVALIDITY:** a number of an IMAP folder that changes when the folder
  starts its UIDs again, so an old UID may then name another message. See [The
  mail backend][mbr].
- **Undo window:** the time that a send waits in the outbox, while it can still
  be cancelled. See [The send flow][sfu].
- **Unit test:** a test in the folder of the code that it tests, in most cases
  in the same package. See [Testing][teu].
- **Untrusted text:** text from a third party, such as the name of a sender, the
  subject, the body and the names of attachments. A tool result puts it in an
  `Untrusted` wrapper. See [One tool call, step by step][tcc].
- **URL (Uniform Resource Locator):** a web address. See [The web portal][wps].
- **Use case:** one job of the program for a caller, such as "send this mail".
  The package `service` holds the use cases. See [Architecture overview][ov].
- **User-only use case:** a use case that only the person may call, such as
  `ApprovePending`. No tool calls it. See [Architecture overview][ovd].
- **UTC (Coordinated Universal Time):** the world standard of time, with no time
  zone offset. The hour and day limits count their windows in UTC. See
  [Permissions][pli].
- **UTF-8 (Unicode Transformation Format, 8-bit):** a way to write each
  character as one to four bytes. See [Mail content][mcp].

## V

- **Vault:** the part that seals and opens values with the keys in the database
  (`secrets.Vault`). It does envelope encryption. See [Storage and
  secrets][ssk].
- **Volume (Docker):** a folder that Docker keeps outside the container. See
  [Local development][ldy].

## W

- **WAL (write-ahead log):** a mode of SQLite where readers and one writer work
  at the same time. See [Architecture overview][ovm].
- **Web portal:** the local web site where the person connects accounts, sets
  what the agent may do, answers the actions that wait, and reads the activity.
  The user manual calls it the web pages. See [The web portal][wp].
- **Worker (of the outbox):** the background function that sends the queued
  messages when they are due. See [The send flow][sfu].
- **Workflow:** a list of steps that GitHub Actions runs. See [Making a
  release][rp].
- **Workflow artifact:** files that a run keeps for download. See [Local
  development][ldc].
- **Wrap:** to encrypt a key with another key. The master key wraps the data
  keys and the server key. See [Storage and secrets][ssk].

## Related pages

- [Tech docs](/tech/): the home page, with the table of questions
- [Architecture overview](/tech/start-here/overview/)
- [Map of packages](/tech/start-here/package-map/)
- [Glossary](/manual/reference/glossary/) of the user manual

[ov]: /tech/start-here/overview/
[ovc]: /tech/start-here/overview/#one-program-several-commands
[ovp]: /tech/start-here/overview/#the-parts-from-the-outside-in
[ovd]: /tech/start-here/overview/#two-doors-into-the-use-cases
[ovt]: /tech/start-here/overview/#the-caller-and-the-tenant
[ovm]: /tech/start-here/overview/#run-modes
[ovb]: /tech/start-here/overview/#the-mail-backend-boundary
[pm]: /tech/start-here/package-map/
[pml]: /tech/start-here/package-map/#the-layer-rule
[pma]: /tech/start-here/package-map/#all-packages
[pmi]: /tech/start-here/package-map/#imports-that-you-may-not-expect
[pmw]: /tech/start-here/package-map/#where-to-add-code
[pmb]: /tech/start-here/package-map/#libraries
[tcs]: /tech/start-here/tool-call/#the-steps
[tcp]: /tech/start-here/tool-call/#where-the-permission-is-checked
[tcc]: /tech/start-here/tool-call/#where-text-from-the-mail-is-cleaned
[sfs]: /tech/inside-the-server/send-flow/#the-steps
[sfw]: /tech/inside-the-server/send-flow/#when-the-person-is-asked
[sft]: /tech/inside-the-server/send-flow/#two-ways-to-ask
[sfi]: /tech/inside-the-server/send-flow/#in-band
[sfy]: /tech/inside-the-server/send-flow/#how-the-yes-is-tied-to-the-message
[sfu]: /tech/inside-the-server/send-flow/#delivery-and-the-undo-window
[sfo]: /tech/inside-the-server/send-flow/#the-outbox
[pel]: /tech/inside-the-server/permissions/#levels
[ped]: /tech/inside-the-server/permissions/#how-a-decision-is-made
[pa]: /tech/inside-the-server/permissions/#full-autonomy-and-the-risk-acceptance
[pes]: /tech/inside-the-server/permissions/#token-scopes
[pli]: /tech/inside-the-server/permissions/#limits
[ssw]: /tech/inside-the-server/storage-and-secrets/#where-the-data-lives
[sso]: /tech/inside-the-server/storage-and-secrets/#how-the-database-opens
[ssg]: /tech/inside-the-server/storage-and-secrets/#migrations
[sst]: /tech/inside-the-server/storage-and-secrets/#the-tables
[sss]: /tech/inside-the-server/storage-and-secrets/#sealed-hashed-or-plain
[ssk]: /tech/inside-the-server/storage-and-secrets/#keys
[ssm]: /tech/inside-the-server/storage-and-secrets/#the-master-key
[ssf]: /tech/inside-the-server/storage-and-secrets/#the-first-start
[ssh]: /tech/inside-the-server/storage-and-secrets/#how-long-data-is-kept
[all]: /tech/inside-the-server/audit-and-logging/#the-activity-log
[alp]: /tech/inside-the-server/audit-and-logging/#the-program-log
[su]: /tech/inside-the-server/startup-and-shutdown/
[g]:
  /tech/inside-the-server/startup-and-shutdown/#the-signals-and-a-second-ctrl-c
[sr]: /tech/inside-the-server/startup-and-shutdown/#run-clean-up-then-three-jobs
[suc]: /tech/inside-the-server/startup-and-shutdown/#close-six-steps
[mb]: /tech/mail/mail-backend/
[mbp]: /tech/mail/mail-backend/#what-a-provider-implements
[mbo]: /tech/mail/mail-backend/#one-backend-for-each-account
[mbc]: /tech/mail/mail-backend/#the-connection-pool
[mbd]: /tech/mail/mail-backend/#the-secure-dialer
[mbr]: /tech/mail/mail-backend/#refs-folders-and-tags
[mbx]: /tech/mail/mail-backend/#changing-mail
[mbs]: /tech/mail/mail-backend/#sending-with-smtp
[caf]: /tech/mail/connecting-an-account/#finding-the-settings
[cas]: /tech/mail/connecting-an-account/#saving-an-account
[car]: /tech/mail/connecting-an-account/#a-refused-login-and-the-reconnect
[mc]: /tech/mail/mail-content/
[mcp]: /tech/mail/mail-content/#two-pipelines
[mcr]: /tech/mail/mail-content/#what-cleanuntrusted-removes
[mcb]: /tech/mail/mail-content/#which-parts-are-the-body
[mch]: /tech/mail/mail-content/#how-html-becomes-text
[mcl]: /tech/mail/mail-content/#limits
[mcc]: /tech/mail/mail-content/#the-copied-parser
[bmo]: /tech/mail/building-mail/#what-goes-out
[bmf]: /tech/mail/building-mail/#what-build-refuses
[bmr]: /tech/mail/building-mail/#recipients
[bml]: /tech/mail/building-mail/#addresses-that-look-like-others
[bmd]: /tech/mail/building-mail/#a-saved-draft
[bmp]: /tech/mail/building-mail/#the-preview-and-the-text-part
[cl]: /tech/ways-in/command-line/
[clr]: /tech/ways-in/command-line/#how-commands-are-registered-and-tested
[clo]: /tech/ways-in/command-line/#what-each-command-opens
[cle]: /tech/ways-in/command-line/#exit-codes-and-flags
[clp]: /tech/ways-in/command-line/#output-and-passwords
[cls]: /tech/ways-in/command-line/#what-the-commands-share-with-the-web-portal
[lh]: /tech/ways-in/local-http/
[lhl]: /tech/ways-in/local-http/#where-it-listens
[lhc]: /tech/ways-in/local-http/#the-checks-in-order
[lht]: /tech/ways-in/local-http/#tokens
[lhs]: /tech/ways-in/local-http/#stateless-mode
[lhw]: /tech/ways-in/local-http/#who-gets-which-channel
[lhp]: /tech/ways-in/local-http/#how-the-server-stops
[wp]: /tech/ways-in/web-portal/
[wps]: /tech/ways-in/web-portal/#when-it-starts-and-where-it-listens
[wpi]: /tech/ways-in/web-portal/#signing-in
[wph]: /tech/ways-in/web-portal/#why-the-portal-has-its-own-host-name
[wpc]: /tech/ways-in/web-portal/#the-checks-in-order
[wpg]: /tech/ways-in/web-portal/#a-get-only-reads
[wpp]: /tech/ways-in/web-portal/#pages-styles-and-the-script
[wpr]: /tech/ways-in/web-portal/#how-the-portal-reaches-the-service
[wpa]: /tech/ways-in/web-portal/#the-approval-page
[sdm]: /tech/security/security-design/#the-model
[sdc]: /tech/security/security-design/#two-channels-into-service
[sdf]: /tech/security/security-design/#failing-closed
[sds]: /tech/security/security-design/#signed-values
[ldm]: /tech/work-on-the-code/local-development/#the-make-targets
[ldc]: /tech/work-on-the-code/local-development/#what-ci-runs
[ldd]: /tech/work-on-the-code/local-development/#dependabot
[ldr]: /tech/work-on-the-code/local-development/#rules-of-the-code
[ldt]: /tech/work-on-the-code/local-development/#commit-and-pull-request-titles
[lda]: /tech/work-on-the-code/local-development/#add-a-package
[lds]: /tech/work-on-the-code/local-development/#the-docs-site
[ldo]: /tech/work-on-the-code/local-development/#add-or-change-a-shot
[k]: /tech/work-on-the-code/local-development/#build-and-try-the-container-image
[ldy]: /tech/work-on-the-code/local-development/#try-it
[tes]: /tech/work-on-the-code/testing/#test-servers
[teu]: /tech/work-on-the-code/testing/#unit-tests
[tee]: /tech/work-on-the-code/testing/#the-end-to-end-tests
[ter]: /tech/work-on-the-code/testing/#the-race-detector
[tef]: /tech/work-on-the-code/testing/#fuzz-targets
[tet]: /tech/work-on-the-code/testing/#build-tags
[tei]: /tech/work-on-the-code/testing/#the-interop-test
[rp]: /tech/work-on-the-code/release-process/
[rpm]: /tech/work-on-the-code/release-process/#what-a-release-makes
[rpo]: /tech/work-on-the-code/release-process/#once-before-the-first-release
[rpe]: /tech/work-on-the-code/release-process/#for-each-release
[rpw]: /tech/work-on-the-code/release-process/#what-the-release-workflow-does
[rpt]: /tech/work-on-the-code/release-process/#try-the-build-on-your-computer
[rpi]: /tech/work-on-the-code/release-process/#the-container-image
[rpr]: /tech/work-on-the-code/release-process/#the-mcp-registry
