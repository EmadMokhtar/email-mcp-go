---
title: Tech docs
description: How the code of email-mcp fits together and why, for the people who change it.
weight: 3
code_areas:
  - cmd/email-mcp/
  - internal/
---

These pages explain how the code of email-mcp works and why it is built this
way. They are for a developer who knows Go and does not know this code.
Each page names the files that it covers, and links to the code at the version
of this site.

email-mcp is an MCP server. MCP (Model Context Protocol) is the open standard
that lets an AI (artificial intelligence) app call the tools of another
program. The agent is the AI program that calls the tools. To learn how a
person installs and uses email-mcp, read the [user manual](/manual/).

Start with the [Architecture overview](/tech/start-here/overview/). It shows the
whole program on one page.

## If you want to know

The questions use a few terms. A tenant is the owner of a set of accounts.
IMAP (Internet Message Access Protocol) is the protocol for reading mail. Stdio
(standard input and output) and HTTP (HyperText Transfer Protocol) are the two
ways that a client talks to the server.

| If you want to know... | read... |
|---|---|
| What are the main parts, and what does each one do? | [Architecture overview](/tech/start-here/overview/#the-layers) |
| Which code can an agent reach, and which code only a person can reach? | [Architecture overview](/tech/start-here/overview/#two-doors-into-the-use-cases) |
| Who is the caller, and what is a tenant in a local install? | [Architecture overview](/tech/start-here/overview/#the-caller-and-the-tenant) |
| What starts when I run `serve`, and what can run at the same time? | [Architecture overview](/tech/start-here/overview/#run-modes) |
| Why can several processes use one database? | [Architecture overview](/tech/start-here/overview/#run-modes) |
| Where does this code live? | [Map of packages](/tech/start-here/package-map/#all-packages) |
| What may a package import? | [Map of packages](/tech/start-here/package-map/#the-layer-rule) |
| Which package do I change for this feature? | [Map of packages](/tech/start-here/package-map/#where-to-add-code) |
| Which library does what, and why is `go-imap` pinned? | [Map of packages](/tech/start-here/package-map/#libraries) |
| What happens between the request of the client and the IMAP server? | [One tool call, step by step](/tech/start-here/tool-call/#the-steps) |
| Where is the permission checked? | [One tool call, step by step](/tech/start-here/tool-call/#where-the-permission-is-checked) |
| Where is the call recorded in the activity log? | [One tool call, step by step](/tech/start-here/tool-call/#where-the-call-is-recorded) |
| Where is text from the mail cleaned? | [One tool call, step by step](/tech/start-here/tool-call/#where-text-from-the-mail-is-cleaned) |
| Where do errors become the texts that the agent sees? | [One tool call, step by step](/tech/start-here/tool-call/#errors-become-fixed-texts) |
| What differs between stdio and HTTP? | [One tool call, step by step](/tech/start-here/tool-call/#what-differs-between-stdio-and-http) |
