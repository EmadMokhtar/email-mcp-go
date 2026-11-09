---
title: Where the data lives
description: Find the folder where email-mcp keeps its data, and see what it stores.
weight: 20
code_areas:
  - cmd/email-mcp/console.go
  - internal/app/app.go
  - internal/app/kek.go
  - internal/secrets/
  - internal/store/
---

After this page, you know where email-mcp keeps its data and what it stores.
The data folder is the folder that holds the database of email-mcp.

## The data folder

| System | Data folder |
|---|---|
| macOS | `~/Library/Application Support/email-mcp/` |
| Linux | `$XDG_DATA_HOME/email-mcp/`, or `~/.local/share/email-mcp/` |
| Windows | `%AppData%\email-mcp\` |

The sign `~` stands for your home folder. `XDG_DATA_HOME` is a standard setting
that says where programs keep their files on Linux. `%AppData%` is the folder
where Windows keeps the data of your programs.

`--data-dir` or `EMAIL_MCP_DATA_DIR` chooses another folder. `--data-dir` is a
flag of a command (see [Commands](/manual/reference/commands/)).
`EMAIL_MCP_DATA_DIR` is an environment variable (a named setting that a program
reads from its surroundings). When both are set, the flag wins. email-mcp makes
the folder with mode 0700 (only you can open it) and the database file
`email-mcp.db` with mode 0600 (only you can read and write it). Keep both
private if you copy or restore the folder. `email-mcp doctor` (the command that
checks the install) checks them. The database is SQLite (a database that
lives in one file) in WAL mode (write-ahead log: a mode that lets several
programs use one database file safely), so several email-mcp programs can use
it at once. On Windows, `email-mcp doctor` does not check these two modes. See
[Troubleshooting](/manual/daily-use/troubleshooting/#start-with-doctor).

## What is stored

email-mcp does not store your mail. It stores your account settings, the
encrypted passwords, the messages that wait in the outbox (the queue of messages
that wait to go out) until they are sent, the sends and the moves to the Trash
that wait for your approval (stored encrypted, and removed when the action
ends), and the activity log (the list of what the agent did, with the recipient
lists and the arguments of tool calls encrypted). The activity log never holds a
message body or attachment data. The
agent is the AI (artificial intelligence) program that uses the tools of
email-mcp. A tool is one action that the agent can call, such as
`search_messages`. The page [Activity and
undo](/manual/daily-use/activity-and-undo/) explains the log.

The master key, which protects the encrypted passwords, is never stored in the
database. See [The master key](/manual/reference/master-key/).

If a command says that the data folder or the database file does not exist, see
[The data folder does not
exist](/manual/daily-use/troubleshooting/#the-data-folder-does-not-exist). If
it says that a mode is wrong, see [A wrong
mode](/manual/daily-use/troubleshooting/#a-wrong-mode).
