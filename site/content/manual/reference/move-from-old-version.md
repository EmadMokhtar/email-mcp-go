---
title: Moving from the old email-mcp
description: Import the account of the first version of email-mcp into the new one.
weight: 50
code_areas:
  - cmd/email-mcp/importenv.go
---

After this page, you can move the account of the first version of email-mcp into
the new version, with one command.

The first version of email-mcp read its settings from a `.env` file (a text file
of settings), or from the `env` block of the AI app config (`IMAP_HOST`,
`IMAP_USERNAME`, and so on). That version is gone. One command moves its account
into the new database. Run it in a terminal (a window where you type commands):

```sh
email-mcp import-env path/to/.env
```

Without a path it reads `.env` in the current folder.

- It reads `IMAP_HOST`, `IMAP_PORT`, `IMAP_USERNAME`, `IMAP_PASSWORD`,
  `IMAP_TLS`, `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD` and
  `SMTP_TLS`, tests the connection, and saves one account at Mail 4. IMAP
  (Internet Message Access Protocol) is the protocol for reading mail. SMTP
  (Simple Mail Transfer Protocol) is the protocol for sending mail. TLS
  (Transport Layer Security) encrypts a connection. Mail 4 is the mail level
  4, named **Send** on the web pages. See [Permission
  levels](/manual/daily-use/permission-levels/).
- `SMTP_USERNAME` and `SMTP_PASSWORD` must be missing or the same as the IMAP
  ones. The new email-mcp uses one login for both servers. If they differ, use
  `email-mcp account add` instead.
- When `IMAP_USERNAME` is not an email address, give the address with
  `--email`.
- `IMAP_TLS=false` or `SMTP_TLS=false` works only for a server on your own
  computer (such as a local bridge: a program on your own computer that offers
  IMAP and SMTP servers for a mail service), and only with
  `--plaintext-i-understand`. To any other server it is refused.

If the file sets `USE_OAUTH=true`, the command refuses with exactly this
message. OAuth is a way to sign in without giving a program your password. An
app password is a separate password that you make in your mail account for one
program.

```text
This .env uses OAuth (USE_OAUTH=true). OAuth accounts are not supported until Google (M1) and Microsoft (M2) support ships. Connect this account with an app password instead.
```

After the import:

1. Delete the `.env` file. It holds your password in plain text.
2. Remove the old `env` block (the `IMAP_*` and `SMTP_*` variables) from the
   config of your AI app, and register the server again as shown on the page
   [Claude Code](/manual/getting-started/connect-ai-app/claude-code/), or on the
   page of your own AI app, which [Connect an
   AI app](/manual/getting-started/connect-ai-app/) lists.
