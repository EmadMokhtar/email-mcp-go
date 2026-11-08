---
title: Mail content
description: How text from third parties is made safe for an agent, how outgoing mail is built so that the preview equals the message, and why the repository holds a copy of an HTML parser.
weight: 20
code_areas:
  - internal/mime/untrusted.go
  - internal/mime/html.go
  - internal/mime/visible.go
  - internal/mime/css.go
  - internal/mime/parts.go
  - internal/mime/decode.go
  - internal/mime/multipart.go
  - internal/mime/params.go
  - internal/mime/compare.go
  - internal/mime/build.go
  - internal/mime/body.go
  - internal/mime/recipients.go
  - internal/mime/draft.go
  - internal/mime/address.go
  - internal/thirdparty/xhtml/
  - internal/provider/imapsmtp/fetch.go
  - internal/provider/imapsmtp/summary.go
  - internal/mcpserver/shape.go
---

This page explains how email-mcp makes the text of mail safe for an agent, how
it builds the mail that an agent writes, and why the repository holds a copy of
an HTML parser. Read it before you change a rule for hidden text, a limit on
HTML, the building of a message, or the copied parser.

A third party is a person who wrote text that email-mcp passes on and who is not
the user: the sender of a mail. Text from a third party is untrusted. It can
hold prompt injection: words that try to give instructions to the agent, often
hidden from the person who reads the mail. The package `mime` handles this. MIME
(Multipurpose Internet Mail Extensions) is the format that gives a mail its
parts, such as the text, the HTML and the attachments. HTML (HyperText Markup
Language) is the language of web pages and of many mails.

## Two pipelines

{{< diagram "mail-pipelines" >}}

**Incoming mail**, from the server to the agent. `GetMessage` in
{{< src "internal/provider/imapsmtp/fetch.go" >}} does steps 1 to 5.

1. **Fetch.** The backend reads the structure of the message first.
   `mime.LayoutOf` chooses the text parts that make the body, by the rules of a
   mail client (see "Which parts are the body"). The backend fetches them with
   `BODY.PEEK`, the way that IMAP (Internet Message Access Protocol, the
   protocol for reading mail) reads a message without marking it as read. It
   fetches an HTML part whole, because a style rule at the end of a document can
   hide an element at the start. It does not fetch or convert an HTML part over
   4 MiB (`maxHTMLBytes`). A MiB (mebibyte) is 1,048,576 bytes.
2. **Decode.** `mime.DecodeText` undoes the transfer encoding (the way that a
   part writes its bytes as plain text: base64 or quoted-printable) and the
   charset (the table that maps bytes to characters). A byte that is not valid
   UTF-8 (the usual way to write text as bytes) becomes U+FFFD, the replacement
   character. A damaged part gives the text up to the damage, and no error.
3. **Parse.** An HTML part goes to `ReadableText` (the `text` body format) or
   `ReadableHTML` (the `html` format). Both start with `parseHTML`, which calls
   the copied parser with a work budget. A document that needs more work gives
   `ErrHTMLTooComplex` and no text.
4. **Hidden-text rules.** A walk over the tree leaves out what a reader of the
   mail does not see, and it makes a report of what it left out.
5. **Clean.** `CleanUntrusted` removes hidden characters from the text. The
   backend cuts the body into pages.
