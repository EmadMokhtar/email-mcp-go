---
title: VS Code
description: Add email-mcp to VS Code, over stdio or over local HTTP.
weight: 40
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, VS Code starts email-mcp, an MCP (Model Context Protocol)
server, and can use its tools. The agent is the AI (artificial intelligence)
program that uses these tools. A tool is one action that the agent can call,
such as `search_messages`.

## Source and date

Sources, checked on 2026-10-29:

- <https://code.visualstudio.com/docs/agents/reference/mcp-configuration>
  (the `mcp.json` format)
- <https://code.visualstudio.com/api/extension-guides/ai/mcp> (the MCP
  features of VS Code)

## Add email-mcp (stdio)

VS Code starts email-mcp over stdio (standard input and output: the app starts
the program and talks to it directly, with no network).

VS Code reads `mcp.json`. Open your user file with the command **MCP: Open User
Configuration** (Command Palette, the search box for the commands of VS Code:
⇧⌘P on macOS, Ctrl+Shift+P on Windows and Linux). A workspace (the folder that
you opened in VS Code) has `.vscode/mcp.json`. The format uses a top-level
`servers` object, not `mcpServers`. The file is in JSON (JavaScript Object
Notation: a text format for data):

```json
{
  "servers": {
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
computer, and VS Code connects to it with a token (a secret that VS Code sends
to the server). To make the token and to start the server, follow steps 1 and 2
of [Local HTTP on the Claude Code page](
/manual/getting-started/connect-ai-app/claude-code/#local-http). Keep the server
running. If your master key is a file, also set `EMAIL_MCP_KEK` first, as that
page says.

Here the token is kept out of the file. An input variable makes VS Code ask for
the token when the server starts for the first time:

```json
{
  "inputs": [
    {
      "type": "promptString",
      "id": "email-mcp-token",
      "description": "email-mcp access token",
      "password": true
    }
  ],
  "servers": {
    "email": {
      "type": "http",
      "url": "http://127.0.0.1:8765/mcp",
      "headers": { "Authorization": "Bearer ${input:email-mcp-token}" }
    }
  }
}
```

## What you should see

Ask the agent: "List my email accounts." The agent lists the accounts that you
connected, with the level and the status of each. If the agent says that it has
no email tools, see [Your first
use](/manual/getting-started/first-use/#ask-the-first-question).

## Confirmation

The extension guide lists elicitation ("request input from the user") among the
features of VS Code. Elicitation is a way for a server to ask the person a
question through the app. So a send over stdio asks in band (the app itself
shows the confirmation form). The guide links to the 2025-06-18 specification
and does not name the protocol version that VS Code uses over HTTP
**[not verified]**. With a version older than 2026-07-28, HTTP gives the
approval link (a link to a web page where you approve or reject the send).

The page [How each AI app confirms](/manual/daily-use/app-confirmation/)
compares the five apps.

Next: [Your first use](/manual/getting-started/first-use/)
