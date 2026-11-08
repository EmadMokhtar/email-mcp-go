---
title: Permissions
description: The levels, the switches, the token scopes and the limits, who can change them, and where the code enforces each one.
weight: 20
code_areas:
  - internal/policy/policy.go
  - internal/policy/limiter.go
  - internal/domain/account/account.go
  - internal/service/service.go
  - internal/service/accounts.go
  - internal/service/links.go
  - internal/service/write.go
  - internal/mcpserver/write_tools.go
  - internal/mcpserver/errors.go
  - internal/web/settings.go
  - internal/tokens/tokens.go
---

This page explains what the agent may do with an account, who can change it, and
where the code checks it. The agent is the AI (artificial intelligence) program
that calls the tools of email-mcp. A tool is one action that the agent can call.
Read this page before you add a tool, a level or a limit. The user view is in
[Permission levels](/manual/daily-use/permission-levels/) and [Full autonomy,
limits and settings](/manual/daily-use/full-autonomy-and-limits/).

The package `policy` decides, on the server, what the agent may do. The agent
only receives the answer, and a sentence that says why when the answer is no.
The agent cannot make these decisions and cannot change them.

## Levels

A level is a number from 0 to 5 that says what the agent may do with the mail of
an account. Each account has its own level for each area, and the mail area is
the only one with actions. A level includes every level under it.

| Level | Name | Action in `policy` | What it allows |
|---|---|---|---|
| 0 | Off | none | Nothing. The agent cannot use the account. |
| 1 | Read | `ActRead` | Read and search messages, folders and tags. Read an attachment. Read the status of a send. |
| 2 | Organize | `ActOrganize` | Set flags, add and remove tags, create folders and tags, and move messages between folders. `move_messages` refuses a Trash folder that email-mcp recognizes. |
| 3 | Draft | `ActDraft` | Save a draft, and move a draft to the Trash with `discard_draft`. |
| 4 | Send | `ActSend` | Send a message, a reply or a forward. Cancel a send. |
| 5 | Delete | `ActTrash` | Move messages to the Trash. |

The numbers are stored in the database, so they must not change
(`account.Level`). Level 5 has the name Delete, and it only moves mail to the
Trash. The page [What the agent can
do](/manual/daily-use/what-the-agent-can-do/) lists the tools of each level. The
function `registerWriteTools` in {{< src "internal/mcpserver/write_tools.go" >}}
registers the write tools, and each tool says its level in its description.

## How a decision is made

`policy.Decide` in {{< src "internal/policy/policy.go" >}} gets an account, an
action and the scopes of the caller (the permissions of its access token: see
"Token scopes"). The level that applies is the lower of two values: the mail
level of the account, and the highest level that the scopes allow. The action is
allowed when that level is at least the level that the action needs.

- A level outside 0 to 5, such as a damaged stored value, counts as Off. It
  never acts as a higher level.
- An action that `policy` does not know is refused at every level. Every check
  fails closed: when it cannot decide, the answer is no.
- Only a send and a move to the Trash can need a confirmation (the "yes" of the
  person before the action goes on). For a send it is the switch `ConfirmSends`
  of the account. For a move to the Trash it is `ConfirmDeletes`. Other actions
  never need one, and a refused action never does.
- A refusal has a reason, a sentence for the agent. It names the account by its
  display name or its address, written with `%q`, so a name cannot end the quote
  early or add a line.

## Where each rule is enforced

| Rule | Where | When |
|---|---|---|
| Level and scopes | `authorize` in `service.go` | Before every use case that acts on the mail of an account, and before any call to a mail backend |
| The level again | `begin`, from `approveSend` and `approveTrash` | When the person approves an action |
| The level again | `newSender` in `internal/app/sender.go` | At each try of an outbox item |
| Limits that no wait can fix | `checkLimit` in `write.go` | Before the person is asked, with no count |
| Counted limits | `Limiter.Reserve`, from `deliver` and `trashNow` | When the send reaches the outbox, and when the messages move |
| No move into a Trash folder that email-mcp recognizes | `Move` and `CreateFolder` | At every level |

