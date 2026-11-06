---
title: Local HTTP mode
description: Connect an AI app that cannot start a program, with a token and a local server.
weight: 40
code_areas:
  - cmd/email-mcp/serve.go
  - cmd/email-mcp/token.go
  - internal/policy/policy.go
  - internal/tokens/
  - internal/httpserve/
  - internal/web/connections.go
  - internal/web/templates/connections.html
---

After this page, you can connect an AI app that cannot start a program. Use
this only for an AI app that cannot start a program over stdio (standard input
and output: the AI app starts the program and talks to it directly, with no
network). An app that can start a program does not need this page. See
[Connect an AI app](/manual/getting-started/connect-ai-app/).

With local HTTP, email-mcp runs a small server on your own computer. HTTP
(HyperText Transfer Protocol) is the protocol that apps and browsers use to talk
over a network. The app connects to the server with a token: a secret that the
app sends with each request. The page of each app has the config for local
HTTP: [Claude Code](/manual/getting-started/connect-ai-app/claude-code/),
[Cursor](/manual/getting-started/connect-ai-app/cursor/), [VS
Code](/manual/getting-started/connect-ai-app/vscode/) and [Codex
CLI](/manual/getting-started/connect-ai-app/codex-cli/). The page [Claude
Desktop](/manual/getting-started/connect-ai-app/claude-desktop/) has no config
for local HTTP: the config that the MCP (Model Context Protocol) docs show
starts local servers over stdio. The MCP docs are the docs that the Claude
Desktop page names as its source, checked on 2026-10-29.

## Make a token and start the server

Do these steps in order. If your master key (the key that protects your stored
passwords) is a file, set the environment variable `EMAIL_MCP_KEK` in each
terminal where you run step 1 or step 2, with the same value as in the config of
your AI app. See [The master key](/manual/reference/master-key/).

1. Make a token. Run this in a terminal (a window where you type commands).
   `--name` is a name for the token, such as the app that uses it. `--scopes`
   says what the token allows. A scope is one permission of a token:

   ```sh
   email-mcp token create --name my-app --scopes email:read,email:draft
   ```

   The command prints a few lines about the token, and then the token: a text
   that starts with `emcp_`. Copy it now. It is shown only once.

2. Start the server. This command keeps running for as long as the app uses
   it, so run it in a second terminal, and keep that terminal open. Stop it
   with Ctrl-C (hold the Ctrl key and press C). `--http` gives the address where
   the server listens:

   ```sh
   email-mcp serve --http 127.0.0.1:8765
   ```

   The server prints a log line that says "serving MCP over local HTTP", with
   the address and the path `/mcp`. The number after the colon is the port (the
   number that names one service on an address). Use another number when this
   one is in use, and use the same number in step 3.

3. Give the app the address `http://127.0.0.1:8765/mcp` and the token. The page
   of your app shows where they go.

This token lets the app read mail and write drafts, but not send. Scopes add up
(see below), so its highest level is 3 (Draft), and it also allows organizing.
Give only the scopes that the app needs.

## Make a token in the browser

The web pages (the pages that email-mcp shows in your browser, from your own
computer) make a token too, with no command. Do these steps in order:

