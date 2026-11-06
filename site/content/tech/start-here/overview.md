---
title: Architecture overview
description: The parts of the program and how they fit together.
weight: 10
code_areas:
  - cmd/email-mcp/main.go
  - internal/app/app.go
  - internal/mcpserver/server.go
---

## The three parts

This page is a placeholder. It shows how a page uses a drawing and a source
link.

{{< diagram "example" >}}

The drawing says three things:

- The AI app starts email-mcp.
- email-mcp talks to the mail server.
- The person uses the web pages of email-mcp.

The list of commands is in {{< src "cmd/email-mcp/main.go" >}}.
