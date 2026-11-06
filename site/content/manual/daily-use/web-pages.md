---
title: The web pages
description: A map of the email-mcp web pages in your browser, with what each menu entry shows and the page of this manual that explains it.
weight: 20
code_areas:
  - cmd/email-mcp/setup.go
  - internal/web/server.go
  - internal/web/accounts.go
  - internal/web/approvals.go
  - internal/web/connections.go
  - internal/web/session.go
  - internal/web/guard.go
  - internal/web/links.go
  - internal/web/templates/layout.html
  - internal/web/templates/accounts.html
  - internal/web/templates/approval.html
  - internal/web/templates/approvals.html
  - internal/web/templates/connections.html
  - internal/web/templates/no_session.html
  - internal/web/templates/use_name.html
---

After this page, you know which web page does what. The web pages of email-mcp
are the pages that email-mcp shows in your browser, from your own computer. You
use them to answer the agent (the AI (artificial intelligence) program that uses
email-mcp), to look after your accounts, and to see what the agent did.

## The address and the sign-in

The web pages run at `email-mcp.localhost`, a name that always means your own
computer. You sign in with a link that the command `email-mcp setup` prints. The
page [Connect your first
account](/manual/getting-started/connect-account/#sign-in-to-the-web-pages)
has the steps.

## Open the pages again

The web pages run only while an email-mcp program serves them. `email-mcp setup`
serves them for as long as it runs. The email-mcp program that your AI app
started serves them too, but it starts them only when it gives you a link. Each
time, the pages get a new port (the number that names one service on an
address). So a tab or a bookmark from an earlier time finds nothing.

To open the pages, run this in a terminal (a window where you type commands),
and open the link that it prints. Keep the terminal open while you use the
pages:

```sh
email-mcp setup
```

Press Ctrl-C (hold the Ctrl key and press C) when you are done. The page
[Connect your first
account](/manual/getting-started/connect-account/#sign-in-to-the-web-pages) has
the steps.

## The menu

After you sign in, every page has a menu. On a wide window the menu is at the
left, and on a narrow window it moves to the top. On a very narrow window, such
as a phone, **Waiting for you** shows as **Waiting**, and an open send or move
to the Trash hides the menu: its back link leads to **Waiting for you**. A
browser that is not signed in has no menu. The menu has these entries:

- **Waiting for you** shows the sends and the moves to the Trash (the folder of
  deleted mail) that wait for your answer. A number beside the name says how
  many wait. The page [Confirmations](/manual/daily-use/confirmations/) explains
  them.
- **Accounts** shows your connected accounts. The page [Manage
  accounts](/manual/daily-use/manage-accounts/) explains them.
- **Activity** shows the sends that wait to go out, and the log of what the
  agent did. The page [Activity and undo](/manual/daily-use/activity-and-undo/)
  explains them.
- **Connections** shows the access tokens of local HTTP mode, and the browsers
  that are signed in. A token is a secret that an app sends to the local server.
  HTTP (HyperText Transfer Protocol) is the protocol that apps and browsers use
  to talk over a network. The page [Local HTTP
  mode](/manual/reference/local-http-mode/) explains the tokens.
- **Sign out** signs this browser out.

{{< screenshot
  name="accounts"
  alt=`The page Accounts. The menu at the left has Waiting for you, Accounts,
    Activity, Connections and Sign out. The page shows one connected account,
    with the buttons Permissions and settings and Reconnect.`
>}}

## What opens first

When you sign in, your browser goes to the page that you need first. When no
account exists yet, it shows **Accounts**. Otherwise, it shows **Waiting for
you**. When nothing waits, that page says "Nothing waits for you". When a send
or a move to the Trash waits, the page lists it, and the first one opens beside
the list, with its buttons.

{{< screenshot
  name="waiting"
  alt=`The page Waiting for you, with one send open. The page shows the
    message and the buttons Reject and Approve and send.`
>}}

## Pages with no menu entry

Four pages have no entry in the menu. You reach each one from another page:

- **Connect an account** opens from the button **Connect account** on
  **Accounts**. The page [Connect your first
  account](/manual/getting-started/connect-account/) has the steps.
- **Permissions and settings** opens from the account on **Accounts**. It holds
  the level (a number from 0 to 5 that says what the agent may do), the
  confirmations and the limits of the account. See [Permission
  levels](/manual/daily-use/permission-levels/).
- **Reconnect** opens from the account on **Accounts**. See [Manage
  accounts](/manual/daily-use/manage-accounts/).
- One action to approve opens from the link that the agent gives you, or from
  **Waiting for you**. See [Confirmations](/manual/daily-use/confirmations/).

## A browser that is not signed in

A browser that is not signed in shows a page with one sentence. It says to run
`email-mcp setup` in a terminal, and then to open the link again. That means
that your sign-in ended, or that this browser never signed in. See [A web page
says to run `email-mcp setup`][ns].

A browser that opens the pages on `127.0.0.1` or on `localhost` gets a page that
says to open email-mcp at its own address, `email-mcp.localhost`. You can sign
in only at that address.

## The sign-in times

The link that `email-mcp setup` prints works once, for 10 minutes. A sign-in
lasts 24 hours at most, and it ends after 2 hours without use. The page
[Connect your first
account](/manual/getting-started/connect-account/#sign-in-to-the-web-pages)
says more.

[ns]: /manual/daily-use/troubleshooting/#a-web-page-says-to-run-email-mcp-setup
