---
title: Activity and undo
description: See what the agent did on the Activity page, and cancel a send during its undo window.
weight: 70
code_areas:
  - cmd/email-mcp/outbox.go
  - internal/mcpserver/capture.go
  - internal/audit/
  - internal/outbox/
  - internal/app/app.go
  - internal/web/activity.go
  - internal/web/templates/activity.html
---

After this page, you can see what the agent did, and you can cancel a send
before it goes out. The agent is the AI (artificial intelligence) program that
uses the tools of email-mcp. A tool is one action that the agent can call, such
as `send_message`.

## The Activity page

The **Activity** page shows two types of row. A *request* is one tool call of an
agent. Every tool and every outcome has one: a read, a refused call, and a call
with wrong arguments too. An *action* is what email-mcp or you did, such as a
send. So a call that changes something has a request and an action. A send or a
move to Trash that you confirm in your AI app has two requests: the first asks
you, and the second brings your answer. A tool call that comes while email-mcp
stops is refused, and it has no row (see [When email-mcp
stops](#when-email-mcp-stops)).

A request keeps the arguments of the call, encrypted, so you can see what the
agent searched for or which messages it asked for. This includes search text,
subjects, recipients and file names. Message bodies and attachment data are
never stored: the row shows only their size. A call whose arguments do not fit
the tool, or a call of a tool that does not exist, keeps only the names and the
sizes of its arguments, because the text of a message could be in any of them. A
request with a wrong access token (a secret that an app sends to the local HTTP
(HyperText Transfer Protocol) server) has no row, because nothing says whose log
it belongs to. The program log has it instead. Rows are kept for 90 days, and
the page shows at most the first 4 KiB (4,096 bytes) of the arguments of a row.

{{< screenshot
  name="activity"
  alt=`The page Activity. The section Queued sends says that no message waits
    to go out. The section Log has the filters Account, Tool, Type and
    Entries, and rows for a send, with Request and Action as types.`
>}}

### The log

The section **Log** lists the newest rows first. Each row shows the tool, the
type (**Request** or **Action**), the outcome, the account, and the name that
the AI app gave for itself. A row with arguments has a line **Arguments** that
you can open. These filters narrow the list. Click **Show** to apply them:

- **Account** shows the rows of one account.
- **Tool** shows the rows of one tool.
- **Type** shows only requests, or only actions.
- **Entries** is how many rows the page shows, from 1 to 200. It starts at 50.

## Cancel a send

A send that you did not confirm waits in the undo window before it goes out. The
undo window is the time that a send waits in the outbox (the queue of messages
that wait to go out) before it goes out. It starts at 30 seconds for a send
without a confirmation, and at 0 seconds after you confirmed it. You can change
both times. See [All settings of an account][all].

The agent or you can cancel a send during this window. You can cancel at any
permission level (a level is a number from 0 to 5 that says what the agent may
do with the mail of an account). While email-mcp stops, the agent cannot cancel
(see [When email-mcp stops](#when-email-mcp-stops)).

To cancel in the browser (if the web pages are closed, see [Open the pages
again][open] first):

1. Click **Activity** in the menu.

2. Find the send in the section **Queued sends**. The list shows the account,
   the time when the send **Goes out**, its **State** and the id of the
   **Send**. The state says "Waits to go out", or "Will try again" after a try
   that did not work.

3. Click **Cancel**. The page shows **Activity** again, and the send is no
   longer in the list. It will not go out.

If the page says "It is too late to cancel this send: it is being sent, or it
has ended.", the send already left, or it is leaving now.

A send that waits for your answer is not in this list. If your AI app shows the
form, answer it there. The agent can also withdraw the send with the tool
`cancel_send`. If the agent gave you an approval link, the send is on **Waiting
for you**: click **Reject** there. See
[Confirmations](/manual/daily-use/confirmations/).

To cancel without a browser, run these two commands in a terminal (a window
where you type commands). They need no web page and no sign-in. The first one
lists the sends that wait to go out, with their ids. The second one stops one
send. Replace `<id>` with the id of the send:

```sh
email-mcp outbox list
email-mcp outbox cancel <id>
```

The first command prints the columns `ID`, `ACCOUNT`, `GOES OUT` and `STATE`.
When nothing waits, it prints "No message waits to go out." The second command
prints "Cancelled the send" and the id, and "It will not go out." The page
[Commands](/manual/reference/commands/) lists every command of email-mcp.

### When email-mcp stops

When you stop email-mcp (Ctrl-C, or you close the AI app), it does not end at
once. Ctrl-C means: hold the Ctrl key and press C. email-mcp first waits for the
sends that are going out. Then it waits up to 5 minutes and sends the queued
messages when their window ends. A message that is not due in those 5 minutes,
such as a send that will try again later, stays queued. A stop usually takes a
few seconds, and at most about 20 minutes.

During the stop, the agent cannot call `cancel_send`: email-mcp refuses every
tool call. Cancel with `email-mcp outbox cancel <id>` in another terminal, or on
the Activity page if that program already shows the pages.

A send that waits for your answer cannot be confirmed during the stop. What
happens next depends on how email-mcp asked you (see [Two ways to
answer](/manual/daily-use/confirmations/#two-ways-to-answer)):

- **An approval link.** The send keeps waiting. You can approve it after the
  next start, until it expires 24 hours after the agent asked. You can still
  reject it on **Waiting for you**, if that program shows the pages.
- **The form of your AI app.** The app sends your answer to email-mcp in a
  new tool call. email-mcp refuses it during the stop, and nothing is sent.
  The form works for 10 minutes after the agent asked. After that, the agent
  must ask again.

A second Ctrl-C ends the program at once. The queued messages stay queued for
the next email-mcp program that sends mail (`email-mcp serve` or `email-mcp
setup`). An AI app can also end the program at once, with the same result. A
send that is going out at that moment is later marked "delivery unknown" (after
15 minutes, by an email-mcp program that sends mail): email-mcp cannot
tell whether the mail server took the message. email-mcp does not send it
again.

[all]: /manual/daily-use/full-autonomy-and-limits/#all-settings-of-an-account
[open]: /manual/daily-use/web-pages/#open-the-pages-again
