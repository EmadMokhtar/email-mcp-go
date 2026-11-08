---
title: The mail backend
description: The interface that every mail provider implements, the IMAP and SMTP backend behind it, how it keeps connections and checks them, how an account is connected, and where another provider would go.
weight: 10
code_areas:
  - internal/domain/mail/backend.go
  - internal/domain/mail/mail.go
  - internal/domain/account/account.go
  - internal/provider/imapsmtp/backend.go
  - internal/provider/imapsmtp/config.go
  - internal/provider/imapsmtp/pool.go
  - internal/provider/imapsmtp/tlsdial.go
  - internal/provider/imapsmtp/ref.go
  - internal/provider/imapsmtp/folders.go
  - internal/provider/imapsmtp/tags.go
  - internal/provider/imapsmtp/organize.go
  - internal/provider/imapsmtp/move.go
  - internal/provider/imapsmtp/search.go
  - internal/provider/imapsmtp/fetch.go
  - internal/provider/imapsmtp/summary.go
  - internal/provider/imapsmtp/drafts.go
  - internal/provider/imapsmtp/smtp.go
  - internal/provider/imapsmtp/probe.go
  - internal/service/backends.go
  - internal/service/accounts.go
  - internal/service/reconnect.go
  - internal/service/read.go
  - internal/discovery/discovery.go
  - internal/connect/connect.go
---

This page explains how email-mcp talks to a mail server. It covers the interface
that every mail provider implements, the IMAP and SMTP backend behind it, the
connections and the checks on them, and the steps that connect an account. Read
it before you change how mail is read, changed or sent, how connections are
kept, or how an account is connected. Read it first if you want to add a
provider.

A mail backend is the part that talks to the mail server of one account. A
provider is the kind of service behind an account. The code has one provider, a
generic server that speaks IMAP (Internet Message Access Protocol, the protocol
for reading mail) and SMTP (Simple Mail Transfer Protocol, the protocol for
sending mail). Its name in the code is `account.ProviderIMAP`.

## What a provider implements

`mail.Backend` in {{< src "internal/domain/mail/backend.go" >}} is the
interface. {{< src "internal/domain/mail/mail.go" >}} has the types that it
uses. They are plain data. They have no network, storage or encryption code, and
`mcpserver` defines its own output types. Nothing in the package may depend on
one mail protocol or one provider. The use cases in `service` call this
interface after the permission check (see [One tool call, step by
step](/tech/start-here/tool-call/)). The sender of the outbox in
{{< src "internal/app/sender.go" >}} calls it too.

| Group | Methods |
|---|---|
| Read | `Capabilities`, `ListFolders`, `ListTags`, `Search`, `Changes`, `GetMessage`, `GetRaw`, `GetAttachment` |
| Change | `CreateFolder`, `CreateTag`, `SetFlags`, `Tag`, `Move`, `Trash` |
| Drafts and send | `SaveDraft`, `DiscardDraft`, `Send` |
| End | `Close` |

The comments of the interface set these rules for every provider:

- **Opaque values.** A `Ref` (a reference to one message), a `Cursor` (a
  position in a folder) and a `PageToken` (the next page of a search) are
  strings that the backend makes. The caller passes them back unchanged and does
  not parse them.
- **Folder aliases.** There are six: `@inbox`, `@archive`, `@sent`, `@drafts`,
  `@trash` and `@junk` (`mail.IsAlias`). A caller names a well-known folder
  without knowing its name at the provider.
- **No permanent delete.** `Trash` moves messages to the Trash folder and does
  nothing more. `DiscardDraft` moves a draft to Trash, and it refuses a message
  that is not a draft with `ErrUnsupported` before it changes anything.
- **Capabilities do not fail.** `Capabilities` takes a context (a Go value that
  tells work to stop) because it may have to ask the server. When the context
  ends, it reports what it knows, and it returns no error. It reports `Tags`
  (does the account support tags) and `MaxBatch` (the most refs in one batch
  call).
- **Errors by value.** A caller checks the errors below with `errors.Is`. A
  backend may wrap them with more detail.

| Error | It means |
|---|---|
| `ErrNotFound` | The message, folder or attachment does not exist. |
| `ErrRefStale` | A ref is not valid any more, for example because the message was moved or deleted outside email-mcp. The caller searches again. |
| `ErrTooLarge` | The data is larger than the limit that the caller gave. |
| `ErrUnsupported` | The backend does not support the feature. |
| `ErrConflict` | The change conflicts with the state, for example a folder that exists. |
| `ErrPartial` | The call made some or all of its change before it failed. The caller must not repeat it, because a second try can make copies or change other messages. It searches again to see the state. |

