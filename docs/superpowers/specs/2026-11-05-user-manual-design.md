# User manual: design

This document is the design of the user manual. The manual is one of the three
parts of the docs site. This document follows the shared decisions of the
foundation and does not reopen them.

- [Docs site foundation](2026-11-05-docs-site-foundation-design.md)
- [Tech docs](2026-11-05-tech-docs-design.md)
- [Landing page](2026-11-05-landing-page-design.md)

How to read this document:

- "(My call.)" marks a choice that the author of this document made. The owner
  can change it at review.
- A path without a folder prefix is under `site/content/manual/`.
- Line numbers are from 2026-11-05 (release 0.6.0). A line number moves when a
  file changes. The builder checks each one before use.
- A page is named by its file name without `.md`, for example `first-use`. Every
  such name is unique in the manual.
- A hub page is a page whose job is to send the reader to other pages.
- A short list of terms is at the end.

## Intent

The manual teaches a person to install email-mcp, to connect a mail account, to
connect an AI app, and to use the tool with confidence. Later it answers
questions: what a setting does, why a send waits for approval, how to fix a
failed login.

The manual is the user part of the old `README.md`, `DOCKER.md` and
`docs/harnesses.md`. It keeps every fact that a user needs. It puts the facts in
an order that a new reader can follow.

## Reader

The reader comes from `PRODUCT.md`: one person, on their own computer, who lets
an AI agent look after their email.

- The reader may not know what MCP (Model Context Protocol: an open standard
  that lets AI apps call tools that other programs offer) or IMAP (the protocol
  for reading mail) mean. No page may need this knowledge before it explains it.
- The reader can open a terminal (a window where you type commands) and paste a
  command. Every way to install, and `email-mcp setup`, need this. The manual
  assumes nothing more. (My call.)
- The reader arrives from the landing page to install the tool and connect an
  account. The reader returns later with a question.
- The reader may not be a developer. The reader's first language may not be
  English.

## Goals

1. A new reader gets a working setup by reading four steps in order: install,
   connect one account, connect one AI app, ask a first question. No step needs
   something that only a later page explains.
2. A returning reader finds the page for a task from the sidebar in one click.
   The first version has no search (see the foundation).
3. Every user fact of the README, `DOCKER.md` and `docs/harnesses.md` is in the
   manual. Each fact is in one place. The only exceptions are short commands
   that a reader needs in two places. "Duplicates to merge" lists them.
4. Every name of a button, a page, a command, a flag and a setting in the manual
   is the name that the program uses.
5. A pull request author can tell which page to change from the code area. See
   "Code area to page".
6. The wording of the safety warnings stays exact (see "Page rules").

## Non-goals

- A course on email, AI apps or MCP in general. The manual explains only what a
  reader needs to use email-mcp.
- Calendar, contacts, Google and Microsoft sign-in (OAuth: a way to sign in
  without giving a program your password), and hosted mode. They cannot be
  installed. The manual says so in one place and gives no date. (My call.)
- Developer content: how to build, test, release or change the code. It goes to
  the tech docs.
- Marketing text. The landing page does that job.
- Screenshots of terminal output, or of the windows of other apps. Terminal
  output stays as text. Only the web pages of email-mcp are shown.
- Pages made from the code, such as a tool list made from the tool schemas. A
  person writes every page in the first version. (My call.)
- Search, translations, and docs for older versions (all from the foundation).

## Anti-goals

These make the manual a failure, even if the site builds:

- A step that the reader cannot do: a wrong button name, a wrong order, a
  command that fails.
- A safety warning that is made softer, shorter or hidden in the move.
- A page that says or suggests that the agent can approve its own action, or
  that a confirmation is safe with any AI app.
- A fact in two places that disagree.
- A page that uses MCP, IMAP, SMTP, stdio or another term of this kind without a
  definition.
- A feature that cannot be installed, described as if it can.
- A fact of the README that no page of the manual holds.
- Developer content in the manual, or user content only in the tech docs.
- A weakness that is not fixed, described on a page, or attack steps. The text
  that the README already publishes moves as it is. The manual adds nothing of
  this kind.
- A page that tells the reader to turn off a protection without the warning that
  belongs to it.

## Decisions

### From the foundation

The builder follows these. They are not open.

- Hugo builds the site. The manual lives in `site/content/manual/` and is served
  at `/manual/`.
- Content follows the content rules of the foundation: front matter (the block
  of settings at the top of a page file), no H1 in the body, lines of 80
  characters, a definition after each technical term.
- The manual has its own glossary page.
- Every fact comes from the code or from an existing document. If they differ,
  the code wins and the difference is reported. See "Differences to resolve".
- The README is cut down in step 6, not in the manual's pull request. See item 6
  of "Definition of done" for the files that the manual pull request changes.

### Made in this document

Each of these can change at review.

- Three sidebar groups: Get started, Daily use, Reference. (My call.)
- The pages about safety (`security`, `deny-rules`) are the last pages of Daily
  use, before account care and troubleshooting. They are not a fourth group, to
  keep the order that the owner asked for. (My call.)
- One page for each AI app, with a hub page above them. (My call.)
- The README section "Confirmations and full autonomy" splits into three pages.
  (My call.)
- The word for the browser side of email-mcp is "web pages", as in the README.
  The manual does not use "portal" (a word from `PRODUCT.md`). (My call.)
- Page front matter has `title`, `description`, `weight` and `code_areas`, as
  the foundation says. See "Differences to resolve", item 15.

## Structure

```text
site/content/manual/
  _index.md                       Start here
  getting-started/                group: Get started
    _index.md
    install.md
    connect-account.md
    connect-ai-app/
      _index.md                   hub page
      claude-code.md
      claude-desktop.md
      cursor.md
      vscode.md
      codex-cli.md
    first-use.md
  daily-use/                      group: Daily use
    _index.md
    what-the-agent-can-do.md
    web-pages.md
    permission-levels.md
    confirmations.md
    app-confirmation.md
    full-autonomy-and-limits.md
    activity-and-undo.md
    security.md
    deny-rules.md
    manage-accounts.md
    troubleshooting.md
  reference/                      group: Reference
    _index.md
    commands.md
    data-folder.md
    master-key.md
    local-http-mode.md
    move-from-old-version.md
    known-limitations.md
    verify-a-release.md
    containers.md
    glossary.md
```

Sidebar rules:

- The order of the sidebar is the reading order. The `weight` values in front
  matter set it. Use 10, 20, 30 and so on, so a page can be added later.
- The three groups come in this order: Get started, Daily use, Reference. The
  title of each group is the title of its `_index.md`.
- The five app pages sit under "Connect an AI app" in the sidebar.
- The template makes "previous" and "next" links from the same order. No page
  writes them by hand.
- Page titles are short and say the task or the thing: "Install email-mcp", not
  "Everything about installing".

Size: 30 pages with content: the start page, 9 pages in Get started, 11 in Daily
use and 9 in Reference. There are also 3 group pages (the `_index.md` of each
group) with two or three sentences each. That is 33 files.

## Page list in reading order

Each entry has the path, the purpose, the source and the kind of work. "Move"
means that the text moves with link fixes only. "Edit" means that it changes as
"More than a move" says. "New" means that a person writes it from the sources
that are named.

### Start

**Start here**

- Path: `_index.md`
- Purpose: Say in plain words what email-mcp is, what the reader needs, and
  which four pages to read first. Help a returning reader find a task.
- Source: README 3-19, and new text.
- Work: Edit.

### Group 1: Get started

**Group page "Get started"**

