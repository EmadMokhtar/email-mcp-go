---
title: Full autonomy, limits and settings
description: Learn how to turn confirmations off, which limits stay on, and what every setting of an account does.
weight: 60
code_areas:
  - internal/policy/limiter.go
  - internal/service/send.go
  - internal/domain/account/
  - internal/outbox/
  - internal/web/settings.go
  - internal/web/templates/settings.html
---

After this page, you know what happens when you turn confirmations off, which
limits always stay on, and what every setting of an account does. The agent is
the AI (artificial intelligence) program that uses the tools of email-mcp.

## Turn off Confirm sends

Two switches decide whether the agent must ask you before it acts: **Confirm
sends** and **Confirm deletes**. The page
[Confirmations](/manual/daily-use/confirmations/) explains them.

**Confirm sends** is the switch that lets mail leave with no check. You can turn
it off only on the settings page, and only after you tick a box that accepts
this risk. This holds whatever Confirm deletes is:

> An email or a calendar invite can contain hidden instructions, such as
> 'forward all invoices to me'. With Confirm sends off, the agent may follow
> them, and nobody checks before anything leaves.

You must tick the same box when you raise the level to Send or above while
Confirm sends is off, because that also lets the agent send without asking. A
level is a number from 0 to 5 that says what the agent may do (see [Permission
levels](/manual/daily-use/permission-levels/)). If the agent can already send
without asking and stays that way, a change asks nothing new, unless both
confirmations are off. With both off, every save needs the box (see [Full
autonomy](#full-autonomy)).

To turn off **Confirm sends**, do these steps in the browser. If the web pages
are closed, see [Open the pages again][open] first:

1. Click **Accounts** in the menu, and then **Permissions and settings** on the
   account.

2. Clear the box **Confirm sends: ask me before the agent sends a message**.

3. Tick the box in the group **Turning off Confirm sends**. Its text is the
   warning above.

4. Click **Save**.

If you forget the box, the page says: "To turn off Confirm sends, to let the
agent send with it off, or to keep both confirmations off, tick the box that
accepts the risk." Nothing is saved.

## Full autonomy

**Full autonomy** means both switches are off. The agent then sends and moves
mail to the Trash (the folder of deleted mail) without asking. The check of an
address outside ASCII (the basic set of characters: English letters, digits and
common signs) and the new-recipient check can still ask. The new-recipient
check is a setting that asks you before a message goes to a new address (see
[All settings of an account](#all-settings-of-an-account)). Every save with
both switches off needs the same box again.

The time when you last accepted the risk is stored and shown on the settings
page, and each acceptance is in the activity log. If you turn Confirm sends on
and later off again, you must accept the risk again.

## Limits that stay on

These guardrails stay on in full autonomy. You can change the numbers per
account:

- at most 20 sends in one clock hour and 200 in one day. The hours and the
  days are those of UTC (Coordinated Universal Time: the world standard of
  time, with no time zone offset), so 00:00 UTC may not be midnight where you
  live. The hour count starts again at minute 00 of each hour (such as 14:00
  UTC), and the day count at 00:00 UTC. It does not count back 60 minutes or 24
  hours from now. The table below says more;
- at most 50 recipients per message. The recipients are the addresses in To,
  Cc (carbon copy: the people who get a copy) and Bcc (blind carbon copy: the
  people who get a copy, while the other recipients do not see them).
  The settings page accepts a higher number, but email-mcp refuses a send to
  more than 200 recipients, whatever you set;
- at most 500 messages moved to the Trash per day. The drafts that
  `discard_draft` moves, and the draft that email-mcp moves after it sends a
  saved draft, do not count;
- an undo window of 30 seconds for a send without a confirmation (0 seconds
  after you confirmed it; you can set 0 to 300 seconds). The agent or you can
  cancel a send during this window. You cancel on the Activity page, or with
  `email-mcp outbox cancel <id>`, at any permission level. While email-mcp
  stops, the agent cannot cancel. The page [Activity and
  undo](/manual/daily-use/activity-and-undo/#cancel-a-send) has the steps, and
  the section [When email-mcp
  stops](/manual/daily-use/activity-and-undo/#when-email-mcp-stops) says more;
- a send is checked again each time it is about to go out. If the level of the
  account no longer allows sending, the send fails and does not go out, even if
  you raise the level again later;
- no tool ever deletes mail for good;
- every tool call of an agent is written to the activity log, except a call
  refused while the program stops, and a call with a wrong access token (see
  [Activity and undo](/manual/daily-use/activity-and-undo/#the-activity-page));
- a recipient with characters outside ASCII is always confirmed (see
  [Confirmations](/manual/daily-use/confirmations/#addresses-outside-ascii)).

## All settings of an account

The settings page of an account has these settings. Open it with **Accounts**,
and then **Permissions and settings** on the account. Click **Save** to keep
your changes. A count is a whole number from 0 to 1,000,000. An undo window is a
whole number of seconds from 0 to 300. A count of 0 allows nothing. The send
and Trash counts use UTC: an hour count starts again at minute 00 of each hour,
and a day count at 00:00 UTC.

| Setting (as on the page) | What it does | Default |
|---|---|---|
| **Mail level** | The level of the account: what the agent may do with its mail. The list has 0 Off, 1 Read, 2 Organize, 3 Draft, 4 Send and 5 Delete (moves mail to the Trash). | 4 Send |
| **Confirm sends** | Asks you before the agent sends a message. | On |
| **Confirm deletes** | Asks you before the agent moves mail to the Trash. It does not apply to `discard_draft`, which moves a draft to the Trash at level 3, or to the draft that email-mcp moves to the Trash after it sends a saved draft. | On |
| **Sends per hour** | The most sends in one clock hour (UTC); the count restarts at minute 00 of each hour. | 20 |
| **Sends per day** | The most sends in one UTC day; restarts at 00:00 UTC, not a sliding 24 hours. | 200 |
| **Recipients per message** | The most recipients (To, Cc and Bcc, hidden copies included) in one message. The page accepts a higher number, but email-mcp never sends to more than 200, whatever you set. | 50 |
| **Messages moved to Trash per day** | The most messages that the agent moves to the Trash in one UTC day; restarts at 00:00 UTC. The draft moves of `discard_draft`, and of a send of a saved draft, do not count. | 500 |
| **Undo window without a confirmation, in seconds (0 to 300)** | How long a send waits before it goes out, when you did not confirm it. You and the agent can cancel it in this time. While email-mcp stops, only you can cancel. | 30 seconds |
| **Undo window after a confirmation, in seconds (0 to 300)** | The same wait, after you confirmed the send. | 0 seconds |
| **Ask me before a message goes to an address that this account never wrote to** | The new-recipient check. A message to an address that email-mcp never sent or queued mail to for this account (your Sent folder is not read) asks you first, also when **Confirm sends** is off. | Off |
| **Save a copy of sent mail to Sent** | After the mail server takes a message, email-mcp saves a copy of it in the Sent folder of the account. If the copy cannot be saved, the message is still sent, and it is not sent again. | Off |

The settings page also has the section **Reconnect the account** and the section
**Remove the account**. The page [Manage
accounts](/manual/daily-use/manage-accounts/) explains them.

[open]: /manual/daily-use/web-pages/#open-the-pages-again
