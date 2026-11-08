---
title: The command line
description: How the commands of email-mcp are registered and tested, what each one opens and closes, and the rules for passwords, prompts and exit codes.
weight: 10
code_areas:
  - cmd/email-mcp/
  - internal/connect/connect.go
  - internal/app/app.go
---

This page explains how the commands of email-mcp are built: how they are
registered and tested, what each one opens, how they read passwords and
answers, and how to add one. Read it before you change a file in
`cmd/email-mcp`. A command is a word after the name of the program, such as
`serve` or `account add`. A flag is an option after it, such as `--data-dir`.
The user manual lists each command and flag for the person: see
[Commands](/manual/reference/commands/).

## How commands are registered and tested

The variable `commands` in {{< src "cmd/email-mcp/main.go" >}} lists the
top-level commands, in the order that the help shows them. Each entry is a
`command`: a name, a summary of one line, and a `run` function. `runWith` reads
the first argument and calls the `run` of the command with that name. `help`,
`-h`, `-help` and `--help` print the list on standard output. A name that is not
in the list gets the list on standard error, with the text "unknown command".
The text does not repeat the name, because the person may have typed a password
there by mistake.

A group, such as `account`, `token`, `outbox` or `key`, has its own list:
`accountCommands`, `tokenCommands`, `outboxCommands` and `keyCommands`.
`runGroup` in {{< src "cmd/email-mcp/account.go" >}} picks the subcommand in the
same way. Most commands and groups have their own file. The table in [What
each command opens](#what-each-command-opens) names them.

`main` calls `run`, which gives `runWith` the real terminal (the window where
the person types commands), the real browser
and the real discovery. Discovery is the part that suggests the mail server
settings of an address. The type `system` in
{{< src "cmd/email-mcp/console.go" >}} holds these parts as four functions:
`isTerminal`, `readPassword`, `openBrowser` and `discover`. `realSystem` returns
the real ones. A test gives `runWith` its own.

The tests of the commands are in {{< src "cmd/email-mcp/cli_test.go" >}}.
`runCLI` runs `runWith` with a `fakeSystem`: a terminal or not, the passwords
that the person "types", a browser that only records the links, and a discovery
with fixed answers. So no test needs a terminal, and no test opens a browser.
`runCLI` returns the exit code, standard output and standard error, and
`requireNoLeak` checks that a secret is in neither of them. The tests that
open data use a real data directory (the folder of the database) in a temporary
folder, with a key file as the master key (the key that protects the other
keys). The fake mail servers `imaptest` and
`smtptest` stand in for IMAP (Internet Message Access Protocol, for reading
mail) and SMTP (Simple Mail Transfer Protocol, for sending mail).

Two tests guard rules that hold for many commands:

- `TestHelpListsTheCommands` checks that the help names each command and each
  subcommand of `account`, and that a group without a known subcommand exits
  with 2 and does not repeat the name.
- `TestNoArgumentIsEchoed` gives a secret value in each place where a person
  could type one by mistake: an unknown command, an extra argument, an unknown
  flag, a bad value. It checks that each command exits with 2, that the value
  is on neither output, and that no data directory was made.

## What each command opens

Most commands open the `App`, call one use case (a function of `service` that
does one job for a caller) and close it. The `App` is what `app.Open` in
{{< src "internal/app/app.go" >}} builds: all the parts of the program,
connected. The way a command closes it matters. `App.Close` drains the outbox
(the queue of outgoing mail): it sends the queued messages that are due.
`App.CloseNow` does not. `serve` speaks MCP (Model Context Protocol) on stdio
(standard input and output: the AI app starts the program and talks to it
through two pipes), or on local HTTP (HyperText Transfer Protocol) with
`--http`. The stop budget is one time limit for the whole stop.

| Command | File | What it opens, and how it closes |
|---|---|---|
| `serve` | {{< src "cmd/email-mcp/serve.go" >}} | `app.Open`, then `App.Run` (the background work), then MCP on stdio or on local HTTP. At the stop, `App.Close` with the stop budget. |
| `setup` | {{< src "cmd/email-mcp/setup.go" >}} | `app.Open` and `App.Run`, as `serve` does, then the web portal, which `NewSetupURL` starts. It serves the portal until the stop, then `App.Close` with the stop budget. With `--revoke-sessions`: `app.Open`, `RevokeSessions`, and `CloseNow`, with no `Run`. |
| `account add`, `list`, `reconnect`, `remove`, `test` | {{< src "cmd/email-mcp/account.go" >}}, {{< src "cmd/email-mcp/reconnect.go" >}} | `openApp`, one use case of `service`, then `closeApp`, which calls `CloseNow`. |
| `token create`, `list`, `revoke` | {{< src "cmd/email-mcp/token.go" >}} | The same. `list` reads `tokens.Manager.List`. |
| `outbox list`, `cancel` | {{< src "cmd/email-mcp/outbox.go" >}} | The same. |
| `import-env` | {{< src "cmd/email-mcp/importenv.go" >}} | First `parseEnvFile` reads the `.env` file of the old email-mcp. Then the connect flow (see below), between `openApp` and `closeApp`. |
| `key rotate` | {{< src "cmd/email-mcp/key.go" >}} | `openApp`, `App.RotateKEK`, then `closeApp`. |
| `doctor` | {{< src "cmd/email-mcp/doctor.go" >}} | It checks the data directory and the database file first, and stops when one is missing. Then `app.Open` with `ExistingKeyOnly` (load the master key, never make one), `ProbeAccount` for each account, and `closeApp`. |
| `version` | `main.go` | Nothing. |

A short command never calls `Run` and queues no message. So the queue belongs to
the program that serves, and `CloseNow` leaves it to that program. The page
[Startup and
shutdown](/tech/inside-the-server/startup-and-shutdown/#short-commands)
explains why, and how `serve` and `setup` stop on Ctrl-C (hold the Ctrl key and
press C).

`parseEnvFile` is strict. A line is empty, a comment, or `NAME=VALUE`. A quoted
value keeps its text as it is, with no escapes. A name that comes twice is
refused. An error gives the number of the line, never its text, because a line
can hold a password. `readEnvFile` reads at most 64 KiB (`maxEnvFileBytes`; a
KiB is 1,024 bytes).

## Passwords, prompts and exit codes

### Exit codes and flags

`run` returns the exit code of the program (the number that a program gives
back when it ends): `exitOK` (0) when the command did its job, `exitError` (1)
when it failed, and `exitUsage` (2) for a wrong use, such as an unknown command,
a bad flag or an extra argument. `doctor` returns 1 when one of its checks
fails.

Each command makes its own `flag.FlagSet` with `newFlags`. The flag set writes
nothing, because the messages of the `flag` package repeat the text of a bad
flag. `parseOnce` prints a fixed text instead, and `flagProblem` names a flag
only when the command has a flag of that name. `-h` prints the flags on standard
output. For `serve` and `version` it prints them on standard error: on stdio,
standard output carries only the MCP messages. `parseFlags` lets flags come
before and after the other arguments, such as `remove ID --yes`, and stops at
`--`. `unexpected` names the place of an extra argument, never its text.

### Output and passwords

The comment at the top of {{< src "cmd/email-mcp/console.go" >}} gives the rules
of the output:

- Standard output gets what the person asked for, and the questions. It may show
  the address of the person's own account, and a new token or the setup link
  once. `token create` prints its notes on standard error and the token alone on
  standard output.
- Standard error gets the errors, the warnings and the log of the `App`. Nothing
  there holds an address, a password, a token or the setup link.
- A password is never a flag, because other users of the computer can read the
  arguments of a process. `readPassword` reads it from the terminal without
  echo (the typed characters are not shown), or from the first line of standard
  input when standard input is not a terminal.

`readTerminalPassword` puts the terminal back as it was when the first Ctrl-C
comes during the read, so the echo does not stay off after a second Ctrl-C.
`TestReadTerminalPasswordRestoresOnCtrlC` checks it. `openBrowser` gets one
argument, which must never hold a secret. So `setup` opens the setup link
through a private file: see [Signing in](/tech/ways-in/web-portal/#signing-in).

### Prompts

`ask` writes a question on standard output and reads one line of standard input,
of at most 64 KiB (`maxLineBytes`). `confirm` takes only `y` or `yes`, in any
case, as a yes. `confirmTyped` needs the typed word `yes`: it is the second,
explicit confirmation of a choice with a risk. Without a terminal, nobody can
answer, so a command that asks first needs a flag instead: `account remove` and
`key rotate` need `--yes`. The connect flow below has its own flags.

## What the commands share with the web portal

The commands and the web portal call the same use cases of `service`, such as
`CreateIMAPAccount`, `ReconnectIMAPAccount`, `RemoveAccount`, `CreateToken` and
`CancelQueuedSend`. Both are the person's door. `userContext` in
`console.go` gives each call the principal (the caller of a request) of the
person at the terminal: the local tenant (the owner of the accounts), no scope
limit (a scope is a permission that an access token carries), and the client
name `cli`. The web portal uses the tenant of the browser session and the client
name `web`.

To connect an account, `account add` and `import-env` share the connect flow of
{{< src "cmd/email-mcp/connect.go" >}}. `CreateIMAPAccount` tests the settings
and saves the account, but it does not decide about a certificate (the signed
proof of identity that a mail server shows) or a plain connection. The command
line decides with the rules of {{< src "internal/connect/connect.go" >}}, the
package that the web portal uses too:

- A plain (unencrypted) connection is allowed only to a server on this
  computer, with `--plaintext-i-understand` or a typed `yes` on a terminal. To
  another host it is refused, also with the flag.
- The person may trust (pin) a certificate only when `connect.CheckCertificate`
  allows it. On a terminal, the person sees the host, the port and the SHA-256
  fingerprint (SHA is Secure Hash Algorithm; the fingerprint is a short value
  computed from the certificate), and answers `y`. A server that is not on this
  computer also needs a typed `yes`.
- Without a terminal, `--trust-fingerprint` must be the fingerprint that the
  server showed in this run. A server that is not on this computer also needs
  `--accept-remote-certificate`.
- A pin is saved only for the server (protocol, host and port) that showed the
  certificate. One connect makes at most 3 tests (`maxConnectTests`).
- A failed test prints the fixed text of `connect.ErrorText`, never the text of
  the server.

`account reconnect` follows the same rules for a server that had no pin. For a
pin that changed, `connect.CheckReconnect` decides. On a terminal the person
types `yes`, for any host. Without a terminal, the command needs
`--trust-fingerprint` with the new fingerprint, and
`--replace-pinned-certificate`. The page [Connecting an
account](/tech/mail/connecting-an-account/#the-shared-package) explains the
package `connect`.

## Add a command

1. Write a `run` function in a new file of `cmd/email-mcp`:
   `func runName(ctx context.Context, c *cli, args []string) int`. For a group,
   give it its own list of `command` and call `runGroup`.
2. Add it to `commands` in `main.go`, where it should appear in the help.
3. Make the flags with `c.newFlags`, read them with `c.parseFlags`, and refuse
   extra arguments with `c.unexpected`. Never take a secret as a flag.
4. Open the `App` with `c.openApp` and close it with `defer c.closeApp`. A
   command that serves calls `Run` and closes with `App.Close` and a
   `stopBudget`, as `serve` and `setup` do.
5. Call the use cases of `service` with `userContext(ctx)`, so that each call
   runs as the person at the terminal.
6. Follow the output rules. Read a secret with `c.readPassword`. Ask before a
   change with a risk, with `c.confirm` or `c.confirmTyped`, and offer `--yes`
   for a run without a terminal.
7. Test it in `cli_test.go` with `runCLI` and `fakeSystem`. Add its name to the
   list of `TestHelpListsTheCommands`, and its arguments to
   `TestNoArgumentIsEchoed`.
8. Add it to the page [Commands](/manual/reference/commands/) of the user
   manual.

## Related pages

- [Local HTTP mode](/tech/ways-in/local-http/)
- [The web portal](/tech/ways-in/web-portal/)
- [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/)
- [Connecting an account](/tech/mail/connecting-an-account/)
- [Commands](/manual/reference/commands/) in the user manual