A backend that returns `ErrPartial` wraps it with the cause, so `errors.Is`
finds both. The interface also asks that a backend holds no secrets in exported
fields.

## One backend for each account

{{< diagram "backend-connections" >}}

`BackendFactory` in {{< src "internal/service/backends.go" >}} gives the service
the backend of an account. `Mail` returns it, and `Forget` closes and drops the
backends of an account that was removed. `BackendCache` is the default factory.
It builds one `imapsmtp.Backend` for each account and keeps it, so the pool of
connections of the account is reused between requests. A use case reaches it
through `mailBackend` in `service`, which calls `authorize` first and then
`Mail`.

- **One slot for each account.** A call to `Mail` holds the slot of its account
  while it gets or builds the backend. Calls for one account take turns. Calls
  for other accounts do not wait. The slot is a channel (a Go value that passes
  values between goroutines) with room for one value, and not a mutex (a lock),
  so a call that waits can stop when its context ends.
- **A fingerprint on every call.** Another process can change an account while
  this one runs. For example, `email-mcp account reconnect` can change the
  password, and it cannot tell the server. So `Mail` reads the sealed
  (encrypted) password from the store on every call. It makes a fingerprint: a
  SHA-256 (Secure Hash Algorithm with a 256-bit result) hash of the server
  settings, the version of the key and the sealed password. The settings are
  hashed as JSON (JavaScript Object Notation), so a field that is added to
  `account.MailServer` is part of the hash with no other change. The setting
  `SaveSentCopy` is one of those fields.
- **Same fingerprint, same backend.** `Mail` returns the cached backend. With a
  different fingerprint it decrypts the password and builds a backend. It
  decrypts the password only then.
- **A replaced backend closes after 2 minutes.** A use case that started earlier
  may still make calls on the old backend. So it is retired: it closes after
  `retireAfter`, 2 minutes. `Forget` and `Close` (the end of the program) close
  at once, retired backends included.
- **Idle backends are swept.** Another process can delete an account, and
  nothing asks this process for it again. `SweepIdle` closes the backends that
  no call asked for during `backendIdle`, 30 minutes. `sweepBackends` in
  {{< src "internal/app/app.go" >}} calls it every `backendSweepEvery`, 5
  minutes. A slot that a call holds is in use, and the sweep skips it.
- **A missing password.** When the store has no sealed password, `Mail` closes
  the cached backend and returns an error that says so. It does not pass on
  `store.ErrNotFound`, because a caller could take it for "account not found".

`ListAccounts` in {{< src "internal/service/read.go" >}} also asks the backend
of each account for its `Capabilities`, when the caller may read the account and
the account does not need to be reconnected. A question can open a connection,
so the list asks at most 8 backends at once (`maxCapabilityCalls`) and waits at
most 5 seconds (`capabilitiesBudget`). An account whose backend did not answer
in time has empty capabilities, and the list still succeeds.

## The connection pool

{{< src "internal/provider/imapsmtp/pool.go" >}} keeps the logged-in IMAP
connections of one account. {{< src "internal/provider/imapsmtp/config.go" >}}
has the settings: `PoolSize` (2), `DialTimeout` (15 seconds), `CommandTimeout`
(60 seconds) and `IdleTimeout` (5 minutes). `New` in
{{< src "internal/provider/imapsmtp/backend.go" >}} applies the defaults. It
makes no connection until the first call.

- **One slot and one connection for a call.** `slots` is a channel with
  `PoolSize` places. A call holds one place and one connection for its whole
  run, so it sends commands without taking turns. The other calls wait. A call
  dials only when no idle connection is left.
- **Idle connections.** A healthy connection goes back to the idle list when the
  call ends. `takeIdle` closes the idle connections that are older than
  `IdleTimeout` or have ended. It does this when a call asks for a connection.
  No timer does it.
- **Forced close.** The pool never calls `Client.Close` or `Client.Logout`. Both
  wait for the reader of the client, and the reader can wait for a call that
  never finishes. The pool closes the network connection (`forceClose`). That
  cannot wait. `run` runs the call in its own goroutine (a function that Go runs
  at the same time as the rest of the program). When the context ends, or the
  call runs longer than `CommandTimeout`, `run` closes the connection and gives
  the call `abandonGrace`, 500 milliseconds, to return. Then it stops waiting
  for it.
- **Retry once.** `with` runs the call again on another connection when the
  first run failed because the connection failed: a network error, or a
  connection that has ended. It tries again once. It does not try again after an
  answer of the server (`NO` or `BAD`), after a failed dial, or after a stop by
  the context or the time limit. So a call under `with` must be safe to repeat.
  Reads, the checks of a batch, `SetFlags` and `Tag` are: setting a flag again
  changes nothing.
