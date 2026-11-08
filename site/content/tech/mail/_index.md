---
title: Mail
description: How email-mcp talks to a mail server, and how it makes the content of mail safe to read and to send.
weight: 30
code_areas:
  - internal/domain/mail/
  - internal/provider/imapsmtp/
  - internal/discovery/
  - internal/connect/
  - internal/mime/
  - internal/thirdparty/xhtml/
---

These pages explain the two parts of email-mcp that touch mail itself: the
backend that talks to the mail server of an account, and the code that reads
and writes the content of mail. Each page names the files that it covers. Read
them when you change how email-mcp connects to a server, when you add a mail
provider, or when you change how it cleans or builds mail.

- [The mail backend](/tech/mail/mail-backend/): the interface that every mail
  provider implements, the IMAP and SMTP backend, its connections and their
  checks, how an account is connected, and where a new provider would go.
- [Mail content](/tech/mail/mail-content/): how text from third parties is
  made safe for an agent, how outgoing mail is built so that the preview equals
  the message, and why the repository holds a copy of an HTML parser.
