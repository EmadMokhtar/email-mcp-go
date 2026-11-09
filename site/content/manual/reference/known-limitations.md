---
title: Known limitations
description: Read the limits of email-mcp that are known.
weight: 60
code_areas:
  - cmd/email-mcp/reconnect.go
  - internal/mcpserver/shape.go
  - internal/mime/
  - internal/outbox/
  - internal/provider/imapsmtp/
  - internal/service/write.go
---

After this page, you know the limits of email-mcp that are known. The agent is
the AI (artificial intelligence) program that uses the tools of email-mcp. A
tool is one action that the agent can call, such as `get_message`.

- After a long sleep of your computer (more than 15 minutes), a message that
  was sent with no undo window (the time that a send waits before it goes out;
  see [Activity and undo](/manual/daily-use/activity-and-undo/)) may show
  "delivery unknown" even though it was sent. email-mcp does not send it again.
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
- In the text of a message that the agent reads, a long link is cut, and
  `...[cut]` marks the place. A very long link is not shown at all. After a cut
  path, the query (the part after the `?`) is dropped too. The `html` body
  format (a choice of `get_message`) has the whole link. When it matters, open
  the mail yourself.
- In mail that the agent writes with HTML, a link keeps its address only when
  the address is short. A link with a long address, such as a tracking link,
  goes out as text, without its address. A character outside ASCII (the basic
  Latin letters, digits and signs) takes more room than it looks. The approval
  preview (what you see before you approve a send; see
  [Confirmations](/manual/daily-use/confirmations/)) shows the text part (the
  plain text version), so you can see which links lose their address.
- Mail servers differ, so email-mcp may not recognize every Trash folder. When
  it matters, look in your Trash and your folders yourself.
- After a send of a saved draft, email-mcp tries to move the draft to the Trash.
  If that move does not work, the message is still sent, and the draft stays
  where it was.

## What the mail server needs

email-mcp works with mail servers that offer IMAP (Internet Message Access
Protocol: the protocol for reading mail) and SMTP (Simple Mail Transfer
Protocol: the protocol for sending mail). Some tools need more. An extension is
an optional feature that a mail server can offer.

- A move (`move_messages`, `trash_messages` and `discard_draft`) needs the IMAP
  extension MOVE (it moves a message in one step) or UIDPLUS (it lets a client
  remove only the messages that it copied). Without both, the move fails and
  changes nothing. With MOVE but without UIDPLUS, the mail moves, but the tool
  may say that it does not know the new references of the messages (the ids
  that the agent uses to find a message).
- `trash_messages` and `discard_draft` need a Trash folder that email-mcp can
  find on the server. If it finds none, they fail. Then move the mail to the
  Trash in your mail program.
- `list_tags` and `tag_messages` work only when the server lets mail clients
  make new tags (IMAP keywords: labels that the server keeps on a message).
- An address with characters outside ASCII, yours or a recipient's, needs the
  SMTP extension SMTPUTF8 (it lets the server take such an address). Without
  it, the send fails, and nothing is sent.
