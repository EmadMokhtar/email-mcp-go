---
title: Startup and shutdown
description: The order in which email-mcp builds, runs and stops its parts, and why that order means that a send is never sent twice, and that a send that is cut off is recorded as failed (delivery unknown).
weight: 50
code_areas:
  - internal/app/app.go
  - internal/app/kek.go
  - internal/app/lock_unix.go
  - internal/app/lock_windows.go
  - internal/app/sender.go
  - internal/service/service.go
  - internal/inflight/inflight.go
  - internal/outbox/outbox.go
  - cmd/email-mcp/serve.go
  - cmd/email-mcp/main.go
  - cmd/email-mcp/setup.go
  - cmd/email-mcp/console.go
---

This page explains the order in which the program builds its parts, runs its
background work and stops. The order matters because a send must never be sent
twice, and a send that may not have gone out must show as failed and not
disappear. Read it before you add a part that `app.Open` builds, a background
job, or a step to the stop.

The package `app` in {{< src "internal/app/app.go" >}} builds the parts and
connects them. A command opens an `App`, and a long-running command (`serve`,
`setup`) also runs it. A background job is work that goes on while the program
serves, such as the outbox worker. The outbox is the queue of messages that wait
to go out.

## Open: six steps

{{< diagram "open-and-close" >}}

`app.Open` builds the parts in this order. Each step needs the ones before it:

1. **The logger.** It sets the default logger to a text log on standard error
   (the stream where a program writes messages about itself). Standard output
   stays free for the MCP (Model Context Protocol) messages on stdio (standard
   input and output: the client starts the server and talks to it through two
   pipes).
2. **The store.** `store.Open` opens the database in the data folder and runs
   the migrations that have not run (a migration is a numbered file that changes
   the structure of the database). Then `EnsureLocalTenant` makes sure that the
   tenant `local` (the owner of the accounts of a local install) exists.
3. **The master key and the vault.** `loadKEK` finds the master key (the key
   that protects the other keys), or makes it on the first start, under a lock
   on a file (see [Storage and
   secrets](/tech/inside-the-server/storage-and-secrets/)). `tryLock` in
   {{< src "internal/app/lock_unix.go" >}} and
   {{< src "internal/app/lock_windows.go" >}} takes the lock.
   With `ExistingKeyOnly`, which `doctor` uses, `Open` never makes a key and
   takes no lock. Then it builds the vault, the part that encrypts and decrypts
   the secrets.
4. **The signer.** Its key comes from the server key of the vault. The vault
   makes the server key, wrapped by the master key, when the database has none.
5. **The other parts.** The limiter (it counts sends and Trash moves), the
   confirm manager (it keeps the actions that wait for the person), the backend
   factory (it makes the mail backends, the parts that talk to the mail
   servers), the outbox, the audit log (the activity log), the tokens (the
   secrets that programs use to call the local server) and the discovery (it
   finds the mail settings of an address). The backend factory comes before the
   outbox, because the sender of the outbox (`newSender` in
   {{< src "internal/app/sender.go" >}}) uses it to reach the mail server.
6. **The web server, then the service.** The web server is the local web site of
   the person, and the service runs the use cases. The service needs the links
   of the web server, so the web server comes first. Then `Attach` gives the
   service to the web server, so each one has the other.

`Open` writes to the database in steps 2 and 4: the migrations, the row of the
local tenant and the server key, each only when it is missing. It starts no
background work.

## Run: clean up, then three jobs

`App.Run` starts the background work. It does nothing after the first call, and
nothing after `BeginStop`. Before it returns, it cleans up after a program that
stopped in the middle of its work:

- `Outbox.RecoverInterrupted` marks every item that has been `sending` for more
  than 15 minutes (`recoverAfter`) as `failed`, with the text "delivery
  unknown". The mail server may have the message, so the item is never sent
  again.
- `expireStaleApproved` ends the approved actions that have no result and whose
  time ended more than 15 minutes ago (`staleApprovedGrace`). Nothing finished
  them, and nothing is left to finish them.

The recovery comes first because the worker must not take an item before it.
Then `Run` starts three goroutines (a goroutine is a function that Go runs at
the same time as the rest of the program):

- the outbox worker, the function that sends the queued messages, which looks
  for due items every second (`outboxTick`);
- the daily jobs, which run at the start and then every 24 hours (`jobEvery`);
- the sweep that closes the mail backends that nobody used for 30 minutes
  (`backendIdle`), every 5 minutes (`backendSweepEvery`).

The web server starts only when a link needs it. [Architecture
overview](/tech/start-here/overview/) explains the run modes and why several
programs can use one database.

## Close: six steps

`BeginStop` and `Close` stop the program. `Close` calls `BeginStop` first, and a
command can call it earlier, when it learns that it must stop. A second call of
either does nothing.