- Path: `getting-started/_index.md`
- Purpose: Two sentences that say the four steps are done in order.
- Source: New.
- Work: New.

**1. Install email-mcp**

- Path: `getting-started/install.md`
- Purpose: Put the `email-mcp` program on the computer and check it.
- Source: README 43-90 (Install).
- Work: Edit.

**2. Connect your first account**

- Path: `getting-started/connect-account.md`
- Purpose: Connect one mail account, in the browser or without a browser.
- Source: README 94-169 (Quick start, steps 1 and 1b).
- Work: Edit.

**3. Connect an AI app**

- Path: `getting-started/connect-ai-app/_index.md`
- Purpose: Help the reader choose their AI app, and say what all apps share.
- Source: README 878-881 (Other AI apps), `docs/harnesses.md` 1-17 and 68-80.
- Work: Edit.

**3a. Claude Code**

- Path: `getting-started/connect-ai-app/claude-code.md`
- Purpose: Add email-mcp to Claude Code, over stdio or local HTTP.
- Source: `docs/harnesses.md` 82-139, README 171-181.
- Work: Edit (two sources merge).

**3b. Claude Desktop**

- Path: `getting-started/connect-ai-app/claude-desktop.md`
- Purpose: Add email-mcp to the config file of Claude Desktop.
- Source: `docs/harnesses.md` 141-187.
- Work: Move.

**3c. Cursor**

- Path: `getting-started/connect-ai-app/cursor.md`
- Purpose: Add email-mcp to Cursor, over stdio or local HTTP.
- Source: `docs/harnesses.md` 189-234.
- Work: Move.

**3d. VS Code**

- Path: `getting-started/connect-ai-app/vscode.md`
- Purpose: Add email-mcp to VS Code, over stdio or local HTTP.
- Source: `docs/harnesses.md` 236-292.
- Work: Move.

**3e. Codex CLI**

- Path: `getting-started/connect-ai-app/codex-cli.md`
- Purpose: Add email-mcp to the Codex CLI, over stdio or local HTTP.
- Source: `docs/harnesses.md` 294-341.
- Work: Move.

**4. Your first use**

- Path: `getting-started/first-use.md`
- Purpose: Check the install, ask a first question, and know what to expect when
  the agent wants to send.
- Source: README 182-192 (example question, "Check that everything works"),
  README 250-255 (what a new account may do).
- Work: Edit.

### Group 2: Daily use

**Group page "Daily use"**

- Path: `daily-use/_index.md`
- Purpose: Two sentences: these pages explain what the agent may do and how you
  stay in control.
- Source: New.
- Work: New.

**5. What the agent can do**

- Path: `daily-use/what-the-agent-can-do.md`
- Purpose: List the tools of the agent, the level each needs, and how mail
  content is marked as untrusted.
- Source: README 194-233.
- Work: Edit.

**6. The web pages**

- Path: `daily-use/web-pages.md`
- Purpose: A map of the email-mcp web pages: what each menu entry shows and
  which manual page explains it.
- Source: Code only (see "More than a move"), and `PRODUCT.md` 38-41.
- Work: New.

**7. Permission levels**

- Path: `daily-use/permission-levels.md`
- Purpose: Explain levels 0 to 5, the start level of a new account, and how only
  you change a level.
- Source: README 235-262.
- Work: Edit.

**8. Confirmations**

- Path: `daily-use/confirmations.md`
- Purpose: Explain when a send or a move to the Trash waits for your "yes", what
  you see, and the two ways to answer.
- Source: README 264-318.
- Work: Edit.

**9. How each AI app confirms**

- Path: `daily-use/app-confirmation.md`
- Purpose: Say which AI apps can show the confirmation form, and which use the
  approval link.
- Source: `docs/harnesses.md` 19-66.
- Work: Edit.

**10. Full autonomy, limits and settings**

- Path: `daily-use/full-autonomy-and-limits.md`
- Purpose: Explain how to turn confirmations off, what stays on, and every
  setting of an account.
- Source: README 320-354, and the settings page of the code.
- Work: Edit.

**11. Activity and undo**

- Path: `daily-use/activity-and-undo.md`
- Purpose: Explain the Activity page, the undo window, and how to cancel a send.
- Source: README 356-371.
- Work: Edit.

**12. Security: what local mode protects against**

- Path: `daily-use/security.md`
- Purpose: Say what local mode protects against and what it does not.
- Source: README 373-452.
- Work: Move, with few edits (see "More than a move").

**13. Recommended Claude Code deny rules**

- Path: `daily-use/deny-rules.md`
- Purpose: Show the deny rules and the sandbox settings that limit what a Claude
  Code agent can reach.
- Source: README 454-585.
- Work: Edit (one sentence).

**14. Manage accounts**

- Path: `daily-use/manage-accounts.md`
- Purpose: Add, reconnect, test and remove accounts.
- Source: README 805-829, and the row for `account` at README 849.
- Work: Edit.

**15. Troubleshooting**

- Path: `daily-use/troubleshooting.md`
- Purpose: Find the cause of a failure with `email-mcp doctor` and fix it.
- Source: README 762-804 and 831-839.
- Work: Edit.

### Group 3: Reference

**Group page "Reference"**

- Path: `reference/_index.md`
- Purpose: Two sentences: look-up pages for commands, files and rare tasks.
- Source: New.
- Work: New.

**16. Commands**

- Path: `reference/commands.md`
- Purpose: List every command and subcommand with one line each.
- Source: README 841-858.
- Work: Edit.

**17. Where the data lives**

- Path: `reference/data-folder.md`
- Purpose: Say where email-mcp keeps its data and what it stores.
- Source: README 860-876.
- Work: Move.

**18. The master key**

- Path: `reference/master-key.md`
- Purpose: Explain where the master key is and how to change it.
- Source: README 587-634.
- Work: Edit.

**19. Local HTTP mode**

- Path: `reference/local-http-mode.md`
- Purpose: Connect an AI app that cannot start a program, with a token.
- Source: README 636-696.
- Work: Edit.

**20. Moving from the old email-mcp**

- Path: `reference/move-from-old-version.md`
- Purpose: Import the account of the first version of email-mcp.
- Source: README 698-734.
- Work: Move.

**21. Known limitations**

- Path: `reference/known-limitations.md`
- Purpose: List the known limits of the product.
- Source: README 736-760.
- Work: Move. Correct any sentence that no longer matches the code.

**22. Verifying a release**

- Path: `reference/verify-a-release.md`
- Purpose: Check the signature and the checksum of a downloaded release.
- Source: README 883-930.
- Work: Edit (one note).

**23. Containers**

- Path: `reference/containers.md`
- Purpose: Explain why the container image is not for daily use on your own
  computer.
- Source: `DOCKER.md` 1-33 and 48-54.
- Work: Edit.

**24. Glossary**

- Path: `reference/glossary.md`
- Purpose: Define every term of the manual in one line.
- Source: README 1080-1120, and new terms.
- Work: Edit.

## Page rules

All pages follow these rules. The builder checks them in the pull request.

**Front matter.** `title`, `description` (one sentence, plain words) and
`weight`. No other field in the first version.

**Headings.** The title is in front matter. The body starts with an H2 (`##`).
Use H2 and H3 only. A `###` of the README that becomes a page is promoted to
`##`.

**Opening.** Each page starts with one or two sentences: what the reader can do
after this page. A reader who arrives from a search or a link must understand
the page without the page before it.

**Terms.** Define each technical term in brackets at its first use on each page,
in a short phrase. Expand each acronym at its first use on each page. Do not
only link to the glossary. (My call.)