1. Sign in to the web pages, if your browser is not signed in yet. Run
   `email-mcp setup`, and open the sign-in link that it prints. `setup` keeps
   running while you use the pages, so keep its terminal open. Stop it with
   Ctrl-C when you are done. The page [Connect your first
   account](/manual/getting-started/connect-account/#sign-in-to-the-web-pages)
   has the steps.
2. Click **Connections** in the menu.
3. In the section **New token**, type a name in **Name**. The name has 1 to 128
   characters.
4. Under **Permissions**, tick at least one box: **All of them**, `email:read`,
   `email:organize`, `email:draft`, `email:send` or `email:delete`.
5. Tick **Let this app ask me in its own window** only for an app that shows you
   the confirmation form. It is the same choice as `--in-band-confirm` (see
   below). Leave it empty otherwise.
6. Click **Make a token**. The page shows "Copy this token now. It is shown only
   once." and the token. Copy it now. When you open the page again, it shows the
   name of the token, and never the token.
7. Look at the section **Tokens**. It lists every token. The picture below shows
   it with the token `my-app` of this page.

{{< screenshot
  name="connections"
  alt=`The page Connections. The section Tokens lists the token my-app with
    the permissions email:draft and email:read, the highest level 3 (Draft),
    and No under Confirms in its own window. Below are the section New token
    with its boxes and the button Make a token, and the section Signed-in
    browsers.`
>}}

For each token, the list shows the **Name** and the id of the token, the
**Permissions**, the **Highest level**, whether it **Confirms in its own
window**, when it was **Made**, when it **Expires**, and when it was **Last
used**. The table scrolls sideways when it does not fit, so scroll to the right
to see the last columns. A token with no end date shows **No end date**, and a
token that was never used shows **Never**.

The last column is **Status**. A token that works has a **Revoke** button.
Click it to stop the token: an app that uses it is refused from now on. A
revoked token shows **Revoked on** and the date, and a token that ended shows
**Expired**.

The same page lists the browsers that are signed in, with a **Sign out** button
for each. The page [The web pages](/manual/daily-use/web-pages/) explains every
page of email-mcp.

## How tokens work

- `token create` prints a token once. Copy it then. The database keeps only a
  hash (a one-way fingerprint) of it. Scopes are `email:read`,
  `email:organize`, `email:draft`, `email:send` and `email:delete`, or `all`. A
  token with fewer scopes allows less, even when the account level allows
  more.
- **Scopes add up. They are not separate permissions.** Each scope stands for a
  [permission level](/manual/daily-use/permission-levels/) (a number from 0 to
  5 that says what the agent may do with the mail of an account), and a level
  includes every level under it. The agent is the AI (artificial intelligence)
  program that uses the tools of email-mcp. So `email:send` alone also allows
  reading, organizing and drafting. `email:delete` (moves mail to the Trash)
  allows everything. A token works on every account of the install. The level
  that applies to an account is the lower of the account level and the highest
  level of the token. `token create` and `token list` print it, for example
  `effective maximum level: 4 (Send), includes read, organize, draft`.
- **A token ends.** A new token stops working after 90 days. Choose another
  time with `--expires-in`, such as `--expires-in 30d` or `--expires-in 12h`
  (from 1 minute to 3650 days). A token from before this rule has no end date
  and works until you revoke it. `token list` shows the end date and about when
  the token was last used. To replace a token, make a new one and revoke the
  old one.
- **Confirmations go to the approval link by default.** A send or a move to the
  Trash (the folder of deleted mail) that needs a confirmation waits on the
  **Waiting for you** page (a web page where you approve or reject the send),
  even when the app says that it can show a form. See
  [Confirmations](/manual/daily-use/confirmations/). To let the app ask you in
  its own window, make the token with `--in-band-confirm`:

  ```sh
  email-mcp token create --name my-app --scopes email:send --in-band-confirm
  ```

  Do this only for an app that shows you the form. With this flag, any program
  with the token can answer the form for you. A token from before this rule
  does not have the flag. Make a new token to get it. The **Connections** page
  has the same choice.
- Some apps keep the token in plain text in their config file. The page
  [Connect an AI app](/manual/getting-started/connect-ai-app/) leads to the page
  of each app, and each page shows how to keep the token out of the file where
  the app allows it.
- The MCP endpoint is `http://127.0.0.1:8765/mcp`. MCP is an open standard that
  lets AI apps call tools that other programs offer. Every request needs the
  header `Authorization: Bearer <token>`. A header is
  an extra line of information that an app sends with a request.
- The server listens only on a loopback address (127.0.0.1, [::1] or
  localhost): an address that only your own computer can reach. It refuses to
  start on any other address, for example `--http 0.0.0.0:8765`, because local
  HTTP has no TLS (Transport Layer Security: the encryption of a connection).
- It refuses requests that come from a web page (requests with an `Origin`
  header), unless you allow that origin with `--allow-origin`.
- `email-mcp token list` shows the tokens. `email-mcp token revoke <id>` stops
  one.

Over HTTP, an app that uses an MCP protocol version older than 2026-07-28 gets
the approval link for every confirmation, not the in-band form, also with
`--in-band-confirm`. A protocol version is a date. It names the version of the
MCP rules that the app and the server use. See
[Confirmations](/manual/daily-use/confirmations/).
