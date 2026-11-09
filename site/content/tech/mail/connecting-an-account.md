---
title: Connecting an account
description: How email-mcp suggests server settings from an address, tests and saves an account, decides about a certificate that no authority signed, and reconnects an account whose login a server refused.
weight: 20
code_areas:
  - internal/discovery/discovery.go
  - internal/connect/connect.go
  - internal/service/accounts.go
  - internal/service/reconnect.go
  - internal/service/service.go
  - internal/service/write.go
  - internal/app/sender.go
  - internal/web/accounts.go
  - internal/web/reconnect.go
---

This page explains how an account gets connected to email-mcp: how server
settings are suggested from an email address, how an account is tested and
saved, what the web portal and the commands share, and how an account is
reconnected after a mail server refused its login. Read it before you change
discovery, the connect flow, the decision about a pin, or the reconnect. A pin
is the fingerprint of a certificate that the person chose to trust. A
certificate is the signed proof of identity that a mail server shows. The
connections that an account uses afterwards are on [The mail
backend](/tech/mail/mail-backend/).

No tool of the agent connects an account. The tool `connect_account` returns a
link to the page where the person does it. The person connects an account in the
web portal (the pages under `/connect`) or with the command `account add` or
`import-env`, and reconnects it in the web portal (the page
`/accounts/{id}/reconnect`) or with `account reconnect`. The commands
`account test` and `doctor` test accounts that exist. All of these call the same
use cases in `service`.

## Finding the settings

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
Transfer Protocol) request or a DNS query. And it stays small (see "Limits"). A
source that fails gives nothing, and the others still count. `Mail` returns
`discovery.ErrNotFound` when no source gave a suggestion, and
`discovery.ErrInvalidAddress` for an address that it cannot use, before any
network use. A host name is accepted only as a plain DNS name with at least two
labels. A label is one part of a name between dots, such as `imap`, `example`
and `com` in `imap.example.com`. A name that ends in a number is refused. A few
names that are for this computer, a local network, tests or examples are
refused too.

The result is only a list of suggestions. The web portal shows it before it asks
for a password, because an MX guess can name the host of a third party, such as
a spam filter. The person chooses the servers that get the password. A test must
pass before an account is saved. Use only the servers that your provider names.

## Saving an account

`Service.CreateIMAPAccount` in {{< src "internal/service/accounts.go" >}}
connects an IMAP and SMTP account for the tenant of the caller. In this order,
it:

1. cleans the address and the settings (outer spaces go, host names and pins
   become lower case, an empty user name becomes the address), and checks them
   with `MailServer.Validate` and the form of the host names and the pins;
2. refuses an address that the tenant has connected already. This saves a test
   that would send the password for nothing. The store checks again when it
   saves, because another process may connect the same address while the probe
   runs;
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

## The shared package

{{< src "internal/connect/connect.go" >}} holds what the web portal and the
commands share, so the two use the same rules:

- the fixed texts for each error of the use cases and for a failed test
  (`ErrorText`, `TestFailureText`). The type of the error chooses the text, so
  the text of a mail server does not reach the person;
- the decision about a pin (`CheckCertificate`, `CheckReconnect`);
- `CleanHost`, which cleans a host name as the service tests it;
- `PlainOnThisComputer`, which says that a server on this computer is set to a
  plain connection. This needs the explicit consent of the person. The service
  refuses a plain connection to another host, and no consent allows it;
- `GroupFingerprint`, which writes a fingerprint in pairs, such as `AB:CD:EF`.

The only value from a server that a caller may show is the fingerprint of a
certificate. The web portal binds an offer to pin to a signed token that lasts
`certOfferTTL`, 10 minutes, in {{< src "internal/web/accounts.go" >}}.

## A refused login and the reconnect

When a call to the mail server fails with `ErrAuthFailed`, `noteBackendError` in
`service` marks the account `needs_reauth`. The same happens in the sender of
the outbox. The mark is written only while the credentials version of the
account is the one that the failing call read. A reconnect adds one to this
version, so a call from before a reconnect cannot undo it. The write runs even
when the context of the call has ended, with its own limit of 30 seconds
(`endTimeout`; the sender has `statusTimeout`). `authorize` then refuses the
next call with a `*ReauthError`, before it reaches the server.

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

`ReconnectPinChange` decides what a reconnect may do with a pin: add it,
replace it, remove it, or nothing (`PinChangeRefused`). The table in [The secure
dialer](/tech/mail/mail-backend/#the-secure-dialer) gives each case.

A decision of the person counts only for the server that shows a certificate
with the same fingerprint in this test. Before a replace or a removal, the
person sees the old fingerprint, the fingerprint of the certificate that the
server shows, and a warning. If the certificate changes again during the test,
the result is `ErrCertificateDecisionStale`, and nothing is saved.

## Limits

| Name | Value | What it bounds |
|---|---|---|
| `defaultLookupTimeout`, `httpTimeout` (`discovery.go`) | 10 seconds | All four sources together, and one HTTP request |
| `maxBodyBytes`, `maxRedirects` (`discovery.go`) | 1 MiB, 3 | One HTTP response, and the redirects of one request. A MiB (mebibyte) is 1,048,576 bytes. |
| `maxServersPerSide`, `maxSuggestions` (`discovery.go`) | 4, 10 | Servers from one source for each protocol, and the length of the result |

## Related pages

- [The mail backend](/tech/mail/mail-backend/)
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
- [Connect your first account](/manual/getting-started/connect-account/) in the
  user manual
- [Manage accounts](/manual/daily-use/manage-accounts/) in the user manual
- [Troubleshooting](/manual/daily-use/troubleshooting/) in the user manual
