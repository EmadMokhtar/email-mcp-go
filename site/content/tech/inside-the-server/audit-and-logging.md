---
title: Activity log and program log
description: The two logs of email-mcp, who reads each one, what they hold, how long they keep it, and the rules for writing to them.
weight: 40
code_areas:
  - internal/audit/audit.go
  - internal/mcpserver/capture.go
  - internal/mcpserver/server.go
  - internal/service/calls.go
  - internal/service/write.go
  - internal/errtype/errtype.go
  - internal/secrets/secret.go
  - internal/app/app.go
  - internal/httpserve/httpserve.go
  - internal/web/activity.go
---

This page explains the two logs of email-mcp, who reads each one, and the rules
for writing to them. Read it before you add an audit entry or a log line. The
user view of the first log is in [Activity and
undo](/manual/daily-use/activity-and-undo/).

email-mcp has two logs that follow different rules. The activity log is a table
in the database. The person reads it on the Activity page of the web portal (the
local web site of the person). The program log is text on standard error (the
stream where a program writes messages about itself). The person who runs the
program reads it.

## The activity log

The package `audit` in {{< src "internal/audit/audit.go" >}} keeps the activity
log. Each row is an entry of one of two kinds:

- A **call entry** is one `tools/call` request of the agent. The agent is the AI
  (artificial intelligence) program that calls the tools of email-mcp, and a
  tool is one action that it can call. Every tool and every outcome has a call
  entry: a read, a refusal, a call with wrong arguments, and a call of a tool
  that does not exist.
- An **action entry** is what a use case of `service` did, or what the person
  did on a page. A use case is one job of the program for a caller, such as
  "send this mail".

One send makes one call entry and one or more action entries: the question, and
later the result.

Where the entries come from:

- `capture` in {{< src "internal/mcpserver/capture.go" >}} is a middleware (a
  function that wraps the handling of every request) of the SDK (software
  development kit: the official Go library for MCP, the Model Context Protocol).
  It writes the call entry when a call ends, through `Service.RecordToolCall` in
  {{< src "internal/service/calls.go" >}}. It sits outside the tool handler,
  because the SDK checks the tool name and the arguments before it runs a
  handler, and only a middleware sees those calls. A call that the program
  refuses while it stops never reaches `capture`, so it has no entry.
- The use cases write the action entries with `record`, `finish` and
  `recordOutcome` in {{< src "internal/service/write.go" >}}. The web pages go
  through the same use cases.

Both end in `audit.Log.Record`.

### The fields of an entry

A tenant is the owner of a set of accounts. To seal a value is to encrypt it
with the key of the tenant. [Storage and
secrets](/tech/inside-the-server/storage-and-secrets/) explains how.

| Field | What it holds | How it is stored |
|---|---|---|
| `Time` | The time from the clock of the log, in whole milliseconds | Plain |
| `Kind` | `call` or `action` | Plain |
| `Tenant`, `Account`, `Area` | The ids. The account is empty for an entry of the tenant. | Plain |
| `Client` | The name that the MCP client (the AI app) gave for itself | Plain, cleaned, at most 128 bytes |
| `TokenID` | The id of the access token of the call, never the token | Plain |
| `Tool` | The name of the tool, or `unknown_tool` | Plain |
| `Count` | The number of items the call affected | Plain |
| `Recipients` | The recipients of the envelope, at most 200 | Sealed |
| `Outcome` | `ok`, `refused`, `declined`, `pending_approval` or `error` | Plain |
| `Detail` | A short code such as `level_too_low` | Plain |
| `Arguments` | The JSON (JavaScript Object Notation) of the arguments of a call | Sealed |

The table repeats the rules of `Detail` and the others with `CHECK` conditions
(rules that the database enforces on each row).

### What is kept and what is dropped

- **No message content.** `omittedArguments` in `capture.go` lists the arguments
  that are never kept: `body`, `html_body` and `data_base64`. The entry keeps
  their size instead. A test makes sure that every argument of every tool is
  either in the list or not there on purpose.
- **A call that passed the schema** (the description of the arguments that the
  tool accepts) keeps its argument values. A long text is cut, with a mark that
  says how long it was (`maxArgumentText`). Arguments that are very large become
  a mark with their size (`audit.MaxArgumentBytes`).
- **A call that failed the schema** keeps only the names and the sizes of its
  arguments, at most 4 KiB (`maxRejectedArgumentBytes`; a KiB, kibibyte, is
  1,024 bytes). The text of a message could be under any name, so the log keeps
  no value. The name of an unknown tool is text from the agent. It goes in the
  sealed arguments, and the column holds `unknown_tool`.