**Language.** Short sentences, one idea each. Plain verbs: "use", not
"leverage". No idioms. No references to sports, the military or the culture of
one country. Keep every number, limit and name. Do not make a fact simpler: only
the language gets simpler.

**Steps.** A how-to page uses numbered steps. One action in each step. Write
each button and menu name in bold, exactly as the web page shows it.

**Commands.** Before each command block, one sentence says what the command does
and what each flag means. After it, one sentence says what the reader should
see. A code block may have a line over 80 characters only when the command
cannot be wrapped. (My call.)

**Failure.** Where a step can fail, link to the entry of `troubleshooting` that
fixes it.

**Links.** Links between pages use Hugo references (the `relref` function), so
that a broken link fails the build. The old anchors map to new pages (see "Link
map").

**Diagrams.** Two inline SVG diagrams (pictures written as text inside the page)
are welcome (My call.), and each needs a text alternative: (1) on Start here,
the three parts and how they connect: the AI app starts email-mcp, email-mcp
talks to the mail server, and the person uses the web pages; (2) on
`confirmations`, the path of a send: request, level check, confirmation, undo
window, delivery. Both come from README text. They show no number that the text
does not hold.

**Texts copied from the program.** These texts must be exact:

- the line that marks untrusted content (README 216-217;
  `internal/mcpserver/shape.go` line 25);
- the risk text of "Confirm sends" (README 324; `internal/web/settings.go` lines
  45-48);
- the refusal text for `USE_OAUTH=true` (README 726;
  `cmd/email-mcp/importenv.go` lines 31-32);
- the quoted text of the Security section (README 379-384);
- the certificate and "no encryption" warnings of the web pages. `PRODUCT.md`
  says that their wording stays.

Copy such a text from the code, not from the README. If the two differ, the code
wins, and the difference goes in the pull request description.

**Names of people and projects.** Use `email-mcp` for the name. No logo.

**What not to write.** No customers, quotes, numbers of users, or speed claims.
`PRODUCT.md` says that none exist. No weakness that the sources do not already
publish, and no steps to attack.

## Coverage table

Every part of `README.md`, `DOCKER.md` and `docs/harnesses.md` goes to a page.
The first column names the part. The page paths are under
`site/content/manual/`. In the three tables, `GS/` means `getting-started/`,
`DU/` means `daily-use/`, and `REF/` means `reference/`. (My call.)

### README.md

The five rows that start with "Confirmations" are parts of one README section
(264-371).

| README part | Lines | Goes to |
|---|---|---|
| Intro and bullets | 1-19 | `_index.md` |
| Contents list | 21-41 | Not carried: the sidebar replaces it |
| Install | 43-90 | `GS/install.md` |
| Quick start, step 1 | 94-145 | `GS/connect-account.md` |
| Quick start, step 1b | 147-169 | `GS/connect-account.md` |
| Quick start, step 2 | 171-181 | `GS/connect-ai-app/claude-code.md` |
| Quick start, example | 182-183 | `GS/first-use.md` |
| Quick start, step 3 | 185-192 | `GS/first-use.md` |
| What the agent can do | 194-233 | `DU/what-the-agent-can-do.md` |
| Permission levels | 235-262 | `DU/permission-levels.md` |
| Confirmations: switches | 264-297 | `DU/confirmations.md` |
| Confirmations: non-ASCII | 299-318 | `DU/confirmations.md` |
| Confirmations: risk, autonomy | 320-337 | `DU/full-autonomy-and-limits.md` |
| Confirmations: guardrails | 339-354 | `DU/full-autonomy-and-limits.md` |
| Confirmations: Activity page | 356-371 | `DU/activity-and-undo.md` |
| Security | 373-452 | `DU/security.md` |
| Deny rules | 454-520 | `DU/deny-rules.md` |
| Honest limits | 522-536 | `DU/deny-rules.md` |
| Stronger: the sandbox | 538-585 | `DU/deny-rules.md` |
| The master key | 587-611 | `REF/master-key.md` |
| Changing the master key | 613-634 | `REF/master-key.md` |
| Local HTTP mode | 636-696 | `REF/local-http-mode.md` |
| Moving from the old email-mcp | 698-734 | `REF/move-from-old-version.md` |
| Known limitations | 736-760 | `REF/known-limitations.md` |
| Troubleshooting | 762-804 | `DU/troubleshooting.md` |
| Troubleshooting: reconnect | 805-829 | `DU/manage-accounts.md` |
| Troubleshooting: more help | 831-839 | `DU/troubleshooting.md` |
| Commands | 841-858 | `REF/commands.md` |
| Where the data lives | 860-876 | `REF/data-folder.md` |
| Other AI apps | 878-881 | `GS/connect-ai-app/_index.md` |
| Verifying a release | 883-930 | `REF/verify-a-release.md` |
| Making a release | 932-1057 | Tech docs (foundation) |
| Development | 1059-1074 | Tech docs (foundation) |
| License | 1076-1078 | Short README, and a line on `_index.md` |
| Glossary | 1080-1120 | `REF/glossary.md` |

Notes on the table:

- "Making a release" includes the MCP Registry paragraph (1048-1057). Both are
  for maintainers: they need repository settings, tokens and release commands.
  No user does these steps.
- "Development" (1059-1074) is for people who change the code. It also holds the
  link to the design document.
- "License": the short README keeps it. The start page repeats it in one line.

### DOCKER.md

| DOCKER.md part | Lines | Goes to |
|---|---|---|
| Title and short version | 1-4 | `REF/containers.md` |
| Paragraph about hosted mode | 6-9 | `REF/containers.md` (reworded) |
| Why the container does not work | 11-33 | `REF/containers.md` |
| What the image is: base, pins | 35-47 | Tech docs |
| What the image is: run, publish | 48-54 | `REF/containers.md` |
| Build it yourself | 56-65 | Tech docs |
| Try it | 67-79 | Tech docs |

Why three parts go to the tech docs: lines 35-47 describe how the image is built
and fixed to exact versions. Lines 56-65 and 67-79 need a copy of the source
code and a local build, and `Try it` uses the image from `Build it yourself`. No
user needs them to use email-mcp. The tech docs spec must take these line
ranges.

### docs/harnesses.md

| harnesses.md part | Lines | Goes to |
|---|---|---|
| Intro | 1-17 | `GS/connect-ai-app/_index.md` |
| How each app confirms a send | 19-59 | `DU/app-confirmation.md` |
| Trust in the app | 61-66 | `DU/app-confirmation.md` (merged) |
| Browser-control agents | 68-80 | `GS/connect-ai-app/_index.md` (merged) |
| Claude Code | 82-139 | `GS/connect-ai-app/claude-code.md` |
| Claude Desktop | 141-187 | `GS/connect-ai-app/claude-desktop.md` |
| Cursor | 189-234 | `GS/connect-ai-app/cursor.md` |
| VS Code | 236-292 | `GS/connect-ai-app/vscode.md` |
| Codex CLI | 294-341 | `GS/connect-ai-app/codex-cli.md` |

Lines 61-66 and 68-80 overlap with README text that stays in other pages. See
"Duplicates to merge" for the exact sentences that survive.

## More than a move

For each page that needs more than a move, this section says exactly what
changes. A page that is not here moves with link fixes only.

### Start here (`_index.md`)

Rewrite README 1-19 as a start page. Keep the facts. Change:

1. Open with what email-mcp is, in the five bullets of README 7-15. Keep their
   wording. Define MCP and IMAP in place, as README 3-9 does.
2. Add "What you need": a mail account at a provider that offers IMAP and SMTP,
   with a password or an app password; a terminal; an AI app that can use MCP
   servers. Each item comes from README 3-9, 47-66 and 94-136.
3. Add "The way in": the four getting-started pages as a numbered list with one
   sentence each.
4. Add "Find your task": 10 to 12 lines of the form "I want to ..." with one
   link each, made from the page list. Include at least: let the agent send
   mail; turn off the access of the agent to an account (level 0); cancel a
   send; see what the agent did; fix a failed login; connect a second account.
5. Add "What is not in this version", in plain words, with no date: mail only.
   Calendar and contacts, sign-in with Google or Microsoft (OAuth), and a hosted
   mode for many people are planned and not available. Until then, connect
   Google and Microsoft accounts with an app password. This replaces the words
   "come next" and "come later" of README 17-19, and it uses the word "planned"
   as the landing page does. (My call.)
6. End with the license line: MIT, with a link to the `LICENSE` file on GitHub.

### Install (`install.md`)

README 43-90 mixes ways for users with ways for developers. Change:

1. Add two sentences before the list: which way to choose. Homebrew for macOS
   and Linux. The release archive for Windows, or when you want to check the
   signature. Go or the source code for developers. All of this is in README
   47-80.
2. Keep the order Homebrew, release archive, Go, source code, Docker. Put "Go"
   and "From the source code" under a heading "For developers". (My call.)
3. Keep the Apple paragraph (README 55-60) as it is.
4. Define PATH at its first use: the list of folders where the shell looks for
   programs.
5. Replace `[Verifying a release](#verifying-a-release)` and the Docker link
   with links to `verify-a-release` and `containers`.
6. Keep `email-mcp version` as the check. Say what the reader should see: the
   word `email-mcp` and the version (code: `cmd/email-mcp/main.go`, function
   `runVersion`).

### Connect your first account (`connect-account.md`)

README 94-169 puts the hosts-file note before the steps. A new reader should see
the steps first. New order, with no change to the sentences:

1. Opening, new: one sentence for what the page does, and one for what the
   reader needs (the email address, and the password or app password).
2. `email-mcp setup` and what it prints (README 96-107). Keep the link and
   sign-in times.
3. The steps in the browser (README 126-136), then the corrections below.
4. A short H2 "App passwords". It holds the second sentence of README 132-134
   ("Many providers ... need an app password: a separate password that you make
   in the security settings of your mail account"). Step 3 of the browser steps
   keeps its first sentence and links here. The landing page and the glossary
   link to this heading, so keep its name. (My call.)
5. "If your browser does not find `email-mcp.localhost`" (README 108-115 and
   117-124).
6. "Stop `setup` when you are done" (README 138-139).
7. "A local bridge" and certificate pinning (README 141-145).
8. A second heading "Without a browser" (README 147-169). Rename the headings
   "1." and "1b." to "In the browser" and "Without a browser".

Corrections to the steps. The real flow has more steps than the README shows
(see "Differences to resolve", item 5). The builder follows the web pages in a
real build and writes:

1. Click **Connect account**. With no account yet, the button reads **Connect
   your first account**.
2. Type the address, click **Find the server settings** (or **Enter the settings
   yourself**).
3. Check the suggestion. The page warns that the settings are only suggestions
   and that the host names must belong to the provider. Click **Use these
   settings**.
4. Check the user name (the page fills it in), and type the password or app
   password.
5. If a certificate warning shows, read the fingerprint. The page has the boxes
   **Trust this certificate** and **I understand the risk**.
6. Click **Test the connection and save**.

Also: the link `the security section` (README 119) points to `security`.

### Connect an AI app (hub, `connect-ai-app/_index.md`)

New page from README 878-881 and `docs/harnesses.md` 1-17 and 68-80. It has:

1. What an AI app is for email-mcp: the app (also called a harness) starts the
   server and calls its tools. From harnesses 3-5 and the glossary lines
   1084-1086.
2. Before you start: connect an account (harnesses 7-8).
3. Two facts for every app (harnesses 10-13): the config of the app never holds
   your mail password; the one setting an app may need is `EMAIL_MCP_KEK`, only
   when your master key is a file.
4. A table: App, how it starts email-mcp, link to its page. Five rows. The
   builder writes it from the five app pages.
5. "Another app": an app that can start a program over stdio uses the command
   `email-mcp serve`. An app that cannot uses `reference/local-http-mode`. If an
   app cannot find `email-mcp`, give it the absolute path from `which email-mcp`
   (README 838-839). (My call.)
6. The meaning of **[not verified]** (harnesses 15-17).
7. One short warning about agents that control a browser, with a link to
   `security`. See "Duplicates to merge".

### The five app pages

Use the same headings in the same order, where the source has the part:

1. Source and date (the first line of each section of harnesses.md).
2. Add email-mcp (stdio).
3. If your master key is a file.
4. Local HTTP (not on the Claude Desktop page: harnesses 47 says it is not used
   there).
5. Confirmation.

Link fixes: every link to a README anchor goes through the "Link map". For
`claude-code.md`, merge README 171-181 with harnesses 82-102: keep the stdio
definition of README 178-180 and the key-file command and flag-order note of
harnesses 93-101. The example question (README 182-183) goes to `first-use`.

### Your first use (`first-use.md`)

New short page. Use only facts from the sources. It has:

1. Run `email-mcp doctor` (README 185-192). Every line says `ok` and none says
   `FAIL`. Link to `troubleshooting` for a `FAIL`.
2. Start the AI app and ask the example question (README 182-183).
3. What a new account may do (README 250-255): read, organize and write drafts
   without questions; every send asks you; moving mail to the Trash is not
   possible until you raise the level to 5.
4. Optional: ask the agent to send a message to your own address, to see the
   approval step. The builder keeps this step only after doing it on a real
   build, with the approval link, the button and `get_send_status`. (My call.)
5. Where to go next: `permission-levels`, `confirmations`, `security`.

### What the agent can do (`what-the-agent-can-do.md`)

Move README 194-233, with these changes:

1. New opening of 3 or 4 sentences: the server offers tools (a tool is one
   action that the agent can call, such as `search_messages`); you do not call
   tools, you ask the agent in normal words; each tool needs a level.
2. Keep the table (README 200-213). A cell over about 300 characters moves to a
   list under the table, headed "Details", with the same words. The
   `get_message` and `search_messages` cells are the long ones. The table keeps
   one short sentence for each.
3. Add "failed" to `get_send_status` (see "Differences to resolve", item 1).
4. Keep README 215-233 as three short sections: untrusted content, addresses in
   received mail, attachments.
5. Fix "see the next section" (README 197) with a link to `permission-levels`.

### The web pages (`web-pages.md`)

New page. Its source is the code. The builder reads `internal/web/server.go`
lines 206-229 (the routes), `internal/web/templates/layout.html` lines 52-56
(the menu) and the template of each page. It has:

1. The address `email-mcp.localhost` and how to sign in: one sentence and a link
   to `connect-account`. Do not repeat the steps.
2. The menu, one short entry for each, with a link to the page that explains it:
   **Waiting for you** (`confirmations`), **Accounts** (`manage-accounts`),
   **Activity** (`activity-and-undo`), **Connections**
   (`reference/local-http-mode`), **Sign out**.
3. What opens first: the page `/` goes to **Waiting for you**, or to
   **Accounts** when no account exists (`internal/web/accounts.go` lines
   280-298).
4. Pages with no menu entry: Connect an account, Permissions and settings,
   Reconnect, and one action to approve.
5. What a browser shows when it is not signed in: a page that says to run
   `email-mcp setup` (`internal/web/templates/no_session.html`).
6. A sentence on the sign-in times, with a link to `connect-account`.

No colours or sizes. Only what the code does. The images are listed in
"Screenshots".

### Permission levels (`permission-levels.md`)

1. Remove the Calendar and Contacts columns of the table (README 241-248). They
   describe things that cannot be installed. Say in one sentence that this
   version has the mail area only (README 237-238). The full table stays in the
   design document, section 7.1. The sentence "This table comes from the design"
   (README 239) is not carried: the table is no longer that table. (My call.)
2. Add a column "Name on the web page": Off, Read, Organize, Draft, Send, Delete
   (code: `internal/domain/account/account.go` lines 60-78). Add one sentence:
   level 5 is named Delete on the web page, and it means moving mail to the
   Trash. No tool deletes mail for good. See "Differences to resolve", item 11.
3. Add the way to open the settings: **Accounts**, then **Permissions and
   settings** on the account, then the **Mail level** list and **Save**
   (`internal/web/templates/accounts.html` line 23, `settings.html` lines
   46-50).
4. Keep README 257-262 (the agent cannot change a level; the requested level and
   **Apply requested level N**).

### Confirmations (`confirmations.md`)

Move README 264-318, with these changes:

1. New opening of 2 sentences: a confirmation is your "yes" before the agent can
   send mail or move mail to the Trash.
2. Replace **Approve** (README 288) with the real button names **Approve and
   send** and **Approve and move to Trash**, and keep **Reject**. See
   "Differences to resolve", item 3.
3. Fix three links: "Local HTTP mode" (README 285) to `local-http-mode`; the new
   page `app-confirmation` after README 297; the "Waiting for you" page to
   `web-pages`.
4. Add a link from the paragraph on what goes out to the Security page, where
   "What you approve is what goes out" stays (README 401-435).
5. Keep the section "Addresses outside ASCII" (README 299-318) as an H2.

### How each AI app confirms (`app-confirmation.md`)

Move harnesses 19-59. Merge harnesses 61-66 with README 291-297 (see "Duplicates
to merge"). Change "the Approvals page" (harnesses 35) to "the **Waiting for
you** page". Open with one sentence and a link to `confirmations`. Keep the
**[not verified]** marks exactly, and the date 2026-10-29.

### Full autonomy, limits and settings (`full-autonomy-and-limits.md`)

Move README 320-354. Keep the risk text (README 324) exact. Add a final H2 "All
settings of an account": a table with Setting, What it does, Default. The
builder fills it from `internal/web/templates/settings.html` lines 43-98 and
`internal/domain/account/account.go` (`NewIMAPAccount`, `DefaultLimits`). Rows:

| Setting (as on the page) | Default |
|---|---|
| Mail level | 4 Send |
| Confirm sends | On |
| Confirm deletes | On |
| Sends per hour | 20 |
| Sends per day | 200 |
| Recipients per message | 50 |
| Messages moved to Trash per day | 500 |
| Undo window without a confirmation | 30 seconds |
| Undo window after a confirmation | 0 seconds |
| Ask before an address never written to | Off |
| Save a copy of sent mail to Sent | Off (check in a build) |

Also state the valid values: counts are whole numbers from 0 to 1,000,000, and
undo windows are 0 to 300 seconds (`internal/web/settings.go`, text
`msgBadLimits`). The last two rows are not in the README yet. See "Differences
to resolve", items 6 and 7. Write one sentence on each from the label on the
page.

### Activity and undo (`activity-and-undo.md`)

Move README 356-371. Add: the **Queued sends** list with a **Cancel** button,
and the **Log** filters Account, Tool, Type and Entries (code:
`internal/web/templates/activity.html` lines 12-58). Add a link to
`full-autonomy-and-limits` for the undo window, and the command
`email-mcp outbox cancel <id>` (README 347-348) with a link to `commands`.

### Security (`security.md`)

This is the public statement of what local mode protects against, so it moves
with few edits:

- The builder checks every sentence of README 373-452 against the code. A
  sentence that no longer matches is corrected, and the pull request lists the
  correction.
- The quoted block stays word for word, because it is a quote from the design.
  If the code has changed what the quote says, a short note after the quote
  gives the correct fact.
- README 384, inside the quote, recommends hosted mode, which cannot be installed
  yet. A one-line note after the quote says so, and says that a separate
  operating system user is the option for now. (My call.)
- Other allowed edits: (a) the link to the design document becomes the
  full address that is shown below, because `docs/` is not part of the site; (b)
  "The next section" (README 389-390) becomes a link to `deny-rules`; (c) "(see
  the known limitations)" (README 409) becomes a link to `known-limitations`.
- No introduction is added. The front matter `description` is the only new text.

The full address of the design document (the line is longer than 80 characters
because it cannot be wrapped):

```text
https://github.com/EmadMokhtar/email-mcp-go/blob/main/docs/superpowers/specs/2026-10-27-email-mcp-rewrite-design.md
```

### Recommended Claude Code deny rules (`deny-rules.md`)

Move README 454-585. One edit: README 582-583 says "use hosted mode (milestone
M3), or run email-mcp under a separate operating system user". Hosted mode
cannot be installed. Change the sentence to "run email-mcp under a separate
operating system user" and keep the sentence about the PreToolUse hook (a script
that Claude Code runs to check each command) after it. (My call.) The same words
are in the quoted block of the Security page (README 384), which does not
change. See "Settled decisions", item 2.

