---
title: Known limitations
description: Read the limits of email-mcp that are known.
weight: 60
code_areas:
  - cmd/email-mcp/reconnect.go
  - internal/mcpserver/shape.go
  - internal/mime/
  - internal/outbox/
---

After this page, you know the limits of email-mcp that are known. The agent is
the AI (artificial intelligence) program that uses the tools of email-mcp. A
tool is one action that the agent can call, such as `get_message`.

- After a long sleep of your computer (more than 15 minutes), a message that
  was sent with no undo window (the time that a send waits before it goes out;
  see [Activity and undo](/manual/daily-use/activity-and-undo/)) may show
  "delivery unknown" even though it was sent. It is never sent twice.
- A change of the server settings of an account (the hosts, the ports or the
  security) needs the account to be removed and added again. A new password or
  a new server certificate does not: reconnect the account (see
  [Troubleshooting](/manual/daily-use/troubleshooting/)).
- In HTML mail (mail that is written in HyperText Markup Language, the language
  of web pages), email-mcp cannot always tell what a reader sees. The agent can
  get text that a reader does not see. It can also miss text that a reader
  sees. When email-mcp cannot work out a style that can hide text, the text
  stays in the body, and `get_message` returns `hidden_content_suspected:
  true`. `hidden_parts_dropped` says how many hidden parts were left out. A mail
  can hold hidden text even when `get_message` does not return
  `hidden_content_suspected: true`. When it matters, open the mail yourself.
- In the text of a message, a long link is cut, and `...[cut]` marks the place.
  At most 32 characters of the user name and password, the last 64 of the host,
  64 of the path and 32 of the query stay. The `html` body format (a choice of
  `get_message`) has the whole link. The same cut applies to the text part (the
  plain text version) of mail that goes out and to the approval preview (what
  you see before you approve a send; see
  [Confirmations](/manual/daily-use/confirmations/)). A link in mail that goes
  out keeps its address only when the text shows the whole address, so a link
  with a cut address goes out without its address.
