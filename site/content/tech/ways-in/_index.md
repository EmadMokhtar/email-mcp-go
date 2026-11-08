---
title: Ways in
description: How the person and the agent reach email-mcp through the commands, local HTTP and the web portal, and the checks on each way in.
weight: 40
code_areas:
  - cmd/email-mcp/
  - internal/httpserve/
  - internal/tokens/
  - internal/web/
---

These pages explain three ways into email-mcp. The person uses the commands at
a terminal (a window where you type commands) and the web portal in a browser.
An AI app that cannot start a program reaches the MCP (Model Context Protocol)
tools through local HTTP (HyperText Transfer Protocol). Each page names the
files that it covers, and the checks that a request passes before it reaches
the use cases. Read them when you add a command, change how a request is
checked, or add a page.

- [The command line](/tech/ways-in/command-line/): how the commands are
  registered and tested, what each one opens, and the rules for passwords,
  prompts and exit codes.
- [Local HTTP mode](/tech/ways-in/local-http/): the checks of `serve --http`,
  the access tokens (the secrets that a program sends with each request), the
  stateless server, and who gets the question in the chat.
- [The web portal](/tech/ways-in/web-portal/): how the portal starts, how a
  person signs in, the checks of each request, and how a page is added.