### Manage accounts (`manage-accounts.md`) and Troubleshooting

README 805-829 holds two long fixes inside the Troubleshooting list. A reader
who changed a password looks for "reconnect", not for "troubleshooting". Change:

1. `manage-accounts` takes README 805-829 as two H2 sections, "Reconnect an
   account" (805-824) and "Change the server settings of an account" (825-829),
   with the text unchanged.
2. `manage-accounts` opens with the other tasks and where they are: add an
   account (`connect-account`), list and test (`reference/commands`), remove
   (README 826-829 and the **Remove account** button, `settings.html` lines
   108-113).
3. `troubleshooting` keeps README 762-804 and 831-839. In the place of 805-829
   it keeps the two bold opening phrases as short entries with one sentence and
   a link.
4. Make each bold opening phrase of the list an H3, with the same words, so that
   other pages can link to it. (My call.)
5. Add one entry: "A web page says to run `email-mcp setup`". Your sign-in ended
   or this browser never signed in. Run `email-mcp setup` again (README 102-106;
   `internal/web/templates/no_session.html`).
6. Translate each error the agent shows into plain words as the README does. Do
   not add messages that the code does not produce.

### Commands (`commands.md`)

Move the table (README 843-855). Change:

1. Add the row `email-mcp help` (code: `cmd/email-mcp/main.go` lines 76 and
   104).
