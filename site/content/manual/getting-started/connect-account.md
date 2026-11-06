---
title: Connect your first account
description: Connect one mail account to email-mcp, in the browser or without a browser.
weight: 20
code_areas:
  - cmd/email-mcp/setup.go
  - cmd/email-mcp/account.go
  - cmd/email-mcp/connect.go
  - cmd/email-mcp/reconnect.go
  - internal/web/session.go
  - internal/web/guard.go
  - internal/web/links.go
  - internal/web/accounts.go
  - internal/web/templates/accounts.html
  - internal/web/templates/connect.html
  - internal/web/templates/no_session.html
  - internal/web/templates/use_name.html
  - internal/provider/imapsmtp/
  - internal/discovery/
  - internal/connect/
---

This page connects one mail account to email-mcp, in the browser or without a
browser. You need your email address, and the password or the app password of
the account. An app password is a separate password for one program. The
section [App passwords](#app-passwords) says more.

## In the browser

### Sign in to the web pages

Run this in a terminal (a window where you type commands). The command starts
the local web pages of email-mcp (pages that email-mcp shows in your browser,
from your own computer) and signs your browser in to them:

```sh
email-mcp setup
```

It prints a link and opens it in your browser. The link starts with
`http://email-mcp.localhost:` and works once, for 10 minutes. It signs your
browser in to the local web pages of email-mcp, which run at
`email-mcp.localhost` (a name that always means your own computer). The
sign-in lasts 24 hours, and it ends sooner after 2 hours without use. Run
`email-mcp setup` again to sign in again.

Your browser then shows the **Accounts** page. With no account yet, the page
has the button **Connect your first account**. If an account exists already,
your browser shows **Waiting for you** first. Click **Accounts** in the menu.

{{< screenshot
  name="accounts-empty"
  alt=`The Accounts page with no account. It says No account is connected
    yet, and it has the buttons Connect account and Connect your first
    account.`
>}}

If your browser does not find the link, see
[If your browser does not find
email-mcp.localhost](#if-your-browser-does-not-find-email-mcplocalhost).

### Add the account

Do these steps in the browser. Each button name is written as the page shows
it.

1. Click **Connect account**. With no account yet, the page also has the button
   **Connect your first account**. Both open the same page,
   **Connect an account**.

   {{< screenshot
     name="connect"
     alt=`The page Connect an account, with the field Email address and the
       buttons Find the server settings and Enter the settings yourself.`
   >}}

2. Type your email address and click **Find the server settings**. email-mcp
   looks up the IMAP (Internet Message Access Protocol: the protocol for
   reading mail) and SMTP (Simple Mail Transfer Protocol: the protocol for
   sending mail) servers of your provider. It sends only the domain of your
   address (the part after `@`) over the network, and it tests nothing yet. You
   can also click **Enter the settings yourself**.

3. Check the suggestion. The page warns that the settings are only suggestions.
   Check that each host name (the name of a server, such as `imap.example.com`)
   belongs to your mail provider, because your password is sent to the servers
   that you choose. Then click **Use these settings**.

   If the page lists more than one suggestion, each one has its own button
   **Use these settings**. If the page says that no server settings were found,
   click **Enter the settings yourself**.

   If you enter the settings yourself, type the **Server**, the **Port** and
   the **Security** of the incoming mail (IMAP) and of the outgoing mail
   (SMTP). The form starts with port 993 for IMAP and port 465 for SMTP, both
   with **TLS** (Transport Layer Security: it encrypts the connection from the
   first byte). **STARTTLS** is for servers that start plain and then switch on
   encryption (usually ports 143 and 587).

4. Check the **User name**. The page fills it in.

5. Type your password or app password in the field **Password or app
   password** (see [App passwords](#app-passwords)). The page never keeps the
   password, so type it again each time you test.

6. Click **Test the connection and save**. The account is saved only when the
   test passes. The page then shows **Accounts**. The account has the status
   **Connected** and the mail level **Send**.

   If the test fails, the page says why, and it saves nothing. For example:
   "The server refused the user name or the password. Some providers need an
   app password, or IMAP access turned on in their settings." For more help,
   see Troubleshooting.

7. If the page shows the section **Certificates**, a server showed a
   certificate (the proof of identity that a server shows) that your computer
   does not trust, and nothing was saved. Read the SHA-256 (Secure Hash
   Algorithm with 256 bits) fingerprint (a short, unique code of the
   certificate) and check it with your provider or with your local bridge (see
   [A local bridge](#a-local-bridge)). To go on, tick **Trust
   this certificate** for each server. Then type the password again, and click
   **Test the connection and save** again.

   A server that is not on your own computer also needs the box **I understand
   the risk**. The page shows this warning with the box:

   > A server that is not on this computer should have a certificate that an
   > authority signed. A certificate that no authority signed can mean that
   > someone on the network reads your mail and your password. Trust it only if
   > your provider gave you this fingerprint.

   A certificate for another host name, or one that has expired or is not valid
   yet, is never accepted. Then the page has no box **Trust this certificate**.

## App passwords

Many providers (for example Google, Microsoft, iCloud and Fastmail) need an app
password: a separate password that you make in the security settings of your
mail account. Type the app password where the page asks for the password. Some
providers also need IMAP access turned on in their settings.

## If your browser does not find `email-mcp.localhost`

Chrome, Edge and Firefox find `email-mcp.localhost` by themselves. If your
browser does not find it, use one of those browsers, or add this line to your
hosts file (the file where your computer keeps names and their addresses;
`/etc/hosts` on macOS and Linux, `C:\Windows\System32\drivers\etc\hosts` on
Windows):

```text
127.0.0.1 email-mcp.localhost
```

`email-mcp setup` also prints the address of a page on 127.0.0.1 (the address
of your own computer) that says the same. You cannot sign in on 127.0.0.1
itself (see the Security page).

The command opens your browser through a private temporary file, so the link
with its one-time token (a secret code) is not in the list of running programs.
If your computer opens `.html` files in another program, or a sandboxed
browser (a browser that runs with limits on what it can reach) cannot read the
file, open the printed link yourself. The command reminds you of this.

## Stop `setup` when you are done

The pages run until you press Ctrl-C (hold the Ctrl key and press C) in the
terminal. Stop `setup` when you are done.

## A local bridge

A local bridge (a program on your own computer that offers IMAP and SMTP
servers for a mail service), such as ProtonMail Bridge, uses a certificate that
no known authority signed. An authority is an organization that your computer
trusts to sign certificates. email-mcp then shows the SHA-256 fingerprint of
the certificate (a short, unique code of it). Check it with the bridge, then
trust it. email-mcp stores the fingerprint and checks it on every connection.
This is called "certificate pinning". It never turns certificate checks off.

## Without a browser

On a computer without a browser, use `account add`. It reads the password from
the terminal without showing it. The flag `--email` is the address of the
account:

```sh
email-mcp account add --email you@example.com
```

When the account is saved, the command prints `Connected`, your address and the
id of the account.

Without `--imap-host` and `--smtp-host`, it looks up the server settings and
asks you to confirm them before it sends the password anywhere. The question is
"Send the password to these servers?". With the flags, it uses your settings.
The flag `--imap-host` is your IMAP server, and `--smtp-host` is your SMTP
server. A flag is an option that starts with `--`:

```sh
email-mcp account add --email you@example.com \
  --imap-host imap.example.com --smtp-host smtp.example.com
```

The default security is `tls` (encrypted from the first byte) on ports 993
(IMAP) and 465 (SMTP). Use `--imap-security starttls` or
`--smtp-security starttls` for servers that start plain and then switch on
encryption (ports 143 and 587). Run `email-mcp account add -h` to see every
flag.
