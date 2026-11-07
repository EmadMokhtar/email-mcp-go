---
title: The master key
description: Find out where your master key is, set it, make a key file, and change the master key.
weight: 30
code_areas:
  - internal/secrets/
  - internal/app/kek.go
  - cmd/email-mcp/key.go
  - cmd/email-mcp/doctor.go
---

After this page, you know where your master key is, how to set it, how to make
a key file, and how to change the master key. The master key is the key that
protects the passwords that email-mcp stores.

## What the master key does

email-mcp encrypts each password with AES-256-GCM (a standard encryption method
that also detects changes). The keys that do this are themselves encrypted with
a master key. The master key is never stored in the database.

The environment variable `EMAIL_MCP_KEK` says where the master key is. An
environment variable is a named setting that a program reads from its
surroundings. KEK means "key encryption key". There is no config file: every
start of email-mcp reads this variable.

| Value | Meaning |
|---|---|
| not set, or `keychain` | The default. A random key in the keychain of your operating system (its password store): service `email-mcp`, account `master-key`. |
| `file:<absolute path>` | A key file with mode 0600 (only you can read it). Use it where there is no keychain, for example on WSL (Windows Subsystem for Linux) or on a server over SSH (Secure Shell: a way to log in to another computer over a network). An absolute path is the full path of a file. The sign `~` (your home folder) is not part of a full path, so do not use it. |

## Which master key do you use?

Run this in a terminal (a window where you type commands). The command checks
the install, and one of its lines names your master key:

```sh
email-mcp doctor
```

The line looks like one of these:

```text
master key (keychain): ok
master key (file:/home/you/.email-mcp.key): ok
```

The text in the brackets is the value of `EMAIL_MCP_KEK` that `doctor` used.
`keychain` means that the master key is in the keychain. `file:` and a path
mean that the master key is in that key file. `doctor` reads the variable in
your terminal. It does not read the config of your AI app. If your AI app
config sets `EMAIL_MCP_KEK`, set the same value in the terminal before you run
`doctor` (see the next section). Otherwise `doctor` looks in the keychain.