2. Add a second table "Flags": for each command, the flags from
   `email-mcp <command> -h`. The builder copies them from the real output, not
   from memory. The flag names in the code today are listed in "Differences to
   resolve", item 9, for checking.
3. Keep README 857-858 (`--data-dir`, `-h`).

### The master key (`master-key.md`)

Move README 587-634. Add a first sentence: most people never need this page,
because the default is the keychain (the password store of the operating
system). Keep the command example (README 605-611) here. It is also on
`claude-code`.

### Local HTTP mode (`local-http-mode.md`)

Move README 636-696. Add:

1. A sentence at the top: use this only for an AI app that cannot start a
   program (README 638). Link to the app pages for the config of each app.
2. A section "Make a token in the browser": the **Connections** page has a **New
   token** form with Name, Permissions, the box **Let this app ask me in its own
   window** and the button **Make a token**, and a **Revoke** button for each
   token (`internal/web/templates/connections.html` lines 21-55). README 678-679
   names the page only for the in-band box. See "Differences to resolve", item
   8.
3. Fix the link to `docs/harnesses.md` (README 681) to the hub page.

### Containers (`containers.md`)

Take DOCKER.md 1-33 and 48-54. Change:

1. Lines 6-9 and 23-24 explain the image as groundwork for hosted mode. Hosted
   mode cannot be installed. Say instead: the image is for testing and for
   building on, not for daily use; a hosted mode for many people is planned and
   not available. No date. (My call.)
