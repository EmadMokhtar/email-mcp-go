---
title: Cursor
description: Add email-mcp to Cursor, over stdio or over local HTTP.
weight: 30
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, Cursor starts email-mcp, an MCP (Model Context Protocol)
server, and can use its tools. A tool is one action that the agent can call,
such as `search_messages`.

## Source and date

Source: <https://cursor.com/docs/context/mcp>, checked on 2026-10-29.

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
start a program (see Local HTTP mode). HTTP (HyperText Transfer Protocol) is the
protocol that apps and browsers use to talk over a network. With local HTTP,
email-mcp runs a small server on your own computer, and Cursor connects to it
with a token (a secret that Cursor sends to the server).

The docs show a remote server with `url` and a `headers` object. A token written
into `headers` is stored **in plain text** in `mcp.json`. Cursor replaces
`${env:NAME}` with an environment variable in `url` and `headers`, so keep the
token in a variable instead, set in the shell or the system environment that
starts Cursor:

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

## Confirmation

The docs list elicitation (a way for a server to ask the person a question
through the app) as supported, so a send over stdio asks in band (the app
itself shows the confirmation form). They do not say whether Cursor supports
form mode, URL (web address) mode or both, or which protocol version it uses
over HTTP **[not verified]**.
