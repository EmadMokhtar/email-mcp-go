---
title: Troubleshooting
description: Find the cause of a failure with email-mcp doctor, and fix it.
weight: 110
code_areas:
  - cmd/email-mcp/doctor.go
  - internal/mcpserver/errors.go
  - internal/provider/imapsmtp/
  - internal/store/
  - internal/secrets/
  - internal/connect/
  - internal/web/templates/no_session.html
  - internal/web/templates/use_name.html
---

After this page, you can find why email-mcp does not work, and fix it. Start
with `email-mcp doctor`. The agent is the AI (artificial intelligence) program
that uses the tools of email-mcp.

## Start with doctor

Run this in a terminal (a window where you type commands). The command checks
the install and prints one line for each check:

```sh
email-mcp doctor
```

If your master key (the key that protects your stored passwords) is a file, set
the environment variable `EMAIL_MCP_KEK` (a named setting that a program reads
from its surroundings) in this terminal first, with the same value as in the
config of your AI app.

It prints one line per check, `<check>: ok` or `<check>: FAIL <reason>`, and
exits with 1 when a check fails. (A program exits with 0 when it works. Another
number means that it failed.) It checks:

1. that the data folder exists and has mode 0700 (only you can open it);
2. that the database file and its write-ahead log files exist and have mode
   0600 (only you can read and write them). A write-ahead log is a set of files
   that lets several programs use one database file safely;
3. that the master key of `EMAIL_MCP_KEK` exists and opens the stored keys.
   `doctor` never makes a new key. When a newer email-mcp wrote the database,
   this line is `database schema: FAIL ...` instead;
4. that every account can log in to its IMAP (Internet Message Access Protocol:
   the protocol for reading mail) and SMTP (Simple Mail Transfer Protocol: the
   protocol for sending mail) servers.

On Windows, `doctor` skips the mode part of checks 1 and 2. It does not check
the mode 0700 of the data folder, and it does not check the mode 0600 of the
database file and its write-ahead log files. It still checks that the folder and
the database file exist.

A line with `FAIL` has the reason after it. Find the reason in the list below.
After a wrong mode and after a failed account, `doctor` goes on with the next
check. After a missing folder, a missing database file, a failure of the master
key or a failure of the schema, it stops.

## Common fixes

### The data folder does not exist

The line says: `data directory <path>: FAIL it does not exist.` Connect an
account first, or point `doctor` at the right folder with `--data-dir` or
`EMAIL_MCP_DATA_DIR`. The page [Connect your first
account](/manual/getting-started/connect-account/) connects an account. The
page [Where the data lives](/manual/reference/data-folder/) says where each
system keeps the data folder.

Two other lines of the same check mean that the path is not a folder (`FAIL it
is not a directory`), or that the folder cannot be read (`FAIL it cannot be
read:` and the error of the system). Check the path that you gave.

When the folder exists but has no database, the next line says `database file:
FAIL <path> does not exist`. No account was connected in this folder. Connect an
account first, or point `doctor` at the right folder. When the line says
`database file: FAIL <path> cannot be read:` and an error of the system, the
file exists but the system refuses to read it. Check the path and the mode of
the file.

### A wrong mode

The mode of a file or a folder says who may read, write or open it. The data
folder must have mode 0700, and the database file must have mode 0600. The line
prints the exact `chmod` command that fixes it. `chmod` changes the mode of a
file. For example:

```text
data directory /home/you/.local/share/email-mcp: FAIL its mode is 0755, it must be 0700. Fix it with: chmod 700 /home/you/.local/share/email-mcp
```

Run the command that the line prints. For the database file, the line says
`has mode 0644, it must be 0600. Fix it with: chmod 600` and the path.

### The master key does not exist

The line says: `master key (...): FAIL the master key does not exist: no
keychain entry email-mcp/master-key, or no key file at the path of
EMAIL_MCP_KEK. Check EMAIL_MCP_KEK. doctor never makes a key`. The keychain is
the password store of your operating system. `EMAIL_MCP_KEK` is not the same as
when you connected the account. Use the same value as the AI
app config. The text in parentheses on the line shows the value that `doctor`
used: `keychain`, or `file:` and the path of the key file.

Other commands, such as `email-mcp setup` and `email-mcp serve`, give a longer
text for the same cause: "the master key file ... does not exist, and the
database has keys that only the old master key can open. Check the path in
EMAIL_MCP_KEK". These commands never make a new key for a data folder that
already has keys, because a new key could never open them. The page [The master
key](/manual/reference/master-key/) explains the master key.

### The master key file is not valid

The line says: `master key (file:<path>): FAIL secrets: key file <path>: master
key must be standard base64 of exactly 32 bytes`. A key file holds the master
key as text. The text must be the base64 form of exactly 32 bytes. Base64 is a
way to write bytes with letters, digits, `+`, `/` and `=`. email-mcp refuses a
file with another content, for example a file of 32 raw bytes, an empty file, or
a short text.