2. Replace "see the README" (DOCKER.md 4) with a link to `install`, and "The
   README shows how to verify it" (DOCKER.md 54) with a link to
   `verify-a-release`.
3. Add one sentence that points to the tech docs for building the image.
4. Keep README 82-84 (the Docker note of Install) as one sentence on `install`.

### Verifying a release (`verify-a-release.md`)

Move README 883-930. The examples use `v0.1.0` and `0.1.0` (README 902-906, 923,
929). Keep them as examples but show them as placeholders (a word that the
reader replaces): write `<version>` where the version goes, or name the example
in a sentence ("Replace `0.1.0` with the version that you downloaded"). The
builder does not change a command in any other way, because the signature check
depends on the exact identity text (`.github/workflows/release.yml`).

### Glossary (`glossary.md`)

Move README 1080-1120. Add the terms that the manual uses and the README does
not define. Each definition comes from README text: agent, tool, level, scope,
token, approval link, in band, web pages, outbox, data folder, activity log,
request and action (README 356-361), draft, Trash, fingerprint (README 141-145),
hosts file, quarantine and Gatekeeper (README 55-58), PATH, environment
variable, terminal, rotate (README 615-616). The builder adds only terms that
appear on a page.

### Pages that move unchanged

These move with link fixes only: `claude-desktop`, `cursor`, `vscode`,
`codex-cli` (apart from the shared headings), `data-folder`,
`move-from-old-version`, `known-limitations`. In `known-limitations` and
`move-from-old-version` the text is not edited.

## Duplicates to merge

The sources repeat some text. Each item says which copy stays and which
sentences of the other copy are carried over.

1. **Trust in the app** (README 291-297 and harnesses 61-66). The README text
   stays in `confirmations`. Carry these sentences to `app-confirmation`: "Only
   use apps that show the form to you"; "`--in-band-confirm` is your statement
   that you trust the app of that token"; "A token in a config file in plain
   text is a reason to leave the flag off".
2. **Agents that control a browser** (README 392-399 and harnesses 68-80). The
   README text stays in `security`, as it is. The hub page gets one warning
   sentence with a link, and the phrase "open the approval links" (harnesses
   77).
3. **Add to Claude Code** (README 171-181 and harnesses 82-102). One text in
   `claude-code`. Keep the stdio definition (README 178-180), the key-file
   command and the flag-order note (harnesses 93-101).
4. **The key-file command** (README 605-611 and harnesses 93-101). Both copies
   stay, in `master-key` and `claude-code`. The same command is useful in two
   places.
5. **In band or link** (README 280-289 and harnesses 21-39). Both copies stay.
   Harnesses adds the reason that the HTTP server is stateless (it keeps no
   memory between requests), and "Use stdio when the app supports it".
6. **Three one-sentence repeats.** The Connections page and the in-band box
   (README 678-679, harnesses 121-122). The log folder of Claude Desktop (README
   836-837, harnesses 182-183). The absolute path of the binary (README 838-839,
   harnesses 151-153). Both copies stay.

An author who changes the key-file command changes both copies. The code-area
map names them.

## Link map

Every link in the three source files changes. The first column is the old
target.

| Old target | New page |
|---|---|
| `#install` | `getting-started/install` |
| `#quick-start` | `getting-started/connect-account` |
| `#1-connect-an-account-in-the-browser` | `getting-started/connect-account` |
| `#2-add-it-to-claude-code` | `getting-started/connect-ai-app/claude-code` |
| `#what-the-agent-can-do` | `daily-use/what-the-agent-can-do` |
| `#permission-levels` | `daily-use/permission-levels` |
| `#confirmations-and-full-autonomy` | `daily-use/confirmations` |
| `#security-what-local-mode-protects-against` | `daily-use/security` |
| `#recommended-claude-code-deny-rules` | `daily-use/deny-rules` |
| `#the-master-key` | `reference/master-key` |
| `#local-http-mode` | `reference/local-http-mode` |
| `#moving-from-the-old-email-mcp` | `reference/move-from-old-version` |
| `#known-limitations` | `reference/known-limitations` |
| `#troubleshooting` | `daily-use/troubleshooting` |
| `#verifying-a-release` | `reference/verify-a-release` |
| `DOCKER.md` | `reference/containers` |
| `docs/harnesses.md` | `getting-started/connect-ai-app` |
| `docs/superpowers/specs/2026-10-27-...` | Full GitHub address (see Security) |
| `LICENSE` | Full GitHub address |

Places with links: README 64, 84, 119, 192, 285, 377, 654-655, 681, 696,
733-734, 744, 817, 1054, 1074; `DOCKER.md` 4 and 54; `docs/harnesses.md` 8, 13,
22, 80, 114 and 139.

## Screenshots

The owner asked for screenshots from the real application, and for automation
that updates them when the UI changes. The foundation, in its section
"Screenshots", says how they are made. This section says which shots the manual
has.

Each shot is one screen of the running program with sample data, at desktop
width, in light and dark. (My call.)

| Name | Screen and state | How the test gets there | Pages |
|---|---|---|---|
| `accounts-empty` | Accounts with no account, and the button **Connect your first account** | Sign in before an account is connected | `getting-started/connect-account` |
| `connect` | The first form of Connect account | Click the button | `getting-started/connect-account` |
| `accounts` | Accounts with one connected account | Connect the sample account | `daily-use/manage-accounts`, `daily-use/web-pages` |
| `account-settings` | The settings page of the account, with its level | Open the settings | `daily-use/permission-levels`, `daily-use/manage-accounts` |
| `waiting` | **Waiting for you** with one send open, with **Reject** and **Approve and send** | An MCP client calls `send_message` | `getting-started/first-use`, `daily-use/confirmations`, `daily-use/web-pages` |
| `result-sent` | The result after an approved send | Click **Approve and send** | `daily-use/confirmations` |
| `activity` | Activity, with the tool calls and the send | Open the page after the steps above | `daily-use/activity-and-undo` |
| `connections` | Connections, with the list of tokens | Make a sample token, then open the list | `reference/local-http-mode` |
| `reconnect` | The Reconnect page | Open the page | `daily-use/manage-accounts` |
| `no-session` | The page for a browser that is not signed in | Open a page with no session | `daily-use/troubleshooting` |

Rules for the pages:

- A shot comes right after the step that it shows, with at most one shot in a
  section.
- The steps stay in the text, and each button keeps its sentence. The image only
  helps.
- The alt text says what the screen shows and names the buttons.
- The `connections` shot shows the list, and never the notice with a new token.
- `connect` shows the first form only. The step that suggests server settings
  looks up the mail provider, and the test uses no outside network. The text
  describes that step.

The builder may add a shot when a step is hard to follow without it. Each new
shot needs a row in this table.

## Code area to page

A pull request that changes a code area checks the pages named after it. Pages
are named by file name, without `.md`. The folder names were checked in the
repository on 2026-11-05.

### Commands: `cmd/email-mcp`

