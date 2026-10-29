# Using email-mcp with AI apps

An AI app that starts and uses MCP servers is also called a "harness". This
page shows how to add email-mcp to Claude Code, Claude Desktop, Cursor, VS Code
and the Codex CLI.

Before you start, connect at least one account with `email-mcp setup` (see the
[README](../README.md#quick-start)).

The config of the AI app never holds your mail password. email-mcp keeps the
passwords in its own encrypted database. The only setting an app may need is
`EMAIL_MCP_KEK`, and only when your master key is a file (see
[The master key](../README.md#the-master-key)).

Each section says where its facts come from, and when they were checked.
Anything marked **[not verified]** is something the official docs of that app
did not say on 2026-10-29. It is not a guess: test it before you depend on it.

## How each app confirms a send

A send or a move to the Trash may need your confirmation (see
[Confirmations](../README.md#confirmations-and-full-autonomy)). email-mcp asks
in one of two ways:

- **In band:** the app shows a form with one "Confirm" box. This needs an app
  that supports MCP elicitation (a way for a server to ask the person a
  question through the app).
  - Over **stdio**, this works with every MCP protocol version.
  - Over **HTTP**, this works only when the app uses the MCP protocol version
    2026-07-28 or newer. The HTTP server is stateless, so it cannot ask an older
    app in the middle of a call.
- **Approval link:** in every other case, the tool answers `pending_approval`
  with a link to the Approvals page. Open it in the browser that
  `email-mcp setup` signed in.

Use stdio when the app supports it. Use local HTTP only for an app that cannot
start a program.

| App | Over stdio | Over local HTTP |
|---|---|---|
| Claude Code | In band (elicitation is documented) | In band on its v2 runtime, which uses protocol 2026-07-28. Approval link on the older v1 runtime. |
| Claude Desktop | **[not verified]**: the docs do not say that it supports elicitation. Without it you get the approval link. | Not used: the documented config starts local servers over stdio. |
| Cursor | In band (its docs list elicitation as supported) | **[not verified]**: the docs do not name the protocol version. Approval link if it is older than 2026-07-28. |
| VS Code | In band (its docs list elicitation as supported) | **[not verified]**: the docs do not name the protocol version. Approval link if it is older than 2026-07-28. |
| Codex CLI | **[not verified]**: the docs do not mention elicitation. Without it you get the approval link. | **[not verified]**, as for stdio. |

"v1 runtime" and "v2 runtime" are the two MCP clients inside Claude Code
(see [MCP client runtimes](https://code.claude.com/docs/en/mcp#mcp-client-runtimes)).
The v1 runtime is built on the MCP TypeScript SDK 1.x. The v2 runtime is the
same code on the TypeScript SDK 2.0, which adds protocol 2026-07-28. The docs
say that Claude Code uses the v2 runtime from v2.1.232 in sessions that fetch
feature flags, and by default from v2.1.274 in sessions that do not. The
environment variable `MCP_SDK_GENERATION` (`v1` or `v2`) chooses the runtime,
and `MCP_PROTOCOL_NEGOTIATION=legacy` keeps Claude Code on the older protocol.

An app that answers the form by itself defeats the confirmation. Only use apps
that show the form to you.

## Claude Code

Source: <https://code.claude.com/docs/en/mcp>, checked on 2026-10-29.

```sh
claude mcp add --scope user email -- email-mcp serve
```

`--scope user` makes the server available in every project. Everything after
`--` is the command that Claude Code runs.

With a key file as the master key:

```sh
claude mcp add --scope user --env EMAIL_MCP_KEK=file:/home/you/.email-mcp.key \
  --transport stdio email -- email-mcp serve
```

Keep another flag, such as `--transport stdio`, between `--env` and the server
name. Otherwise Claude Code reads the name as another variable.

Confirmation:

- Claude Code shows elicitation forms in a dialog. The docs say that it
  declares form and URL elicitation on connections that use protocol
  2026-07-28, and on the stdio and HTTP servers of your own MCP config on older
  protocol versions (Claude Code v2.1.287 or later).
- In a non-interactive session (`claude -p`), Claude Code does not show the
  form. It asks the server to cancel instead, so the send is declined.
- Do not add an `Elicitation` hook that answers the form automatically. It
  would turn the confirmation off.

Local HTTP (see [Local HTTP mode](../README.md#local-http-mode)):

```sh
claude mcp add --transport http --scope user email \
  http://127.0.0.1:8765/mcp --header "Authorization: Bearer <token>"
```

With `--header`, Claude Code stores the token **in plain text** in
`~/.claude.json`. To keep it out of that file, use a
[`headersHelper`](https://code.claude.com/docs/en/mcp#use-dynamic-headers-for-custom-authentication):
a command that Claude Code runs at each connection, and that prints the headers
as JSON. This one reads the token from the environment variable
`EMAIL_MCP_TOKEN`, which you set in the shell that starts Claude Code (for
example from your password manager):

```sh
claude mcp add-json --scope user email '{"type":"http","url":"http://127.0.0.1:8765/mcp","headersHelper":"printf '"'"'{\"Authorization\":\"Bearer %s\"}'"'"' \"$EMAIL_MCP_TOKEN\""}'
```

`claude mcp list` then shows the server as connected when `EMAIL_MCP_TOKEN` is
set, and as refused (HTTP 401) when it is not.

Also add the [recommended deny rules](../README.md#recommended-claude-code-deny-rules).

## Claude Desktop

Source: <https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers>,
checked on 2026-10-29.

Open **Settings → Developer → Edit Config**. That opens the config file:

- macOS: `~/Library/Application Support/Claude/claude_desktop_config.json`
- Windows: `%APPDATA%\Claude\claude_desktop_config.json`

Add email-mcp under `mcpServers`. Use the absolute path of the binary (find it
with `which email-mcp`, or `where email-mcp` on Windows). The docs say that the
paths must be absolute.

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

With a key file as the master key, add an `env` block:

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

Restart Claude Desktop after you change the file. The log of the server is in
`~/Library/Logs/Claude/` (macOS) or `%APPDATA%\Claude\logs` (Windows).

Confirmation: **[not verified]**. The docs do not say whether Claude Desktop
supports elicitation. If it does not, every send that needs a confirmation
returns the approval link.

## Cursor

Source: <https://cursor.com/docs/context/mcp>, checked on 2026-10-29.

The config file is `~/.cursor/mcp.json` for every project, or
`.cursor/mcp.json` in a project for that project only. The docs list `type`
as a required field for a stdio server, with the value `"stdio"`.

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

With a key file as the master key, add
`"env": { "EMAIL_MCP_KEK": "file:/Users/you/.email-mcp.key" }`.

Do not put the email-mcp server in a project's `.cursor/mcp.json` that you
share: other people's computers have neither your accounts nor your key.

Local HTTP: the docs show a remote server with `url` and a `headers` object. A
token written into `headers` is stored **in plain text** in `mcp.json`. Cursor
replaces `${env:NAME}` with an environment variable in `url` and `headers`, so
keep the token in a variable instead, set in the shell or the system
environment that starts Cursor:

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

Confirmation: the docs list elicitation as supported, so a send over stdio
asks in band. They do not say whether Cursor supports form mode, URL mode or
both, or which protocol version it uses over HTTP **[not verified]**.

## VS Code

Sources, checked on 2026-10-29:

- <https://code.visualstudio.com/docs/agents/reference/mcp-configuration>
  (the `mcp.json` format)
- <https://code.visualstudio.com/api/extension-guides/ai/mcp> (the MCP
  features of VS Code)

VS Code reads `mcp.json`. Open your user file with the command
**MCP: Open User Configuration** (Command Palette: ⇧⌘P on macOS,
Ctrl+Shift+P on Windows and Linux). A workspace has `.vscode/mcp.json`. The
format uses a top-level `servers` object, not `mcpServers`.

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

With a key file as the master key, add
`"env": { "EMAIL_MCP_KEK": "file:/Users/you/.email-mcp.key" }`.

Local HTTP, with the token kept out of the file. An input variable makes VS
Code ask for the token when the server starts for the first time:

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

Confirmation: the extension guide lists elicitation ("request input from the
user") among the features of VS Code, so a send over stdio asks in band. The
guide links to the 2025-06-18 specification and does not name the protocol
version that VS Code uses over HTTP **[not verified]**. With a version older
than 2026-07-28, HTTP gives the approval link.

## Codex CLI

Source: <https://developers.openai.com/codex/mcp> (it redirects to
<https://learn.chatgpt.com/docs/extend/mcp?surface=cli>), checked on
2026-10-29.

Add the server with the CLI:

```sh
codex mcp add email -- email-mcp serve
```

With a key file as the master key:

```sh
codex mcp add email --env EMAIL_MCP_KEK=file:/home/you/.email-mcp.key -- email-mcp serve
```

Or edit `~/.codex/config.toml` yourself:

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

Local HTTP: the docs list `url` and `bearer_token_env_var` (the name of an
environment variable that holds the token):

```toml
[mcp_servers.email]
url = "http://127.0.0.1:8765/mcp"
bearer_token_env_var = "EMAIL_MCP_TOKEN"
```

Confirmation: **[not verified]**. The docs do not mention elicitation. Without
it, every send that needs a confirmation returns the approval link. Codex has
its own approval setting for MCP tools (`default_tools_approval_mode`, and
`tools.<tool>.approval_mode` per tool). That setting decides whether Codex asks
you before it calls a tool. It does not replace the confirmation of email-mcp,
and it cannot turn it off.
