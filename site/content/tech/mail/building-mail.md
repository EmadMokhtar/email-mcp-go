---
title: Building mail
description: How email-mcp builds the mail that an agent writes, what it refuses, and how it makes a saved draft ready to send, so that the preview shows the text that goes out.
weight: 40
code_areas:
  - internal/mime/build.go
  - internal/mime/body.go
  - internal/mime/recipients.go
  - internal/mime/draft.go
  - internal/mime/address.go
  - internal/service/send.go
---

This page explains how email-mcp builds the mail that an agent writes: the
checks that refuse a message, the rules for recipients, the subject, the HTML
and the addresses, a saved draft, and what the preview shows. Read it before you
change `Build`, `FromDraft` or the rules for addresses. [Mail
content](/tech/mail/mail-content/#two-pipelines) draws the outgoing pipeline.

## What goes out

`Build` in {{< src "internal/mime/build.go" >}} writes a message, a reply, a
reply to all, or a forward, in the form of RFC 5322 (the numbered document for
the internet message format). {{< src "internal/mime/body.go" >}} has the bodies
and the attachments, {{< src "internal/mime/recipients.go" >}} the recipients,
and {{< src "internal/mime/address.go" >}} the rules for addresses. A message
that the agent writes goes out as the preview can show it. A saved draft goes
out as it was saved (see [A saved draft](#a-saved-draft)).

## What Build refuses

A refusal returns an error that names the field. It holds no address, subject or
other text of the message.

| Error | The case |
|---|---|
| `ErrInvalidAddress` | An address is missing, cannot be parsed, or cannot be sent as it is: a quoted local part such as `"a b"@example.com`, a domain literal such as `bob@[1.2.3.4]`, or a space, a quote or one of `<>()[],;:\` in it |
| `ErrNonASCIILocalPart` | A recipient that the caller typed has characters outside ASCII (English letters, digits and common signs) before the `@`. It matches `ErrInvalidAddress`. |
| `ErrNoRecipients` | The message goes to nobody. A draft may have none. |
| `ErrHeaderInjection` | A header value has a CR (carriage return), an LF (line feed) or a NUL (the zero byte). A line break would let the value add a header, such as a Bcc. |
| `ErrHiddenText` | The subject, the text body or a display name has a character that `CleanUntrusted` would remove. |
| `ErrEmptyHTML` | Nothing is left of the HTML body after the parts that a reader cannot see, and those that cannot go out, are removed. |
| `ErrInvalidFilename` | The file name of an attachment has a character that `CleanUntrusted` would remove, or a line or paragraph separator. |
| `ErrInvalidContentType` | The content type of an attachment cannot be written, is multipart, has a charset other than UTF-8 or US-ASCII, or has a media type over `MaxMediaTypeBytes` (100 bytes). |
| `ErrNoOriginal`, `ErrReplyAndForward` | A reply or a forward has no original message, or one request asks for both. |
| `mail.ErrTooLarge` | The attachments are together over `MaxAttachmentBytes`, 10 MiB. A MiB (mebibyte) is 1,048,576 bytes. |
| `ErrHTMLTooComplex` | The HTML body is too complex to clean or to make the text of. |

A value from the original message is cleaned and not refused, because a third
party wrote it and it must not stop a reply. An address of the original with a
line break is refused, because cleaning would change it.

## Recipients

A reply goes to the `Reply-To` addresses of the original, or to its sender. A
reply to all also copies the To and Cc addresses of the original to Cc. Then the
To, Cc and Bcc addresses of the request are added. For a reply to all, the
addresses of the user (`BuildContext.Self`) are then removed from To and Cc.
Each address stays once, and the first one wins, in the order To, Cc, Bcc. All
are in `RawMessage.Recipients`. A Bcc is a blind copy: the other recipients do
not see it. The message has no `Bcc` header. A draft keeps the header
(`BuildContext.ForDraft`), and `FromDraft` removes it before the send.

## Subject and threading

When the request has no subject, a reply gets "Re: " before the subject of the
original, unless that starts with "re:" in any case, and a forward gets "Fwd: ".
A reply gets `In-Reply-To` and `References` headers. `Build` does not quote the
original in a reply. A forward puts the text of the original under the header
"Forwarded message". It copies the attachments of the original only when
`IncludeOriginalAttachments` is set, with cleaned file names and types. A media
type over `MaxMediaTypeBytes` goes out as `application/octet-stream`. The HTML
of the original is never copied.

## HTML in outgoing mail

`outgoingHTML` is `VisibleHTML` with one more rule. Both clean the HTML with the
policy of `SanitizeHTML`. The policy keeps headings, paragraphs, inline text
such as `b` and `em`, lists, tables and links. A link keeps `href`, and the
fixed `rel="nofollow"`, only when its address uses `http`, `https` or `mailto`,
and the text shows the whole address. A link whose address the text cuts or does
not show goes out as plain text. The result has no comment, no style and no
image, and no attribute but those two. A message has one text: when the request
has an HTML body, its plain text body is not used, and the text part is made
from the HTML.

## Addresses that look like others

`ShownAddress` writes the ASCII form (punycode, the `xn--` spelling) of a domain
outside ASCII next to the address, so that a look-alike domain shows.
`AddressWarning` names each part of a domain that mixes writing systems, by the
Unicode TS 39 (the Unicode technical standard on security) at the level "highly
restrictive". The send refuses a domain with no exact ASCII form
(`HasExactASCIIDomain`). `HasNonASCIIAddress` makes the service ask the person
before it sends to an address outside ASCII (see [The send
flow](/tech/inside-the-server/send-flow/)).

## The shape of the message

One `text/plain` part; or a `multipart/alternative` with the text part first and
the HTML part last, because mail programs use the last part that they can show;
and a `multipart/mixed` around it when there are attachments. `Build` gives the
message its own `Message-ID`: 16 random bytes in hex, an `@`, and the domain of
the sender. Of the content type of an attachment, only the media type and a
UTF-8 (a way to write each character as one to four bytes) or US-ASCII charset
go out.

## A saved draft

`FromDraft` in {{< src "internal/mime/draft.go" >}} makes a saved draft ready to
send. It reads the sender from the first `From` address and the recipients from
`To`, `Cc` and `Bcc`. The message that goes out is the draft as it was saved,
minus the `Bcc` fields. It refuses a bare CR (a carriage return with no line
feed after it) in the header (`ErrHeaderInjection`), a missing sender or an
unreadable address (`ErrInvalidAddress`), and a draft with no recipient
(`ErrNoRecipients`). It does not refuse or clean hidden characters or HTML the
way `Build` does: a draft goes out as the person saved it, with its styles and
images. The preview of a draft counts its hidden characters and shows its text
only. Check a draft that came from another mail program before you send it (see
[Security](/manual/daily-use/security/)).

## The preview and the text part

For a message made from HTML, `composeBodies` makes the text part with
`HTMLToText` of the cleaned HTML, and `Build` writes it first. The service, in
{{< src "internal/service/send.go" >}}, reads the built message again and shows
the person the start of the text part. So the start of the text that the person
sees is the start of the text that goes out. For a saved draft, the preview
shows the parts that `LayoutOf` picks. [The send
flow](/tech/inside-the-server/send-flow/#what-the-person-sees) explains the
preview and how the answer of the person is tied to the message.

## Related pages

- [Mail content](/tech/mail/mail-content/)
- [The send flow](/tech/inside-the-server/send-flow/)
- [What the agent can do](/manual/daily-use/what-the-agent-can-do/) in the user
  manual
- [Security](/manual/daily-use/security/) in the user manual