- **Calls that are not repeated.** `withNoRetry` runs the call once. These calls
  use it: `SaveDraft` and the Sent copy (`APPEND`, the command that stores a
  message in a folder), `CreateFolder` (`CREATE`), and the change step of `Move`
  and `Trash`. After a dropped connection the server may have done the command.
  A second try could store a message twice, report a folder that exists, or copy
  the messages twice.
- **A rule for each call.** A call must finish or close every command that it
  starts, for example with `defer cmd.Close()` for a `FETCH`. `imapclient` stops
  reading the connection while a `FETCH` body is unread. The next call that gets
  the connection then waits for `CommandTimeout`, fails, and the pool closes the
  connection. So a mistake in one call shows up as the failure of another call.
- **Reads leave the mail as it is.** `Search`, `Changes` and `GetMessage` open
  the folder read-only with `EXAMINE` and fetch with `BODY.PEEK`, the IMAP way
  to read a message without marking it as read.

## The secure dialer

`dialWithConn` in {{< src "internal/provider/imapsmtp/tlsdial.go" >}} makes a
connection, secures it and logs in. The connection to the SMTP server follows
the same rules (see "Sending with SMTP"). In this order:

1. `MailServer.Validate` checks the settings: a host, a port from 1 to 65535,
   and a security mode that is `tls`, `starttls` or `plain`. `MailServer` is the
   type that holds the IMAP and SMTP settings of an account, with the pins, in
   {{< src "internal/domain/account/account.go" >}}. A plain (unencrypted)
   connection passes only when `Config.Local` is true and the host is
   `localhost` or a loopback IP (Internet Protocol) address (an address of this
   computer).
2. The dial, the secure step, the greeting and the login together get
   `DialTimeout`.
3. The security mode decides the next step. With `tls`, TLS (Transport Layer
   Security, the protocol that encrypts a connection) starts at the first byte.
   With `starttls`, the client sends `STARTTLS` (the command that upgrades a
   plain connection to TLS) before any other command. If the server does not
   support it, or the upgrade fails, the dial fails. There is no fall back to
   plain text. With `plain`, the dialer also checks the address that the
   connection really reached, because a name such as `localhost` can be changed
   to point at another computer.
4. The client logs in with the IMAP `LOGIN` command (the command that sends the
   user name and the password). The password goes out here and in the SMTP login
   only.

TLS needs version 1.2 or newer (`tls.VersionTLS12`).

**The certificate check.** `newPinnedTLSConfig` replaces the standard check of
`crypto/tls` with `verifyCertificate`. Go calls it for every connection, also
for a resumed session. It gets the leaf certificate: the certificate of the
server itself, the first one in the chain.

- **With a pin.** A pin is the SHA-256 fingerprint of the leaf certificate, in
  lowercase hex, that the person chose to trust. The fingerprint of the
  certificate must equal it, and the comparison takes the same time for any
  input. Only a match accepts the certificate.
- **Without a pin.** The certificate must be valid for the host name, be inside
  its dates (it has not expired, and its first day has come), and chain to the
  root certificates (the list of certificate authorities that the system trusts;
  `RootCAs` replaces it in tests). The name and the dates come first. The
  verifier of macOS and Windows reports "unknown authority" for every
  self-signed certificate, also when the name is wrong or the certificate has
  expired. With the order of the code, the reason is the same on every platform,
  and "unknown authority" means that the name and the dates are right.

A certificate that is not accepted gives a `*CertificateUnknownError`. It has
the protocol (`imap` or `smtp`), the host, the fingerprint, `Pinned` (a pin was
set and the certificate did not match) and `Reason` (the error of the check
without a pin).

**When may a pin be offered?** Only when the pin is a first one, the only
problem is that no known authority signed the certificate, and its fingerprint
is a SHA-256. This is the rule of `connect.CheckCertificate`. The web portal
(the local web site of the person) and the commands follow it:

| The certificate | A pin is offered? |
|---|---|
| No pin is set, and no known authority signed it. The name and the dates are right. | Yes |
| No pin is set, and the host name does not match. | No |
| No pin is set, and the certificate is outside its dates. | No |
| A pin is set, and the certificate is another one. | Not as a first pin. Only a reconnect may change the pin (see "A refused login and the reconnect"). |

The login has four errors that callers check with `errors.Is`:

| Error | It means |
|---|---|
| `ErrAuthFailed` | The server refused the login: an IMAP `NO` answer to `LOGIN`, or an SMTP reply 535 or 534 to `AUTH`. The exceptions are the codes `UNAVAILABLE` and `SERVERBUG` (the server does not work) and `INUSE` and `LIMIT` (it is busy). |
| `ErrServerBusy` | The IMAP server answered `LOGIN` with `NO` and the code `INUSE` or `LIMIT`. The password was not refused. |
| `ErrServerUnreachable` | No working connection could be made: it was refused, the server closed it before the login ended, or the host was not found. |
| `ErrConnectionLost` | A logged-in connection ended during a call. The server may have done the command. |

The text of these errors holds no free text of the server, because servers
repeat the user name or the address in it, and logs must not hold addresses.

## Refs, folders and tags

**Refs.** {{< src "internal/provider/imapsmtp/ref.go" >}} makes a `Ref` from
three values: the folder name, the UIDVALIDITY of the folder and the UID of the
message. A UID is the number that a folder gives to a message. The UIDVALIDITY
is a number that changes when the folder starts its UIDs again, so an old UID
may then name another message. The ref is these values as JSON, in base64url (a
text form of bytes that is safe in a web address) without padding, and at most
4,096 characters (`maxRefLen`). `decodeRef` refuses a ref with no folder, UID 0
or UIDVALIDITY 0. A ref is not signed: it only names a message in a folder of
the account, and the login of the account can open every folder.

`checkRefFolder` in {{< src "internal/provider/imapsmtp/fetch.go" >}} opens the
folder of a ref. A different UIDVALIDITY, or a folder that is gone, gives
`ErrRefStale`, and the UID is not used. A message that is not in the folder any
more gives `ErrNotFound`.

**Folders.** {{< src "internal/provider/imapsmtp/folders.go" >}} turns an alias
into a folder name with the `SPECIAL-USE` attributes (an IMAP extension in which
the server marks the Trash folder, the Sent folder and so on). `INBOX` has a
fixed name. When no folder has the attribute of an alias, the error wraps
`ErrNotFound`, and a folder is not guessed by its name. A folder with several
attributes gets one alias, chosen by `aliasPriority`, which starts with
`@trash`. `Folder.Trash` is true for every folder with the `\Trash` attribute.
The counts of unread and total messages come with the list only when the server
supports `LIST-STATUS` (an IMAP extension that sends the counts with the list of
folders). A folder name has at most 1,024 bytes (`maxFolderNameLen`) and no
control character.

**Tags.** {{< src "internal/provider/imapsmtp/tags.go" >}} makes a tag from an
IMAP keyword (a flag without a backslash). One rule (`isTag`) says what a tag
is: 1 to 64 letters, digits, `-` or `_` (`tagPattern`), and not `Junk`,
`NonJunk` or `NotJunk`, which the spam filters of mail clients set. So a system
flag such as `\Deleted` is never a tag, and the name is checked before it
reaches a command. An account supports tags when the answer of the server for
the inbox lists the wildcard `\*` in `PERMANENTFLAGS` (the flags that the server
lets a client keep), which means that a client may create keywords. A folder
opened for writing with the IMAP command `SELECT` gives a sure answer. A list
with no wildcard from a read-only open (`EXAMINE`) says nothing, because some
servers report no permanent flags then. So while the answer is not known,
`Capabilities`, `ListTags` and `Tag` open the inbox with `SELECT` once. That
changes nothing in the inbox. `CreateTag` only checks the name, because a
keyword exists from the time it is first used.

**Search and changes.** {{< src "internal/provider/imapsmtp/search.go" >}}
searches one folder, newest first by UID. A page has 50 messages by default
(`defaultLimit`) and 100 at most (`maxLimit`). `Changes` returns the messages
that arrived after the position that a cursor marks, oldest first, at most 100
in one call (`maxChanges`). An empty cursor starts the tracking and returns no
message. A cursor of a folder with another UIDVALIDITY gives the reset flag. The
page token and the cursor are signed by `signing.Signer`, with a purpose and
with `TokenScope`, which the factory sets to `<tenant>/<account>`. So a token of
one account fails for another account. A page token lasts `pageTokenTTL`, 1
hour. A cursor does not expire.

**Reading a message.** `GetMessage` fetches the structure of the message first.
The body that it returns holds the text parts that a mail client shows. It may
fetch other versions of the body to compare them, and it does not return them.
`GetAttachment` fetches an attachment, and nothing else does.
{{< src "internal/provider/imapsmtp/summary.go" >}} makes the summary and the
snippet of each message in a list. [Mail content](/tech/mail/mail-content/)
explains how a text part is cleaned, and its limits.

## Changing mail

The calls that change messages are in
{{< src "internal/provider/imapsmtp/organize.go" >}},
{{< src "internal/provider/imapsmtp/move.go" >}} and
{{< src "internal/provider/imapsmtp/drafts.go" >}}. They follow these rules:

- **Check every ref, then change.** `newBatch` reads the refs and groups them by
  folder and UIDVALIDITY. A call of more than 500 refs (`maxBatch`) is refused
  with `ErrUnsupported`. `check` opens each folder read-only and checks that the
  UIDVALIDITY is the one in the refs and that each UID is in the folder and not
  marked `\Deleted`. A message marked `\Deleted` counts as gone: a search does
  not show it, and another client can expunge it at any time. A ref that is
  stale or missing fails the whole call, and nothing changes.
- **Change once for each folder.** `SetFlags` and `Tag` send
  `UID STORE -FLAGS.SILENT` for the flags to remove and
  `UID STORE +FLAGS.SILENT` for the flags to add (`STORE` changes the flags of
  messages, and `.SILENT` asks the server not to send the flags back). They open
  the folder for writing and check the UIDVALIDITY again, because a folder can
  change between the check and the change.
- **All or nothing holds until the first change.** If a later folder fails, the
  error says how many messages were already changed, and it wraps `ErrPartial`.
- **Nothing is deleted for good.** The only `EXPUNGE` is `UID EXPUNGE` of the
  messages that the fallback of `Move` has just copied.

**Move and Trash.** `Move` uses the `MOVE` command when the server has it. The
fallback is, for each folder, `UID COPY`, then
`UID STORE +FLAGS.SILENT (\Deleted)`, then `UID EXPUNGE` of the same UIDs
(`EXPUNGE` removes the messages that are marked `\Deleted`). This needs
`UIDPLUS`, an IMAP extension that makes the server report the UIDs of the
messages that it copies or stores. A plain `EXPUNGE` would also remove other
messages that a mail client marked `\Deleted`, so it is not used. When the
server has neither `MOVE` nor `UIDPLUS`, the call fails before it changes
anything, with `ErrUnsupported`. The refs of the moved messages come from the
`COPYUID` answer of the server (the UIDs of the copies). The backend does not
guess them. When the answer is missing, the messages are moved or copied, and
the error wraps `ErrPartial`. A message that is in the target folder already
stays, with its ref. `Trash` is `Move` to `@trash`. When the account has no
Trash folder, the call fails and nothing changes.

**Drafts.** `SaveDraft` stores the message in the `@drafts` folder with the
flags `\Draft` and `\Seen`. It takes the ref from the `APPENDUID` answer of the
server (the UID of the stored message, from `UIDPLUS`). When the server lacks
`UIDPLUS`, it finds the draft by its `Message-ID`, and it refuses a draft with
no `Message-ID` before it stores anything. After the draft is stored, every
error says that the draft was saved and that its ref is not known, and wraps
`ErrPartial`, so nobody saves it again. `DiscardDraft` checks the `\Draft` flag,
and then calls `Trash`. The flag decides, not the folder.

## Sending with SMTP

`Send` in {{< src "internal/provider/imapsmtp/smtp.go" >}} sends one message.
{{< src "internal/provider/imapsmtp/probe.go" >}} uses the same connection code
to test a login. The send works in these steps:

1. `checkOutgoing` refuses a message with no data or no recipient, or with an
   address that cannot be written into an SMTP command. The error is
   `ErrInvalidMessage`, and nothing is sent.
2. `withSMTP` opens an SMTP connection for this message alone. It does not use
   the pool. It validates the settings and dials. Then `secureSMTP` secures the
   connection with the rules of the secure dialer: `tls`, or `starttls` with no
   fall back, or `plain` to a loopback address on a local install. The
   certificate check is the same code, against `PinnedSMTPCert`. After the
   hello, a `tls` or `starttls` connection must have a finished handshake with
   TLS 1.2 or newer.
3. The login is `AUTH PLAIN` (the command that sends the user name and the
   password in one step), and no other. `loginSMTP` fails if the server does not
   offer it. A reply 535 or 534 matches `ErrAuthFailed`.
4. `sendOn` sends `MAIL FROM` (the sender), one `RCPT TO` for each recipient,
   and the data. It is all or nothing for the recipients: when the server
   refuses one, `Send` resets the transaction, sends no data and returns a
   `*RecipientRejectedError` with the place of the recipient. A message with
   addresses outside ASCII (the basic set of characters: English letters, digits
   and common signs) needs `SMTPUTF8`, an SMTP extension that allows such
   addresses. Without it, the error is `ErrSMTPUTF8Unsupported`, and nothing is
   sent.