6. **Wrap as untrusted.** `messageOut` in
   {{< src "internal/mcpserver/shape.go" >}} cleans each string again and puts
   the text that a sender wrote inside the `Untrusted` wrapper. The text copy of
   the result (the copy for clients that do not read structured content) starts
   with a fixed banner line. [One tool call, step by
   step](/tech/start-here/tool-call/#where-text-from-the-mail-is-cleaned)
   explains the wrapper.

**Outgoing mail**, from the agent to the server. `Service.Send` and
`CreateDraft` call `mime.Build`.

1. **Compose.** The agent gives a `mail.Compose`: the recipients, a subject, a
   text body or an HTML body, a reply or a forward, and attachments as bytes. A
   file path is not an attachment.
2. **Check the addresses and the headers.** `Build` refuses an address that
   cannot go into an SMTP (Simple Mail Transfer Protocol, the protocol for
   sending mail) command, and a value with a line break. Each address stays
   once, in the order To, Cc, Bcc (a blind copy: the other recipients do not see
   it).
3. **Refuse hidden characters.** The subject, the text body and the display
   names of the recipients are refused when they have a character that
   `CleanUntrusted` would remove. They are not cleaned.
4. **Clean the HTML, make the text.** `outgoingHTML` cleans the HTML, and
   `HTMLToText` makes the text part from the result.
5. **Write the message.** The text part, the HTML part and the attachments. A
   message has no `Bcc` header, except a draft.
6. **Preview.** `service` shows the text part to the person. The text part is
   the text that goes out.

Where each part is:

| File | What it holds |
|---|---|
| {{< src "internal/mime/untrusted.go" >}} | `CleanUntrusted`, `HiddenRunes`, `CleanAndCount`: the removal of hidden characters |
| {{< src "internal/mime/html.go" >}} | `HTMLToText`, `ReadableText`, `HiddenReport`, the walk over the tree, the links, the work budget, and `SanitizeHTML` |
| {{< src "internal/mime/visible.go" >}} | `VisibleHTML`, `ReadableHTML` and `outgoingHTML`: the same rules, with HTML as the result |
| {{< src "internal/mime/css.go" >}} | A small reader of CSS (Cascading Style Sheets) that finds the elements a mail client hides |
| {{< src "internal/mime/parts.go" >}} | `LayoutOf`: which parts are the body, and which are other versions |
| {{< src "internal/mime/decode.go" >}}, {{< src "internal/mime/multipart.go" >}} and {{< src "internal/mime/params.go" >}} | The decoders of text and transfer encodings, `ParseMessage`, the readers of header blocks and multipart bodies, and the names and parameters in the forms of RFC 2231 and RFC 2047 (RFC is a Request for Comments, a numbered document that defines an internet standard) |
| {{< src "internal/mime/compare.go" >}} | `TextsDiffer`: do two versions of a body say the same? |
| {{< src "internal/mime/build.go" >}}, {{< src "internal/mime/body.go" >}} and {{< src "internal/mime/recipients.go" >}} | `Build`: the headers, the bodies, the forward, the attachments and the recipients |
| {{< src "internal/mime/draft.go" >}} | `FromDraft`: a saved draft made ready to send |
| {{< src "internal/mime/address.go" >}} | The rules for how an address is checked and shown |
| {{< src "internal/provider/imapsmtp/summary.go" >}} | The summary and the snippet of each message in a list |
| {{< src "internal/thirdparty/xhtml/" >}} | The copied HTML parser |

## What CleanUntrusted removes

`CleanUntrusted` makes text from a third party safe to show to an agent. A
character that nobody sees can carry data, so the function removes the
characters that draw nothing. It works in this order, and the result is always
valid UTF-8:

| Step | What it does |
|---|---|
| Bytes | Each run of bytes that is not valid UTF-8 becomes U+FFFD, which a reader sees. |
| Line breaks | `\r\n` and a lone `\r` become `\n`. |
| Format characters | Every character of the Unicode category Cf (format characters) goes: the zero width space, the direction marks, the bidi (bidirectional text) embeddings, overrides and isolates, the word joiner, the soft hyphen, the byte order mark, the tag characters and more. The exceptions are the joiners below and the few that a reader sees, such as the Arabic number signs. |
| Other invisible characters | The tag block U+E0000 to U+E0FFF, the variation selectors U+FE00 to U+FE0E, the private use areas, the braille blank, the combining grapheme joiner, the Hangul fillers, the Mongolian free variation selectors, the Khmer inherent vowels, the object replacement character, and the code points without an assignment in two format ranges. |
| Control characters | Category Cc goes, except `\n` and `\t`. |
| Joiners | U+200C (zero width non-joiner), U+200D (zero width joiner) and U+FE0F (variation selector 16, which asks for an emoji, a small picture that is a character) go unless they change what a reader sees. |

**Why the joiners stay in some places.** In Persian, Arabic and the Indic
scripts, a joiner changes the shape of the letters. In an emoji sequence, a
joiner makes one picture of several. So `keepsJoiner` keeps a joiner next to a
letter of a script that joins letters, and inside an emoji sequence. Between
Latin letters, for example, a joiner changes nothing on the screen, and text can
hide data in it, so it goes. A run of joiners is cut to what a word or an emoji
needs. Cleaning the result again changes nothing.

`HiddenRunes` counts the characters that `CleanUntrusted` removes, and
`CleanAndCount` returns both in one pass. `MarkInvisible` is for a value in
which each character counts, such as an address: it writes U+FFFD in place of
each character that a reader cannot see, because a removed character would give
another address.

## Which parts are the body

`mime.LayoutOf` in {{< src "internal/mime/parts.go" >}} is the one rule for what
a mail client shows. The read path and the preview of a draft use it, so an
agent reads what the person reads. Only `text/plain` and `text/html` parts can
be the body. In a `multipart/alternative`, a client shows the last part that it
can show, and the parts before it are other versions. In a `multipart/related`,
it shows the root part. In another multipart, it shows every part, in order. A
part with the disposition `attachment` is never the body.

The backend reads at most 16 parts for a body (`maxShownParts`). It compares the
body with up to 4 other versions (`maxOtherVersions`). With more than 4, the
result says that the versions differ. The result also says when it left text out
(`text_left_out`) and when another version of the body says something else
(`alternatives_differ`). `TextsDiffer` compares the words of the two versions,
not their text. It gives a hint to the reader. It is not a check that stops a
message.

## How HTML becomes text

`HTMLToText` turns HTML into plain text. It keeps what a person sees in a mail
client and leaves out what a person does not see, because a sender can use the
hidden parts to give instructions to an agent.
{{< src "internal/mime/html.go" >}} has a long comment above `HTMLToText` with
the full list of rules. This page gives the groups. It does not copy the list,
so that the comment stays the one place to change.

The text leaves out:

- **Elements by name.** Elements that a browser does not show or that hold
  another document (`script`, `style`, `head`, `iframe`, `svg` and more), images
  with their alt text, and elements that hold only a fallback or a list of
  choices (`video`, `canvas`, `select` and more).
- **Elements hidden by an attribute.** The `hidden` and `popover` attributes,
  `aria-hidden="true"`, a `dialog` that is not open, and all but the first
  `summary` of a closed `details`.
- **Elements hidden by a style.** A style can hide a whole element and
  everything in it, for example with `display:none`, an opacity close to zero,
  or a position far off the page. The styles come from the `style` attribute and
  from the rules in `style` elements.
- **Text hidden by an inherited style.** `font-size` and `visibility` pass from
  a parent to its children. Text that has no readable size, or is not visible,
  is left out. A child can set its own value and show its text.
- **Comments.**

**The principle: a hiding rule on an element wins.** A style can come from the
`style` attribute and from the rules for the name, the classes and the id of the
element. If any of them hides the element, it is hidden. The order, the strength
of the source and `!important` do not matter. This is stricter than a browser,
which picks one source. A rule that hides a whole element, such as
`display:none`, holds even if something else would show the element again.
Inside one source, `display`, `visibility`, `opacity` and `font-size` work as in
a browser: the last declaration counts.

**When a style cannot be worked out.** The code reads just enough CSS (Cascading
Style Sheets, the language of styles) to find hidden elements. A value that
needs a calculation (`calc()` or `var()`) in `font-size` or `opacity` counts as
hiding. A rule with a selector that is longer than a class, an id, an element
name or a compound of these is not used. Other styles that can hide text, and
that the code cannot work out, keep the element in place and set `unsure`. In
these cases the report says so. `HiddenReport` has `Suspected` (a style that can
hide text could not be worked out) and `Dropped` (how many parts with text were
left out). They become `HiddenContentSuspected` and `HiddenParts` on
`mail.Message`, the tool result fields `hidden_content_suspected` and
`hidden_parts_dropped`, and the second line of the text copy
(`hiddenContentNotice`). The manual states what this means for the person: see
[Known limitations](/manual/reference/known-limitations/).

**What the text looks like.** A block element becomes a line break, a list item
starts with `- `, and table cells are kept apart by a space. A link is written
as `text (url)`. The address is parsed and written with spaces, round brackets
and other unsafe characters as `%XX` (a percent sign and two hexadecimal
digits), so that it is one word. It is shown only for `http`, `https` and
`mailto`, only if it is at most 2,048 characters (`maxShownURL`), and only if
the link can be seen. An address with a part over its limit is cut, and
`...[cut]` marks the place: the user name and password keep at most 32
characters, the host keeps the last 64, the path keeps 64, and the query keeps
32. Each part of an address is a place where text can hide, and a person rarely
    reads it.

**HTML as the result.** The `html` body format uses `ReadableHTML`, and outgoing
mail uses `outgoingHTML`. Both are in {{< src "internal/mime/visible.go" >}} and
run `prunedHTML`. It applies the same rules (`prune` uses the same `visibility`
as the walk of `HTMLToText`), renders what is left, and cleans it with the
policy of `SanitizeHTML` (built on `bluemonday`, a library that cleans HTML).
The result has no `img`, `script` or `style` element and no `style` attribute.
The one attribute that stays is `href`, and a link that cannot be seen loses it.

## Limits

The size of an HTML document does not bound the work of the parser or the size
of the output. These limits do. The sizes use the exact unit: a KiB (kibibyte)
is 1,024 bytes.

| Name | Value | What it bounds |
|---|---|---|
| `maxHTMLBytes` (`fetch.go`) | 4 MiB | An HTML part. A larger part is not fetched or converted. |
| `maxHTMLCost` (`html.go`) | 16,000,000 | The work of the parser. A node costs 16 (`xhtml.NodeCost`), and an attribute costs 16 (`xhtml.AttrCost`) and its bytes. |
| `maxTagAttrs` (`xhtml/parse.go`) | 1,000 | Different attributes in one tag |
| Nesting depth (`xhtml/parse.go`) | 512 elements | The parser, as upstream, refuses a deeper document. |
| `maxConvertedTextBytes` (`html.go`) | 16 MiB | The text that `HTMLToText` makes |
| `maxVisibleHTMLBytes` (`visible.go`) | 16 MiB | The HTML that `VisibleHTML` writes, before and after the cleaning |
| `maxRuleWork` (`css.go`) | 64 MiB | Bytes of names compared to match the compound rules of one document. At the limit the other compound rules are left out, and the report warns. |
| `maxRuleDepth`, `maxMediaDepth` (`css.go`) | 8, 4 | Levels of at-rules and rules in rules, and brackets in a media query |
| `maxTextFetchBytes` (`fetch.go`) | 25 MiB | The bytes of a plain text part that are fetched |
| `maxShownParts`, `maxOtherVersions` (`fetch.go`) | 16, 4 | Text parts read for a body, and other versions compared with it |
| `defaultBodyChars`, `maxBodyChars` (`fetch.go`) | 20,000, 100,000 | The characters in a page of a body |
| `referencesFetchBytes` (`fetch.go`) | 64 KiB | The `References` header that is read |
| `snippetFetchBytes`, `snippetRunes` (`summary.go`) | 2,048 bytes, 200 characters | The text/plain part fetched for a snippet, and the snippet |
| `maxSnippetHTMLBytes`, `snippetHTMLBudget` (`summary.go`) | 1 MiB, 8 MiB | The largest HTML part that a snippet is made from, and the HTML bytes that one list fetches for snippets |
| `maxListedAddresses`, `maxSnippetRunes`, `maxMediaNameBytes` (`shape.go`) | 50, 200, 127 | Addresses of one header in a result, a snippet, and the type or subtype of a content type |
| `maxShownURL` (`html.go`) | 2,048 characters | A link address in the text. A longer one is not shown. |
| `maxShortUser`, `maxShortHost`, `maxShortPath`, `maxShortQuery` (`html.go`) | 32, 64, 64, 32 | The parts of a link address that is cut |
| `missingWordShare`, `maxMissingWords` (`compare.go`) | 1 word in 20, 8 | Words that one version may have that the other lacks |
| `maxParts`, `maxDepth` (`decode.go`) | 10,000, 32 | Parts and multipart nesting that `ParseMessage` follows |
| `maxHeaderBytes`, `maxHeaderFields` (`decode.go`) | 1 MiB, 10,000 | One header block |
| `maxBoundary`, `qpBufSize` (`decode.go`) | 200, 256 KiB | A multipart boundary, and a line of quoted-printable text |
| `multipartBufSize` (`multipart.go`) | 16 KiB | The read buffer of a multipart body |
| `MaxAttachmentBytes` (`body.go`) | 10 MiB | The attachment data of one message |
| `MaxMediaTypeBytes` (`body.go`) | 100 bytes | The media type of an attachment |
| `MaxAddressBytes`, `maxLocalPartBytes`, `maxLabelBytes` (`address.go`) | 254, 64, 63 bytes | An address, its part before the `@`, and a part of its domain |

The other constants of `fetch.go` (`tailRunes`, `plainBytesPerRune` and
`fetchSlack`) are estimates for the size of a fetch. They are not limits.

An HTML part that is too large or too complex is left out of the body, and the
result says so (`text_left_out`). When no part can be read, the error wraps
`mail.ErrTooLarge`. It advises the agent to ask for `body_format` "none", which
still returns the headers and the attachment list.

## The copied parser

`internal/thirdparty/xhtml` is a copy of the HTML parser of `golang.org/x/net`,
with a limit on its work. It is the only third-party code that lives in the
repository. Only `mime` imports it. The sanitizer (`bluemonday`) uses the
upstream package and builds no tree, so it is not changed.

**Why a size limit is not enough.** The size of a document does not bound the
work of the parser. The tree builder keeps a list of the formatting elements
that are open (`a`, `b`, `i`, `font` and so on), and it copies each of them into
the tree again for each piece of text. So the number of nodes can grow much
faster than the size of the document. The code after the parser has the same
problem: it reads the attributes of each copy, and `Render` writes them for each
copy. A check on the tokens cannot follow all the rules of the tree builder, so
the parser counts its own work and stops.

**What the patch does.** `ParseWithLimit(r, maxCost, ...)` stops with
`ErrTooComplex` at the first step after which the cost is over `maxCost`. The
cost goes up by `NodeCost` for each node that the parser makes, copies included,
and by `AttrCost` plus the bytes for each attribute of such a node. A tag with
more than 1,000 different attributes stops the tokenizer (the first step of the
parser, which cuts the text into tags and words) (`maxTagAttrs`). `mime` wraps
the error in `ErrHTMLTooComplex`, and gives no text. Every changed line has a
comment that starts with `// email-mcp:`. A test checks that the cost is exactly
the cost of the tree (`TestCostIsTheCostOfTheTree`), and another checks that the
trees are the same as the trees of upstream (`TestTreesAreTheSameAsUpstream`).

**What the folder holds.** The files `const.go`, `doctype.go`, `entity.go`,
`escape.go`, `foreign.go`, `node.go`, `nodetype_string.go`, `parse.go`,
`render.go` and `token.go` are copies. Only `parse.go` and `token.go` have more
than the change of the package name. The project added `limit_test.go`,
`race_on_test.go`, `race_off_test.go`, `staticcheck.conf` and the
{{< src "internal/thirdparty/xhtml/README.md" >}}. `LICENSE` and `PATENTS` are
the files of the upstream module.

**How to update it.** The README has the steps. In short:

1. Change the version of `golang.org/x/net` in `go.mod`.
2. Copy the files from the module cache (the folder where Go keeps downloaded
   modules; `go env GOMODCACHE` prints it) over the files here, and check the
   difference with `diff`.
3. Rename the package, and apply the patch to the copied `parse.go` and
   `token.go`. Search `parse.go` for every place that makes a node or adds
   attributes. A place that is not counted makes the test of step 4 fail.
4. Run `go test -race ./internal/thirdparty/... ./internal/mime/`.
5. Change the version in the README.

## Building mail

`Build` in {{< src "internal/mime/build.go" >}} writes a message, a reply, a
reply to all, or a forward, in the form of RFC 5322 (the internet message
format). {{< src "internal/mime/body.go" >}} has the bodies and the attachments,
{{< src "internal/mime/recipients.go" >}} the recipients, and
{{< src "internal/mime/address.go" >}} the rules for addresses. What goes out is
what a preview can show.

**What `Build` refuses.** A refusal returns an error that names the field. It
holds no address, subject or other text of the message.

| Error | The case |
|---|---|
| `ErrInvalidAddress` | An address is missing, cannot be parsed, or cannot be sent as it is: a quoted local part such as `"a b"@example.com`, a domain literal such as `bob@[1.2.3.4]`, and any address with a space, a quote or one of `<>()[],;:\` |
| `ErrNonASCIILocalPart` | A recipient that the caller typed has characters outside ASCII (the basic set of characters: English letters, digits and common signs) before the `@`. It matches `ErrInvalidAddress`. |
| `ErrNoRecipients` | The message goes to nobody. A draft may have none. |
| `ErrHeaderInjection` | A value that goes into a header has a CR (carriage return), an LF (line feed) or a NUL (the zero byte). A line break would let the value add a header of its own, such as a Bcc. |
| `ErrHiddenText` | The subject, the plain text body or a display name has a character that `CleanUntrusted` would remove. |
| `ErrEmptyHTML` | Nothing is left of the HTML body after the parts that a reader cannot see, and those that cannot go out, are removed. |
| `ErrInvalidFilename` | The file name of an attachment has a character that `CleanUntrusted` would remove, or a line or paragraph separator. |
| `ErrInvalidContentType` | The content type of an attachment cannot be written, is multipart, has a charset other than UTF-8 or US-ASCII, or has a media type over 100 bytes. |
| `ErrNoOriginal`, `ErrReplyAndForward` | A reply or a forward has no original message, or one request asks for both. |
| `mail.ErrTooLarge` | The attachments of the request and the forwarded ones together are over 10 MiB (`MaxAttachmentBytes`). |
| `ErrHTMLTooComplex` | The HTML body is too complex to clean or to make the text of. |

A hidden character in the subject, the text or a name is refused and not
cleaned, because the caller must know that its text is not sent. The values that
come from the original message are another case: a third party wrote them, and
they must not stop a reply. So they are cleaned. Each run of CR, LF and NUL in
the subject and in the names becomes one space, and `CleanUntrusted` runs on
them. A Message-ID or a `References` entry with a line break is not used. An
address of the original with a line break cannot be cleaned, because that would
change the address, so `Build` refuses it.

**Recipients.** A reply goes to the `Reply-To` addresses of the original, or to
its sender. A reply to all also copies the To and Cc addresses of the original
to Cc. Then the To, Cc and Bcc addresses of the request are added. For a reply
to all, the addresses of the user (`BuildContext.Self`) are removed from To and
Cc after that. Each address stays once, and the first one wins, in the order To,
Cc, Bcc. All of them are in `RawMessage.Recipients`. The message has no `Bcc`
header, so nobody learns who else got a copy. A draft is the exception:
`BuildContext.ForDraft` keeps the header, and `FromDraft` removes every `Bcc`
field, with its continuation lines, before the send.

**Subject and threading.** When the request has no subject, a reply gets "Re: "
before the subject of the original, unless that starts with "re:" in any case,
and a forward gets "Fwd: ". A reply gets `In-Reply-To` and `References` headers
from the original. `Build` does not quote the text of the original in a reply. A
forward puts the text of the original under a short header, "Forwarded message".
With an HTML body, the HTML form is the same, with every value escaped and the
original text in a quoted block. The HTML of the original is never copied. A
forward copies the attachments of the original only when
`IncludeOriginalAttachments` is set. Their file names and types are cleaned, and
a media type over 100 bytes goes out as `application/octet-stream`.

**HTML in outgoing mail.** `outgoingHTML` is `VisibleHTML` with one more rule: a
link keeps its `href` only when the text shows the whole address. A link whose
address the text cuts or does not show goes out as plain text. The result has
nothing that a preview cannot show: no comment, no style, no image, no attribute
but `href`. A message has one text: when the request has an HTML body, its plain
text body is not used, and the text part is made from the HTML.

**Addresses that look like others.** `ShownAddress` writes the ASCII form
(punycode, the `xn--` spelling) of a domain outside ASCII next to the address,
so that a look-alike domain shows. `AddressWarning` names each part of a domain
that mixes writing systems, using the sets of Unicode TS 39 (the Unicode
technical standard on security) at the level "highly restrictive".
`HasExactASCIIDomain` is false for a domain that has no exact ASCII form, and
the send refuses it. `HasNonASCIIAddress` makes the service ask the person
before it sends to an address outside ASCII. [The send
flow](/tech/inside-the-server/send-flow/) shows where.

**The shape of the message.** One `text/plain` part; or a
`multipart/alternative` with the text part first and the HTML part last, because
mail programs use the last part that they can show; and a `multipart/mixed`
around it when there are attachments. `Build` gives the message its own
`Message-ID`: 16 random bytes in hex, an `@`, and the domain of the sender. Of
the content type of an attachment, only the media type and a UTF-8 or US-ASCII
charset go out.

## The text part is what the preview shows

For a message made from HTML, `composeBodies` makes the text part with
`HTMLToText` of the cleaned HTML, and `Build` writes the text part first. The
service reads the built message again and shows the person the start of its text
part. So the text that the person sees is the text of what goes out. For a draft
that a person saved, the preview shows the parts that `LayoutOf` picks. [The
send flow](/tech/inside-the-server/send-flow/#what-the-person-sees) explains the
preview and how the answer of the person is tied to the message.

## Change the hidden-text rules

1. **Change one place.** `HTMLToText` and `VisibleHTML` share the type
   `visibility` in `html.go`: `classify` says whether an element is shown, and
   `textShown` says whether a text node is shown. The rules for styles are in
   `parseStyle` and `styleRules` in `css.go`. A rule that is added to `classify`
   or `parseStyle` reaches both results. A rule that is added to one of the two
   walks does not.
2. **Keep the two results the same.**
   `TestVisibleHTMLNeverShowsWhatHTMLToTextHides` checks that the text of the
   HTML result is the text of `HTMLToText`.
3. **Update the comment of `HTMLToText`.** It is the list of the rules, and this
   page links to it.
4. **Add a case to `hiddenPayloads`** in `hidden_test.go`. Each case has a word
   in a place that a reader does not see. If the code works the case out, the
   word must be gone. If it does not, the report must say so.
5. **Keep the answer on the safe side.** A style that you cannot work out sets
   `unsure`, and the report says so. Do not show the element with no sign.
6. **Keep the work bounded.** `walk` is a loop. `prune` calls itself, and the
   depth limit of the parser (512) bounds it. Code that adds nodes to the tree
   must charge for them in `xhtml` (see the README). A loop over the rules needs
   a limit like `maxRuleWork`.
7. **Run the tests.** `go test -race ./internal/mime/ ./internal/thirdparty/...`
   runs the unit tests and the seeds of the fuzz tests. To search for more
   cases, run one fuzz test:
   `go test -run '^$' -fuzz FuzzHTMLToText ./internal/mime/`. The flag
   `-run '^$'` runs no normal test, and `-fuzz` names the fuzz test. There are
   also `FuzzVisibleHTML` and `FuzzCleanUntrusted`.

## Related pages

- [The send flow](/tech/inside-the-server/send-flow/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [The mail backend](/tech/mail/mail-backend/)
- [Map of packages](/tech/start-here/package-map/)
- [What the agent can do](/manual/daily-use/what-the-agent-can-do/) in the user
  manual
