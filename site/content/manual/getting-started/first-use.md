---
title: Your first use
description: Check that the install works, ask the agent a first question, and see what happens when it wants to send mail.
weight: 40
code_areas:
  - cmd/email-mcp/doctor.go
  - internal/domain/account/
---

After this page, you know that your setup works, and you have asked the agent
your first question. You also know what to expect when the agent wants to send
mail.

## Check that everything works

Run this in a terminal (a window where you type commands). The command checks
the data folder (the folder where email-mcp keeps its data), the master key (the
key that protects your stored passwords) and every account. For each account,
it logs in to the mail servers:

```sh
email-mcp doctor
```

It prints one line for each check. Every line should say `ok`, and none `FAIL`.
For example:

```text
data directory /Users/you/Library/Application Support/email-mcp: ok
database file: ok
master key (keychain): ok
account mxavsb75q46brlmikhbydlxiyi (you@example.com): ok
```

The first line names your own data folder. If a line says `FAIL`, see
Troubleshooting.

## Ask the first question

Start your AI (artificial intelligence) app, and ask, for example: "List my
email accounts, then show the unread mail of today." The agent lists your
accounts, and then it shows your unread mail from today.

## What a new account may do

A new account starts at mail level 4, named **Send** on the web page, with
**Confirm sends** and **Confirm deletes** on. **Confirm sends** asks you before
the agent sends a message. **Confirm deletes** asks you before the agent moves
mail to the Trash (the folder of deleted mail). So:

- reading, organizing and writing drafts (messages that are saved but not sent)
  happen without questions;
- every send asks you first;
- moving mail to the Trash is not possible until you raise the level to 5. The
  web page names level 5 **Delete**. It moves mail to the Trash, and it never
  deletes mail for good.

A level is what the agent may do with an account. The levels go from 0 to 5
(see Permission levels).

## Try a send (optional)

Do this to see the approval step. The agent sends a message to your own
address, so nobody else gets it.

1. Ask the agent to send a short message to your own address. For example:
   `Send a short test message to you@example.com`. Use your own address in
   place of `you@example.com`.

2. Your AI app asks you in one of two ways.

   - If your AI app shows a small form with one box, "Confirm", read the message
     in the form. Tick the box to confirm.
   - Otherwise, the agent tells you that the send waits for your approval, and
     it gives you a link. Open the link in the browser that `email-mcp setup`
     signed in. The page **Waiting for you** shows the whole message, with the
     buttons **Reject** and **Approve and send**. The link expires after 24
     hours.

   {{< screenshot
     name="waiting"
     alt=`The page Waiting for you, with one send open. The page shows the
       message and the buttons Reject and Approve and send.`
   >}}

3. Click **Approve and send**. The page shows **Sent**, and the text "The mail
   server accepted the message." Click **Reject** instead if you do not want to
   send the message. Then nothing is sent.

The agent can check the result with `get_send_status`. This tool shows whether a
send is queued, sent, failed, or waiting for approval.

## Where to go next

- Permission levels explains what the agent may do.
- Confirmations explains when a send or a move to the Trash waits for you.
- Security explains what email-mcp protects against.