If `doctor` stops before this line, because the data folder or the database file
does not exist, email-mcp has not opened that folder yet. The data folder is
the folder where email-mcp keeps its data (see [Where the data
lives](/manual/reference/data-folder/)). See [The data folder does not
exist](/manual/daily-use/troubleshooting/#the-data-folder-does-not-exist). The
page [Troubleshooting](/manual/daily-use/troubleshooting/) explains every
other line.

## Set EMAIL_MCP_KEK

Every program that uses the same data folder needs the same setting. So set the
variable in your terminal, and in the config of each AI app that starts
email-mcp.

In a terminal on macOS and Linux, this command sets the variable. `export`
makes the setting available to the programs that you start from this terminal.
The setting lasts until you close the terminal window. The path is an example:
use the full path of your own key file.

```sh
export EMAIL_MCP_KEK=file:/home/you/.email-mcp.key
```

On Windows, use this line in the Command Prompt:

```text
set EMAIL_MCP_KEK=file:C:\Users\you\.email-mcp.key
```

In PowerShell, use this line:

```text
$env:EMAIL_MCP_KEK = "file:C:\Users\you\.email-mcp.key"
```

In the config of an AI app, the setting goes in the `env` block of the server.
Each page below has the step for its app, in the section "If your master key is
a file": [Claude Code](/manual/getting-started/connect-ai-app/claude-code/),
[Claude Desktop](/manual/getting-started/connect-ai-app/claude-desktop/),
[Cursor](/manual/getting-started/connect-ai-app/cursor/), [VS
Code](/manual/getting-started/connect-ai-app/vscode/) and [Codex
CLI](/manual/getting-started/connect-ai-app/codex-cli/).

When Claude Code starts the server, pass the variable there too. The page
[Claude Code](/manual/getting-started/connect-ai-app/claude-code/) names the
source and the date of this command:

```sh
claude mcp add --scope user --env EMAIL_MCP_KEK=file:/home/you/.email-mcp.key \
  --transport stdio email -- email-mcp serve
```

(Put another flag, such as `--transport stdio`, between `--env` and the server
name. Otherwise Claude Code reads the name as another variable.)

If `doctor` says that the value is not valid, see the entry "The value of
EMAIL_MCP_KEK is not valid" of [Troubleshooting](
/manual/daily-use/troubleshooting/#the-value-of-email_mcp_kek-is-not-valid).

## The key file

### The format

A key file is a text file. It holds the master key as base64 text. Base64 is a
way to write bytes with letters, digits, `+`, `/` and `=`. The key has exactly
32 bytes, so the text has exactly 44 characters, and the last one is `=`. Spaces
and empty lines before and after the text are allowed. A line break inside the
text is not.

email-mcp refuses a file with other content. For example, a file with 32 raw
bytes, a file with a key in hex (letters and digits from 0 to 9 and a to f), an
empty file, or a short text. See [The master key file is not
valid](/manual/daily-use/troubleshooting/#the-master-key-file-is-not-valid).

### The mode 0600 rule

The mode of a file says who may read and write it. On macOS and Linux, a key
file must have mode 0600, or a stricter mode, such as 0400. Mode 0600 means that
you can read and write the file, and nobody else has any access to it.
email-mcp refuses a key file when any other user of the computer has access to
it. A file with mode 0644 is refused, for example. Windows has no such modes, so
email-mcp does not check the mode there.

The command `ls -l` shows the mode in its first column. `-rw-------` is mode
0600 (a macOS system may add a sign `@` after it). To fix a wrong mode, run
`chmod 600` and the path of the file. `chmod` changes the mode of a file. See
the entry "The master key file has the wrong mode" of [Troubleshooting](
/manual/daily-use/troubleshooting/#the-master-key-file-has-the-wrong-mode).

### Make a key file

There are two ways. Both work only for a data folder that has no stored keys
yet, for example the folder of a new install. A data folder with stored keys
needs the key that protects them. A new key could never open them. To change the
master key of such a folder, use [the rotation below](#change-the-master-key).

1. Let email-mcp make the file. Set `EMAIL_MCP_KEK` to `file:` and a path where
   no file exists. The folder of the path must exist. Then run a command that
   opens the data, such as `email-mcp account list`. email-mcp makes the file
   with mode 0600, and it writes a log line with the text `app: made a new
   master key file` to standard error (the place where a program writes messages
   about itself: your terminal shows it).
2. Make the file yourself, on macOS and Linux. This command writes 32 random
   bytes as base64 text. `openssl rand -base64 32` makes the text. `openssl` is
   the command of OpenSSL (a toolkit of encryption programs). `>` writes the
   text to the file. `umask 077` makes new files that only you can read, and the
   round brackets keep this setting inside this one command. The path is an
   example:

   ```sh
   (umask 077; openssl rand -base64 32 > /home/you/.email-mcp.key)
   ```

   The command prints nothing. To check the file, run these two commands. The
   first column of `ls -l` must start with `-rw-------`. `wc -c` (`wc` counts
   the content of a file, and `-c` counts bytes) must print 45: the 44
   characters of the key and the line break:

   ```sh
   ls -l /home/you/.email-mcp.key
   wc -c < /home/you/.email-mcp.key
   ```

Without the master key, email-mcp cannot open the stored passwords. See [The
master key is the wrong
one](/manual/daily-use/troubleshooting/#the-master-key-is-the-wrong-one).

## Change the master key

`email-mcp key rotate` moves to a new master key. To rotate a key means to
replace it with a new one. The command re-encrypts only the wrapped keys (the
keys that protect your passwords, each one encrypted with the master key), so it
is quick.

1. Stop every program that uses the data folder: `serve`, `setup`, and the AI
   apps that start email-mcp.
2. Run the rotation. Run only one of the two commands below, the one that
   fits the master key that you want. To change to a new key file (it must not
   exist yet), run this command:

   ```sh
   email-mcp key rotate --to file:/home/you/.email-mcp-2.key
   ```

   To change to the keychain, run this command instead:

   ```sh
   email-mcp key rotate --to keychain
   ```

   It asks you to confirm that every other program is stopped. Without a
   terminal, add `--yes`.
3. It prints the new `EMAIL_MCP_KEK` setting. Use it everywhere from now on,
   also in the `env` of your AI app config.
4. Run `email-mcp doctor` with the new setting. When it passes, you may delete
   an old key file. The command never deletes the old keychain entry, because
   another data folder may use it.

The command makes the new key file itself, with mode 0600, and it never
overwrites a file: when a file exists at the path, it stops and changes nothing.
When the command works, it prints "The master key was changed. Every stored key
is now wrapped by the new master key", and then the new setting to use from now
on. The page [Commands](/manual/reference/commands/) lists the flags of `key
rotate`.
