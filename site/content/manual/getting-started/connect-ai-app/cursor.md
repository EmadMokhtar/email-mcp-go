---
title: Cursor
description: Add email-mcp to Cursor, over stdio or over local HTTP.
weight: 30
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, Cursor starts email-mcp, an MCP (Model Context Protocol)
server, and can use its tools. The agent is the AI (artificial intelligence)
program that uses these tools. A tool is one action that the agent can call,
such as `search_messages`.

## Source and date

Source: <https://cursor.com/docs/context/mcp>, checked on 2026-10-29. The text
about `${env:NAME}` in `url` and `headers` is on the same page, checked on
2026-11-07.

## Add email-mcp (stdio)

Cursor starts email-mcp over stdio (standard input and output: the app starts
the program and talks to it directly, with no network).

The config file (the file that holds the settings of the app) is
`~/.cursor/mcp.json` for every project, or `.cursor/mcp.json` in a project for
that project only. The docs list `type` as a required field for a stdio server,
with the value `"stdio"`. The file is in JSON (JavaScript Object Notation: a
text format for data):

```json
{
  "mcpServers": {
    "email": {
      "type": "stdio",
      "command": "/opt/homebrew/bin/email-mcp",
      "args": ["serve"]
    }
  }
}
```

The path in this example is only an example: use the absolute path of your own
program (the full path of the program file). Find it with `which email-mcp`, or
`where email-mcp` on Windows.

Do not put the email-mcp server in a project's `.cursor/mcp.json` that you
share: other people's computers have neither your accounts nor your key.

## If your master key is a file

Use this only when your master key (the key that protects your stored
passwords) is a file. By default, the master key is in the keychain (the
password store of your operating system), and you need nothing more.

With a key file as the master key, add
`"env": { "EMAIL_MCP_KEK": "file:/Users/you/.email-mcp.key" }`. It sets the
environment variable (a named setting that a program reads from its
surroundings) `EMAIL_MCP_KEK`. KEK means key encryption key.

## Local HTTP

Use stdio when the app supports it. Use local HTTP only for an app that cannot
start a program (see [Local HTTP mode](/manual/reference/local-http-mode/)).
HTTP (HyperText Transfer Protocol) is the protocol that apps and browsers use to
talk over a network. With local HTTP, email-mcp runs a small server on your own
computer, and Cursor connects to it with a token (a secret that Cursor sends to
the server). To make the token and to start the server, follow steps 1 and 2 of
[Local HTTP on the Claude Code page](
/manual/getting-started/connect-ai-app/claude-code/#local-http). Keep the server
running. If your master key is a file, also set `EMAIL_MCP_KEK` first, as that
page says.

The docs show a remote server with `url` and a `headers` object. A token written
into `headers` is stored **in plain text** in `mcp.json`. Cursor replaces
`${env:NAME}` with an environment variable in `url` and `headers`, so keep the
token in a variable instead, set in the shell or the system environment that
starts Cursor. The variable here is `EMAIL_MCP_TOKEN`:

```json
{
  "mcpServers": {
    "email": {
      "url": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer ${env:EMAIL_MCP_TOKEN}" }
    }
  }
}
```

Cursor reads the token from the environment variable `EMAIL_MCP_TOKEN`, which
you set in the shell that starts Cursor (for example from your password
manager). Set it before you start Cursor. For example, on macOS and Linux,
`export EMAIL_MCP_TOKEN=<token>` sets it for that terminal. Replace `<token>`
with the token of step 1, the text that starts with `emcp_`.

Cursor is a graphical app (an app with windows). A Cursor that you start from
the Dock does not see a variable that you set only in a terminal. So start
Cursor from that same terminal, after the `export`. Or set the variable in the
system environment, as said above. If Cursor is already running, close it and
start it again after you set the variable.

Without a valid token, the server answers HTTP 401 (the answer "not
authorized").

## What you should see

Ask the agent: "List my email accounts." The agent lists the accounts that you
connected, with the level and the status of each. If the agent says that it has
no email tools, see [Your first
use](/manual/getting-started/first-use/#ask-the-first-question).

## Confirmation

The docs list elicitation (a way for a server to ask the person a question
through the app) as supported, so a send over stdio asks in band (the app
itself shows the confirmation form). They do not say whether Cursor supports
form mode, URL (web address) mode or both, or which protocol version it uses
over HTTP **[not verified]**.

The page [How each AI app confirms](/manual/daily-use/app-confirmation/)
compares the five apps.

Next: [Your first use](/manual/getting-started/first-use/)
