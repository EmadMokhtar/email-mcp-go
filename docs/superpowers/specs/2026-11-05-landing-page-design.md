# Landing page: design

This document is one of three specs that build on the
[docs site foundation](2026-11-05-docs-site-foundation-design.md):

- [User manual](2026-11-05-user-manual-design.md)
- [Tech docs](2026-11-05-tech-docs-design.md)
- Landing page (this document)

It follows the foundation and does not reopen its decisions. It gives a skilled
builder everything needed to make the page at `/` without asking anyone. The
words of the page are final. The builder chooses only the code.

Where this spec makes a choice that the foundation, `PRODUCT.md` and
`DESIGN.md` do not make, the choice is marked "(My call.)". The owner can change
any of them at review.

Terms used in this spec are defined where they first matter. The Glossary at
the end lists all of them. The first four are needed at once:

- **MCP** (Model Context Protocol): an open standard that lets AI apps use tools
  that other programs offer. email-mcp is an MCP server.
- **IMAP** and **SMTP**: the standard ways to read mail and to send mail.
- **Hero**: the first block of a landing page, with the headline, the main
  action and a picture.
- **Truth table**: the list in this spec of each claim and its proof.

## Intent

The README does every job today. A person who has never heard of email-mcp
cannot see what it does without reading a long file. The landing page fixes this
with one short page.

The page does two jobs, in this order:

1. It tells a new reader what email-mcp does, who decides, and where it stops.
2. It sends the reader to the next step: the manual, to install it and set up an
   account. A developer can use the same page to reach the tech docs.

The page is not documentation. It never replaces the manual.

## Reader

A person who has never heard of email-mcp. They may not know what MCP means.
They may use an AI agent already, or they may be curious. They arrive from a
search, a link in a chat, or the GitHub page. They have a few seconds to decide
if the page is for them.

A second, smaller group are developers who want the tech docs. The top menu
and the last section give them a path.

## Goals

- **Five-second test.** A person who sees only the first screen can say what
  email-mcp does: an AI agent works with your email, and you decide what goes
  out. A fresh agent stands in for that person, as the foundation allows.
- **One next step.** The main button goes to the manual. Every section ends
  with the reader knowing more, not with a new decision.
- **Every claim is true and carries its limit.** "By default" stays "by
  default". Mail only in this release, and the app password for Google and
  Microsoft, are said on the page.
- **The picture is the real approval page.** Its labels are the labels of
  `internal/web/templates/approval.html`, and a test keeps them equal.
- **Calm and plain.** The voice of `PRODUCT.md`: the person stays in control,
  and only real risk looks alarming.
- **Light.** One HTML file and the one shared stylesheet. No image on the page,
  no script, no request to another site.

## Non-goals

- Install steps, commands, flags and troubleshooting. They live in the manual
  only. The landing page is outside the docs check (see the foundation), so a
  copy of them here would become wrong without any warning. (My call.)
- A tour of the code. That is the tech docs.
- A comparison with other tools, prices, a roadmap with dates, a changelog or a
  blog.
- Customers, quotes, counts, stars, download numbers, logos or badges. None
  exist, and none are invented.
- Analytics, cookies, a newsletter form, a pop-up or a cookie banner.
- Animation, a video, or a demo that you can click.
- A theme switch. The page follows the setting of the computer (foundation).
- Translations and search (foundation).

## Anti-goals

These make the page a failure even if it builds:

- A reader cannot say what email-mcp is after the first screen.
- A sentence that is only sometimes true. Example: "Every send waits for you"
  with no "by default".
- A bare "secure", "private" or "safe". Claims name what the code does.
- Wording that suggests mail never leaves the computer. When the agent reads a
  message, the text goes to the AI app. The page says so.
- Wording about control that goes beyond what the code does. The page says what
  the tools of email-mcp can and cannot do, and nothing more.
- A weakness described, or any attack step. The page describes what the code
  does.
- The approval picture shows a label that the portal does not have.
- Red or amber on the page. The page shows no real risk, so it has none of
  these colours. Green is used only as the One Green Rule allows.
- The picture looks like a working form and invites clicks.
- Something from another site, a script, or a cookie.

## What the owner already decided

From the foundation, and not reopened here:

- The first screen is option A, "Show the decision": the headline "Your agent
  asks. You decide." and a picture of the approval page.
- Hugo builds the site with our own templates. GitHub Pages hosts it. It is
  published at each release.
- The page follows `DESIGN.md`: system fonts, flat, no gradient, no shadow, no
  uppercase labels. It loads nothing from other sites and works without script.
- There is no logo. The name is `email-mcp`.
- The headline may be larger than 1.625rem.
- The top menu is shared: `email-mcp`, Manual, Tech docs, GitHub. The footer
  shows the released version.

Options B ("Show the levels") and C ("Show a conversation") were not chosen for
the first screen. This spec uses their ideas lower on the page, because they
answer the next questions a reader has: how far can the agent go, and what does
a request look like. (My call.)

## Choices made in this spec

For a quick review. Each is marked "(My call.)" where it appears.

1. Seven sections. The order follows the questions of the reader.
2. The second hero action is a text link, "See how it works", not a second
   button.
3. The hero sits on the ground colour. The other sections sit on the pane
   colour.
4. The picture is a simplified drawing of one waiting action, not a copy of
   the three-pane portal.
5. The social card is text only, so it cannot become wrong when the portal
   changes.
6. A Go test keeps the labels of the picture equal to the labels of the portal.
7. The page states that the text of a message goes to the AI app when the agent
   reads it. The owner confirmed this sentence.
8. The page has no tab icon of its own. The shared layout reuses
   `internal/web/static/icon.svg`, as the foundation says.

One point stays open, with a default:

- **The "AI apps" row** (section 6). It names the five apps that have a page
  under `manual/getting-started/connect-ai-app/`. Default: keep it while the
  manual keeps those pages.

## Where the page lives

| Item | Path |
|---|---|
| Words and front matter | `site/content/_index.md` |
| Home template | `site/layouts/index.html` |
| Approval picture | `site/layouts/_partials/approval-picture.html` |
| Styles | the one shared stylesheet, in a "landing" part |
| Social card | `site/static/` or `site/assets/`, as the builder chooses |

Rules:

- The words in this spec are the words on the page. A reader of `_index.md`
  finds them there without reading templates. (My call: the structured parts,
  such as the levels, go in front matter. Prose goes in the body.)
- `_index.md` follows `.github/markdown.instructions.md` (front matter, no H1 in
  the body, lines of 80 characters).
- Next to each claim in the source, a short note names its row in the truth
  table below. In front matter it is a YAML comment, such as `# T13`. In the
  Markdown body it is `{{< truth "T13" >}}`, a shortcode that prints nothing. It
  helps the next person find the proof.
  (My call.)
- Every internal link uses Hugo `relURL` (a function that adds the base path of
  the site to a link). The site is published under a path
  (`/email-mcp-go/`), so a link written as `/manual/` would leave the site.
  This spec writes `/manual/` as the address inside the site.

## The page, section by section

There are seven sections, between the shared menu and the shared footer.

