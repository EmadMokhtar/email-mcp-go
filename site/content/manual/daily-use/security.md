---
title: "Security: what local mode protects against"
description: Learn which attackers local mode stops, which ones it cannot stop, and what you can do about it.
weight: 80
code_areas:
  - internal/web/session.go
  - internal/web/guard.go
  - internal/web/links.go
  - internal/mime/
---

This is the local threat model, word for word from the design (section 10.1).
The numbers in brackets are sections of the design document at
{{< src "docs/superpowers/specs/2026-10-27-email-mcp-rewrite-design.md" >}}.

> - **Local mode stops an agent that can only call this server's tools and
>   fetch URLs.**
>   - Such an agent cannot change permissions, approve its own confirmations, or
>     reach the web UI. Links carry no tokens (8.2), and the session lives only
>     in the user's browser.
> - **Local mode cannot stop an agent that runs shell commands as the same OS
>   user.**
>   - Such an agent can read the keychain and the database, or use the mail
>     password directly. No local design can prevent that; only OS-level
>     isolation can.
>   - The docs say this plainly. They show how to deny shell access to the data
>     folder, the `email-mcp` command, and the keychain tools in Claude Code
>     (16.2).
>   - Users who need protection from such an agent should use hosted mode, or
>     run the server under a separate OS user.

Hosted mode (one server that many people use) cannot be installed yet. For now,
the option is to run email-mcp under a separate operating system user: another
account on your computer, not your own. In the quote, OS means operating system,
the web UI (user interface) is the web pages of email-mcp, and the keychain is
the password store of your operating system. The agent is the AI (artificial
intelligence) program that uses the tools of email-mcp. A level is a number from
0 to 5 that says what the agent may do with the mail of an account.

In plain words: the links that the agent gets never carry a secret. Only the
browser that `email-mcp setup` signed in can change a level, or approve a send
with an approval link. A send that your AI app confirms with its own form is
answered in that app (see [Trust in the AI
app](/manual/daily-use/confirmations/#trust-in-the-ai-app)). But an agent that
may run any shell command as you can do anything that you can do, also outside
email-mcp. The page [Recommended Claude Code deny
rules](/manual/daily-use/deny-rules/) shows how to limit that in Claude Code.

**An agent that controls your browser is the same risk.** Browser-control tools,
such as Claude in Chrome or a Playwright MCP (Model Context Protocol) server
that uses your browser profile (a separate set of browser settings and
sign-ins), act in your signed-in browser, with its cookies (small pieces of data
that a browser keeps for a website, such as a sign-in). Such a tool can open the
email-mcp web pages there, raise the level of an account, turn off a
confirmation, or press **Approve and send** on its own pending send. No code can
tell a human click from an automated one. So run `email-mcp setup`, and use the
email-mcp web pages, in a browser or a browser profile that no agent controls,
and do not give browser-control tools access to that profile.

**What you approve is what goes out.** A message that an agent writes can only
hold what a confirmation can show:

- HTML (HyperText Markup Language: the language of web pages) is reduced to
  text, headings, lists, tables and `http`, `https` or `mailto` links, and the
  text of the message is made from the result. Styles, images, comments, other
  attributes and hidden text are removed, so an HTML mail from an agent looks
  plain. A link keeps its address only when the text of the message shows the
  whole address. A long address is cut in the text (see the known limitations),
  so such a link goes out as plain text, without its address.
- A subject, a plain text body or a display name (the name that shows next to an
  address) with a character that a reader cannot see is refused. Such characters
  are zero width characters, direction controls, tag characters and the like.
  Direction marks (LRM, RLM and ALM: marks that set the direction of text and
  draw nothing) are refused too, also in Arabic or Hebrew text, because nobody
  can see them. Emoji and the letters of any language are fine, also the joiners
  that Persian, Arabic and Indic text need, and the signs that a reader sees,
  such as the Arabic number sign.
- HTML that has nothing left after this is refused.
- The same rules apply to `create_draft`.
- A confirmation shows the display name of each recipient next to the address. A
  long name is cut, and the confirmation says how much it left out.
- A confirmation counts every character of the text that it does not show, such
  as padding, and the approval covers the whole message: the text, the HTML, the
  attachments and the display names. A change in any of these means that you are
  asked again.
- A confirmation shows the media type of each attachment (the kind of file, such
  as `application/pdf`) next to its name. An attachment keeps only the `charset`
  (the character set of a text) of its content type, and other parameters do not
  go out. A media type over 100 bytes is refused. A forwarded attachment with
  such a type goes out as `application/octet-stream` (the type for bytes of no
  known kind).

A draft that you write yourself in another mail program goes out as you saved
it, with its styles and images. The confirmation shows the text of it, and it
counts hidden characters in its text and its HTML. It does not list the styles
or the images. An agent cannot make such a draft, but a draft that it saved with
an older version of email-mcp can still hold them.

**Other web services on your computer.** A browser sends a cookie to every port
(the number that names one service on an address) of a host. So a sign-in cookie
on `127.0.0.1` would also go to every other local web service that you open in
that browser, such as a dev server (a program that a developer runs to test a
website), a notebook, a dashboard or a local AI tool. That service, a dependency
inside it (a library that it uses), or its request log could then use your
sign-in to approve a send or make a token (a secret that an app sends to the
local server). This is why the web pages have their own name,
`email-mcp.localhost`, which no other program uses. Only that name gets the
cookie, and no sign-in works on `127.0.0.1` or `localhost`.

One gap stays. email-mcp uses a random port, so if you open an old link after
email-mcp has stopped, and another program now listens on that port, your
browser sends the cookie to that program. To limit this, a sign-in lasts at most
24 hours and ends after 2 hours without use. Click **Sign out** when you finish.
The **Connections** page lists the signed-in browsers, and you can sign out each
one. `email-mcp setup --revoke-sessions` signs out every browser at once, and
stops every sign-in link that was not used yet.
