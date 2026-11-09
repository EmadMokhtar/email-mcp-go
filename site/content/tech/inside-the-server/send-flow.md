---
title: The send flow
description: How a send_message call goes from the agent to the mail server, with the question to the person, the outbox and the undo window.
weight: 10
code_areas:
  - internal/service/send.go
  - internal/service/write.go
  - internal/service/approvals.go
  - internal/service/queue.go
  - internal/confirm/confirm.go
  - internal/signing/signing.go
  - internal/mcpserver/elicit.go
  - internal/mcpserver/write_tools.go
  - internal/outbox/outbox.go
  - internal/app/sender.go
  - cmd/email-mcp/outbox.go
---

This page follows the tool `send_message` from the request of the agent to the
mail server. It shows when the person is asked, how the "yes" of the person is
tied to the exact message, and what the outbox does until the mail leaves. Read
it before you change `Service.Send`, the confirmation or the outbox.

The agent is the AI (artificial intelligence) program that calls the tools of
email-mcp. A tool is one action that the agent can call. The person is the human
who owns the mail accounts. The outbox is the queue of messages that wait to go
out. The undo window is the time that a send waits in the outbox, while it can
still be cancelled. The user view of the same path is in
[Confirmations](/manual/daily-use/confirmations/) and [Activity and
undo](/manual/daily-use/activity-and-undo/).

## The steps

{{< diagram "send-decision" >}}

The numbers match the drawing. `sendMessage` in
{{< src "internal/mcpserver/write_tools.go" >}} reads the arguments: the fields
of a message (it can be a reply or a forward), or the `draft_ref` of a saved
draft, and exactly one of the two. A draft is a message that is saved but not
sent. Then it calls `Service.Send` in {{< src "internal/service/send.go" >}}.
The doc comment of `Send` has the order of the steps, and the order is on
purpose.

1. **Permission.** `Send` counts itself as an action that may send mail
   (`beginAction`), so that a stop can wait for it. Then `begin` finds the
   principal (the caller: its tenant, the scopes of its access token and the
   name of its client) and the account in the tenant. A tenant is the owner of a
   set of accounts, and the scopes are the permissions that the token carries.
   `begin` asks `policy.Decide` for the action `ActSend`, which needs level 4 (a
   level is a number from 0 to 5 that says what the agent may do with the mail
   of an account). This comes first, so a caller who may not send learns nothing
   about the message or the mail server. See
   [Permissions](/tech/inside-the-server/permissions/).
2. **Build.** `newSendRequest` makes the canonical payload of the request (the
   exact request in one fixed text form: see "How the yes is tied to the
   message"). `prepareSend` builds the message and its preview (what the person
   sees before they say yes) with `mime.Build`. A reply or a forward reads the
   original message first. A draft is read with `GetRaw` (at most 25 MiB,
   `maxDraftBytes`) and `mime.FromDraft`, after `buildSend` has checked that it
   has the draft state and is from the address of the account. The payload is at
   most 15 MiB (`maxSendPayloadBytes`), because it waits in the database until
   the person answers. A MiB (mebibyte) is 1,048,576 bytes.
3. **Limits that no wait can fix.** `checkSendLimits` refuses, with `ErrLimit`,
   a message with more recipients than the account allows, and a send limit of
   0. It counts nothing. A full limit is a different case: see step 9.
4. **The question.** With no reason to ask, `deliver` runs at once with
   `UndoWindowAuto`. With a reason, `ask` stores the action and the person
   answers in one of two ways. Steps 5 to 8 depend on the channel. Steps 9 and
   10 are in "Delivery and the undo window".

## When the person is asked

A send asks the person in three cases. `Send` tests them in this order, and the
first one that holds is the reason. The audit entry of the question (an entry of
the activity log) holds the reason as its `detail` (see [Activity log and
program log](/tech/inside-the-server/audit-and-logging/)).

- `confirm_sends`: the account has the switch **Confirm sends** on. `Decide`
  reports it in `Decision.NeedsConfirmation`.
- `new_recipient`, first case: a recipient has a character outside printable
  ASCII (American Standard Code for Information Interchange: the basic set of
  characters, `mime.HasNonASCIIAddress`). Such an address can look like another
  address, so the person is asked every time, also when both switches are off.
- `new_recipient`, second case: the account has the new-recipient check on
  (`Limits.NewRecipientCheck`), and a recipient of the envelope (the list of
  addresses that the mail goes to) is an address that the account never sent to.

