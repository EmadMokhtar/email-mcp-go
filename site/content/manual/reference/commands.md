---
title: Commands
description: Look up every email-mcp command and its flags.
weight: 10
code_areas:
  - cmd/email-mcp/main.go
  - cmd/email-mcp/console.go
  - cmd/email-mcp/serve.go
  - cmd/email-mcp/setup.go
  - cmd/email-mcp/account.go
  - cmd/email-mcp/connect.go
  - cmd/email-mcp/reconnect.go
  - cmd/email-mcp/token.go
  - cmd/email-mcp/outbox.go
  - cmd/email-mcp/importenv.go
  - cmd/email-mcp/key.go
  - cmd/email-mcp/doctor.go
---

After this page, you can find the command for a task, and you know its flags.
You run each command in a terminal (a window where you type commands).

## The commands

Write `email-mcp`, then the name of the command. The commands `serve` and
`setup` keep running until you press Ctrl-C (hold the Ctrl key and press C). The
other commands end when they are done.

| Command | What it does |
|---|---|
| `email-mcp serve` | Serves MCP (Model Context Protocol: an open standard that lets AI apps call tools that other programs offer) over stdio (standard input and output: the AI app starts the program and talks to it directly, with no network). This is what AI apps run. See [Connect an AI app](/manual/getting-started/connect-ai-app/). |
| `email-mcp serve --http 127.0.0.1:8765` | Serves MCP over local HTTP (HyperText Transfer Protocol: the protocol that apps and browsers use to talk over a network) at `/mcp`. See [Local HTTP mode](/manual/reference/local-http-mode/). |
| `email-mcp setup` | Signs your browser in to the local web pages (the pages that email-mcp shows in your browser, from your own computer), and serves them until Ctrl-C. See [Connect your first account](/manual/getting-started/connect-account/). |
| `email-mcp setup --revoke-sessions` | Signs every browser out, cancels unused sign-in links, and stops. |
| `email-mcp account add\|list\|reconnect\|remove\|test` | Manages accounts without a browser. `reconnect <id>` gives an account a new password or trusts its new certificate (the proof of identity that a server shows), and keeps the account. `remove <id>` asks first; add `--yes` without a terminal. See [Manage accounts](/manual/daily-use/manage-accounts/). |
| `email-mcp token create\|list\|revoke` | Manages the tokens (the secrets that an app sends to the local server) of local HTTP mode. `create` takes `--expires-in` and `--in-band-confirm`. See [Local HTTP mode](/manual/reference/local-http-mode/). |
| `email-mcp outbox list\|cancel` | Lists the sends that wait to go out. `cancel <id>` stops one, at any permission level (a number from 0 to 5 that says what the agent may do with the mail of an account). See [Activity and undo](/manual/daily-use/activity-and-undo/). |
| `email-mcp import-env [path]` | Imports the account of an old `.env` file (a text file of settings). See [Moving from the old email-mcp](/manual/reference/move-from-old-version/). |
| `email-mcp key rotate --to file:<path>\|keychain` | Changes the master key (the key that protects your stored passwords). See [The master key](/manual/reference/master-key/). |
| `email-mcp doctor` | Checks the install. See [Troubleshooting](/manual/daily-use/troubleshooting/). |
| `email-mcp version` | Prints the version. |
| `email-mcp help` | Prints the list of commands. `email-mcp -h` and `email-mcp --help` do the same. |

Every command that opens the data takes `--data-dir`. `email-mcp <command> -h`
lists the flags of a command.

## Flags

The table lists the flags that `email-mcp <command> -h` prints. The output of
`-h` writes each flag with one dash, such as `-http`. A flag works with one dash
or with two dashes. The table writes two dashes.

Every command in the table takes `--data-dir`, except `help` and `version`. It
names the data folder (the folder where email-mcp keeps its data). Without it,
email-mcp uses the folder in `EMAIL_MCP_DATA_DIR`, and then the folder of your
system. See [Where the data lives](/manual/reference/data-folder/). The flag
wins when both are set.

A password is never a flag, because other users of the computer can read the
flags of a running program. `account add` and `account reconnect` read the
password from standard input (the input of a program: what you type, or what a
pipe from another program gives it). The terminal asks for it, or, when
standard input is not a terminal, the first line of the input is the password.
An empty password for `account reconnect` keeps the stored one.

