---
title: Connect an AI app
description: Choose your AI app, and find the page that shows how to add email-mcp to it.
weight: 30
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, you know which page to follow for your AI (artificial
intelligence) app. An AI app (such as Claude Code) is the program that starts
email-mcp and calls its tools. The agent is the AI program that uses these
tools. A tool is one action that the agent can call, such as `search_messages`.
An AI app that starts and uses MCP (Model Context Protocol) servers is also
called a "harness". This part shows how to add email-mcp to Claude Code, Claude
Desktop, Cursor, VS Code and the Codex CLI.

## Before you start

Connect at least one account with `email-mcp setup` (see
[Connect your first account](/manual/getting-started/connect-account/)).

## Two facts for every app

- The config (the settings file) of the AI app never holds your mail password.
  email-mcp keeps the passwords in its own encrypted database.
- An app may need the setting `EMAIL_MCP_KEK`, and only when your master
  key is a file (see [The master key](/manual/reference/master-key/)).
  `EMAIL_MCP_KEK` is an environment variable: a named setting that a program
  reads from its surroundings. KEK means key encryption key. The master key is
  the key that protects your stored passwords. The email-mcp commands that you
  run yourself in a terminal, such as `email-mcp setup`, `email-mcp token
  create` and `email-mcp serve --http`, need the same value of `EMAIL_MCP_KEK`.
  An app needs one more setting, `EMAIL_MCP_DATA_DIR`, only when you use a data
  folder other than the default one, for example a new folder that you made
  after you lost the old key (see [Where the data
  lives](/manual/reference/data-folder/)). Set it the same way, in the config
  of the app and in your terminal.

## Choose your app

An app can start email-mcp over stdio (standard input and output: the AI app
starts the program and talks to it directly, with no network). Some apps can
also connect over local HTTP. HTTP (HyperText Transfer Protocol) is the protocol
that apps and browsers use to talk over a network. For local HTTP, email-mcp
runs a small server on your own computer, and the app connects to it with a
token (a secret that the app sends to the server).

| App | How it starts email-mcp | Page |
|---|---|---|
| Claude Code | The command `claude mcp add`, over stdio or local HTTP | [Claude Code](/manual/getting-started/connect-ai-app/claude-code/) |
| Claude Desktop | The config file `claude_desktop_config.json`, over stdio | [Claude Desktop](/manual/getting-started/connect-ai-app/claude-desktop/) |
| Cursor | The config file `mcp.json`, over stdio or local HTTP | [Cursor](/manual/getting-started/connect-ai-app/cursor/) |
| VS Code | The config file `mcp.json`, over stdio or local HTTP | [VS Code](/manual/getting-started/connect-ai-app/vscode/) |
| Codex CLI | The command `codex mcp add`, or the file `config.toml`, over stdio or local HTTP | [Codex CLI](/manual/getting-started/connect-ai-app/codex-cli/) |

Each page says where its facts come from, and when they were checked. Each page
also says how that app confirms a send. The page [How each AI app
confirms](/manual/daily-use/app-confirmation/) compares the five apps. Anything
marked **[not verified]** is something the official docs of that app did not say
on 2026-10-29. It is not a guess: test it before you depend on it.

## Another app

If your app is not in the table, check how it starts MCP servers. An app that
can start a program over stdio uses the command `email-mcp serve`. An app that
cannot uses local HTTP mode (see [Local HTTP
mode](/manual/reference/local-http-mode/)).

If an app cannot find `email-mcp`, give it the absolute path of the program as
the command. An absolute path starts at the root of your file system, such as
`/opt/homebrew/bin/email-mcp`. The command `which email-mcp` shows it (use
`where email-mcp` on Windows).

## Agents that control a browser

Some apps give the agent a browser-control tool (a tool that lets the agent use
your browser). Do not give such a tool access to the browser, or to the browser
profile (a separate set of browser settings and sign-ins), where you run
`email-mcp setup`, open the approval links (the links where you answer a
confirmation) and use the email-mcp web pages (the pages that email-mcp shows
in your browser). See the [Security](/manual/daily-use/security/) page.
