---
title: Codex CLI
description: Add email-mcp to the Codex CLI, over stdio or over local HTTP.
weight: 50
code_areas:
  - cmd/email-mcp/serve.go
  - internal/mcpserver/elicit.go
---

After this page, the Codex CLI (the Codex program that you use in a terminal)
starts email-mcp, an MCP (Model Context Protocol) server, and can use its
tools. A tool is one action that the agent can call, such as `search_messages`.

## Source and date

Source: <https://developers.openai.com/codex/mcp> (it redirects to
<https://learn.chatgpt.com/docs/extend/mcp?surface=cli>), checked on
2026-10-29.

## Add email-mcp (stdio)

Codex starts email-mcp over stdio (standard input and output: the app starts
the program and talks to it directly, with no network).

Add the server with the CLI (command line interface: a program that you use by
typing commands). The name `email` is the name of the server. Everything after
`--` is the command that Codex runs:

```sh
codex mcp add email -- email-mcp serve
```

Or edit `~/.codex/config.toml` yourself. The file is in TOML (Tom's Obvious
Minimal Language: a text format for settings):

```toml
[mcp_servers.email]
command = "email-mcp"
args = ["serve"]

# Only with a key file as the master key:
[mcp_servers.email.env]
EMAIL_MCP_KEK = "file:/home/you/.email-mcp.key"
```

A project can also have `.codex/config.toml`, but Codex reads it only in
trusted projects.

## If your master key is a file

Use this only when your master key (the key that protects your stored
passwords) is a file. By default, the master key is in the keychain (the
password store of your operating system), and you need nothing more.

With a key file as the master key, this command sets the environment variable
(a named setting that a program reads from its surroundings) `EMAIL_MCP_KEK`
for the server. KEK means key encryption key:

```sh
codex mcp add email --env EMAIL_MCP_KEK=file:/home/you/.email-mcp.key \
  -- email-mcp serve
```

In the file `config.toml`, the section `[mcp_servers.email.env]` of the block
above does the same.

## Local HTTP

Use stdio when the app supports it. Use local HTTP only for an app that cannot
start a program (see Local HTTP mode). HTTP (HyperText Transfer Protocol) is the
protocol that apps and browsers use to talk over a network. With local HTTP,
email-mcp runs a small server on your own computer, and Codex connects to it
with a token (a secret that Codex sends to the server).
To make the token and to start the
server, follow steps 1 and 2 of
[Local HTTP on the Claude Code page](
/manual/getting-started/connect-ai-app/claude-code/#local-http). Keep the server
running. If your master key is a file, also set `EMAIL_MCP_KEK` first, as that
page says.

The docs list `url` and `bearer_token_env_var` (the name of an environment
variable that holds the token):

```toml
[mcp_servers.email]
url = "http://127.0.0.1:8765/mcp"
bearer_token_env_var = "EMAIL_MCP_TOKEN"
```

## Confirmation

**[not verified]**. The docs do not mention elicitation (a way for a server to
ask the person a question through the app). Without it, every send that needs a
confirmation returns the approval link (a link to a web page where you approve
or reject the send). Codex has its own approval setting for
MCP tools (`default_tools_approval_mode`, and `tools.<tool>.approval_mode` per
tool). That setting decides whether Codex asks you before it calls a tool. It
does not replace the confirmation of email-mcp, and it cannot turn it off.
