---
title: Claude Desktop
description: Add email-mcp to the config file of Claude Desktop.
weight: 20
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, Claude Desktop starts email-mcp, an MCP (Model Context
Protocol) server, and can use its tools. A tool is one action that the agent can
call, such as `search_messages`.

## Source and date

Source:
<https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers>,
checked on 2026-10-29.

## Add email-mcp (stdio)

Claude Desktop starts email-mcp over stdio (standard input and output: the app
starts the program and talks to it directly, with no network).

Open **Settings → Developer → Edit Config**. That opens the config file (the
file that holds the settings of the app):

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

Add email-mcp under `mcpServers`. Use the absolute path of the binary (the full
path of the program file; find it with `which email-mcp`, or `where email-mcp`
on Windows). The docs say that the paths must be absolute. The file is in JSON
(JavaScript Object Notation: a text format for data):

```json
{
  "mcpServers": {
    "email": {
      "command": "/opt/homebrew/bin/email-mcp",
      "args": ["serve"]
    }
  }
}
```

Restart Claude Desktop after you change the file. The log of the server is in
`~/Library/Logs/Claude/` (macOS) or `%APPDATA%\Claude\logs` (Windows).

## If your master key is a file

Use this only when your master key (the key that protects your stored
passwords) is a file. By default, the master key is in the keychain (the
password store of your operating system), and you need nothing more.

With a key file as the master key, add an `env` block. It sets the environment
variable (a named setting that a program reads from its surroundings)
`EMAIL_MCP_KEK` for the server. KEK means key encryption key. Restart Claude
Desktop after you change the file.

```json
{
  "mcpServers": {
    "email": {
      "command": "/opt/homebrew/bin/email-mcp",
      "args": ["serve"],
      "env": {
        "EMAIL_MCP_KEK": "file:/Users/you/.email-mcp.key"
      }
    }
  }
}
```

## Confirmation

**[not verified]**. The docs do not say whether Claude Desktop supports
elicitation (a way for a server to ask the person a question through the app).
If it does not, every send that needs a confirmation returns the approval link
(a link to a web page where you approve or reject the send).

The page [How each AI app confirms](/manual/daily-use/app-confirmation/)
compares the five apps.
