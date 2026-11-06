---
title: User manual
description: email-mcp gives an AI agent access to your email, and this manual shows how to install it, connect an account, and use it every day.
weight: 2
code_areas:
  - cmd/email-mcp/main.go
  - cmd/email-mcp/serve.go
---

email-mcp gives an AI (artificial intelligence) agent access to your email. The
agent is the AI program that uses the tools of email-mcp. A tool is one action
that the agent can call, such as `search_messages`. email-mcp is an MCP server.
MCP (Model Context Protocol) is an open standard that lets AI apps, such as
Claude Code, call tools that other programs offer.

- It works with any mail provider that offers IMAP (Internet Message Access
  Protocol: the protocol for reading mail) and SMTP (Simple Mail Transfer
  Protocol: the protocol for sending mail), with a password or an app password.
  An app password is a separate password that you make in your mail account for
  one program. See
  [App passwords](/manual/getting-started/connect-account/#app-passwords).
- It runs on your own computer. Your passwords stay on your computer,
  encrypted.
- You decide what the agent may do, per account: only read, also organize,
  also write drafts, also send, also move mail to the Trash. Each choice is a
  level: a number from 0 (no access) to 5 (also move mail to the Trash).
- By default, every send waits for your "yes". A move to the Trash needs level
  5 first, and then it waits for your "yes" too. The one exception is a draft:
  at level 3, the agent can move a draft to the Trash, and it does not ask you.
- One server can hold several accounts.

## How the parts fit

email-mcp has three parts that work together. The AI app starts email-mcp.
email-mcp talks to your mail server. You use the web pages of email-mcp in your
browser. The web pages are pages that email-mcp shows on your own computer.

{{< diagram "three-parts" >}}

## What you need

- A mail account at a provider (the company that runs your mail) that offers
  IMAP and SMTP, with a password or an app password.
- A terminal (a window where you type commands). You paste a few commands into
  it.
- An AI app that can use MCP servers, such as Claude Code.

## The way in

Read these four pages in order. After them, you have a working setup.

1. [Install email-mcp](/manual/getting-started/install/) puts the program on
   your computer and checks that it runs.
2. [Connect your first account](/manual/getting-started/connect-account/)
   connects one mail account, in the browser or without a browser.
3. [Connect an AI app](/manual/getting-started/connect-ai-app/) adds email-mcp
   to Claude Code, Claude Desktop, Cursor, VS Code or the Codex CLI.
4. [Your first use](/manual/getting-started/first-use/) checks the install,
   asks a first question, and shows what to expect when the agent wants to
   send.

## Find your task

- I want to [install email-mcp](/manual/getting-started/install/).
- I want to [connect a mail
  account](/manual/getting-started/connect-account/).
- I want to [connect a second
  account](/manual/daily-use/manage-accounts/).
- I want to [connect an AI app](/manual/getting-started/connect-ai-app/).
- I want to [let the agent send
  mail](/manual/daily-use/permission-levels/).
- I want to [answer a send that waits for
  me](/manual/daily-use/confirmations/).
- I want to [turn off the access of the agent to an account (level
  0)](/manual/daily-use/permission-levels/#turn-off-the-access-of-the-agent).
- I want to [cancel a send](/manual/daily-use/activity-and-undo/#cancel-a-send).
- I want to [see what the agent
  did](/manual/daily-use/activity-and-undo/).
- I want to [fix a failed
  login](/manual/daily-use/troubleshooting/#an-account-fails).
- I want to [use an AI app that cannot start a
  program](/manual/reference/local-http-mode/).
- I want to [check a download](/manual/reference/verify-a-release/).

## What is not in this version

This version covers mail only. Calendar and contacts, sign-in with Google or
Microsoft (OAuth: a way to sign in without giving a program your password), and
a hosted mode for many people (one server that many people use) are planned.
They are not available. Until then, connect Google and Microsoft accounts with
an app password.

email-mcp uses the MIT license. See {{< src "LICENSE" >}} on GitHub.