The known recipients are kept as keyed hashes, so the table holds no address.
`recipientHashes` makes a keyed hash (HMAC-SHA-256: a hash made with a secret
key; HMAC is hash-based message authentication code, and SHA-256 is Secure Hash
Algorithm with a 256-bit result) of each lower-case address. The key is secret,
and the database keeps it only in wrapped form (see [Storage and
secrets](/tech/inside-the-server/storage-and-secrets/)). `rememberRecipients`
records the hashes after the outbox has accepted the message, also when the
check is off. If that write fails, the program log gets a fixed code, and the
next send to the address asks again.

## Two ways to ask

`ask` stores the action with `Manager.Create` in
{{< src "internal/confirm/confirm.go" >}}. The action has a mode, and the mode
never changes. Only the channel of the mode can answer the action, so one
message cannot be approved twice: once in the chat and once on the page.

### In band

In band means that the question appears in the chat of the client (the AI app
that talks to the server). `canElicitInBand` in
{{< src "internal/mcpserver/elicit.go" >}} decides whether this caller may
confirm in band, and whether the client can show the question as a form in
this call (form elicitation: a way for a server to ask the person a question
through the app). [Who gets which
channel](/tech/ways-in/local-http/#who-gets-which-channel) has its three
conditions and the channel of each caller.

{{< diagram "send-two-calls" >}}

- **Step 5a, the first call.** `Send` stores the action in the mode `in_band`,
  which waits for 10 minutes (`confirm.InBandLifetime`), and returns
  `NeedsConfirmation`. `sendMessage` makes a result with no content from it: an
  input request `confirm` (a form with one box, off by default) and the
  `RequestState` (a signed token that names the waiting action). The client
  shows the form to the person. `sendPreview` makes its text.
- **Step 6a, the second call.** It is the same tool with the same arguments, the
  answer and the state. `Send` checks the permission and builds the message
  again from these arguments (steps 1 and 2) before it acts on the answer.
  `confirmAnswer` refuses a call that has only one of the two. The answer is a
  yes only when it is an elicitation result with the action `accept` and the
  field `confirm` set to the JSON (JavaScript Object Notation) boolean true
  (`accepted`). Every other answer is a no, and so is the string "true". Nothing
  in the arguments of a tool can say yes.
- **A no** moves the action to `rejected` (`declineAnswer`), and the result is
  `declined`. A yes goes on to steps 7a and 8a.
- A caller that may not confirm in band, and sends an answer, gets
  `confirm.ErrStateInvalid` before anything reads the state.

### The approval page

When the caller or the client cannot ask in band, the action has the mode
`approval` and waits for 24 hours (`confirm.ApprovalLifetime`).

- **Step 5b.** The tool answers `pending_approval`, with a link to the approval
  page of the web portal (the local web site of the person) that
  `Links.Approval` makes. The link never holds a token. It can be empty when it
  cannot be built, and the action still waits on the approvals page.
- **Step 6b.** The person reads the message there and presses Approve or Reject.
  `ApprovePending` in {{< src "internal/service/write.go" >}} is the use case
  behind the Approve button. Meanwhile the agent calls `get_send_status`, and it
  gets the link again for an action of this mode only.

## What the person sees

`previewOf` builds a `confirm.Preview` from the built message: the sender, every
recipient of the envelope (To, Cc and Bcc, and the people that a reply to all
adds), the display names, the subject, the attachments with their media types,
the start of the text, and warnings about recipients that can look like another
address.

- Each value is cleaned of characters that a reader cannot see
  (`mime.CleanUntrusted`) before a person reads it.
- A recipient whose address would show differently after cleaning is refused
  (`ErrHiddenAddress`), and so is a domain outside ASCII that has no exact ASCII
  form to show next to it (`ErrInexactDomain`). The person must see the address
  that the mail goes to, so this check runs before anyone is asked.
- The text is the first 500 characters (`previewChars`) after runs of white
  space are made short (`collapseSpace`), so blank lines cannot push the text
  out of view. `BodyMore` counts what the preview leaves out.
- In the chat, `sendPreview` makes one plain text. On the page, `PendingDetail`
  in {{< src "internal/service/approvals.go" >}} builds the message again and
  the page shows all of it.

## How the yes is tied to the message

The "yes" must be a yes to this message and no other. Five parts do this:

1. **A canonical payload.** The payload is a JSON text of the request:
   `{"v":1,"compose":...,"draft":...}`. The same request always gives the same
   bytes: the fields have a fixed order, and a list is always written. The built
   message cannot be the payload, because `mime.Build` makes a different
   Message-ID and date each time. Every call builds the message from the payload
   that it reads back.
2. **A hash.** `confirm.HashPayload` makes a hash (a fingerprint of bytes) of
   the kind of action, a zero byte and the payload. The action and the
   `RequestState` both hold it.
3. **A signed state.** `signing.Signer.Seal` in
   {{< src "internal/signing/signing.go" >}} makes the `RequestState` from the
   id of the action, the hash and the tenant, with an expiry of 10 minutes, and
   an HMAC-SHA-256 signature. Anyone can read the state. Nobody can change it,
   or make one, without the key. The signer keeps no record of the states that
   it made, so the "yes" works only once because of the claim.
4. **A claim (step 7a).** `Manager.Claim` checks the signature, the expiry, the
   tenant, the hash, that the action still waits, that the payload has the
   stored hash, and that the mode is `in_band`. Then one database statement
   moves the action from `pending` to `approved`. It is a compare-and-set (it
   changes the row only if the row still has the expected value). If two callers
   claim one action, one wins and the other gets `ErrNotPending`. Before the
   claim, `claimAnswer` checks that the action is of the right kind and account,
   so the "yes" of one account cannot act on another.
5. **A compare (step 8a).** `samePreview` compares the preview of the message
   built again with the stored preview: the sender, the recipients with their
   display names, the subject, the attachments, the start of the text, the
   counts of what is not shown, the warnings, and `BodyHash`, a hash of
   everything that goes out. It is needed because a reply or a forward reads the
   original message again, so the same request could give another message. If
   the previews differ, the action is rejected and the error wraps
   `confirm.ErrPayloadChanged`.

The order of the last two differs between the channels. In band, the second call
builds the message first (step 6a), and then `answerSend` claims (step 7a) and
compares (step 8a). On the approval page (steps 7b and 8b), `approveSend` builds
the message again with `rebuildApproval`, compares, checks the send limits, and
only then moves the action to `approved` with `Transition`. `rebuildApproval`
also makes the page, so the page shows exactly what Approve sends. If the page
cannot build the whole message, Approve fails too, and the person can still
reject.

A claim proves that the payload is the stored one, that the request is for this
tenant, that it is in time, and that it happens once. It does not prove consent.
Consent is the answer of the person in a channel that the agent cannot answer:
the form of the client, or the Approve button of the page. The service acts only
on `ConfirmAnswer.Accepted`, which `mcpserver` sets only for a real `accept` of
the form. So the "yes" is only as good as the AI app that shows the form (see
[Confirmations](/manual/daily-use/confirmations/#trust-in-the-ai-app)). The
payload and the preview are sealed (encrypted) in the database, and the
statement that ends the action deletes both.

## Delivery and the undo window

`deliver` is step 9. It calls `Limiter.Reserve` for one send. The limiter (the
part that counts sends per hour and per day) checks the hour limit and the day
limit and counts in one transaction (a group of database steps that all happen
or none do), so a declined or refused send uses no quota. A full limit gives a
`*policy.RateLimitError` that says when to retry. Then `deliver` calls
`Outbox.Enqueue` with the undo window: `UndoWindowAuto` when nobody confirmed,
and `UndoWindowConfirmed` after a confirmation.
[Permissions](/tech/inside-the-server/permissions/) has the defaults. If the
limiter or the outbox fails, the action is rejected and nothing is sent. Once
the outbox has the message, it owns it, and the action ends as `done` with the
id of the outbox item.

- **Step 10a, a window of 0.** The item is stored already claimed, as `sending`.
  `sendInline` sends it before `Enqueue` returns, in the goroutine of the caller
  (a goroutine is a function that Go runs at the same time as the rest of the
  program). The result is `sent`, `failed`, or `queued` after a transient error.
  So a send with no window runs inside its tool call or its web request, and
  this is why a stop waits for such calls.
- **Step 10b, a longer window.** The item is stored as `queued`. Its
  `SendAfter` is the end of the undo window, and `SendResult.SendAt` gives the
  same time to the agent. A worker (a background function that sends the queued
  messages) sends it when it is due.

`SendResult.SendID` is the id of the action while it waits, and the id of the
outbox item after it. `SendStatus` follows the action to its result, so
`get_send_status` follows one send to its end.

## The outbox

{{< diagram "send-states" >}}

The drawing has two state diagrams. A pending action (`confirm`) starts as
`pending`. It becomes `approved` when the "yes" is claimed or the person presses
Approve. It becomes `rejected` after a no, after a cancel by the agent, or when
the message changed. It becomes `expired` when its time is over. An `approved`
action becomes `done` when the work is done, `rejected` when a refusal comes
after the claim, and `expired` when nothing finished it (see [Startup and
shutdown](/tech/inside-the-server/startup-and-shutdown/)). `done`, `rejected`
and `expired` are final, and the payload and the preview are deleted then. An
outbox item starts as `queued`, or as `sending` when the window is 0. It ends as
`sent`, `failed` or `cancelled`. The doc comment of
{{< src "internal/outbox/outbox.go" >}} has the rules of the outbox:

- **One send.** A send starts only after an atomic claim, one statement that
  moves the item from `queued` to `sending`. Two workers, or two programs,
  cannot both win it. The network send runs outside any transaction.
- **Retries.** A transient error puts the item back in the queue with a wait of
  30 seconds, then 2 minutes, then 10 minutes (`retryDelays`). The fourth failed
  try ends the item as `failed`.
- **Unknown results.** `SendDeadline` is 13 minutes from the claim. A send that
  runs into it is marked `failed` and is not tried again, because the server may
  have the message. An error made with `outbox.Permanent` also ends the item as
  `failed` with no retry. An item that stays `sending` for 15 minutes
  (`recoverAfter`) is marked `failed` by `RecoverInterrupted`, which the
  programs that run the worker (`serve` and `setup`) call at their start and
  then about once a minute (`recoverEvery`).
- **A send that does not start.** If the program was told to stop after the
  claim and before the send, or less than 30 seconds (`minSendBudget`) of the
  send time is left, the item goes back to the queue. It is not a failed try.
- **Many accounts.** The worker looks every second (`outboxTick`). Each worker
  has at most one send in progress for an account, and at most four sends run
  at the same time in all (`maxParallelSends`). So a slow mail server does
  not delay the other accounts.
- **A stop.** A stop waits for the sends that run, sends the queued messages
  that are due within 5 minutes (`drainWait`), and leaves the others queued for
  the next start. See [Startup and
  shutdown](/tech/inside-the-server/startup-and-shutdown/).

The sender in {{< src "internal/app/sender.go" >}} does the send. It checks the
level of the account again at each try, because a message can wait for minutes.
A level that no longer allows a send makes the item fail for good, also if the
level goes up again. When the server refuses the stored login, it marks the
account for a reconnect. After the server has accepted the message, it moves the
draft to the Trash, and a failure of that step is only logged, because an error
would make the outbox send the message again. `sendError` makes an error
permanent when a retry could send the message twice, or would get the same
answer. It tests the types of the errors, never their text.

## Cancel

The agent has `cancel_send`, which needs level 4. For a `queued` item it calls
`Outbox.Cancel`. For an action that waits for the person, it moves the action to
`rejected` with the result `cancelled`. The person can cancel on the Activity
page and with `email-mcp outbox cancel`, at any level, so the person can lower
the level to Off and then cancel what waits (`CancelQueuedSend` in
{{< src "internal/service/queue.go" >}} and
{{< src "cmd/email-mcp/outbox.go" >}}). Once a send is running, the cancel is
too late (`outbox.ErrTooLate`). While the program stops, it refuses every tool
call, `cancel_send` too. [Startup and
shutdown](/tech/inside-the-server/startup-and-shutdown/#close-six-steps) says
how the person can still cancel during a stop.

## Trash uses the same path

`Service.Trash` has the same parts with the kind `trash`. It needs level 5, the
switch **Confirm deletes** gives the question (the reason is `confirm_deletes`),
and the daily limit is counted when the messages move (`trashNow`). The preview
lists up to 20 messages (`trashPreviewMessages`). The payload is the sorted list
of refs (the opaque ids that name messages), and the action is tied to the refs
themselves, so there is no compare: an IMAP (Internet Message Access Protocol)
message does not change under its UID (unique identifier), the number that names
it in a folder.

## Related pages

- [Permissions](/tech/inside-the-server/permissions/)
- [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/): what a
  stop does to a send
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [How each AI app confirms](/manual/daily-use/app-confirmation/) in the user
  manual
