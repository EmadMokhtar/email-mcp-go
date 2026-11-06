---
title: Glossary
description: Look up the terms of this manual, each in one line.
weight: 90
---

After this page, you can look up a term of this manual. The terms are in
alphabetical order.

- **Action:** what email-mcp or you did, such as a send. The Activity page shows
  actions and requests. See [Activity and
  undo](/manual/daily-use/activity-and-undo/).
- **Activity log:** the list of requests and actions that the Activity page
  shows. email-mcp keeps each row for 90 days.
- **Agent:** the AI (artificial intelligence) program that uses the tools of
  email-mcp.
- **AI app (harness) / MCP server:** email-mcp is the server. The AI app (such
  as Claude Code) is the client that starts it and calls its tools.
- **App password:** a separate password for one program, made in the security
  settings of your mail account. See [App
  passwords](/manual/getting-started/connect-account/#app-passwords).
- **Approval link:** a link to a web page where you approve or reject a send or
  a move to the Trash.
- **Base64:** a way to write bytes with letters, digits, `+`, `/` and `=`. A key
  file holds the master key as base64 text.
- **Certificate pinning:** storing the fingerprint of a server certificate and
  refusing any other certificate later.
- **Certificate:** the proof of identity that a server shows. email-mcp checks
  it, and it can pin it. See [A local
  bridge](/manual/getting-started/connect-account/#a-local-bridge).
- **Checksum:** a short code that a function makes from the content of a file.
  Another content gives another code.
- **Confirmation:** your "yes" before the agent can send mail or move mail to
  the Trash. See [Confirmations](/manual/daily-use/confirmations/).
- **cosign / Sigstore:** tools and a public service that sign files with
  short-lived certificates instead of long-lived keys.
- **Data folder:** the folder where email-mcp keeps its data. See [Where the
  data lives](/manual/reference/data-folder/).
- **Deny rule:** a Claude Code permission rule that blocks a tool call.
- **Draft:** a message that is saved but not sent.
- **Elicitation:** a way for an MCP server to ask the person a question through
  the AI app.
- **Environment variable:** a named setting that a program reads from its
  surroundings, such as `EMAIL_MCP_KEK`.
- **File mode:** the number that says who may read, write or open a file or a
  folder. Mode 0600 means that only you can read and write a file. Mode 0700
  means that only you can open a folder.
- **Fingerprint:** a short, unique code of a certificate (its SHA-256 checksum).
  You compare it before you trust a certificate.
- **Full autonomy:** both confirmation switches are off. The agent acts without
  asking.
- **Gatekeeper:** the program check of macOS. It looks at a program that has the
  quarantine mark.
- **Hosted mode:** one server that many people use. It is planned and not
  available.
- **Hosts file:** the file where your computer keeps names and their addresses:
  `/etc/hosts` on macOS and Linux, and
  `C:\Windows\System32\drivers\etc\hosts` on Windows.
- **IMAP / SMTP:** the protocols for reading mail and for sending mail.
- **In band:** the AI app shows the confirmation form itself, so you answer in
  the app. The other way is the approval link. See
  [Confirmations](/manual/daily-use/confirmations/).
- **Key file:** a text file that holds the master key, as base64 text of exactly
  32 bytes. See [The master key](/manual/reference/master-key/).
- **Keychain:** the password store of your operating system (macOS Keychain,
  Linux Secret Service, Windows Credential Manager).
- **Level (permission level):** a number from 0 to 5 that says what the agent
  may do with the mail of an account. The web page names the levels Off, Read,
  Organize, Draft, Send and Delete. Level 5, Delete, moves mail to the Trash. It
  never deletes mail for good. See [Permission
  levels](/manual/daily-use/permission-levels/).
- **Local bridge:** a program on your own computer that offers IMAP and SMTP
  servers for a mail service.
- **Local mode:** how email-mcp runs today: on your own computer, for you. See
  [Security](/manual/daily-use/security/).
- **Local HTTP mode:** email-mcp runs a small server on your own computer, and
  an AI app that cannot start a program connects to it with a token. See [Local
  HTTP mode](/manual/reference/local-http-mode/).
- **Loopback address:** an address, such as 127.0.0.1, that only the same
  computer can reach.
- **Master key (KEK, key encryption key):** the key that encrypts the keys that
  encrypt your passwords.
- **MCP (Model Context Protocol):** an open standard that lets AI apps call
  tools that other programs offer.
- **OAuth:** a way to sign in without giving a program your password. This
  version does not support it.
- **Outbox:** the queue of messages that wait to go out.
- **PATH:** the list of folders where the shell looks for programs.
- **Provenance attestation:** a signed record of which workflow built which file
  from which commit.
- **Punycode:** the ASCII spelling of a domain name with characters outside
  ASCII. It starts with `xn--`.
- **Quarantine:** a mark that macOS puts on a downloaded program. Gatekeeper
  looks at a program that has it.
- **Request:** one tool call of an agent. The Activity page shows a request for
  every tool call, also a refused one.
- **Rotate (a key):** replace a key with a new one. `email-mcp key rotate`
  replaces the master key.
- **Sandbox:** a limit, enforced by the operating system, on what shell commands
  may read and write.
- **SBOM (software bill of materials):** a list of every module inside a binary.
- **Scope:** one permission of a token, such as `email:send`. Scopes add up:
  each scope also allows the levels under it.
- **stdio:** standard input and output. The AI app starts the program and talks
  to it directly, with no network.
- **Terminal:** a window where you type commands.
- **TLS / STARTTLS:** encryption of a connection. TLS encrypts from the first
  byte. STARTTLS starts plain and then switches encryption on.
- **Token:** a secret that an AI app sends to the local HTTP server. It stands
  for a set of scopes.
- **Tool:** one action that the agent can call, such as `search_messages`.
- **Trash:** the folder of deleted mail. Level 5 moves mail there.
- **Undo window:** the time a send waits in the outbox before it goes out. It
  can be cancelled during this time, by the agent, on the Activity page, or with
  `email-mcp outbox cancel`.
- **WAL (write-ahead log):** a SQLite mode that lets several programs use one
  database file safely.
- **Web pages:** the pages that email-mcp shows in your browser, from your own
  computer, at `email-mcp.localhost`. See [The web
  pages](/manual/daily-use/web-pages/).
