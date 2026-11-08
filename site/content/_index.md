---
# The landing page, at the root of the site. All its words are in this file:
# the prose in the body, and the lists in this front matter. The body is the
# page in order. Its shortcodes (layouts/_shortcodes/hero.html, section.html,
# rows.html and install.html) give each part its shape. The page and its words
# come from docs/superpowers/specs/2026-11-05-landing-page-design.md.
#
# Each claim has a note with its row in the truth table of that spec: a YAML
# comment such as "# T13" here, and the shortcode truth with the same name in
# the body, right after the sentence with no space before it (a space would
# stay in the page as a double space). The shortcode prints nothing. When a pull
# request changes the code that a row names, read the words with that note
# again.
#
# A few words were first written differently in the spec. The code is the
# source of truth, so the spec was changed to match the page. A comment next
# to each says which code the words follow. Three are in the body. In section
# 4, the heading says "You see the message" and not "the whole message", and
# the paragraph says what an AI app shows when it asks in its own window. Such
# a window shows only the start of the text (previewBodyChars in
# internal/mcpserver/elicit.go). The approval page shows the whole text as the
# recipient will see it, and says when the message also has characters that
# cannot be seen (Body and MoreChars in internal/service/approvals.go). In
# section 3, a link to the Security page of the manual follows "Only you can
# change a level": like the manual, the page says where that holds. It holds
# for an agent that can only call the tools of email-mcp, not for one that
# runs shell commands as you or controls your browser.

# The <title> of the page and its og:title.
title: "email-mcp: let an AI agent use your email"  # T1
# The meta description of the page and its og:description.
description: "email-mcp connects an AI agent, such as Claude Code, to your mail accounts. It runs on your computer. By default, every send waits until you approve it."  # T1 T4 T3
# The og:image:alt of the preview card. The card shows the same words
# (assets/social-card.svg).
card_alt: "email-mcp. Your agent asks. You decide."
weight: 1

# Section 1, the first screen. The lead paragraph is in the body.
hero:
  # The one h1 of the page, one sentence on each line. The lead right under
  # it says "By default". Keep the two together.
  headline:
    - Your agent asks.
    - You decide.  # T3
  # The text link next to the main button. It goes to section 2.
  link:
    label: See how it works
    anchor: how-it-works

# The main button. Section 1 and section 7 show it with the same label and the
# same address, so the page never gives two names to one action.
install:
  label: Install and set up
  page: /manual/getting-started/install/  # T36

# Section 1: the three facts under the first screen.
facts:
  - term: On your computer
    text: >-  # T4 T5
      email-mcp runs on your computer. It stores your mail passwords there,
      encrypted.
  - term: Works with IMAP and SMTP
    text: >-  # T6
      IMAP reads mail and SMTP sends it. You connect with a password or an app
      password.
  - term: Your rules
    text: For each account, you choose how far the agent may go.  # T7

# Section 2: the four steps, in order.
steps:
  - title: Connect your mail.
    text: >-  # T10
      Add an account in your browser, with your address and a password.
      email-mcp tests the connection and saves the account only when the test
      passes.
  - title: Add email-mcp to your AI app.
    text: >-  # T12
      You do this once. After that, your agent can use the email tools.
  - title: Ask your agent.
    text: >-  # T11
      Use plain words. For example, you can type: "Find Anna's message about
      Thursday and reply that 10:00 works." The agent uses the email tools to
      search, read, organize, write drafts and send.
  - title: Decide.
    # An AI app that asks in its own window shows only the start of the text,
    # so the page says "the message" and not "the whole message" (T19, T24).
    text: >-  # T3 T19
      By default, email-mcp holds each send until you approve it. You see the
      message first.

# Section 3: the six mail levels. The names are the names that the portal
# shows (MailName in internal/domain/account/account.go).
levels:
  - term: 0 Off
    text: The agent cannot use the account.  # T13
  - term: 1 Read
    text: Search and read messages and attachments.  # T13
  # move_messages refuses, at every level, a folder that email-mcp recognizes
  # as the Trash and every folder inside it (ErrMoveToTrash, refuseTrashTarget
  # and inTrash in internal/service/write.go). So the page speaks only of the
  # folders that email-mcp recognizes, as the tech page Permissions does
  # (content/tech/inside-the-server/permissions.md).
  - term: 2 Organize
    text: >-  # T13
      Mark as read, flag, tag, make folders and move messages. These moves
      never go to a folder that email-mcp recognizes as the Trash, or to a
      folder inside it.
  # discard_draft moves a draft to the Trash at level 3, and it never asks
  # (internal/service/write.go, DiscardDraft), so the page says so after
  # "Write drafts".
  - term: 3 Draft
    text: >-  # T13 T16
      Write drafts, and move a draft to the Trash. Moving a draft does not ask
      you.
  - term: 4 Send
    text: >-  # T13 T15 T3
      Send, reply and forward. A new account starts here. By default, every
      send asks you first.
  # Confirm deletes covers trash_messages, the tool of this level, and not the
  # move of a draft at level 3. So the page says "each of these moves", not
  # "every move".
  - term: 5 Delete
    text: >-  # T13 T16 T17
      Move messages to the Trash. By default, each of these moves asks you
      first. email-mcp has no tool that deletes mail for good.

