---
title: Mail
description: How email-mcp talks to a mail server, how an account is connected, and how it cleans and builds mail.
weight: 30
code_areas:
  - internal/domain/mail/
  - internal/provider/imapsmtp/
  - internal/discovery/
  - internal/connect/
  - internal/mime/
  - internal/thirdparty/xhtml/
---

These pages explain the parts of email-mcp that touch mail itself: the backend
that talks to the mail server of an account, the steps that connect an account,
and the code that reads and writes the content of mail. Each page names the
files that it covers. Read them when you change how email-mcp connects to a
server, when you add a mail provider, or when you change how it cleans or builds
mail.

- [The mail backend](/tech/mail/mail-backend/): the interface that every mail
  provider implements, the IMAP and SMTP backend, its connections and their
  checks, and where a new provider would go.
- [Connecting an account](/tech/mail/connecting-an-account/): how the server
  settings of an address are found, how an account is tested and saved, the
  decision about a certificate, and the reconnect.
- [Mail content](/tech/mail/mail-content/): how email-mcp cleans text from
  third parties before an agent reads it, how HTML becomes text, and why the
  repository holds a copy of an HTML parser.
- [Building mail](/tech/mail/building-mail/): how outgoing mail is built, what
  it refuses, and how it is built so that the preview shows the text that goes
  out.
