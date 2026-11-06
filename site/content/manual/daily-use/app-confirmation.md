---
title: How each AI app confirms
description: See which AI apps can show the confirmation form, and which ones use the approval link.
weight: 50
code_areas:
  - internal/mcpserver/elicit.go
  - internal/httpserve/
  - internal/confirm/
---

After this page, you know which AI (artificial intelligence) apps show the
confirmation form to you, and which ones give you the approval link. The page
[Confirmations](/manual/daily-use/confirmations/) explains what a confirmation
is. An AI app (such as Claude Code) is the program that starts email-mcp and
calls its tools.

## In band or link

A send or a move to the Trash may need your confirmation (see
[Confirmations](/manual/daily-use/confirmations/)). email-mcp asks in one of two
ways:

- **In band:** the app shows a form with one "Confirm" box. This needs an app
  that supports MCP (Model Context Protocol) elicitation (a way for a server to
  ask the person a question through the app).
  - Over **stdio** (standard input and output: the app starts the program and
    talks to it directly, with no network), this works with every MCP protocol
    version. A protocol version is a date. It names the version of the MCP rules
    that the app and the server use.
  - Over **HTTP** (HyperText Transfer Protocol: the protocol that apps and
    browsers use to talk over a network), this needs two things. The app must
    use the MCP protocol version 2026-07-28 or newer: the HTTP server is
    stateless (it keeps no memory between requests), so it cannot ask an older
    app in the middle of a call. And its token (a secret that the app sends to
    the server) must be made with `email-mcp token create --in-band-confirm`:
    without the flag, the server does not accept the answer of the app.
- **Approval link:** in every other case, the tool answers `pending_approval`
  with a link to the **Waiting for you** page. Open it in the browser that
  `email-mcp setup` signed in.

Use stdio when the app supports it. Use local HTTP only for an app that cannot
start a program.

## What each app can do

The table below says what each app can do. Over local HTTP, a token without
`--in-band-confirm` always gets the approval link. Each app page names the
source of its facts and the date when they were checked. Anything marked **[not
verified]** is something the official docs of that app did not say on
2026-10-29. It is not a guess: test it before you depend on it.

| App | Over stdio | Over local HTTP |
|---|---|---|
| [Claude Code](/manual/getting-started/connect-ai-app/claude-code/) | In band (elicitation is documented) | In band on its v2 runtime, which uses protocol 2026-07-28. Approval link on the older v1 runtime. |
| [Claude Desktop](/manual/getting-started/connect-ai-app/claude-desktop/) | **[not verified]**: the docs do not say that it supports elicitation. Without it you get the approval link. | Not used: the documented config starts local servers over stdio. |
| [Cursor](/manual/getting-started/connect-ai-app/cursor/) | In band (its docs list elicitation as supported) | **[not verified]**: the docs do not name the protocol version. Approval link if it is older than 2026-07-28. |
| [VS Code](/manual/getting-started/connect-ai-app/vscode/) | In band (its docs list elicitation as supported) | **[not verified]**: the docs do not name the protocol version. Approval link if it is older than 2026-07-28. |
| [Codex CLI](/manual/getting-started/connect-ai-app/codex-cli/) | **[not verified]**: the docs do not mention elicitation. Without it you get the approval link. | **[not verified]**, as for stdio. |

"v1 runtime" and "v2 runtime" are the two MCP clients inside Claude Code (see
[MCP client runtimes](https://code.claude.com/docs/en/mcp#mcp-client-runtimes)).
The v1 runtime is built on the MCP TypeScript SDK (software development kit: a
library for programmers) 1.x. The v2 runtime is the same code on the TypeScript
SDK 2.0, which adds protocol 2026-07-28. The docs say that Claude Code uses the
v2 runtime from v2.1.232 in sessions that fetch feature flags, and by default
from v2.1.274 in sessions that do not. The environment variable
`MCP_SDK_GENERATION` (`v1` or `v2`) chooses the runtime, and
`MCP_PROTOCOL_NEGOTIATION=legacy` keeps Claude Code on the older protocol. These
are facts of the Claude Code documentation, checked on 2026-10-29.

## Trust in the app

The page [Confirmations](/manual/daily-use/confirmations/#trust-in-the-ai-app)
explains why a confirmation is only as good as the AI app. Only use apps that
show the form to you. `--in-band-confirm` is your statement that you trust the
app of that token. A token in a config file in plain text is a reason to leave
the flag off.