# Section 4: what the approval page promises. One sentence each.
# The page offers Approve only when it can show the whole text that the
# recipient will see (CanApprove in internal/web/approvals.go).
decide:
  - >-  # T20
    **Approve and send** appears only when the page can show the whole text
    that the recipient will see.
  - If the message changes after you look at it, it does not go out.  # T21
  - >-  # T22
    After you decide, the page tells you what happened. It says **Sent** only
    when the mail server accepted the message.
  - If you do nothing, the request expires and nothing is sent.  # T23

# Section 5: where the pieces are.
local:
  - term: Passwords
    text: >-  # T5
      Stored on your computer, encrypted with AES-256-GCM (a standard
      encryption method). By default, the key that protects them is in the
      keychain (the password store) of your operating system.
  - term: Your mail
    text: >-  # T26
      email-mcp does not keep a copy of your mailbox. Your messages stay on
      your mail server.
  - term: Web pages
    text: >-  # T27
      The approval page and the settings pages run only on your computer, at
      the name `email-mcp.localhost`. That name always means your own computer.
  - term: Activity
    text: >-  # T28
      Every tool call of your agent is written to an Activity page that you can
      read. The log never stores message bodies or attachment data.
  - term: What your agent reads
    text: >-  # T29
      When your agent reads a message, the text goes to your AI app. What that
      app does with it is up to the app.

# Section 6: the limits of this release. The links go to pages of the manual,
# and the link hook stops the build if one of them is gone.
covers:
  - term: Mail only
    text: This release covers mail. Calendar and contacts are planned.  # T30
  - term: Google and Microsoft
    text: >-  # T31 T32
      Sign-in on the Google or Microsoft website (OAuth) is planned. Until
      then, connect these accounts with an
      [app password](/manual/getting-started/connect-account/#app-passwords):
      a separate password that you make in the security settings of your mail
      account. Your provider must offer app passwords.
  # The five apps that the manual has a page for, under
  # manual/getting-started/connect-ai-app/. If the manual drops one, remove
  # its name here.
  - term: AI apps
    text: >-  # T33
      The manual shows [the setup](/manual/getting-started/connect-ai-app/)
      for Claude Code, Claude Desktop, Cursor, VS Code and the Codex CLI.
  - term: Systems
    text: Release builds exist for macOS, Linux and Windows.  # T34
  - term: Open source
    text: >-  # T35
      email-mcp is open source under the MIT license: you may use, copy and
      change the code. You can read it on
      [GitHub](https://github.com/EmadMokhtar/email-mcp-go).
---

{{< hero >}}
email-mcp connects an AI agent, such as Claude Code, to your
email.{{< truth "T1" >}} The agent can read, organize and write
mail.{{< truth "T2" >}} By default, every send waits until you approve
it.{{< truth "T3" >}}
{{< /hero >}}

{{< section id="how-it-works" heading="How it works" >}}

Your AI app can use tools that other programs offer. MCP (Model Context
Protocol) is the open standard for this. email-mcp is an MCP server: a program
that offers email tools to your AI app.{{< truth "T9" >}}

{{< rows "steps" "ol" >}}

{{< /section >}}

{{< section id="levels" heading="You choose how far each account may go" >}}

Each mail account has its own level.{{< truth "T7" >}} A higher level
includes the levels below it.{{< truth "T14" >}}

{{< rows "levels" "dl" >}}

Only you can change a level, on a page in your browser. The agent has no tool
for this. It can ask for more access. That only gives you a link to the
settings page, and nothing changes until you choose.{{< truth "T18" >}} The
manual page [Security](/manual/daily-use/security/) says when this holds.

{{< /section >}}

{{< section id="decide" heading="You see the message before you decide" >}}

By default, email-mcp holds a message when your agent wants to send
it.{{< truth "T3" >}} On the approval page, you see who will get it,
including hidden copies (Bcc), the subject, the attachments and the whole text
as the recipient will see it.{{< truth "T19" >}} If the message also has
characters that cannot be seen, the page says so.{{< truth "T19" >}} Then you
press **Reject** or **Approve and send**.

{{< rows "decide" "ul" >}}

Some AI apps can ask you in their own window. There, email-mcp shows the start
of the text, and says how much of it is not shown. For the others, your agent
gives you a link to the approval page.{{< truth "T24" >}}

You can turn this question off for an account. email-mcp then asks you to tick
a box that accepts the risk. Limits still apply when it is off: sends per hour
and per day, and recipients per message.{{< truth "T25" >}}

{{< /section >}}

{{< section id="local" heading="email-mcp runs on your computer" >}}

{{< rows "local" "dl" >}}

{{< /section >}}

{{< section id="covers" heading="What it covers today" >}}

{{< rows "covers" "dl" >}}

{{< /section >}}

{{< section id="start" heading="Start with the manual" >}}

The manual shows you how to install email-mcp, connect an account and add it
to your AI app.{{< truth "T36" >}}

{{< install >}}

For developers: [Read the tech docs](/tech/) or
[see the code on GitHub](https://github.com/EmadMokhtar/email-mcp-go).

{{< /section >}}