| # | `id` | Heading | Job |
|---|---|---|---|
| 1 | `top` | Your agent asks. You decide. | Say what it is |
| 2 | `how-it-works` | How it works | Say how it fits together |
| 3 | `levels` | You choose how far each account may go | Show the limit |
| 4 | `decide` | You see the message before you decide | Show the choice |
| 5 | `local` | email-mcp runs on your computer | Say where things are |
| 6 | `covers` | What it covers today | Say the limits of this release |
| 7 | `start` | Start with the manual | Give the next step |

The page is about 850 words, with the picture. A reader can scan the headings in
under a minute. (My call: this is the whole story that a new reader needs before
the manual.)

### 1. First screen

**Job.** Answer "what is this?" in a few seconds, and give the main action.

**Words.**

- Heading (the one `h1`, two lines, one sentence on each line):
  "Your agent asks." / "You decide."
- Lead paragraph: "email-mcp connects an AI agent, such as Claude Code, to your
  email. The agent can read, organize and write mail. By default, every send
  waits until you approve it."
- Primary button: "Install and set up". Links to `/manual/` (see "Calls to
  action").
- Text link next to it: "See how it works". Links to `#how-it-works`. (My call:
  a link, not a second button. Two buttons to nearly the same place make the
  choice less clear, and the menu already has Manual.)

**Beside it.** The approval picture, with this caption below it: "The approval
page of email-mcp, drawn with sample data." See "The approval picture".

**Facts strip.** Under the two columns, a hairline, then three facts in a
description list (`dl`). Each is a short term and one sentence:

- Term "On your computer". Text: "email-mcp runs on your computer. It stores
  your mail passwords there, encrypted."
- Term "Works with IMAP and SMTP". Text: "IMAP reads mail and SMTP sends it. You
  connect with a password or an app password."
- Term "Your rules". Text: "For each account, you choose how far the agent may
  go."

**Notes.**

- The heading is the promise of the product in short form. The lead right below
  it carries the qualifier "By default". Do not move the lead away from the
  heading, and do not shorten "By default" out of it.
- No eyebrow text above the heading and no uppercase label (DESIGN.md).
- The green on this screen is the primary button, the primary button of the
  picture, and the text link. The button of the picture is green because the
  button of the portal is green. It does nothing, and the caption says that the
  picture is a drawing. (My call: the facts strip has no colour.)

### 2. How it works

**Job.** Explain MCP in plain words and show the four steps from start to the
first decision.

**Words.**

- Heading: "How it works".
- Paragraph: "Your AI app can use tools that other programs offer. MCP (Model
  Context Protocol) is the open standard for this. email-mcp is an MCP server: a
  program that offers email tools to your AI app."
- A numbered list of four rows. Each row has a title and a sentence:
  1. "Connect your mail." "Add an account in your browser, with your address and
     a password. email-mcp tests the connection and saves the account only when
     the test passes."
  2. "Add email-mcp to your AI app." "You do this once. After that, your agent
     can use the email tools."
  3. "Ask your agent." "Use plain words. For example, you can type: “Find
     Anna's message about Thursday and reply that 10:00 works.” The agent
     uses the email tools to search, read, organize, write drafts and send."
  4. "Decide." "By default, email-mcp holds each send until you approve it. You
     see the message first." (The spec said "the whole message". An AI app that
     asks in its own window shows only the start of the text. The code is the
     source of truth, so the page says "the message". See T19 and T24.)

**Beside it.** Nothing. The empty space on the right is intended (DESIGN.md:
left aligned, nothing centred).

**Notes.** The numbers are plain text in the soft ink colour, not green and not
in a circle. This section uses the idea of option C (a request in plain words),
in step 3. (My call.)

### 3. You choose how far each account may go

**Job.** Show the limit of the agent. This is the idea of option B.

**Words.**

- Heading: "You choose how far each account may go".
- Paragraph: "Each mail account has its own level. A higher level includes the
  levels below it."
- A list of six rows. The names are the names that the portal shows:
  - "0 Off". "The agent cannot use the account."
  - "1 Read". "Search and read messages and attachments."
  - "2 Organize". "Mark as read, flag, tag, make folders and move messages.
    These moves, and new folders, never go into a folder that email-mcp
    recognizes as the Trash, or into a folder inside that folder."
  - "3 Draft". "Write drafts, and move a draft to the Trash. Moving a draft does
    not ask you."
  - "4 Send". "Send, reply and forward. A new account starts here. By default,
    every send asks you first."
  - "5 Delete". "Move messages to the Trash. By default, each of these moves
    asks you first. email-mcp has no tool that deletes mail for good."

- Closing paragraph: "Only you can change a level, on a page in your browser.
  The agent has no tool for this. It can ask for more access. That only gives
  you a link to the settings page, and nothing changes until you choose. The
  manual page [Security] says when this holds." The link goes to
  `/manual/daily-use/security/`.

**Beside it.** Nothing.

**Notes.**

- Level 4 is not marked with green. "New account starts here" is said in words.
  Green marks the current place and the primary action, and this is neither.
  (My call.)
- Level 5 is called "Delete" in the portal and "Move to Trash" in the table of
  `manual/daily-use/permission-levels.md`.
  The page uses the portal name and explains it in the same row.
- Three rows changed after review, to match the code, which is the source of
  truth. `move_messages` refuses, at every level, a folder that email-mcp
  recognizes as the Trash and every folder inside it, and `create_folder`
  refuses to make a folder there (`ErrMoveToTrash` and the backend name check,
  in `internal/service/write.go` and `internal/provider/imapsmtp/folders.go`).
  So level 2 says that its moves and new folders never go into such a folder,
  or into a folder inside one. `discard_draft` moves a draft to the Trash at
  level 3 and never asks (`DiscardDraft`), so level 3 names that move, and
  level 5 says "each of these moves" and not "every move" (T16).
- "Only you can change a level" holds for an agent that can only call the
  tools of email-mcp, and not for one that runs shell commands as you or
  controls your browser. The manual says so each time. The page adds no claim:
  it links to the Security page of the manual, which says when it holds (T18).

### 4. You see the message before you decide

**Job.** Show what the person gets to see and what the page promises about
that moment.

**Words.**

- Heading: "You see the message before you decide".
- Paragraph: "By default, email-mcp holds a message when your agent wants to
  send it. On the approval page, you see who will get it, including hidden
  copies (Bcc), the subject, the attachments and the whole text of the
  message. If the text of the message also has characters that cannot be
  seen, the page says so. For a draft that you wrote in another mail program, the page shows
  the text that a mail program would show, without its styles or images. The
  page lists each attachment by name and type. Then you press Reject or
  Approve and send."
- Four rows, one sentence each:
  - "Approve and send appears only when the page can show the whole text of
    the message."
  - "If the message changes after you look at it, it does not go out."
  - "After you decide, the page tells you what happened. It says Sent only when
    the mail server accepted the message."
  - "If you do nothing, the request expires and nothing is sent."
- Paragraph: "Some AI apps can ask you in their own window. There, email-mcp
  shows the start of the text, and says how much of it is not shown. For the
  others, your agent gives you a link to the approval page."
- Paragraph: "You can turn this question off for an account. email-mcp then asks
  you to tick a box that accepts the risk. Limits still apply when it is off:
  sends per hour and per day, and recipients per message."

**Beside it.** Nothing.

**Notes.**

