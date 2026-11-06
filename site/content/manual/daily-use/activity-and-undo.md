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
you, and the second brings your answer.

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
do with the mail of an account).

To cancel in the browser:

1. Click **Activity** in the menu.

2. Find the send in the section **Queued sends**. The list shows the account,
   the time when the send **Goes out**, its **State** and the id of the
   **Send**. The state says "Waits to go out", or "Will try again" after a try
   that did not work.

3. Click **Cancel**. The page shows **Activity** again, and the send is no
   longer in the list. It will not go out.

If the page says "It is too late to cancel this send: it is being sent, or it
has ended.", the send already left, or it is leaving now.

A send that still waits for your answer is not in this list. It is on **Waiting
for you**. Click **Reject** there. See
[Confirmations](/manual/daily-use/confirmations/).

To cancel without a browser, run these two commands in a terminal (a window
where you type commands). The first one lists the sends that wait to go out,
with their ids. The second one stops one send. Replace `<id>` with the id of the
send:

```sh
email-mcp outbox list
email-mcp outbox cancel <id>
```

The first command prints the columns `ID`, `ACCOUNT`, `GOES OUT` and `STATE`.
When nothing waits, it prints "No message waits to go out." The second command
prints "Cancelled the send" and the id, and "It will not go out." The page
Commands lists every command of email-mcp.

[all]: /manual/daily-use/full-autonomy-and-limits/#all-settings-of-an-account