Use the key file that you used when you connected the account. If you wrote the
file again by mistake, put the old key back. A new key cannot open the keys that
are in the database already (see the next entry).

To make a key file for a new data folder, set `EMAIL_MCP_KEK` to a path where no
file exists, and run any command that opens the data, such as `email-mcp account
list`. email-mcp then makes a valid key file with mode 0600. You can also make
one yourself. This command writes 32 random bytes as base64 text. `umask 077`
makes new files that only you can read, and the round brackets keep this setting
inside this one command:

```sh
(umask 077; openssl rand -base64 32 > /home/you/.email-mcp.key)
```

The page [The master key](/manual/reference/master-key/#make-a-key-file) says
more.

### The master key file has the wrong mode

The line says: `FAIL secrets: key file permissions are too open: <path> has mode
0644, it must not be accessible to group or other users (use 0600)`. Other users
of the computer could read the file. Run `chmod 600 <path>`. The line `FAIL
secrets: key file <path> is not a regular file` means that the path is a folder
or another kind of item. Give the path of the key file.

### The master key is the wrong one

The line says: `FAIL secrets: unwrap server key: wrong master key or corrupted
data: secrets: decryption failed`. The master key is valid, but it is not the
key that protects this data folder. Use the key that you used when you
connected the account. A different key, also a valid one, cannot open the stored
keys. If the old key is lost, email-mcp cannot open the stored passwords. Then
connect the accounts again in a new data folder, with `--data-dir` or
`EMAIL_MCP_DATA_DIR`.

### The value of EMAIL_MCP_KEK is not valid

The line starts with `master key (...): FAIL app:` and says one of these:

- `unknown master key source "..."`: `EMAIL_MCP_KEK` must be `keychain`, or
  `file:` and the absolute path of the key file. An absolute path is the full
  path of a file.
- `the master key file path "..." must be absolute`: give the full path, not a
  path that starts in the current folder.
- `the master key file path must not have a ".." part`: write the path without
  `..`.

### The keychain gives an error

The line starts with `master key (keychain): FAIL secrets: keychain`, or
another command stops with `secrets: keychain` and an error. The keychain of
your operating system (its password store) gave an error. The text after it is
the answer of the keychain. A computer with no keychain, such as WSL (Windows
Subsystem for Linux) or a server that you reach over SSH (Secure Shell: a way to
log in to another computer over a network), gives this error at the first
command.

The fix is a key file. Set `EMAIL_MCP_KEK` to `file:` and the absolute path of a
key file, in this terminal and in the config of your AI app, and run the command
again. The page [The master
key](/manual/reference/master-key/#make-a-key-file) shows how to make the file.
This fix is for a data folder that has no stored keys yet. A data folder with
stored keys needs the master key that protects them, and a new key cannot open
them.

### `database schema` fails

The line says: `database schema: FAIL this data was written by a newer
email-mcp; install that version or newer`. A newer email-mcp used this data
folder. Install that version or a newer one. To go back to an older version,
you need a copy of the data folder from before the update. See [Go back to an
older version](/manual/reference/update/#go-back-to-an-older-version).

### An account fails

The line starts with `account <id> (<address>): FAIL`. Run `email-mcp account
test <id>` for the details. `email-mcp account list` shows the ids. The text
after `FAIL` is one of these:

| The text after `FAIL` | What it means and what to do |
|---|---|
| The server refused the user name or the password. Some providers need an app password, or IMAP access turned on in their settings. | The mail server did not accept the login. Check the user name and the password. An app password is a separate password that you make in the security settings of your mail account. If the password changed, reconnect the account. |
| The mail server is busy, or has too many connections for this account. Close other mail programs that use it, or try again in a few minutes. | The server is busy, or it has more connections for this account than it allows. Do what the text says. |
| A server name was not found. Check the host names. | The name of the IMAP or the SMTP server is wrong, or the computer cannot find it. Check the host names. A host name cannot change on an account: remove the account and add it again. |
| A server did not answer in time. Check the host names and the ports, then try again. | The server did not answer. Check the host names and the ports, and try again. |
| A secure connection could not be made. Check the ports and the security settings. | The port and the security setting do not fit each other, for example TLS (Transport Layer Security: it encrypts the connection from the first byte) on a port that does not use TLS. Check the ports and the security settings of your provider. |
| Could not connect to the server. Check the host names and the ports. | The server refused the connection, or closed it at once. With a local bridge (a program on your own computer that offers IMAP and SMTP servers for a mail service), start the bridge. |
| The connection test failed. Check the user name, the password and the server settings. Some providers need an app password. | The test failed for a reason that has no more exact text. Check the same things. |
| The IMAP certificate (SHA-256 ...): ... or The SMTP certificate (SHA-256 ...): ... | The server shows a certificate (the proof of identity that a server shows) that email-mcp does not accept. SHA-256 (Secure Hash Algorithm with 256 bits) is the kind of fingerprint, a short, unique code of the certificate. The text after the colon says why. See below. |
| `the account could not be tested:` and an error | The test could not run, because of the database or the master key. Fix the lines above it first. |

The text about a certificate is one of these:

- "The certificate is not the one you chose to trust. Someone on the network may
  have changed it. This certificate cannot be trusted here." The server shows
  another certificate than the one that you trusted. A local mail server may
  make a new one when it is installed again. Reconnect the account: the page
  shows the old and the new fingerprint. Check the new one with your provider or
  your bridge first.
- "No authority that this computer trusts signed this certificate. A local
  bridge (such as Proton Mail Bridge) uses such a certificate. Trust it only if
  you expect it." The account has no trusted certificate for this server. See
  [Connect your first
  account](/manual/getting-started/connect-account/#a-local-bridge).
- "The certificate is not for this host name. Check the host name.", "The
  certificate has expired or is not valid yet." and "The certificate is not
  valid.", each followed by "This certificate cannot be trusted here.":
  email-mcp never accepts such a certificate. Check the host name of the
  account, or ask your provider.

`accounts: FAIL` and an error means that `doctor` could not read the list of
accounts from the database. The text after it is the error.

### The agent says that the mail server refused the stored login

The agent may also say that the account needs to be reconnected. When the mail
server refuses the stored password (for example an app password was revoked),
email-mcp marks the account as "needs to be reconnected" and stops calling the
server for it. If the password did not change, run `email-mcp account test
<id>`: when the test works (the server only refused for a short time), the
account is marked as connected again. Otherwise, see [The password or the
certificate changed](#the-password-or-the-certificate-changed).

### The agent says that it could not connect to the mail server

The server refused the connection or closed it at once, or its host was not
found. With a local bridge, such as Proton Mail Bridge, start the bridge.
Otherwise check the network and the server settings. `email-mcp account test
<id>` tests the connection. The status `ok` of `list_accounts` does not say that
the server can be reached: it only says that the server did not refuse the
login.

### The password or the certificate changed

Reconnect the account. The page [Manage
accounts](/manual/daily-use/manage-accounts/#reconnect-an-account) shows how, in
the browser and without a browser.

### The server settings changed

A new host, port or security needs a new account. Remove the account and add it
again. The page [Manage accounts](
/manual/daily-use/manage-accounts/#change-the-server-settings-of-an-account)
shows how, in the section "Change the server settings of an account".

### A web page says to run `email-mcp setup`

The page shows one sentence: "Run `email-mcp setup` in a terminal, then open
this link again." Your sign-in ended, or this browser never signed in. A sign-in
lasts 24 hours at most, and it ends after 2 hours without use. Run `email-mcp
setup` again, open the link that it prints, and then open the page again. The
page [Connect your first
account](/manual/getting-started/connect-account/#sign-in-to-the-web-pages) has
the steps.

{{< screenshot
  name="no-session"
  alt=`The page for a browser that is not signed in. It has one sentence: Run
    email-mcp setup in a terminal, then open this link again.`
>}}

### The shell cannot find `email-mcp`

The shell is the program that reads your commands. In zsh the message is `zsh:
command not found: email-mcp`. In bash it is `bash: email-mcp: command not
found`. On Windows, the message says that `email-mcp` is not recognized. The
shell looks for programs in the folders of your `PATH` (the list of folders
where the shell looks for programs). The folder that holds `email-mcp` is not in
it.

Put the program in a folder on your `PATH`. The page [Install
email-mcp](/manual/getting-started/install/#put-the-program-on-your-path) shows
how. Or run the program from the folder where you unpacked it. A dot means the
current folder, so `./email-mcp doctor` runs the program that is in the current
folder. On Windows, the command is `.\email-mcp.exe doctor`.

## More help

- email-mcp writes its log to standard error (the place where a program writes
  messages about itself: your terminal shows it). The log never holds subjects,
  bodies, names, addresses or tokens.
- In Claude Code, `/mcp` shows whether the server is connected. The page [Claude
  Code](/manual/getting-started/connect-ai-app/claude-code/) names the source
  and the date of this fact.
- Claude Desktop writes the server log to `~/Library/Logs/Claude/` (macOS) or
  `%APPDATA%\Claude\logs` (Windows). The page [Claude
  Desktop](/manual/getting-started/connect-ai-app/claude-desktop/) names the
  source and the date of this fact.
- An app that cannot find `email-mcp`: give the absolute path of the binary (see
  `which email-mcp`) as the command. The page [Connect an AI
  app](/manual/getting-started/connect-ai-app/#another-app) says more.