- The last paragraph says that the question can be turned off. It keeps "by
  default" honest. It is plain text, not an amber notice. The page shows no
  active risk, and the Real Risk Rule keeps amber for that.
- The numbers of the limits are not on the page, because each account can change
  them and the page would become wrong.
- The heading said "the whole message" before review. That is true on the
  approval page only. An AI app that asks in its own window shows the first
  part of the text and a count of what it leaves out, so the heading says "the
  message", the first paragraph says "On the approval page", and the paragraph
  on AI apps says what such a window shows (T19, T24).
- The approval page shows the text of the message, not every character that
  goes out. So the page says "the whole text of the message" and not "the full
  text", and it says that the page tells you when the text of the message
  also has characters that cannot be seen (T19, T20). This is the meaning of the manual
  (`manual/daily-use/confirmations.md`: "The approval page shows the whole
  text").
- The page said "the whole text as the recipient will see it" in the first
  draft. The approval page shows text: for a message with HTML, `Body` is the
  text of the HTML, so a draft written in another mail program shows no styles
  or images. Attachments are shown by name and media type, not by content
  (`manual/daily-use/security.md`). So the paragraph now says "the whole text
  of the message" and names these two limits (T19).

### 5. email-mcp runs on your computer

**Job.** Say where the pieces are, without promising more than the code does.

**Words.**

- Heading: "email-mcp runs on your computer".
- A description list of five rows (term, then text):
  - "Passwords". "Stored on your computer, encrypted with AES-256-GCM (a
    standard encryption method). By default, the key that protects them is in
    the keychain (the password store) of your operating system."
  - "Your mail". "email-mcp does not keep a copy of your mailbox. Your messages
    stay on your mail server."
  - "Web pages". "The approval page and the settings pages run only on your
    computer, at `email-mcp.localhost`, a name that Chrome, Edge and Firefox
    send to your own computer." (It said "That name always means your own
    computer" in the first draft. Another browser may need a line in the hosts
    file, as `manual/getting-started/connect-account.md` says. T27.)
  - "Activity". "Every tool call of your agent is written to an Activity page
    that you can read. The log never stores message bodies or attachment data."
  - "What your agent reads". "When your agent reads a message, the text goes to
    your AI app. What that app does with it is up to the app."

**Beside it.** Nothing.

**Notes.**

- The last row is a limit. Without it, "runs on your computer" could suggest
  that the content of mail never leaves the computer. (My call: it is not in
  the README today. I add it because hiding it would mislead.)
- `email-mcp.localhost` uses the inline code style of DESIGN.md.

### 6. What it covers today

**Job.** State the limits of this release, so nobody installs it and finds a
surprise.

**Words.**

- Heading: "What it covers today".
- A description list of five rows:
  - "Mail only". "This release covers mail. Calendar and contacts are planned."
  - "Google and Microsoft". "Sign-in on the Google or Microsoft website (OAuth)
    is planned. Until then, connect these accounts with an app password: a
    separate password that you make in the security settings of your mail
    account. Your provider must offer app passwords."
  - "AI apps". "The manual shows the setup for Claude Code, Claude Desktop,
    Cursor, VS Code and the Codex CLI."
  - "Systems". "Release builds exist for macOS, Linux and Windows."
  - "Open source". "email-mcp is open source under the MIT license: you may use,
    copy and change the code. You can read it on GitHub."

- "GitHub" in the last row links to the repository (see "Calls to action").

**Beside it.** Nothing.

**Notes.**

- "Planned" is softer than the words that the README first used ("come next",
  "come later"). Do not write a date or a version.
- The "AI apps" row depends on the manual keeping the setup for these five
  apps (the pages under `manual/getting-started/connect-ai-app/`). If the
  manual drops one, the builder removes its name from this row.

### 7. Start with the manual

**Job.** Give the next step, and the way for a developer.

**Words.**

- Heading: "Start with the manual".
- Paragraph: "The manual shows you how to install email-mcp, connect an account
  and add it to your AI app."
- Primary button: "Install and set up". Links to `/manual/`.
- Line under the button: "For developers: [Read the tech docs] or [see the code
  on GitHub]." The first link goes to `/tech/`. The second goes to the
  repository.

**Beside it.** Nothing.

**Notes.** The button has the same label and the same address as the one in
section 1, so the page never offers two names for one action.

## The approval picture

**What it is.** An HTML and CSS drawing of the approval page with sample data.
It is not a screenshot, so it follows the light and dark setting, scales with
the text size, and has no image to load.

**What it must show.** The labels below are the labels of the portal. The test
in "Keeping the page true" checks them.

| Part | Text on the picture | Source in `approval.html` |
|---|---|---|
| Title | Waiting for you | `<h1 id="queue-title">` |
| Note | Nothing goes out until you approve. | `actionbar-note`, for a send |
| Neutral button | Reject | `<button ...>Reject</button>` |
| Primary button | Approve and send | `Approve and send`, for a send |
| Label | From | `<dt>From</dt>` |
| Label | To, every recipient (Bcc included) | `<dt>To, every ...</dt>` |
| Label | Attachments | `<dt>Attachments</dt>` |

Sample data, all on `example.com`:

- Subject: "Re: Thursday's meeting"
- From: `you@example.com`
- To: `anna@example.com`
- Attachments: "None" (the portal shows "None" when there is no attachment)
- Text: "Thursday at 10:00 works for me. See you then."

**Markup.** The builder may change class names. The structure and the text stay.

```html
<figure class="picture">
  <div class="picture-art" role="img"
       aria-label="A drawing of the approval page of email-mcp, with sample
         data. A reply to anna@example.com, with the subject Re: Thursday's
         meeting, is waiting for you. A note says that nothing goes out until
         you approve. There are two buttons: Reject and Approve and send.">
    <p class="pic-title">Waiting for you</p>
    <div class="pic-bar">
      <p class="pic-note">Nothing goes out until you approve.</p>
      <div class="pic-buttons">
        <span class="pic-button">Reject</span>
        <span class="pic-button pic-primary">Approve and send</span>
      </div>
    </div>
    <p class="pic-subject">Re: Thursday's meeting</p>
    <dl class="pic-meta">
      <div><dt>From</dt><dd>you@example.com</dd></div>
      <div><dt>To, every recipient (Bcc included)</dt>
          <dd>anna@example.com</dd></div>
      <div><dt>Attachments</dt><dd>None</dd></div>
    </dl>
    <p class="pic-body">Thursday at 10:00 works for me. See you then.</p>
  </div>
  <figcaption>The approval page of email-mcp, drawn with sample
    data.</figcaption>
</figure>
```

**Look.**

- Colours come from the tokens only. The frame is a 1px hairline (`--line`) with
  an 8px radius, on the pane colour. The hero sits on the ground colour, so the
  picture stands apart by tone and hairline (Flat Pane Rule). No shadow.
  (My call: the frame has a radius, because the picture shows a piece of the
  portal and is not a pane that meets the edge of a column.)
- "Waiting for you" uses the Title style (1.125rem, 600). The subject uses the
  Row Title style, a little smaller than the portal, so the drawing fits.
- The note is soft text, 0.9375rem. A hairline sits under the bar.
- Reject is the neutral button: pane colour, ink text, Control Edge border.
  It is never red (DESIGN.md: "Don't use red for Reject").