| Step | What it does | Waits? |
|---|---|---|
| 1 | `BeginStop`: no send, Trash move, approval or tool call can start. | No |
| 2 | It cancels the context of `Run` (a Go value that tells work to stop). The outbox worker finishes the send that it runs and writes its result. | Yes, with no deadline |
| 3 | It waits for the tool calls and the service actions that run. | Yes, with no deadline |
| 4 | `Outbox.Drain` sends the queued messages that are due, or that become due within `drainWait`, 5 minutes. The messages that are left stay queued for the next start. | Yes, until the deadline |
| 5 | It shuts down the web server. | Until the requests end |
| 6 | It closes the mail backends and the store. | No |

Step 1 makes the service refuse `Send`, `Trash` and `ApprovePending` with
`ErrStopping`, and `trackToolCalls` refuse every tool call. An action that waits
for the person keeps waiting. If the stop lasts more than a second, one log line
says so (`slowStopNotice`).

Two counters of the type `inflight.Tracker` in
{{< src "internal/inflight/inflight.go" >}} make steps 1 and 3 work. The counter
of `App` counts the tool calls (`trackToolCalls`), and the counter of the
service counts its actions. `Stop` makes a counter refuse new work. It returns a
channel that is closed when no counted work runs. `BeginStop` calls `Stop` on
both counters, and step 3 waits for the two channels (`callsIdle` and
`actingIdle`).

**Why steps 2 and 3 ignore the deadline.** A send that has started must finish
and write its result. If it were cut off, its item would end as "delivery
unknown". A send without an undo window runs inside its tool call or its web
approval, which is why step 3 waits for those too.

**Why the web server stays up in step 4.** The web server starts only when a
link needs it. When this program already runs it, the person can still reject an
action that waits, and can still cancel a queued send on the Activity page.
Nothing else in the process can cancel a queued send, because tool calls are
refused. The command `email-mcp outbox cancel` works from another process in
every case.

**Why a stop has a budget.** `StopTimeout` is `drainWait` (5 minutes) plus
`outbox.SendDeadline` (13 minutes) plus 2 minutes, which is 20 minutes. A stop
takes at most about 20 minutes, and usually a few seconds. The drain starts no
send later than `SendDeadline` before the deadline (`drainContext`), so every
send that it starts can end in time. When the time is over, the messages that
are left stay queued and go out at the next start. That is still a clean stop,
and the log says it.

## The signals and a second Ctrl-C

`main` in {{< src "cmd/email-mcp/main.go" >}} listens for `os.Interrupt` (the
signal that Ctrl-C sends: hold the Ctrl key and press C) and SIGTERM (the signal
that asks a program to stop). The first signal starts the clean stop. Then
`main` gives the default handling back, so a second signal ends the program at
once. A send that is cut off then stays `sending`, and it is never sent again. A
program that runs the worker marks it `failed` ("delivery unknown") when it is
15 minutes old.

## Stdio and HTTP

The command `serve` can serve MCP on stdio or on HTTP (HyperText Transfer
Protocol). {{< src "cmd/email-mcp/serve.go" >}} stops each way in its own way,
with one budget. `stopBudget` starts when the stop starts: at the first signal,
where `context.AfterFunc` calls `BeginStop` and starts the budget, or when the
client closes stdin. Every part of the stop gets the same deadline, so the parts
do not add up.

- **Stdio.** `serveStdio` returns when the client closes stdin, or when a signal
  ends the context. Then `serve` calls `Close` with the budget.
- **HTTP.** `serveHTTP` calls `BeginStop` when the context ends. It stops taking
  connections and waits for the requests that run, within the budget. If they do
  not end in time, it closes their connections, and a send that has started
  still finishes in the outbox. Then `serve` calls `Close`.

The command `setup` in {{< src "cmd/email-mcp/setup.go" >}} runs the `App` too,
and serves the web portal until Ctrl-C. It stops in the same way: the first
signal calls `BeginStop` and starts the budget, and then `setup` calls `Close`.

## Short commands

A command such as `account list` opens the `App`, does one job and leaves. It
never calls `Run` and it queues no message. `closeApp` in
{{< src "cmd/email-mcp/console.go" >}} calls `CloseNow`, which is `Close`
without step 4. The queue belongs to the program that serves, and that program
sends the messages when they are due. Without `CloseNow`, each short command
could wait up to 5 minutes (`drainWait`) for the queue of another program.

## After a crash

The next start cleans up by itself. The operating system releases the lock of
the master key when a program ends, so a crash leaves no lock. After a crash:

- A queued message stays queued, and the worker sends it when it is due.
- A message that was `sending` becomes `failed` when it is 15 minutes old, and
  it is not sent again. `RecoverInterrupted` looks for such items at the start
  and then about once a minute (`recoverEvery`). The person sees the failed item
  and decides.
- An approved action with no result expires. An action that still waits for the
  person keeps waiting until its time ends.

## Related pages

- [The send flow](/tech/inside-the-server/send-flow/)
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
- [Architecture overview](/tech/start-here/overview/)
- [One tool call, step by step](/tech/start-here/tool-call/)