5. `Send` returns nil when the server has accepted the message. If
   `SaveSentCopy` is on, it then saves a copy in the Sent folder with `APPEND`.
   The person turns `SaveSentCopy` on in the settings page of the account in the
   web portal. No command sets it. A failure of the copy is logged with a fixed
   code and does not fail the send. An error would make the caller try again,
   and the message would go out twice.

The time limit of the whole conversation is `CommandTimeout` plus the time that
the data needs at 100 KiB per second (`smtpDataRate`). A KiB (kibibyte) is 1,024
bytes. A message of 25 MiB adds 256 seconds to `CommandTimeout`. A MiB
(mebibyte) is 1,048,576 bytes.

**What an error says.** A reply code from 400 to 599 at a step is a
`*ReplyError` with the step and the code. After the data was sent, only such a
code is a sure refusal. When the connection fails, the time runs out, or the
answer is garbled or has another code than 250, nobody can tell whether the
server took the message. The error then wraps `ErrDeliveryUnknown`. The caller
must not send the message again without asking a person. These errors hold no
text of the server. `sendError` in `internal/app/sender.go` reads their types to
decide between a retry and a final failure. See [The send
flow](/tech/inside-the-server/send-flow/) for the outbox.

## Connecting an account

No tool of the agent connects an account. The tool `connect_account` returns a
link to the page where the person does it. The person connects an account in the
web portal (the pages under `/connect`) or with the command `account add` or
`import-env`. The person reconnects an account in the web portal (the page
`/accounts/{id}/reconnect`) or with `account reconnect`. The commands
`account test` and `doctor` test accounts that exist. All of these call the same
use cases in `service`.

### Finding the settings

`Finder.Mail` in {{< src "internal/discovery/discovery.go" >}} suggests server
settings from an email address. It asks four sources at the same time, so a slow
source cannot use the time of the others. The result keeps the order of the
sources.

| Source | What it reads | What it suggests |
|---|---|---|
| The ISP (internet service provider) database of Thunderbird | `https://autoconfig.thunderbird.net/v1.1/<domain>` | The servers in the file, with `ssl` or `starttls` only |
| The file of the domain | `https://autoconfig.<domain>/mail/config-v1.1.xml` | The same, from the domain itself |
| DNS SRV records (DNS is the Domain Name System; an SRV record names the host and port of a service) | `_imaps._tcp` for IMAP, `_submissions._tcp` and then `_submission._tcp` for SMTP | The hosts and ports in the records. `_imap._tcp` is not used, because it has no TLS from the first byte. |
| A guess from the MX record (the record that names the mail server of a domain) | The MX host with the lowest preference number | `imap.<base>` on port 993 with TLS, and `smtp.<base>` on port 587 with `STARTTLS`, where `<base>` is the registrable domain of the MX host (the domain that a person can register, such as `example.co.uk`) |

The package keeps three promises. It does not suggest an unencrypted connection.
It does not send the email address: only the domain goes into an HTTP (HyperText
Transfer Protocol) request or a DNS query. And it stays small. Its limits are in
the table "Limits and lifetimes". A source that fails gives nothing, and the
others still count. `Mail` returns `discovery.ErrNotFound` when no source gave a
suggestion, and `discovery.ErrInvalidAddress` for an address that it cannot use,
before any network use. A host name is accepted as a plain DNS name with at
least two labels. A name that ends in a number is refused. So is a name that
ends in `localhost`, `local`, `localdomain`, `internal`, `invalid`, `test` or
`example`: such a name stands for the local machine or the local network, and a
suggestion with it would send the login there.

The result is only a list of suggestions. The web portal shows it before it asks
for a password, because an MX guess can name the host of a third party, such as
a spam filter. The person chooses the servers that get the password. A test must
pass before an account is saved.

### Saving an account

`Service.CreateIMAPAccount` in {{< src "internal/service/accounts.go" >}}
connects an IMAP and SMTP account for the tenant of the caller. In this order,
it:

1. cleans the address and the settings (outer spaces go, host names and pins
   become lower case, an empty user name becomes the address), and checks them
   with `MailServer.Validate` and the form of the host names and the pins;
2. refuses an address that the tenant has connected already. This saves a test
   that would send the password for nothing. The store checks again when it
   saves, because another process may connect the same address meanwhile;
3. tests the settings with the password (`imapsmtp.Probe`) and refuses unless
   both the IMAP login and the SMTP login work;
4. makes the account with `account.NewIMAPAccount`: level Send (4), both
   confirmations on, and the default limits;
5. seals the password with the key of the tenant and stores the account and the
   sealed password in one transaction, so a failure leaves nothing;
6. writes the audit entry `account_connected`, which holds the account id and no
   address.