- `main.go` (the command list, help): `commands`.
- `serve.go`: `commands`, `local-http-mode`, and the five app pages.
- `setup.go`: `connect-account`, `web-pages`, `commands`.
- `account.go`, `connect.go`, `reconnect.go`: `connect-account`,
  `manage-accounts`, `commands`.
- `token.go`: `local-http-mode`, `commands`.
- `outbox.go`: `activity-and-undo`, `commands`.
- `importenv.go`: `move-from-old-version`, `commands`.
- `key.go`: `master-key`, `commands`.
- `doctor.go`: `first-use`, `troubleshooting`, `commands`.
- `console.go` (flags that all commands share): `commands`, `data-folder`.

### Tools, levels and limits

- `internal/mcpserver/read_tools.go` and `write_tools.go`:
  `what-the-agent-can-do`, `permission-levels`.
- `internal/mcpserver/shape.go`: `what-the-agent-can-do`, `known-limitations`.
- `internal/mcpserver/elicit.go`: `confirmations`, `app-confirmation`, and the
  five app pages.
- `internal/mcpserver/errors.go`: `troubleshooting`.
- `internal/mcpserver/capture.go`: `activity-and-undo`.
- `internal/policy/policy.go`: `permission-levels`, `local-http-mode`.
- `internal/policy/limiter.go`: `full-autonomy-and-limits`.
- `internal/domain/account`: `permission-levels`, `full-autonomy-and-limits`.
- `internal/tokens`: `local-http-mode`.
- `internal/httpserve`: `local-http-mode`, `app-confirmation`.
- `internal/confirm`: `confirmations`, `app-confirmation`.
- `internal/outbox`: `full-autonomy-and-limits`, `activity-and-undo`,
  `known-limitations`.
- `internal/audit` (the activity log): `activity-and-undo`.

### Web pages: `internal/web`

- `server.go` (the routes) and `templates/layout.html` (the menu): `web-pages`.
- `session.go`, `guard.go`, `links.go`: `connect-account`, `web-pages`,
  `security`.
- `accounts.go` and `templates/connect.html`: `connect-account`.
- `templates/accounts.html`: `manage-accounts`, `web-pages`.
- `settings.go` and `templates/settings.html`: `permission-levels`,
  `full-autonomy-and-limits`, `manage-accounts`.
- `reconnect.go` and `templates/reconnect.html`: `manage-accounts`.
- `approvals.go`, `templates/approval.html` and `approvals.html`:
  `confirmations`, `web-pages`.
- `activity.go` and `templates/activity.html`: `activity-and-undo`.
- `connections.go` and `templates/connections.html`: `local-http-mode`,
  `web-pages`.
- `templates/no_session.html` and `use_name.html`: `connect-account`,
  `troubleshooting`.

### Other areas

- `internal/mime`: `what-the-agent-can-do`, `security`, `known-limitations`.
- `internal/provider/imapsmtp`: `what-the-agent-can-do`, `connect-account`,
  `troubleshooting`.
- `internal/discovery` and `internal/connect`: `connect-account`,
  `manage-accounts`.
- `internal/secrets` and `internal/app/kek.go`: `master-key`, `data-folder`.
- `internal/app/app.go`: `data-folder`, `activity-and-undo`.
- `internal/store`: `data-folder`, `troubleshooting`.
- `internal/version`: `install`.
- `.goreleaser.yaml`: `install`, `verify-a-release`.
- `.github/workflows/release.yml`: `verify-a-release`.
- `Dockerfile`: `containers`.

No manual page: `internal/service` (it follows the area of the change),
`internal/signing`, `internal/inflight`, `internal/errtype`,
`internal/thirdparty`, `internal/e2e` and `internal/testutil`.

The builder adds the paths of this list to the front matter of each page as
`code_areas`, a list that the template does not show. The foundation requires it
on every manual page. At the cutover, this map also moves to the tech docs page
`local-development`.

## Differences to resolve

I compared a sample of the README with the code on 2026-11-05. Where the code
and a document differ, the code wins.

### Checked and equal

- The 20 tools: names and levels match `internal/mcpserver/read_tools.go` (lines
  166-203) and `write_tools.go` (lines 230-300).
- The commands: the nine commands and their subcommands match the lists in
  `cmd/email-mcp/main.go` (line 30), `account.go` (line 29), `token.go` (line
  33), `outbox.go` (line 20) and `key.go` (line 24).
- The levels 0 to 5, the new account at Mail 4, and both confirmations on
  (`internal/domain/account/account.go` lines 52-57 and 312-325).
- The default limits: 20 sends per hour, 200 per day, 50 recipients, 500 moves
  to Trash per day, 30 and 0 seconds of undo, at most 300 seconds (`account.go`
  lines 197 and 218-227).
- The times: setup link 10 minutes, sign-in 24 hours and 2 hours without use
  (`internal/web/session.go` lines 29-36), approval link 24 hours
  (`internal/confirm/confirm.go` line 129), token 90 days by default and 3650 at
  most (`internal/tokens/tokens.go` lines 48-52), log kept 90 days
  (`internal/app/app.go` line 66).
- The sizes: 100 results, snippet of 200 characters, preview of 500 characters
  and 20 messages (`internal/mcpserver/read_tools.go` line 21,
  `internal/provider/imapsmtp/summary.go` lines 22-35,
  `internal/confirm/confirm.go` lines 119-122).
- The install targets (`.goreleaser.yaml` lines 47-52), the checks of `doctor`
  (`cmd/email-mcp/doctor.go` lines 17-27), the refusal text for `USE_OAUTH` and
  the risk text of Confirm sends.

### Differences

1. **`get_send_status`.** README 208 says "queued, sent, or waiting for
   approval". The code description (`read_tools.go` line 203) also says
   "failed". Add "failed".
2. **Size unit.** README 207 and the tool texts say "10 MB" (`read_tools.go`
   line 198, `write_tools.go` lines 79 and 259, `errors.go` lines 311 and 314).
   The limit is `10 << 20` bytes, which is 10 MiB (`internal/mime/body.go` line
   22). A MiB is 1,048,576 bytes. A MB is 1,000,000 bytes. The README uses MiB
   for other sizes (206, 207). See "Settled decisions", item 4.
3. **Approve button.** README 288 says "click **Approve** or **Reject**". The
   buttons read **Approve and send** or **Approve and move to Trash**, and
   **Reject** (`internal/web/templates/approval.html` lines 49 and 53).
4. **Name of the page.** `docs/harnesses.md` 35 says "the Approvals page". The
   menu says **Waiting for you** (`layout.html` line 52), and the README uses
   that name (287, 668). `PRODUCT.md` 40 says "Approvals" and the route is
   `/approvals`. Use **Waiting for you**.
5. **Connect steps.** README 128-136 skips a step. After **Find the server
   settings**, the page shows the suggestions with the warning to check the host
   names and the button **Use these settings** (`connect.html` lines 34-57).
   With no account, the first button reads **Connect your first account**
   (`accounts.html` line 33). The certificate step has the boxes **Trust this
   certificate** and **I understand the risk** (`connect.html` lines 146 and
   152).
6. **New-recipient check.** README 303-304 names "the new-recipient check" and
   never explains it. The setting is on the settings page: "Ask me before a
   message goes to an address that this account never wrote to" (`settings.html`
   line 90). Default off (`account.go` lines 212-226).
7. **Copy of sent mail.** No document mentions the setting **Save a copy of sent
   mail to Sent** (`settings.html` lines 95-97; `account.go` lines 116-118). The
   code default is off. The builder confirms this in a build.
