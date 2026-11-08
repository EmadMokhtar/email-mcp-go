# email-mcp

email-mcp gives an AI (artificial intelligence) agent access to your email. It
is an MCP server. MCP (Model Context Protocol) is an open standard that lets AI
apps, such as Claude Code, call tools that other programs offer.

- It works with any mail provider that offers IMAP (Internet Message Access
  Protocol: the protocol for reading mail) and SMTP (Simple Mail Transfer
  Protocol: the protocol for sending mail), with a password or an app password.
  An app password is a separate password that you make in your mail account for
  one program.
- It runs on your own computer. Your passwords stay on your computer,
  encrypted.
- You decide what the agent may do, per account: only read (level 1), also
  organize (2), also write drafts (3), also send (4), also move mail to the
  Trash (5). Level 0 gives no access.
- By default, every send waits for your "yes". A move to the Trash needs level
  5 first, and then it waits for your "yes" too. The one exception is a draft:
  at level 3, the agent can move a draft to the Trash, and it does not ask you.
- One server can hold several accounts.

This version covers mail only. Calendar and contacts, and sign-in with Google
or Microsoft (OAuth: a way to sign in without giving a program your password),
are planned. Until then, connect Google and Microsoft accounts with an app
password.

## Install

With Homebrew (a package manager: a program that installs other programs), on
macOS and Linux:

```sh
brew install --cask emadmokhtar/tap/email-mcp
```

Check the install:

```sh
email-mcp version
```

The page [Install email-mcp][install] shows the other ways: a release archive
(also for Windows), Go, and the source code. It also explains how Homebrew
treats the macOS program check, and it links to the page that shows how to
check the signature of a release.

## Documentation

All documentation is on the site <https://emadmokhtar.github.io/email-mcp-go/>.

- [The start page][site]: what email-mcp does, on one page.
- [The user manual][manual]: install email-mcp, connect an account and an AI
  app, and use it every day.
- [The tech docs][tech]: how the code works, how to test it, and how to make a
  release. They are for people who change the code.

[site]: https://emadmokhtar.github.io/email-mcp-go/
[manual]: https://emadmokhtar.github.io/email-mcp-go/manual/
[tech]: https://emadmokhtar.github.io/email-mcp-go/tech/
[install]: https://emadmokhtar.github.io/email-mcp-go/manual/getting-started/install/

## License

MIT. See [LICENSE](LICENSE).
