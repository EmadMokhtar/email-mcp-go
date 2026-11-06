---
title: Claude Code
description: Add email-mcp to Claude Code, over stdio or over local HTTP.
weight: 10
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, Claude Code starts email-mcp, an MCP (Model Context Protocol)
server, and can use its tools. A tool is one action that the agent can call,
such as `search_messages`.

## Source and date

Source: <https://code.claude.com/docs/en/mcp>, checked on 2026-10-29.

## Add email-mcp (stdio)

Run this in a terminal. The command adds email-mcp to Claude Code under the
name `email`. `--scope user` makes the server available in every project.
Everything after `--` is the command that Claude Code runs. `email-mcp serve`
talks MCP over stdio (standard input and output:
Claude Code starts the program and talks to it directly, with no network).

```sh
claude mcp add --scope user email -- email-mcp serve
```

Start Claude Code. The command `/mcp` shows whether the server is connected.

## If your master key is a file

Use this only when your master key (the key that protects your stored
passwords) is a file. By default, the master key is in the keychain (the
password store of your operating system), and you need nothing more.

With a key file as the master key, this command also sets the environment
variable (a named setting that a program reads from its surroundings)
`EMAIL_MCP_KEK` for the server. KEK means key encryption key. `--env` sets the
variable, and `--transport stdio` says that Claude Code starts the server as a
program:

```sh
claude mcp add --scope user --env EMAIL_MCP_KEK=file:/home/you/.email-mcp.key \
  --transport stdio email -- email-mcp serve
```

Keep another flag, such as `--transport stdio`, between `--env` and the server
name. Otherwise Claude Code reads the name as another variable. As before, the
command `/mcp` shows whether the server is connected.

## Local HTTP

Use stdio when the app supports it. Use local HTTP only for an app that cannot
start a program (see Local HTTP mode). HTTP (HyperText Transfer Protocol) is the
protocol that apps and browsers use to talk over a network. With local HTTP,
email-mcp runs a small server on your own computer, and Claude Code connects to
it with a token (a secret that Claude Code sends to the server). This command
adds the server. `--transport http` says that Claude Code connects over HTTP.
`--header` adds a header (an extra line of information) with your token to
every request. Replace `<token>` with the token that `email-mcp token create`
printed:

```sh
claude mcp add --transport http --scope user email \
  http://127.0.0.1:8765/mcp --header "Authorization: Bearer <token>"
```

To get the form in Claude Code instead of the approval link (a link to a web
page where you approve or reject the send), make the token with
`email-mcp token create --in-band-confirm`.

With `--header`, Claude Code stores the token **in plain text** in
`~/.claude.json`. To keep it out of that file, use a
[`headersHelper`](https://code.claude.com/docs/en/mcp): a command that Claude
Code runs at each connection, and that prints the headers as JSON (JavaScript
Object Notation: a text format for data). The Claude Code docs describe it in
the section "Use dynamic headers for custom authentication". This one reads
the token from the environment variable `EMAIL_MCP_TOKEN`, which you set in
the shell that starts Claude Code (for example from your password manager):

```sh
claude mcp add-json --scope user email '{"type":"http","url":"http://127.0.0.1:8765/mcp","headersHelper":"printf '"'"'{\"Authorization\":\"Bearer %s\"}'"'"' \"$EMAIL_MCP_TOKEN\""}'
```

`claude mcp list` then shows the server as connected when `EMAIL_MCP_TOKEN` is
set, and as refused (HTTP 401) when it is not.

## Confirmation

- Claude Code shows elicitation forms in a dialog. Elicitation is a way for a
  server to ask the person a question through the app. The docs say that it
  declares form and URL (web address) elicitation on connections that use
  protocol 2026-07-28, and on the stdio and HTTP servers of your own MCP config
  on older protocol versions (Claude Code v2.1.287 or later). A protocol
  version is a date. It names the version of the MCP rules that the app and the
  server use.
- In a non-interactive session (a run with no person at the keyboard, such as
  `claude -p`), Claude Code does not show the form. It asks the server to
  cancel instead, so the send is declined.
- Do not add an `Elicitation` hook (a script that Claude Code runs when an
  event happens) that answers the form automatically. It would turn the
  confirmation off.

## Recommended deny rules

Also add the recommended deny rules (see the page Recommended Claude Code deny
rules).
