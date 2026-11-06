---
title: Confirmations
description: Learn when a send or a move to the Trash waits for your yes, what you see, and the two ways to answer.
weight: 40
code_areas:
  - internal/mcpserver/elicit.go
  - internal/confirm/
  - internal/web/approvals.go
  - internal/web/templates/approval.html
  - internal/web/templates/approvals.html
---

A confirmation is your "yes" before the agent can send mail or move mail to the
Trash. The agent is the AI (artificial intelligence) program that uses the tools
of email-mcp, and the Trash is the folder of deleted mail.

## The two switches

Two switches apply to each account. A switch is a box on the settings page of
the account. You tick it or clear it:

- **Confirm sends** (on by default): ask before the agent sends a message.
- **Confirm deletes** (on by default): ask before the agent moves mail to the
  Trash.

Moving mail to the Trash also needs level 5 first. A level is a number from 0 to
5 that says what the agent may do with the mail of an account. See [Permission
levels](/manual/daily-use/permission-levels/).

## The path of a send

A send goes through five steps. The agent asks to send. email-mcp checks that
the level of the account allows a send. The send waits for your yes, when a
confirmation is needed. Then the send waits in the undo window: the time before
the mail goes out, when you or the agent can still cancel it (see [Activity and
undo](/manual/daily-use/activity-and-undo/)). Then the mail goes out.

{{< diagram "send-path" >}}

## What you see

When a confirmation is needed, the server shows you what will happen. For a
send: who it goes to, the subject, the attachments and the first 500 characters
of the text. In that preview, long runs of blank space are made short, so blank
lines cannot push the text out of view, and a last line says how many
characters are not shown. The approval page shows the whole text. For a move to
the Trash: the date, the sender address and the subject of each message (up to
20, and how many more). The page [Security](/manual/daily-use/security/) says
what a message that the agent writes can hold, so that what you approve is what
goes out.

## Two ways to answer

The server asks in one of two ways:

- **In the AI app ("in band").** The app shows a small form with one box,
  "Confirm". The app must support MCP (Model Context Protocol) elicitation (a
  way for a server to ask the person a question through the app). Over stdio
  (standard input and output: the AI app starts the program and talks to it
  directly, with no network) this works for every protocol version. A protocol
  version is a date. It names the version of the MCP rules that the app and the
  server use. Over HTTP (HyperText Transfer Protocol: the protocol that apps and
  browsers use to talk over a network) it works only for apps that use the MCP
  protocol version 2026-07-28 or newer, and only with a token (a secret that the
  app sends to the server) that you made with `--in-band-confirm` (see [Local
  HTTP mode](/manual/reference/local-http-mode/)).
- **With an approval link.** Otherwise the tool answers `pending_approval` with
  a link to the **Waiting for you** page (a web page where you approve or reject
  the send). Open it in the browser that `email-mcp
  setup` signed in, and click **Approve and send** (for a send) or **Approve and
  move to Trash** (for a move to the Trash), or click **Reject**. The link
  expires after 24 hours. The agent can check the result with
  `get_send_status`. The page [The web pages](/manual/daily-use/web-pages/)
  lists every page of email-mcp.

You must answer the form within 10 minutes. After that, nothing is done, and the
agent must ask again. The page [How each AI app
confirms](/manual/daily-use/app-confirmation/) says which apps show the form
and which use the approval link.

### The page Waiting for you

The page shows the whole message of a send, with the buttons **Reject** and
**Approve and send**. A note says "Nothing goes out until you approve." For a
move to the Trash, the page lists the messages, the buttons read **Reject** and
**Approve and move to Trash**, and the note says "Nothing moves until you
approve."

{{< screenshot
  name="waiting"
  alt=`The page Waiting for you, with one send open. The page shows the
    message and the buttons Reject and Approve and send.`
>}}

If the page cannot show the whole message, it has only the button **Reject**,
and it says: "The whole message cannot be shown now, so it cannot be approved."
Reload the page when the mail server answers, or reject the action.

### After you answer

Click **Approve and send**. The page then shows the true result of the send.
When the mail server took the message, the page shows **Sent** and "The mail
server accepted the message."

{{< screenshot
  name="result-sent"
  alt=`The page after an approved send. It shows Sent, the subject and the
    recipient, the text The mail server accepted the message, and the button
    See the activity.`
>}}

These are the other results that the page can show:

- **Approved**, with one of these texts: "It is being carried out."; "It goes
  out at" and a time, or "It waits to be sent." (the send is in the queue); or
  "The result is not known yet. Check Activity."
- **Sending**: "The mail server is still working on it. Reload this page to see
  the result."
- **Not sent**: "The mail server did not take the message, and it will not try
  again. Ask the agent to send it again."
- **Rejected**: "Nothing was done."
- **Cancelled**: "The message was cancelled before it went out."
- **Expired**: "It can no longer be approved."
- **Moved to Trash**: "The messages are in the Trash folder of the account."

## Trust in the AI app

The confirmation is only as good as the AI app. The server trusts the app to
show the form to a person. It does not trust the model. An app that answers the
form by itself defeats the confirmation. For example, do not set up a Claude
Code `Elicitation` hook (a script that Claude Code runs when an event happens;
the page [Claude Code][cc] names the source and the date) that answers it
automatically. Over stdio, this trust goes to the app that you started. Over
HTTP, the server gives this trust only to a token where you chose it, because
any program with a token can claim to show a form. Every other token uses the
approval link.

The page [How each AI app confirms](/manual/daily-use/app-confirmation/) lists
what each app can do.

## Addresses outside ASCII

A letter of another alphabet can look like a Latin letter, so an address can
look right and still be another domain (the part of an address after the `@`).
ASCII is the basic set of characters: English letters, digits and common signs.
So for a recipient with any character outside printable ASCII:

- The server always asks you. It asks when **Confirm sends** and the
  new-recipient check are off, and also when you wrote to the address before.
  The new-recipient check is a setting that asks you before a message goes to an
  address that the account never wrote to. See [All settings of an
  account][all].
- The preview and the approval page show the domain in ASCII next to the address
  (its punycode form, `xn--...`: the ASCII spelling of a domain name with
  characters outside ASCII), and a warning line. The warning names each part of
  the domain that mixes alphabets. The display name of the recipient (the name
  that shows next to an address) comes after the ASCII form, in quotes, so a
  name cannot pose as an address.
- The agent cannot type an address that has such characters before the `@`,
  because that part has no ASCII form to compare. A reply or a reply to all that
  reaches such an address still works, and it always asks you. So does a draft
  that you wrote yourself.
- A domain with no exact ASCII form is refused, so the form that you see is the
  form of the domain that the message goes to. This covers a domain that would
  change when it is encoded, such as one with full-width or capital letters. One
  such address stops a whole reply to all, and the error says why. A plain reply
  that names the other people in Cc (the people who get a copy) still works.

[all]: /manual/daily-use/full-autonomy-and-limits/#all-settings-of-an-account
[cc]: /manual/getting-started/connect-ai-app/claude-code/#confirmation