`Probe` logs in to the IMAP server, authenticates to the SMTP server with
`AUTH PLAIN`, sends no message and opens no mailbox. A certificate that is not
accepted does not fail the probe: it is reported in `IMAPCert` or `SMTPCert`,
and the other server is still tested. So the person can pin both certificates
after one probe. The service then returns a `*CertificateError`. A failed login
or connection gives a `*ConnectionTestError`. Neither holds the password or the
text of a server.

### The shared package

{{< src "internal/connect/connect.go" >}} holds what the web portal and the
commands share, so the two use the same rules:

- the fixed texts for each error of the use cases and for a failed test
  (`ErrorText`, `TestFailureText`). A text is chosen by the type of the error
  and not from the text of the error, so the text of a mail server does not
  reach the person;
- the decision about a pin (`CheckCertificate`, `CheckReconnect`);
- `CleanHost`, which cleans a host name as the service tests it;
- `PlainOnThisComputer`, which says that a server on this computer is set to a
  plain connection. This needs the explicit consent of the person. A plain
  connection to another host is refused by the service, so no consent allows it;
- `GroupFingerprint`, which writes a fingerprint in pairs, such as `AB:CD:EF`.

The only value from a server that a caller may show is the fingerprint of a
certificate. The web portal turns an offer to pin into a signed token that is
bound to the session, the protocol, the host, the port and the fingerprint, and
that lasts `certOfferTTL`, 10 minutes (`internal/web/accounts.go`).

### A refused login and the reconnect

When a call to the mail server fails with `ErrAuthFailed`, `noteBackendError` in
`service` marks the account `needs_reauth`. The same happens in the sender of
the outbox. The mark is written only while the credentials version of the
account is the one that the failing call read. A reconnect adds one to this
version, so a call from before a reconnect cannot undo it. The write runs even
when the context of the call has ended, with its own limit of 30 seconds
(`endTimeout`; the sender has `statusTimeout`). `authorize` then refuses the
next call before it reaches the server, with a `*ReauthError`.

The account works again after `ProbeAccount` passes (`account test` and `doctor`
call it) or after `ReconnectIMAPAccount`, in
{{< src "internal/service/reconnect.go" >}}. A reconnect keeps the id, the
server settings, the levels, the queued messages and the activity of the
account. It tests the stored settings with the password that the person gave, or
the stored one when none is given. It saves that password and the pins that the
person decided, in one step, and marks the account connected. A server whose
certificate is not accepted fails at the handshake, so the password is not sent
to it. A change of the hosts, the ports or the security modes is not a
reconnect: the person removes the account and adds it again.

`ReconnectPinChange` decides what a reconnect may do with a pin:

| The certificate | Change |
|---|---|
| No pin was set. No known authority signed it. The name and the dates are right. | `PinChangeAdd`: pin it |
| A pin was set. This is another certificate. The name and the dates are right, and no known authority signed it. | `PinChangeReplace`: pin this one |
| A pin was set. This is another certificate that a known authority signs for the host. | `PinChangeRemove`: stop pinning |
| Any other case: a wrong name, a certificate outside its dates, another problem, or a value that is not a SHA-256 | `PinChangeRefused`: nothing may change |

A decision of the person counts only for the server that shows a certificate
with the same fingerprint in this test. Before a replace or a removal, the
person sees the old fingerprint, the fingerprint of the certificate that the
server shows, and a warning. If the certificate changes again during the test,
the result is `ErrCertificateDecisionStale`, and nothing is saved. The user
manual explains the steps for a person: see [Connect your first
account](/manual/getting-started/connect-account/), [Manage
accounts](/manual/daily-use/manage-accounts/) and
[Troubleshooting](/manual/daily-use/troubleshooting/).

## Limits and lifetimes

