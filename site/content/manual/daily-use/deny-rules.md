---
title: Recommended Claude Code deny rules
description: Add deny rules and sandbox settings to Claude Code, to limit what a Claude Code agent can reach.
weight: 90
code_areas:
  - internal/app/app.go
  - internal/secrets/
---

After this page, you can limit what a Claude Code agent can reach: the
`email-mcp` command, the data folder (the folder where email-mcp keeps its data)
and the keychain (the password store of your operating system). An agent is the
AI (artificial intelligence) program that uses the tools of email-mcp. Claude
Code is one of the AI apps that you can use with email-mcp (see [Claude
Code](/manual/getting-started/connect-ai-app/claude-code/)).

Claude Code has permission rules. A "deny" rule blocks a tool call. Deny rules
win over allow rules from any settings file, and they apply in every permission
mode (a setting that says how much Claude Code asks before it acts), also in
`bypassPermissions`. These rules come from the Claude Code
[permissions documentation](https://code.claude.com/docs/en/permissions),
checked on 2026-10-29.

Add this to `~/.claude/settings.json` (your user settings, for every project).
The file is JSON (JavaScript Object Notation: a text format for data):

```json
{
  "permissions": {
    "deny": [
      "Bash(email-mcp *)",
      "Bash(security *)",
      "Bash(secret-tool *)",
      "Read(~/Library/Application Support/email-mcp/**)",
      "Edit(~/Library/Application Support/email-mcp/**)",
      "Read(~/.local/share/email-mcp/**)",
      "Edit(~/.local/share/email-mcp/**)",
      "Read(~/.config/email-mcp/**)",
      "Edit(~/.config/email-mcp/**)",
      "Read(~/AppData/Roaming/email-mcp/**)",
      "Edit(~/AppData/Roaming/email-mcp/**)"
    ]
  }
}
```

## What each rule does

- `Bash(email-mcp *)` stops the agent from running `email-mcp` in a shell, for
  example `email-mcp setup` (which makes a sign-in link) or `email-mcp account
  add`. It also matches a bare `email-mcp`. It does not stop the MCP (Model
  Context Protocol) server: Claude Code starts the server itself, not through
  the Bash tool.
- `Bash(security *)` stops the macOS keychain command line tool.
- `Bash(secret-tool *)` stops the Linux keychain (Secret Service) command line
  tool.
- The `Read` and `Edit` rules stop Claude's file tools, and the shell commands
  that Claude Code recognizes as reading or writing files (such as `cat`,
  `head` and redirections), from touching the data folder. The folder is
  `~/Library/Application Support/email-mcp` on macOS,
  `~/.local/share/email-mcp` on Linux (or `$XDG_DATA_HOME/email-mcp`), and
  `~/AppData/Roaming/email-mcp` on Windows. On Linux, `~/.config/email-mcp`
  holds only a lock file, and the rule covers it too.

## Adjust the rules to your setup

- If you set `EMAIL_MCP_DATA_DIR` or `--data-dir`, add the rules for that
  folder too.
- On Linux, the folders follow the XDG variables (standard settings that say
  where programs keep their files). A custom `XDG_DATA_HOME`
  moves the data folder to `$XDG_DATA_HOME/email-mcp`. A custom
  `XDG_CONFIG_HOME` moves the lock file folder to `$XDG_CONFIG_HOME/email-mcp`
  (this folder is used only when the master key is in the keychain). Then the
  `~/.local/share` and `~/.config` rules above match nothing: write the rules
  and the sandbox paths below for the real folders instead, with their
  absolute paths (`//` in the rules).
- If your master key is a file (`EMAIL_MCP_KEK=file:<path>`), add
  `"Read(//<path>)"` and `"Edit(//<path>)"`. In these rules, `//` starts an
  absolute path, and `~/` starts at your home folder. A single `/` at the start
  is **not** absolute: it is relative to the settings file.
- Write spaces in a path as they are, without quotes.

Deny rules also cover compound commands (several commands on one line). The docs
say: "Deny and ask rules apply when any subcommand matches them". This includes
`&&`, `;`, `|`, subshells, and wrappers such as `timeout`, `nice` and `nohup`.

## Honest limits

These rules stop the normal tool calls of the agent. They do **not** stop a
determined process. The Claude Code docs say that a Bash rule "isn't a security
boundary around the program". The rules do not stop, for example:

- `sh -c '...'` or `bash -c '...'`;
- an absolute path, such as `/opt/homebrew/bin/email-mcp`;
- `env email-mcp ...`, or `go run ./cmd/email-mcp`;
- `sqlite3` (a command line tool for databases), or a script (Python, Node and
  so on) that opens the files itself;
- on Windows, a PowerShell script that reads the Windows Credential Manager.
  There is no single command to deny there.

That is the local threat model of the page
[Security](/manual/daily-use/security/): local mode protects you from an agent
that has only the MCP tools, not from an agent with free shell access.

## Stronger: the sandbox

Source of this section: the Claude Code sandboxing documentation,
<https://code.claude.com/docs/en/sandboxing>, checked on 2026-11-06.

The Claude Code sandbox limits what shell commands, and every process they
start, can read and write. It is enforced by the operating system, so it also
covers `sh -c`, scripts and `sqlite3`. Add this to `~/.claude/settings.json`
as well:

```json
{
  "sandbox": {
    "enabled": true,
    "allowUnsandboxedCommands": false,
    "filesystem": {
      "denyRead": [
        "~/Library/Application Support/email-mcp",
        "~/.local/share/email-mcp",
        "~/.config/email-mcp"
      ],
      "denyWrite": [
        "~/Library/Application Support/email-mcp",
        "~/.local/share/email-mcp",
        "~/.config/email-mcp"
      ]
    }
  }
}
```

On Linux with a custom `XDG_DATA_HOME` or `XDG_CONFIG_HOME`, put the real
folders in both lists (see "Adjust the rules to your setup" above).
`allowUnsandboxedCommands: false` stops Claude from running a command outside
the sandbox when the command fails inside it. Limits of the sandbox:

- It works on macOS, Linux and WSL2 (Windows Subsystem for Linux) only. It is
  off by default. Do not add `failIfUnavailable: true` to a settings file that
  Windows users share: on native Windows it makes Claude Code stop at startup.
- It covers shell commands only. Claude's own file tools are outside it, so
  keep the deny rules too.
- The docs do not say whether it blocks the macOS keychain (`security`) or the
  Linux Secret Service (`secret-tool`, which talks over D-Bus: the way that
  programs on Linux talk to each other). Do not count on it for the keychain.
- Local MCP servers run outside the sandbox. That is why `email-mcp serve` can
  still read its own data folder.

For the strongest protection, run email-mcp under a separate operating system
user. A PreToolUse hook (a script that Claude Code runs to check each command
before it runs) can add your own checks.