- Approve and send is the primary button: green fill, Go Ink text, weight 600.
- The labels use the soft colour. Each label sits above its value, always
  stacked, so the long label "To, every recipient (Bcc included)" can wrap.
- The text of the message uses body size and a line length of at most 34rem.
- Buttons in the drawing are `span` elements, not `button` or `a`. They have the
  default cursor, no hover and no focus. They do nothing, and no one can reach
  them by keyboard.
- No count pill, no menu and no list of other waiting actions. The real page has
  these. The drawing keeps one action so that the reader sees one decision.
  (My call.) The caption says "drawn".

**Accessibility of the drawing.**

- `role="img"` with `aria-label` makes a screen reader read one description. The
  elements inside are hidden from it, so the screen reader does not read two
  disabled-looking buttons.
- The visible caption stays outside the `role="img"` element.
- The drawing has no heading element, so it adds nothing to the heading order.

**Staying equal to the portal.** The labels are checked by a test (see "Keeping
the page true"). The colours follow the shared tokens, which follow
`DESIGN.md`. The drawing is simplified by design, so a new row or a new
detail in the portal does not make it wrong. A change to what the buttons do,
or a new button, does: re-read the picture, section 4 and T20 to T22 then.

**Phone.** The bar wraps. The note takes a full line. The two buttons share the
next line in equal widths (as the portal does on a phone). The drawing needs no
scrolling at 320px wide. Addresses break with `overflow-wrap: anywhere`.

**Dark.** Same markup. Tokens change. In the dark theme the primary button is
the lighter green with dark text (contrast 7.63; see "Accessibility").

**What it does not claim.** Some apps, such as Claude Code over stdio, ask in
their own window and not on this page (`manual/daily-use/confirmations.md`;
`manual/daily-use/app-confirmation.md`). Section 4 says this in words (T24).
The caption says "the approval page", which is true: this page exists, and the
agent links to it when the app cannot ask.

## Calls to action

| Where | Label | Goes to |
|---|---|---|
| Menu | email-mcp | `/` |
| Menu | Manual | `/manual/` |
| Menu | Tech docs | `/tech/` |
| Menu | GitHub | `https://github.com/EmadMokhtar/email-mcp-go` |
| Section 1 | Install and set up (button) | `/manual/` |
| Section 1 | See how it works (link) | `#how-it-works` |
| Section 6 | GitHub (link) | `https://github.com/EmadMokhtar/email-mcp-go` |
| Section 7 | Install and set up (button) | `/manual/` |
| Section 7 | Read the tech docs (link) | `/tech/` |
| Section 7 | see the code on GitHub (link) | the repository |
| Footer | shared | shared |

- "Install and set up" goes to the install page of the manual. The exact page
  comes from the user manual spec, which another author writes now. This spec
  fixes only `/manual/`. The builder replaces it with the install page when that
  page exists, and uses the same address in both buttons.
- Other links in section 6 may point to deeper manual pages (app passwords, AI
  apps) if the manual spec has them. Each target must exist at build time. The
  broken-link check of the build job enforces this.
- All links to GitHub open in the same tab. No `target="_blank"`. (My call: it
  avoids a new window that the reader did not ask for.)
- The order of work puts the landing page after the manual and the tech docs
  (foundation, step 4), so these links are real when this page is built.

## Page data

All values are self-hosted. Nothing is fetched from another site.

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#f4f6f3"
      media="(prefers-color-scheme: light)">
<meta name="theme-color" content="#131414"
      media="(prefers-color-scheme: dark)">
<title>email-mcp: let an AI agent use your email</title>
<meta name="description" content="email-mcp connects an AI agent, such as Claude Code, to your mail accounts. It runs on your computer. By default, every send waits until you approve it.">
<link rel="canonical" href="{{ .Permalink }}">
<meta property="og:type" content="website">
<meta property="og:site_name" content="email-mcp">
<meta property="og:title" content="email-mcp: let an AI agent use your email">
<meta property="og:description" content="email-mcp connects an AI agent, such as Claude Code, to your mail accounts. It runs on your computer. By default, every send waits until you approve it.">
<meta property="og:url" content="{{ .Permalink }}">
<meta property="og:image" content="{{ "social-card.png" | absURL }}">
<meta property="og:image:type" content="image/png">
<meta property="og:image:width" content="1200">
<meta property="og:image:height" content="630">
<meta property="og:image:alt" content="email-mcp. Your agent asks. You decide.">
<meta name="twitter:card" content="summary_large_image">
</head>
```

Notes on this block:

- The two long `description` lines stay on one line each, because they are copy.
- The title has 41 characters and the description has 152. Both fit the usual
  limits of search results and preview cards. (My call.)
- The title says what the tool is. The description adds where it runs and the
  decision. The headline "Your agent asks. You decide." is on the page and on
  the card. (My call.)
- `og:url`, `og:image` and `canonical` must be absolute. Build them from the
  base address that the build takes from `actions/configure-pages` (foundation).
  Do not write an address in the file.
- `twitter:card` is the only `twitter:` tag. Other sites read the `og:` tags
  when a `twitter:` tag is missing. There is no `twitter:site`, because the
  project has no account to name.
- No JSON-LD (structured data). It would add claims to keep true. (My call.)
- The title, `lang`, viewport and `color-scheme` meta come from the shared
  layout. They are listed here so the page can be checked as a whole.
- Tab icon: the shared layout reuses `internal/web/static/icon.svg`, as the
  foundation says. The landing page never shows it as a logo.

**The social card.**

- File: PNG, 1200 by 630 pixels, light colours only, below 200 KB.
- Content, text only, left aligned, on the ground colour `#f4f6f3`:
  - "email-mcp" in soft ink `#55615b`, weight 650.
  - "Your agent asks. You decide." in ink `#16201c`, large, weight 650, on two
    lines.
  - "Let an AI agent use your email. By default, every send waits until you
    approve it." in soft ink.
- No logo, no picture, no gradient, no green. (My call: no portal picture on the
  card, so a change in the portal cannot make it wrong.)
- It is built before commit (foundation: no browser script draws it). Keep its
  source file in the repository so the card can be rebuilt. How to produce the
  PNG is left to the builder. Any system sans-serif font is fine, because the
  text is baked into the image.
- The card is not loaded by the page. Only the `og:image` tag points to it.

## Layout and breakpoints

The shared layout gives the container, the menu and the footer. If it fixes no
width, use a container of at most 66rem, centred as a block, with text aligned
left. (My call.)

Backgrounds: the shared menu, the hero and the footer sit on the ground colour.
Sections 2 to 7 sit on the pane colour. (My call. This mirrors the portal, where
the menu is on the ground and the page is a pane. It also keeps neutral buttons
on a colour where their edge passes 3:1; see "Accessibility".)

Side gutters (the space at the left and right of the page): 44px (the `gutter`
step), 24px below 900px, 16px below 680px.
Vertical space between sections: 48px, and 32px below 680px. Each section from
2 to 7 has a hairline above it. (My call. A landing page needs a little more
space than the portal, and the values still come from the spacing steps.)

Type, in addition to the shared styles (all in `rem`, so zoom works):

| Element | Size | Weight | Notes |
|---|---|---|---|
| `h1` | see below | 650 | Line height 1.15, letter spacing -0.02em. |
| Lead | 1.125rem | 400 | Line height 1.55, at most 34rem wide. |
| `h2` | 1.375rem | 650 | The Empty Title style. |
| Row title | 1rem | 600 | Row Title style. |
| Body | 1rem | 400 | Line height 1.55, at most 34rem wide. |
| Small | 0.9375rem | 400 | Soft ink. |
| Caption | 0.875rem | 400 | Soft ink. |

(My call on the `h1` scale and the lead size.) The `h1` size is
`clamp(1.875rem, 1.2rem + 3vw, 2.75rem)`. Each sentence of the `h1` sits on its
own line with a block-level `span`. With a minimum of 1.875rem, "Your agent
asks." fits in 288px (a 320px phone with 16px gutters).

Row pattern, used in sections 2 to 6: a row has a hairline above it and 14px
padding. On a wide screen it has two columns: the title on the left (12rem), the
text on the right (at most 34rem). A list that you read in order uses `ol`. A
list of terms uses `dl`. When bullets are removed with CSS, add `role="list"`
to the `ol` and `ul`, so VoiceOver (the screen reader of macOS and iOS) still
reads a list.

**1100px and wider.**

- Hero: two columns, text `1fr` and picture `1.1fr`, 44px between, aligned at
  the top. (My call: top, not centre, so the heading and the picture start on
  the same line.)
- Facts strip: three columns with a hairline between them.

**900 to 1099px.**

- Same as above. The picture keeps at least 22rem.

**680 to 899px.**

- The shared menu is a row (DESIGN.md: the menu moves to a row at 900px).
- Hero: one column. Heading, lead and actions first, then the picture at most
  34rem wide, left aligned, then the facts. The facts become one column, with a
  hairline between rows. (My call: three columns of 200px are too narrow for
  the text.)
- Rows keep two columns.

**Below 680px.**

- Gutters 16px. Vertical space 32px.
- The primary button is full width and 44px high. The text link sits under it,
  with a 40px high touch area.
- Rows have one column: title above text.
- In the level list, the level number and name share the first line.
- The picture bar wraps (see "The approval picture").
- The menu must keep all four links reachable at 320px. They may wrap to a
  second line. No menu that needs script.

**Always.**

- No horizontal scroll from 320px up, and at 200 percent zoom.
- Long words and addresses wrap (`overflow-wrap: anywhere`).
- Nothing is centred. All text is left aligned.
- The page has no table, so nothing needs a sideways scroll.

## Dark mode

- Light and dark follow `prefers-color-scheme`. There is no switch.
- Use the tokens of `DESIGN.md` and their `-dark` values. Never pair a day value
  with a night value. No hex colour in the landing styles.
- The picture and all sections use tokens, so they change together.
- At night the ground loses its green tint, and green becomes lighter. The
  text on the primary button becomes dark (Go Ink at night).
- The social card is light only. Preview cards show on both light and dark
  screens, and a light card is readable on both.
- The `theme-color` meta tags in "Page data" tell the browser bar to match.
- Check both themes at 1440px and 375px wide before merge.

## Accessibility

**Contrast.** Computed with the WCAG formula from the values in `DESIGN.md`.
Text needs 4.5:1. The edge of a control needs 3:1.

| Pair | Light | Dark |
|---|---|---|
| Ink on ground | 15.37 | 15.31 |
| Ink on pane | 16.70 | 14.32 |
| Soft text on ground | 5.95 | 8.13 |
| Soft text on pane | 6.47 | 7.61 |
| Green text on ground | 5.98 | 9.75 |
| Green text on pane | 6.50 | 9.12 |
| Go Ink on green (primary button) | 6.50 | 7.63 |
| Go Ink on hover green | 8.23 | 9.24 |
| Control Edge on pane | 3.10 | 3.59 |
| Control Edge on ground | 2.85 | 3.84 |

- The Control Edge on the ground colour is 2.85 in the light theme, below 3.
  So no bordered neutral button sits on the ground colour. The hero has one
  green button and one text link, and the neutral Reject button of the picture
  sits on the pane colour (3.10). Keep it so. (My call.)
- Links in text use the green text colour and are always underlined, so colour
  is not the only sign of a link.
- The focus ring has 5.98 or better against the ground and the pane in both
  themes.

**Headings.** One `h1` (the headline). Then `h2` for sections 2 to 7. There is
no `h3`, so no level is skipped. The facts strip is a `dl`, and the picture has
no heading. Each heading makes sense when read alone, as in the heading list of
a screen reader.

**Landmarks.** `header` with `nav` (named "Main menu" like the portal), `main`,
and `footer`. Each `section` has `aria-labelledby` with the id of its heading.

**Skip link.** The first link on the page is "Skip to the main content". It is
hidden until it has focus. Then it shows at the top left, with the pane colour,
ink text and the focus ring. It goes to `main`. The shared layout supplies it.
If it does not, the builder adds it.

**Focus.** Every link and button shows the DESIGN.md ring on `:focus-visible`:
2px green outline, 2px offset. Never remove it. Nothing clips the ring. The tab
order equals the reading order: skip link, menu, the button, the text link, the
links in the sections, the footer. The picture is not in the tab order.

**Keyboard.** Everything is a link. Nothing needs a mouse. No information shows
only on hover.

**Reduced motion.** The page has no animation at all. The only movement is the
150ms colour change on hover and the 100ms press scale (0.98) of a button, both
from DESIGN.md. Under `prefers-reduced-motion: reduce`, remove both: set the
transition to none and do not scale. Do not set smooth scrolling. The jump to
`#how-it-works` is instant.

**Text and zoom.** `lang="en"`. Sizes in `rem`. Line height 1.55. Reading width
at most 34rem. Text is never justified and never inside an image. The layout
holds at 200 percent zoom and at 320px wide (WCAG reflow).

**Touch.** Buttons are at least 40px high (DESIGN.md), 44px on a phone. The text
link has a 40px high area.

**No script.** The page needs none and loads none. It sets no cookie.

**Checks before merge.** A keyboard-only pass. A check of the accessibility tree
(the owner may add a pass with VoiceOver at review). Both colour themes. Reduced motion on. The browser zoom at 200 percent. An
automatic checker (Lighthouse or axe), run from tools that the builder already
has. Nothing is added to the repository for this. A checker helps, but it does
not replace the passes above.

## Truth table

Each claim of the page, with the proof. Paths are in the repository. A page of
the docs site is named by its path under `site/content/`, such as
`manual/daily-use/confirmations.md`, and a section of that page is named in
quotes. If the code and a page disagree, the code is the source of truth, and
the difference is reported (foundation). The rows were first written against
sections of `README.md`. Those sections moved to the manual, so each row now
names the manual page.

"Qualifier" is a word that must stay on the page.

**T1. Connects an AI agent, such as Claude Code, to your email.**
- Manual: `manual/_index.md` (the opening list);
  `manual/getting-started/connect-ai-app/`.
- Code: `cmd/email-mcp/serve.go` (MCP server over stdio);
  `internal/mcpserver/server.go`.

**T2. The agent can read, organize and write mail.**
- Manual: `manual/daily-use/what-the-agent-can-do.md`;
  `manual/daily-use/permission-levels.md`.
- Code: `internal/mcpserver/read_tools.go`, `write_tools.go` (the tools);
  `internal/policy/policy.go` (levels).
- Qualifier: "can". The level decides (T13).

**T3. By default, every send waits until you approve it.**
- Manual: `manual/daily-use/permission-levels.md` ("A new account");
  `manual/daily-use/confirmations.md` ("The two switches").
- Code: `internal/domain/account/account.go` (`NewIMAPAccount`:
  `ConfirmSends: true`); `internal/policy/policy.go` (`Decide`);
  `internal/service/send.go` (`Send`, reason `confirm_sends`).
- Qualifier: "By default". Confirm sends can be turned off (T25). Do not write
  "always" or "never".

**T4. Runs on your computer.**
- Manual: `manual/_index.md` (the opening list; "How the parts fit");
  `manual/getting-started/connect-account.md`.
- Code: `internal/web/server.go` (`listenAddr = "127.0.0.1:0"`);
  `cmd/email-mcp/serve.go`.

**T5. Stores your mail passwords on your computer, encrypted (AES-256-GCM). By
default the key is in the operating system keychain.**
- Manual: `manual/reference/master-key.md`;
  `manual/reference/data-folder.md`.
- Code: `internal/secrets/aead.go` (AES-256-GCM, 32-byte key);
  `internal/secrets/kek.go` (keychain entry `email-mcp` / `master-key`);
  `internal/secrets/vault.go`.
- Qualifier: "By default". The master key can be a file (`EMAIL_MCP_KEK`).

**T6. Works with IMAP and SMTP mail, with a password or an app password.**
- Manual: `manual/getting-started/connect-account.md`;
  `manual/reference/known-limitations.md` (what the mail server needs).
- Code: `internal/provider/imapsmtp/`.
- The page does not say "any", and the README does not either. Some tools need
  more from the mail server: a move needs MOVE or UIDPLUS (`applyMove` in
  `internal/provider/imapsmtp/move.go`), tags need support from the server
  (`Tag` in `organize.go`), and an address outside ASCII needs SMTPUTF8
  (`ErrSMTPUTF8Unsupported` in `smtp.go`). The interop test runs against
  Dovecot and Postfix only (`tech/work-on-the-code/testing.md`). See "Not
  proven" below.

**T7. For each account, you choose how far the agent may go.**
- Manual: `manual/daily-use/permission-levels.md`.
- Code: `internal/domain/account/account.go` (`Levels` per account);
  `internal/web/settings.go`.

**T8. The picture uses the real labels.**
- Code: `internal/web/templates/approval.html` (`Waiting for you`, `Reject`,
  `Approve and send`, `From`, `To, every recipient (Bcc included)`,
  `Attachments`, and the note `Nothing goes out until you approve.` for a send).
- Kept equal by the test in "Keeping the page true".

**T9. MCP is an open standard. email-mcp is an MCP server.**
- Manual: `manual/_index.md` (the opening paragraph);
  `manual/reference/glossary.md`.
- Code: `internal/mcpserver/server.go`.

**T10. email-mcp tests the connection and saves the account only when the test
passes.**
- Manual: `manual/getting-started/connect-account.md` ("Add the account").
- Code: `internal/service/accounts.go` (`probe`, before the save);
  `internal/web/templates/connect.html` ("Test the connection and save").

**T11. The agent uses the email tools to search, read, organize, write drafts
and send.**
- Manual: `manual/daily-use/what-the-agent-can-do.md` ("The tools").
- Code: `internal/mcpserver/read_tools.go`, `write_tools.go`.

**T12. You add email-mcp to your AI app once. After that, your agent can use
the email tools.**
- Manual: `manual/getting-started/connect-ai-app/claude-code.md` (`--scope
  user` makes it available in every project), and the other pages under
  `manual/getting-started/connect-ai-app/`.
- Code: `cmd/email-mcp/serve.go`.

**T13. The six levels and what each allows.**
- Manual: `manual/daily-use/permission-levels.md` ("The levels").
- Code: `internal/domain/account/account.go` (`MailOff` to `MailDelete`, and
  `MailName`: Off, Read, Organize, Draft, Send, Delete);
  `internal/web/settings.go` (`levelAllows`);
  `internal/policy/policy.go` (`RequiredMailLevel`).
- The portal names level 5 "Delete". It moves mail to the Trash (T17).
- Level 2, no move to a folder that email-mcp recognizes as the Trash, and no
  new folder there: `internal/service/write.go` (`ErrMoveToTrash`,
  `refuseTrashTarget` and `inTrash`: `move_messages` refuses, at every level,
  a folder that email-mcp recognizes as the Trash and every folder inside it,
  and `CreateFolder` refuses to make a folder there);
  `internal/provider/imapsmtp/folders.go` (`CreateFolder` refuses a name
  that holds the hierarchy delimiter).
- Level 3 includes moving a draft to the Trash:
  `internal/mcpserver/write_tools.go` (`discard_draft`, "Needs Mail 3");
  `internal/service/write.go` (`DiscardDraft` uses the draft action).

**T14. A higher level includes the levels below it.**
- Code: `internal/domain/account/account.go` (comment on `Level`);
  `internal/policy/policy.go` (`Decide` compares `current < required`).

**T15. A new account starts at Send.**
- Manual: `manual/daily-use/permission-levels.md` ("A new account").
- Code: `internal/domain/account/account.go` (`NewIMAPAccount`:
  `AreaMail: MailSend`).

**T16. By default, each move to the Trash at level 5 asks you first. Moving a
draft to the Trash at level 3 does not ask.**
- Manual: `manual/daily-use/confirmations.md` ("The two switches": Confirm
  deletes); `manual/daily-use/permission-levels.md`. The code has one
  exception, the move of a draft at level 3. The manual and the README state
  it.
- Code: `NewIMAPAccount` (`ConfirmDeletes: true`);
  `internal/policy/policy.go` (`Decide` asks only for `ActTrash`);
  `internal/service/write.go` (`Trash`; `DiscardDraft` uses `ActDraft`, which
  never asks).
- Qualifier: "By default", and "each of these moves" for level 5. Do not write
  "every move to the Trash".

**T17. email-mcp has no tool that deletes mail for good.**
- Manual: `manual/daily-use/what-the-agent-can-do.md` (`trash_messages`);
  `manual/daily-use/full-autonomy-and-limits.md` ("Limits that stay on").
- Code: `internal/mcpserver/write_tools.go` (tool list and the description of
  `trash_messages`); `internal/provider/imapsmtp/move.go` (`Trash`: "It
  deletes nothing for good").
- Qualifier: "email-mcp has no tool that". Keep this scope.

**T18. Only you can change a level. The agent has no tool for this. It can ask
for access, which only gives a link, and nothing changes until you choose.**
- Manual: `manual/daily-use/permission-levels.md` ("Only you change a
  level").
- Code: `internal/mcpserver/useronly_test.go` (the tools may not call
  `UpdateSettings`, `ApprovePending` and the other user-only calls);
  `internal/service/links.go` (`PermissionLink`: "It changes nothing");
  `internal/web/templates/settings.html` ("Nothing has changed yet.").
- Qualifier: "The agent has no tool for this." Keep exactly this scope. Do not
  rewrite it as "the agent cannot".
- Limit: it holds for an agent that can only call the tools of email-mcp
  (`manual/daily-use/security.md`, `manual/daily-use/permission-levels.md`).
  The page links to the Security page of the manual right after the
  paragraph, and adds no claim.

**T19. On the approval page, you see who gets it (Bcc included), the subject,
the attachments and the whole text of the message. If the text of the message
also has characters that cannot be seen, the page says so. For a draft that you
wrote in another mail program, the page shows the text that a mail program
would show, without its styles or images. The page lists each attachment by
name and type.**
- Manual: `manual/daily-use/confirmations.md` ("What you see": the approval
  page shows the whole text; the preview of the AI app shows the first 500
  characters); `manual/daily-use/security.md` (a confirmation shows the media
  type of each attachment and not its content; a draft that you write in
  another mail program).
- Code: `internal/web/templates/approval.html` (the labels `From`, `To, every
  recipient (Bcc included)` and `Attachments`, and `message-body`);
  `internal/service/approvals.go` (`Body`: "the whole text that the recipient
  sees: the text of the HTML when the message has HTML"; `Attachments`: the
  file name of each attachment, with its media type; `MoreChars`: the count of
  the characters that cannot be seen);
  `internal/service/send.go` (`sendContent`: `body` and `hidden`;
  `draftUnseen`; for a draft, `Body` is `ShownText`, the text of the parts that
  a mail program shows, as `LayoutOf` in `internal/mime/parts.go` decides);
  `internal/web/approvals.go` (the page shows `Body` and adds the `MoreChars`
  line).
- The comment on `Body` says "the whole text that the recipient sees". The page
  says less than that comment: "the whole text of the message".
- Scope: the approval page only. An AI app that asks in its own window shows
  the start of the text (T24), so the heading of section 4 and step 4 of
  section 2 say "the message", not "the whole message".
- Limit: `Body` is text. The page does not show the styles or the images of a
  message with HTML, so the page says "the whole text of the message" and not
  "as the recipient will see it".

**T20. Approve and send appears only when the page can show the whole text of
the message.**
- Code: `internal/web/templates/approval.html` (`{{if .CanApprove}}`);
  `internal/web/approvals.go` (`CanApprove` is set only with the detail of
  the message, which holds `Body` and `MoreChars`; otherwise
  `msgDetailUnavailable` and only Reject);
  `internal/service/approvals.go` (`Body`, `MoreChars`).

**T21. If the message changes after you look at it, it does not go out.**
- Manual: `manual/daily-use/security.md` ("What you approve is what goes
  out"): a change means that you are asked again.
- Code: `internal/confirm/confirm.go` (`Claim` checks the payload hash);
  `internal/service/send.go` (`answerSend`);
  `internal/web/approvals.go` (`msgMessageChanged`: "nothing was sent").
- The page says "does not go out". It does not say "you are asked again",
  because the web page says "ask the agent to send it again".

**T22. The page tells you what happened. It says Sent only when the mail server
accepted the message.**
- Code: `internal/web/approvals.go` (`outcomeSent`: "The mail server accepted
  the message."; `outcomeFailed`: "Not sent"; `sendOutcome`).

**T23. If you do nothing, the request expires and nothing is sent.**
- Manual: `manual/daily-use/confirmations.md` ("Two ways to answer": the link
  expires after 24 hours).
- Code: `internal/confirm/confirm.go` (`StateExpired`, `ApprovalLifetime`);
  `internal/web/approvals.go` (`outcomeExpired`).
- The page gives no number of hours.

**T24. Some AI apps can ask you in their own window. There, email-mcp shows the
start of the text, and says how much of it is not shown. For the others, your
agent gives you a link to the approval page.**
- Manual: `manual/daily-use/confirmations.md` ("Two ways to answer");
  `manual/daily-use/app-confirmation.md` ("What each app can do").
- Code: `internal/mcpserver/elicit.go` (the form, and the fallback
  `pending_approval` with a link; `previewBodyChars` and `sendPreview`: the
  first 500 characters of the text, and a last line that says how many
  characters are not shown).
- The page gives no number of characters.
- Qualifier: "Some" and "for the others". Which app does which is a fact about
  the app, and the page does not name apps here.

**T25. You can turn the question off. email-mcp asks you to tick a box that
accepts the risk. Limits still apply.**
- Manual: `manual/daily-use/full-autonomy-and-limits.md` ("Turn off Confirm
  sends"; "Limits that stay on").
- Code: `internal/service/accounts.go` (`NeedsRiskAcceptance`);
  `internal/web/templates/settings.html`;
  `internal/domain/account/account.go` (`DefaultLimits`);
  `internal/policy/limiter.go`.
- The page gives no numbers for the limits.

**T26. email-mcp does not keep a copy of your mailbox. Your messages stay on
your mail server.**
- Manual: `manual/reference/data-folder.md` ("What is stored": "email-mcp
  does not store your mail").
- Code: `internal/store/migrations/` (the tables are accounts, keys, tokens, web
  sessions, the outbox, pending actions, the activity log and counters; there is no
  table for messages).
- The outbox and a waiting approval hold an outgoing message until it is sent
  or ends. They are not a copy of the mailbox.

**T27. The approval page and the settings pages run only on your computer, at
`email-mcp.localhost`, a name that Chrome, Edge and Firefox send to your own
computer.**
- Manual: `manual/getting-started/connect-account.md` ("If your browser does
  not find `email-mcp.localhost`").
- Code: `internal/web/server.go` (`uiHost = "email-mcp.localhost"` and
  `listenAddr`).
- Qualifier: "Chrome, Edge and Firefox". Do not write "always" or "every
  browser".

**T28. Every tool call is written to an Activity page that you can read. The log
never stores message bodies or attachment data.**
- Manual: `manual/daily-use/activity-and-undo.md` ("The Activity page");
  `manual/reference/data-folder.md`.
- Code: `internal/mcpserver/capture.go` (a call entry for every `tools/call`);
  `internal/audit/audit.go` (no body, no attachment data);
  `internal/web/activity.go`.
- Manual, `manual/daily-use/activity-and-undo.md`: a request with a wrong
  access token has no row, because nothing says whose log it belongs to. The
  page says "your agent", so the claim holds.

**T29. When your agent reads a message, the text goes to your AI app.**
- Code: `internal/mcpserver/read_tools.go` (`get_message` returns the body to
  the caller).
- This claim is a limit. It is true by how MCP works, and it stops "runs on
  your computer" from being read as more than it is. The owner confirmed this
  sentence.

**T30. This release covers mail. Calendar and contacts are planned.**
- Manual: `manual/_index.md` ("What is not in this version").
- Code: the tool lists in `internal/mcpserver/read_tools.go` and
  `write_tools.go` hold mail tools only.

**T31. Google and Microsoft: sign-in on their website (OAuth) is planned. Until
then, use an app password. Your provider must offer app passwords.**
- Manual: `manual/_index.md` ("What is not in this version");
  `manual/getting-started/connect-account.md` ("App passwords").
- Code: `cmd/email-mcp/importenv.go` (an `.env` with `USE_OAUTH=true` is refused
  with "Connect this account with an app password instead.").
- That a provider offers app passwords is the provider's choice. The manual
  states it. Code cannot prove it. See "Not proven" below.

**T32. An app password is a separate password that you make in the security
settings of your mail account.**
- Manual: `manual/getting-started/connect-account.md` ("App passwords");
  `manual/reference/glossary.md`.

**T33. The manual shows the setup for Claude Code, Claude Desktop, Cursor, VS
Code and the Codex CLI.**
- Manual: `manual/getting-started/connect-ai-app/` (one page for each of the
  five apps).
- Depends on the manual spec. Check at build time. If a name is gone, remove it.

**T34. Release builds exist for macOS, Linux and Windows.**
- Manual: `manual/getting-started/install.md` ("A release archive").
- Code: `.goreleaser.yaml` (`targets`: linux amd64 and arm64, darwin arm64 and
  amd64, windows amd64).
- The page says "builds exist". It does not say "tested on", because the
  continuous integration (CI, the automatic checks on each change) workflow runs
  on Linux only.

**T35. Open source under the MIT license: use, copy and change the code.**
- README: "License"; manual: `manual/_index.md`. Code: `LICENSE`.

**T36. The manual shows how to install email-mcp, connect an account and add it
to your AI app.**
- Manual: `manual/getting-started/install.md`,
  `manual/getting-started/connect-account.md` and
  `manual/getting-started/connect-ai-app/`.
- Depends on the manual spec.

**T37. Terms.** MCP (T9), IMAP and SMTP, and keychain
(`manual/reference/glossary.md`), Bcc (a hidden copy of a message), Trash (the
folder of the mail account).

**Not proven by this repository.**

- That every provider with IMAP and SMTP works. The page says "works with IMAP
  and SMTP mail", not "any provider" (T6).
- That a given provider offers app passwords (T31).
- That Claude Code and the other apps show the form in their own window. This
  comes from the documents of those apps, as the "Source and date" section of
  each page under `manual/getting-started/connect-ai-app/` records, and
  `manual/daily-use/app-confirmation.md` sums up. The page does not name an app
  next to this claim (T24).

## Depends on the other specs

- **Foundation (shared layout).** The menu, the footer with the version, the
  skip link, the base template and the token stylesheet. The page uses them and
  does not make its own.
- **User manual.** The address of the install page (the target of "Install and
  set up"). The setup pages for Claude Code, Claude Desktop, Cursor, VS Code and
  the Codex CLI (T33). The explanation of app passwords (T32).
- **Tech docs.** One line in its map from code area to page (see "Keeping the
  page true").

If a spec drops something that this page names, the builder removes the claim
from the page and tells the owner. The builder does not keep a claim that has no
proof.

## Keeping the page true

The docs check of the foundation does not cover the landing page. So three
things keep it true:

1. **A label test.** A Go test in `internal/web` (name chosen by the builder)
   reads `site/layouts/_partials/approval-picture.html` and
   `internal/web/templates/approval.html`. For each label below, it checks that
   the text is in both files:
   `Waiting for you`, `Reject`, `Approve and send`, `From`,
   `To, every recipient (Bcc included)`, `Attachments`, `until you approve.`,
   `goes out`.
   The note `Nothing goes out until you approve.` is built from pieces in the
   template, so the test checks the two pieces. If a pull request renames a
   label in the portal, the test fails until the picture changes too. If the
   `site/` folder is missing (for example in a source archive), the test skips
   with a message. (My call.) This guard matters because the docs check of the
   foundation covers the manual and the tech docs, and it does not cover this
   picture.
2. **The truth table.** The source notes (`T13`) tie each claim to its row.
   A pull request that changes a "Proof" location should re-read its rows.
3. **Few moving parts.** The page has no version, no count and no number from
   the code. The footer shows the version (foundation). The only facts that can
   change are the level names, the defaults of a new account, the labels of the
   approval page, the list of apps in the manual, and the build targets. All are
   in the table above.

The social card is text only, so the portal cannot make it wrong.

Request to the author of the tech docs spec: its map from code area to page
should list `internal/web/templates/approval.html` and point to the picture and
the test. This spec does not edit that file.

## Done when

The builder checks each item before the pull request:

- The first screen passes the five-second test with a fresh agent that sees only
  that screen. It can say that an AI agent works with your email and that you
  approve what goes out.
- Every word on the page matches this spec. Every claim has a row in the truth
  table.
- The page builds with no warning. All links work under the real base path, in
  a local preview with that path set.
- The label test passes, and fails when one label of the picture is changed.
- No request leaves the site (check the network panel of the browser), and no
  cookie is set.
- The page works with script turned off. It has no script.
- Widths 320, 375, 680, 900, 1100 and 1440 pixels: no horizontal scroll, and the
  layout of each breakpoint above.
- Light and dark themes both checked. No hex colour in the landing styles.
- The accessibility checks in "Accessibility".
- The page head matches "Page data". The card is 1200 by 630 pixels.
- The page has no red, no amber, no shadow, no gradient, no uppercase label and
  no icon character from a font.

## Left to the builder

- Hugo template and partial names, and how front matter feeds them.
- The CSS names and structure, inside the tokens and sizes above.
- The exact page under `/manual/` for "Install and set up", from the manual
  spec.
- Whether other links in section 6 go deeper into the manual.
- The code and the file name of the label test.
- How the social card is made, and where its source is kept.
- Whether the shared layout already provides the container width and the skip
  link.
- The small details between the sizes in this spec (for example the exact gap
  between the button and the text link).

The words, the order of sections, the claims and their limits are not left to
the builder.

## Glossary

- **Approval page:** the page of the portal named "Waiting for you". It shows a
  send that waits, with the buttons Reject and Approve and send.
- **App password:** a separate password for one program, made in the security
  settings of your mail account.
- **Bcc:** blind carbon copy. A recipient that the other recipients cannot see.
- **Contrast ratio:** how different two colours are, from 1:1 (none) to 21:1
  (black on white). WCAG AA asks for 4.5:1 for text and 3:1 for the edges of
  controls.
- **Ground and pane:** the two tones of `DESIGN.md`. The ground is the tinted
  page behind everything. The pane is the white reading area.
- **Gutter:** the empty space at the left and right edge of the page.
- **Hairline:** a 1px rule in the `--line` colour.
- **Hero:** the first block of a landing page.
- **Keychain:** the password store of your operating system.
- **Landmark:** a named area of a page (such as `main`) that a screen reader
  user can jump to.
- **Level:** how much the agent may do on one account, from 0 (Off) to 5
  (Delete, which means move to the Trash).
- **OAuth:** a sign-in method where you log in on the website of the provider,
  and the program never sees your password.
- **Open Graph:** `og:` tags in the page head. Chat apps and social sites read
  them to draw a preview card when someone shares a link.
- **Partial:** a Hugo template file included by other templates.
- **Portal:** the local web pages of email-mcp (Accounts, Waiting for you,
  Activity, Connections).
- **Skip link:** the first link of a page. It lets a keyboard user jump past the
  menu to the main content.
- **Token:** a named design value (a colour, a size) in `DESIGN.md` and in the
  shared stylesheet.
- **Truth table:** the list in this spec of each claim and its proof.
- **WCAG** (Web Content Accessibility Guidelines): the rules for pages that
  everyone can use. This spec aims at level AA, the usual legal level.