8. **Tokens in the browser.** README 641-696 describes `email-mcp token create`
   only. The **Connections** page also makes, lists and revokes tokens
   (`connections.html` lines 21-55; routes at `internal/web/server.go` lines 226
   and 227).
9. **Flags.** README 841-858 lists no flag table. The code has these flags that
   the README does not list in one place: `account add`: `--imap-port`,
   `--smtp-port`, `--imap-security`, `--smtp-security`, `--username`,
   `--accept-remote-certificate`, `--trust-fingerprint`,
   `--plaintext-i-understand` (`account.go` lines 82-98); `account reconnect`:
   `--trust-fingerprint`, `--accept-remote-certificate`,
   `--replace-pinned-certificate` (`reconnect.go` lines 52-59); `import-env`:
   `--email`, `--accept-remote-certificate`, `--trust-fingerprint`,
   `--plaintext-i-understand` (`importenv.go` lines 44-51); `serve`: `--http`,
   `--allow-origin`; `token create`: `--name`, `--scopes`, `--expires-in`,
   `--in-band-confirm`; `key rotate`: `--to`, `--yes`; `account remove`:
   `--yes`. The command `help` is also missing from the table (`main.go` lines
   76 and 104).
10. **Reconnect link.** README 806-808 says a **Reconnect** link is on the
    settings page. The button there reads **Reconnect the account**
    (`settings.html` line 104). On the Accounts page it reads **Reconnect**
    (`accounts.html` line 24).
11. **Level 5 name.** README 248 says "+ Move to Trash". The code names level 5
    **Delete** (`account.go` lines 74-75), the settings list shows "5 Delete"
    (`settings.go` line 308), and the scope is `email:delete`
    (`internal/policy/policy.go` line 76). A reader may think that Delete
    removes mail for good. The manual shows both names and says that no tool
    deletes mail for good (README 352).
12. **Version in examples.** README 902-906, 923 and 929 use `0.1.0`. The latest
    release is 0.6.0 (`.release-please-manifest.json`). The examples work as
    placeholders only if the page says so.
13. **Calendar and contacts columns.** README 241-248 shows levels for calendar
    and contacts. They cannot be installed. See "Permission levels".
14. **Features that are not available.** README 17-19 ("come next", "come
    later"), README 582-583 and 384, and `DOCKER.md` 6-9 and 23-24 name hosted
    mode or later releases. See "Start here", "Deny rules" and "Containers", and
    "Settled decisions", item 2.
15. **Front matter rules.** The foundation says content follows
    `.github/markdown.instructions.md`. That file applies to `docs/**/*.md` (its
    line 3) and asks for the fields `post_title`, `author1`, `microsoft_alias`,
    `featured_image` and `categories` from `/categories.txt`. No categories file
    exists in the repository. Those fields belong to another project. This
    document uses `title`, `description`, `weight` and `code_areas`, and keeps
    the other rules: no H1, H2 and H3 only, fenced code blocks with a language, alt text,
    and lines of 80 characters. (My call.)
16. **Missing sign-in entry.** The Troubleshooting list has no entry for the
    page that says to run `email-mcp setup` (`no_session.html`). The manual adds
    one.
17. **Dated facts.** `docs/harnesses.md` says "checked on 2026-10-29" for the
    docs of five apps (lines 84, 143-144, 191, 238, 297-298) and for Claude Code
    versions (lines 56-58, 108). Those facts are about other products. See
    "Settled decisions", item 3.

## What other parts rely on

Hugo makes the address of a page from its path. For example,
`getting-started/install.md` is served at `/manual/getting-started/install/`.
Other specs name these pages, so the paths do not change without a check of
those specs.

- The landing page spec needs the install page (`getting-started/install`), the
  five app pages under `getting-started/connect-ai-app/`, and the explanation of
  an app password (the H2 "App passwords" on `connect-account`).
- The tech docs spec must take these parts: README 932-1074 ("Making a release"
  and "Development"), and `DOCKER.md` 35-47 and 56-79.
- The foundation's pull request template points authors to the tech docs page
  `local-development`, which holds the "Code area to page" map after the cutover.

## Settled decisions

1. **Known limitations.** The page stays, as the foundation says, because hiding
   a limit would mislead users. A sentence that no longer matches the code is
   corrected. The private security review checks this page before launch.
2. **Hosted mode in the Security text.** The quote stays word for word, with a
   note after it (see "More than a move"). The same idea in README 582-583 and
   in the container text is reworded, as above.
3. **Facts about other products.** The app facts of 2026-10-29 are not checked
   again before the first publish. The pages keep their dates, so a reader knows
   when each fact was checked.
4. **MB or MiB.** The manual writes "10 MiB" for the attachment limit, and says
   in a note that messages of the program say "10 MB".
5. **Name of level 5.** The foundation sets this: "Delete", with the explanation
   that it moves mail to the Trash (see difference 11).

## Left to the builder

- The words of the new text: the start page, the three group pages, the hub
  page, `first-use`, `web-pages`, the opening sentences, and the settings table.
  The sources and the page rules above bound them.
- The two diagrams: shape, colours and text alternative.
- The sample data, the width and the crop of each screenshot.
- Which long table cells move to a "Details" list.
- The flags table, copied from `-h` output of a real build.
- Extra glossary terms, within the rule above.
- The exact heading texts. Keep each heading from the source where it exists,
  because other pages link to it.
- The check of every step. The builder follows each how-to page with a clean
  data folder, a real build of the current code and the test mail servers in
  `test/interop`, and records in the pull request description which steps were
  run.

## Definition of done

The manual's pull request is done when:

1. Every row of the coverage table has its page, and no line range of a source
   is missing.
2. The Hugo build passes with no broken link and no warning.
3. Every line of content is 80 characters or less, except code blocks that
   cannot be wrapped.
4. Every command, flag, button and menu name was checked against the code or a
   real build.
5. The texts of "Texts copied from the program" are equal to the code.
6. The pull request changes only files under `site/content/manual/`, the SVG
   files of its two diagrams, and the screenshot test in `internal/e2e`. It does
   not change the README, `DOCKER.md` or `docs/harnesses.md`.
7. A fresh agent that has not read the README follows the four getting-started
   pages against the test mail servers, and writes down where it stopped. The
   builder fixes those places.
8. The pull request follows the rules of the foundation: one issue assigned to
   `EmadMokhtar`, `Closes #N` in the body, the pull request assigned to
   `EmadMokhtar`, and a title like `docs: add the user manual`.
9. Every shot of "Screenshots" is on its pages, in light and dark, with alt text,
   and shows sample data only.

## Terms used in this document

- **Agent:** the AI program that uses the tools of email-mcp.
- **AI app (harness):** a program such as Claude Code that starts email-mcp and
  calls its tools.
- **Anchor:** the part of a link after `#`. It points to a heading.
- **Elicitation:** a way for an MCP server to ask the person a question through
  the AI app.
- **Front matter:** the block of settings at the top of a page file.
- **Hugo:** the program that builds the site from the page files.
- **IMAP / SMTP:** the protocols for reading mail and for sending mail.
- **In band:** the AI app shows the confirmation form itself.
- **KEK (key encryption key):** the master key. `EMAIL_MCP_KEK` says where it
  is.
- **MCP (Model Context Protocol):** an open standard that lets AI apps call
  tools that other programs offer.
- **Scope:** a permission of an access token, such as `email:send`.
- **Sidebar:** the list of pages at the side of every manual page.
- **stdio:** standard input and output. The AI app starts the program and talks
  to it directly, with no network.
- **Tool:** one action that the agent can call, such as `search_messages`.
- **Token:** a secret that an AI app sends to the local HTTP server.