| Name | Value | What it bounds |
|---|---|---|
| `maxBatch` (`backend.go`) | 500 | Refs in one batch call |
| `defaultPoolSize` (`config.go`) | 2 | IMAP connections for one account |
| `defaultDialTimeout` (`config.go`) | 15 seconds | Connect and secure step. For IMAP, also the greeting and the login |
| `defaultCommandTimeout` (`config.go`) | 60 seconds | One pooled call, and the SMTP conversation before the data time |
| `defaultIdleTimeout` (`config.go`) | 5 minutes | Age at which `takeIdle` closes an idle connection |
| `capabilitiesTimeout` (`backend.go`) | 15 seconds | One call of `Capabilities` |
| `abandonGrace` (`pool.go`) | 500 milliseconds | Wait for a call after its connection is closed |
| `smtpDataRate` (`smtp.go`) | 100 KiB per second | Time that the data of a send adds to the SMTP limit |
| `defaultLimit`, `maxLimit` (`search.go`) | 50, 100 | Messages in a search page |
| `maxChanges` (`search.go`) | 100 | Messages in one call of `Changes` |
| `pageTokenTTL` (`search.go`) | 1 hour | Life of a page token. A cursor has no end. |
| `maxRefLen` (`ref.go`) | 4,096 characters | A ref |
| `maxFolderNameLen` (`folders.go`) | 1,024 bytes | A folder name |
| `tagPattern` (`tags.go`) | 1 to 64 characters | A tag name |
| `retireAfter` (`backends.go`) | 2 minutes | Life of a replaced backend |
| `backendIdle`, `backendSweepEvery` (`app.go`) | 30 minutes, 5 minutes | Time unused before the sweep closes a backend, and the time between sweeps |
| `capabilitiesBudget`, `maxCapabilityCalls` (`read.go`) | 5 seconds, 8 | `ListAccounts` waits at most this long for the capabilities of the backends, and asks at most 8 at once |
| `endTimeout` (`write.go`), `statusTimeout` (`sender.go`) | 30 seconds | The write of `needs_reauth` after the context of the call has ended |
| `maxAddressBytes`, `maxHostBytes`, `maxLabelBytes` (`accounts.go`, `discovery.go`) | 254, 253, 63 bytes | An address, a host name, and a part of a host name between dots |
| `mime.MaxAddressBytes` (`address.go`) | 254 bytes | An address in an SMTP command (`validEnvelopeAddress`) |
| `defaultLookupTimeout`, `httpTimeout` (`discovery.go`) | 10 seconds | All four sources together, and one HTTP request |
| `maxBodyBytes`, `maxRedirects` (`discovery.go`) | 1 MiB, 3 | One HTTP response, and the redirects of one request |
| `maxServersPerSide`, `maxSuggestions` (`discovery.go`) | 4, 10 | Servers from one source for each protocol, and the length of the result |
| `maxHostInputBytes` (`discovery.go`) | 1,024 bytes | The host name that discovery converts |
| `certOfferTTL` (`web/accounts.go`) | 10 minutes | An offer to trust a certificate |

The limits on the text of a body, the HTML and the snippets are in [Mail
content](/tech/mail/mail-content/#limits).

## Adding a provider

A provider needs a type that implements `mail.Backend`. The code that chooses
the backend has one answer, and the places that name `imapsmtp` show what else
knows about it. This list is a starting point. It is not a recipe that was
tested.

The command lists the packages that import `imapsmtp`. `go list` prints
information about packages, `-f` sets the output with a template, and `./...`
means every package of the module. `grep` keeps the lines where the list of
imports has `imapsmtp`, and `cut` prints the part before the first colon:

```sh
go list -f '{{.ImportPath}}: {{.Imports}}' ./... | grep 'imapsmtp[] ]' | cut -d: -f1
```

The result is `cmd/email-mcp`, `internal/app`, `internal/connect`,
`internal/mcpserver`, `internal/service` and `internal/web`. Each one uses it
for this:

| Package | What it takes from `imapsmtp` |
|---|---|
| `service` | `imapsmtp.New` in `BackendCache` (the one place that builds a backend), `imapsmtp.Probe` as the default test, the types `Config`, `ProbeResult` and `CertificateUnknownError`, and `ErrAuthFailed` |
| `app` | The error values in `sender.go`, to decide between a retry and a final failure of a send |
| `mcpserver` | The error values and the certificate error, to choose the fixed texts for the agent |
| `connect` | The certificate error and the login errors, for the fixed texts and the pin decision |
| `web` | The certificate error for the offers to pin, and the connection errors on the approval page |
| `cmd/email-mcp` | The certificate error, in the connect flow of the commands |

These facts also matter:

- `account.Provider` has one value, `ProviderIMAP`, and `NewIMAPAccount` sets
  it. The account row stores it. `BackendCache.Mail` builds an
  `imapsmtp.Backend` and does not read the provider.
- `Deps.Probe` and the use cases `CreateIMAPAccount` and `ReconnectIMAPAccount`
  take IMAP and SMTP settings: hosts, ports, security modes and pins. A provider
  with other settings has no place in them.
- `imapsmtp.New` panics when `Config.TokenScope` is empty or the signer is nil.
  These are mistakes in the code that sets the backend up. The scope binds page
  tokens and cursors to one account.
- The fake IMAP and SMTP servers of the tests are in
  `internal/testutil/imaptest` and `internal/testutil/smtptest`.

## Related pages

- [Mail content](/tech/mail/mail-content/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [The send flow](/tech/inside-the-server/send-flow/)
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
- [Map of packages](/tech/start-here/package-map/)