| Command | Flag | What it does |
|---|---|---|
| `serve` | `--http <address>` | Serves MCP over local HTTP on this loopback address (an address that only your own computer can reach), such as `127.0.0.1:8765`, instead of stdio. |
| `serve` | `--allow-origin <origin>` | A browser origin (the address of a web page, such as `http://localhost:5173`) that may call the HTTP server. Repeat the flag for more. It needs `--http`. |
| `setup` | `--revoke-sessions` | Signs every browser out, cancels unused sign-in links, and stops, without a new sign-in. |
| `account add` | `--email <address>` | The email address of the account. Required. |
| `account add` | `--imap-host <host>` | The IMAP (Internet Message Access Protocol: the protocol for reading mail) server, such as `imap.example.com`. Give both hosts, or neither to look them up. |
| `account add` | `--smtp-host <host>` | The SMTP (Simple Mail Transfer Protocol: the protocol for sending mail) server, such as `smtp.example.com`. |
| `account add` | `--imap-security <mode>` | The IMAP security: `tls`, `starttls` or `plain`. The default is `tls`. `tls` (Transport Layer Security) encrypts from the first byte. `starttls` starts plain and then switches encryption on. `plain` has no encryption. |
| `account add` | `--smtp-security <mode>` | The SMTP security: `tls`, `starttls` or `plain`. The default is `tls`. |
| `account add` | `--imap-port <number>` | The IMAP port (the number that names one service on an address). The default is 993 for `tls`, and 143 for `starttls` and `plain`. |
| `account add` | `--smtp-port <number>` | The SMTP port. The default is 465 for `tls`, 587 for `starttls`, and 25 for `plain`. |
| `account add` | `--username <name>` | The login name of both servers. The default is the email address. |
| `account add` | `--plaintext-i-understand` | Allows a connection without encryption to a server on this computer. |
| `account add` | `--trust-fingerprint <fingerprint>` | When standard input is not a terminal: trusts the certificate with this SHA-256 (Secure Hash Algorithm with 256 bits) fingerprint (a short code that identifies the certificate), if a server shows it in this run. Repeat the flag for the second server. |
| `account add` | `--accept-remote-certificate` | When standard input is not a terminal: also needed to trust, with `--trust-fingerprint`, the certificate of a server that is not on this computer. |
| `account reconnect` | `--trust-fingerprint <fingerprint>` | When standard input is not a terminal: accepts the certificate with this SHA-256 fingerprint, if a server shows it in this run. Repeat the flag for the second server. |
| `account reconnect` | `--accept-remote-certificate` | When standard input is not a terminal: also needed to trust, with `--trust-fingerprint`, the certificate of a server that is not on this computer and had no trusted certificate. |
| `account reconnect` | `--replace-pinned-certificate` | When standard input is not a terminal: also needed to replace, with `--trust-fingerprint`, the trusted certificate of a server whose certificate changed. |
| `account remove` | `--yes` | Removes without a question. Needed when standard input is not a terminal. |
| `token create` | `--name <name>` | A name for the token, such as the client that uses it. Required. |
| `token create` | `--scopes <list>` | What the token allows, as a comma-separated list of `email:read`, `email:organize`, `email:draft`, `email:send`, `email:delete`, or `all`. Required. |
| `token create` | `--expires-in <time>` | How long the token stays valid, such as `30d` or `12h`. The default is 90d, and the most is 3650d. |
| `token create` | `--in-band-confirm` | Lets the app of this token answer a confirmation in its own window, instead of the approval page. Any program with the token can then answer for you. |
| `import-env` | `--email <address>` | The email address of the account. The default is `IMAP_USERNAME`, when it is an address. |
| `import-env` | `--plaintext-i-understand` | Allows `IMAP_TLS=false` or `SMTP_TLS=false` to a server on this computer. |
| `import-env` | `--trust-fingerprint <fingerprint>` | When standard input is not a terminal: trusts the certificate with this SHA-256 fingerprint, if a server shows it in this run. Repeat the flag for the second server. |
| `import-env` | `--accept-remote-certificate` | When standard input is not a terminal: also needed to trust, with `--trust-fingerprint`, the certificate of a server that is not on this computer. |
| `key rotate` | `--to <key>` | The new master key: `file:<absolute path>` (a new file, made with mode 0600: only you can read and write it) or `keychain` (the password store of your operating system). |
| `key rotate` | `--yes` | Rotates without a question. Needed when standard input is not a terminal. |

The commands `account list`, `account test`, `token list`, `token revoke`,
`outbox list`, `outbox cancel` and `doctor` have no flag except `--data-dir`.
`version` and `help` have no flag.