A use case is one job of the program for a caller, such as "send this mail". A
mail backend is the part that talks to the mail server of one account. The
outbox is the queue of messages that wait to go out. The undo window is the time
that a send waits in the outbox, while it can still be cancelled. `authorize`
returns the permission error before the error that the account must be
reconnected, so a caller that may not act learns nothing about the connection.
The use cases of the person for accounts and settings, such as `UpdateSettings`,
`RemoveAccount` and `ProbeAccount`, check no level. No tool calls them (see [One
tool call, step by
step](/tech/start-here/tool-call/#where-the-permission-is-checked)).

## Who changes a level

Only the person changes a level, on the settings page of the account in the web
portal (the local web site of the person). The use case is
`Service.UpdateSettings` in {{< src "internal/service/accounts.go" >}}. No tool
calls it. The test `TestNoToolReachesTheUserOnlyUseCases` in
`internal/mcpserver` reads the Go files of `mcpserver`, without the tests, and
fails when one of them names a use case in its list `userOnlyCalls`, or imports
`web` or `tokens`.

The agent can ask. `request_permission_change` calls `Service.PermissionLink`,
which returns a link to the settings page of the account with the area and the
level in the query. It changes nothing, and it needs no level, because the agent
may always ask the person for more access. The link never holds a token. The
settings page shows the requested level apart from the form, and offers its own
button to apply it. That button posts to `/accounts/{id}/level`, and
`handleApplyRequestedLevel` in {{< src "internal/web/settings.go" >}} calls
`UpdateSettings`, so every rule of a save holds for it. The button **Save**
never applies a requested level.

This holds for an agent that can use only the tools of email-mcp. An agent that
can run shell commands as you, or that controls your browser, is outside what
email-mcp can control. The page [Security](/manual/daily-use/security/) of the
manual says more.

## Full autonomy and the risk acceptance

Full autonomy means that both switches, `ConfirmSends` and `ConfirmDeletes`, are
off. These two switches then ask nothing before a send or a move to the Trash.
Other checks can still ask: an address with a character outside ASCII (the
basic set of characters: English letters, digits and common signs), and the
new-recipient check (see [When the person is
asked](/tech/inside-the-server/send-flow/#when-the-person-is-asked)).
`NeedsRiskAcceptance` in {{< src "internal/service/accounts.go" >}} says when a
save needs the person to accept the risk. It has three cases:

1. The save turns `ConfirmSends` from on to off, at any level.
2. The save starts unattended sends: the state after the save lets the agent
   send with no question, and the state before it did not. So raising the level
   to Send with `ConfirmSends` off needs it too.
3. The save ends with both switches off, every time.

`UpdateSettings` refuses the save with `ErrAutonomyNotAccepted` when the box is
not ticked. When the save needs the acceptance, `AutonomyAcceptedAt` records the
time, and the activity log (the record of what the agent and the person did)
gets the entries `settings_changed` and `autonomy_accepted`. The time stays when
a switch goes on again. It is only a record, and it never replaces the
acceptance that a save needs. The warning text is in `internal/web/settings.go`.
This page does not copy it.

When an account is connected, it starts at level Send with both switches on and
the default limits (`account.NewIMAPAccount`, `DefaultLimits`).

## Token scopes

On local HTTP (HyperText Transfer Protocol), the caller has an access token (a
secret that a program sends with its requests), and the token has scopes:
strings such as `email:read`. `policy.KnownScopes` lists them from the lowest
level to the highest: `email:read`, `email:organize`, `email:draft`,
`email:send` and `email:delete`. Scopes are not separate permissions. They add
up to the highest level that they name, and that level includes the levels under
it. So `email:send` alone also allows reading, organizing and drafting. The
level of the account can still lower what a token may do.

- A nil list of scopes means no limit. The stdio caller has it (stdio is
  standard input and output: the AI app starts the program and talks to it
  directly), and so has the person at the web portal or at the terminal, because
  none of them has a token.
- An empty list allows nothing, and a scope that `policy` does not know has no
  effect.
- `tokens.Create` refuses a token with no scope, or with a scope that `policy`
  does not know. `httpserve.Principal` copies the scopes into a list that is
  never nil, so a token never means no limit.
- A token also has an end date. A new token stops working after 90 days by
  default (`tokens.DefaultLifetime`). `tokens.Verify` refuses a token that is
  revoked or past its end date. [Storage and
  secrets](/tech/inside-the-server/storage-and-secrets/#how-long-data-is-kept)
  says how long each kind of data is kept.
- When the level of the account would allow the action and the scopes lowered
  it, `PermissionError.TokenLimited` is true. The error then has no link to the
  settings, because the settings cannot help.

## Limits

The limits of an account are in `account.Limits`. `DefaultLimits` gives the
values that an account has when it is connected. The person can change them on
the settings page. A count is a whole number from 0 to 1,000,000 (`maxCount` in
`internal/web/settings.go`). An undo window is a whole number of seconds from 0
to 300 (`maxUndoWindow`).

| Limit | Default | Where it applies |
|---|---|---|
| Sends per hour | 20 | `Limiter.Reserve`, when the send reaches the outbox |
| Sends per day | 200 | The same |
| Recipients per message | 50 | `checkLimit`, with the hard cap `maxRecipients` of 200 |
| Messages moved to the Trash per day | 500 | `Limiter.Reserve`, when the messages move |
| Undo window with no confirmation | 30 seconds | `Outbox.Enqueue`, see [The send flow](/tech/inside-the-server/send-flow/) |
| Undo window after a confirmation | 0 seconds | The same |

The new-recipient check is a switch in `Limits` and is off by default.

- **Hour and day limits.** Each limit is counted in a fixed window, in UTC
  (Coordinated Universal Time). The hour window starts at the top of each hour,
  and the day window at 00:00 UTC (`now.Truncate` in `Limiter.Reserve`). The
  windows do not slide. So 20 sends at 10:59 and 20 more at 11:00 can both pass.
  The wait in a full limit is the time until its window ends. A limit of 0
  allows nothing.
- **Counting.** `Limiter.Reserve` checks and counts in one database transaction
  (a group of database steps that all happen or none do). So two sends at the
  same time, in one program or in several that share the database, cannot pass a
  limit together. The counters are kept for 48 hours (`retention`).
- **When to count.** A send counts when it reaches the outbox, so a send that
  the person declines uses no quota. A move to the Trash counts when the
  messages move.
- **A full limit.** The error is a `*policy.RateLimitError`. Its text is fixed.
  It names the kind of limit and the wait in whole minutes, rounded up, and it
  names no account.
- **A limit that no wait can fix.** `checkLimit` returns `ErrLimit` for a
  message with more recipients than `min(limit, maxRecipients)`, for a send
  limit or a Trash limit of 0, and for more messages in one call than the Trash
  limit of a day. The activity log keeps at most `maxRecipients` recipients in
  one entry, which is why the cap exists.

## What no level allows

- **Deleting mail for good.** `mail.Backend` has no method for it, and its
  comment asks each implementation to keep it so. `Trash` moves messages to the
  Trash folder and nothing more.
- **A move into the Trash.** `move_messages` refuses a target that email-mcp
  recognizes as the Trash folder or as a folder inside it, and `create_folder`
  refuses to make a folder inside such a folder (`ErrMoveToTrash`). The tool
  `trash_messages` is the way to move mail to the Trash. It counts toward the
  Trash limit and asks the person when the account wants it.
- **A file path as an attachment.** The tools take base64 data only
  (`AttachmentIn`: base64 is text made of letters, digits and a few signs, that
  stands for bytes). A path would make the program read a file of the computer
  for the agent.

## The text that the agent sees

`toolError` in {{< src "internal/mcpserver/errors.go" >}} turns a permission
error into a fixed text. It matches the error with `errors.As`, never by its
text. For a `*service.PermissionError` it uses the reason of the decision,
cleaned again with `mime.CleanUntrusted`, and one of three endings:

- With `TokenLimited`: the code is `token_scope`, the text says that the access
  token does not allow the action, and it holds no link.
- With a link: the text says where the person can change the level, and that the
  agent must not retry until the person has done this.
- Without a link, because it could not be built: the text names the email-mcp
  settings in general.

`service` fills the reason and the link for the agent. The text of the error
itself does not go to the agent. The one exception is a reached limit, whose
text is a fixed sentence.

## Related pages

- [The send flow](/tech/inside-the-server/send-flow/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
- [Permission levels](/manual/daily-use/permission-levels/) in the user manual
- [Full autonomy, limits and
  settings](/manual/daily-use/full-autonomy-and-limits/) in the user manual
