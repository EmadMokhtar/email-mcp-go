---
title: Start here
description: The whole program, the map of its packages, and the path of one tool call.
weight: 10
code_areas:
  - cmd/email-mcp/
  - internal/app/
  - internal/mcpserver/
  - internal/service/
---

These pages give the big picture before the details. They show how the parts
of email-mcp fit together, where each package sits, and what happens in one
tool call, from the MCP (Model Context Protocol) client to the mail server and
back.

- [Architecture overview](/tech/start-here/overview/): the parts, the two
  doors, the run modes and the rules that hold in all code.
- [Map of packages](/tech/start-here/package-map/): one row for each package,
  its layer, and where to add code.
- [One tool call, step by step](/tech/start-here/tool-call/): the path of
  `get_message`, with each check and each record.