- **`Client` is not trusted.** The client chooses its name, and the log watches
  the client. So `Record` cleans the name: invalid UTF-8 (Unicode Transformation
  Format, 8 bits) and control, format, line and paragraph separator characters
  become U+FFFD, and the name is cut to 128 bytes. `Record` never refuses an
  entry because of the name, because a bad name must never hide an action.
- **`Detail` is a code, never free text.** It has 0 to 64 characters from `a-z`,
  `0-9` and `_`. `Tool` is not empty, and it is at most 128 bytes (`maxText`) of
  valid UTF-8 with no control, format, line or paragraph separator character.
  `TokenID` has 0 to 64 characters from `a-z` and `0-9`. All three come from the
  server. `Record` refuses an entry with a bad one (`ErrInvalidEntry`), and its
  error never shows a value.

### Rules for writers

- A failure to write an entry does not change the answer of the call. The action
  has happened or was refused, and the agent must learn that. The program log
  gets a fixed code, the tool and the outcome.
- The write does not stop when the caller leaves, because the action has
  happened. It has a time limit of 30 seconds (`storeTimeout`), and 5 seconds
  (`callStoreTimeout`) for a call entry, which the agent waits for.
- A call without a caller has no entry, because a row needs a tenant. The
  program log gets a fixed code instead.

### How long entries stay, and who reads them

`App.dailyJobs` deletes the entries that are older than 90 days
(`auditRetention`). The entries of an account go with the account
(`ON DELETE CASCADE` is a rule that deletes the rows that belong to a deleted
row). The Activity page in {{< src "internal/web/activity.go" >}} calls
`audit.Log.List`, newest first. It shows 50 entries by default and at most 200,
and at most 4 KiB of the arguments of an entry (`maxArgumentsShown`). `List`
fails if it cannot open the recipients of an entry. It never leaves them out
without a sign. Arguments that it cannot open do not stop the list: `List` sets
`ArgumentsUnreadable` on the entry, so one bad row does not hide what the agent
did.

## The program log

The program log is `slog` (the log package of the Go standard library) in text
form, on standard error. `app.Open` sets it as the default logger, and
`Config.LogOutput` can change the writer. The writer must never be standard
output. On stdio (standard input and output: the client starts the server and
talks to it through two pipes), standard output carries the MCP protocol, and
any other byte there breaks it.

The log holds ids, counts and fixed codes only. It never holds a secret, a link
with a token, an address, a subject, a body, or the text of an error, because
that text can hold text from a mail server. Five parts keep this true:

- **Fixed codes.** A line has a fixed message and a `code`, such as
  `audit_write_failed`, `pending_end_failed` or `http_server_error`.
- **`errtype.Chain`** in {{< src "internal/errtype/errtype.go" >}} returns the
  Go type names of an error and of the errors it wraps, at most six. A type name
  holds no text from the error. The log gets this, never the error.
- **`secrets.Secret`** prints `[redacted]`, so a value of this type is hidden in
  a line in the normal cases. Its doc comment lists what it does not cover.
- **The logger of the SDK is not set.** The SDK writes error texts to it.
  `mcpserver.New` gives it no logger, and `Options.Logger` is a different logger
  that only gets fixed codes.
- **`httpserve.ErrorLog`** in {{< src "internal/httpserve/httpserve.go" >}}
  makes the logger that `net/http` writes its own errors to. Such a text can
  hold a part of a request, so each line becomes one line with the fixed code
  `http_server_error`.

`safe` in {{< src "internal/mcpserver/server.go" >}} logs the name of the tool
and a fixed code for a refused caller, a panic, an error with no known text, a
timeout, a failed connection to the mail server and a cancel. It logs a known
refusal nowhere, because the agent gets its text and the activity log has the
entry.

### Add a log line

1. Use a fixed message and add a fixed `code`.
2. Add only ids, counts, fixed codes and the name of a tool. Never add a secret,
   an address, a subject, a body, a token or the text of an error.
3. For an error, add `errtype.Chain(err)`.
4. Keep a secret value in the type `secrets.Secret`, and check the comment of
   the type.
5. Write nothing to standard output.
6. If the person should see what happened, add an audit entry too, with a
   `detail` code.

## Related pages

- [The send flow](/tech/inside-the-server/send-flow/)
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [Activity and undo](/manual/daily-use/activity-and-undo/) in the user manual
