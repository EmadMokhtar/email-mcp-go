---
title: Mail content
description: How email-mcp cleans text from third parties before an agent reads it, how HTML becomes text, what limits stop a hostile message from using all the memory, and why the repository holds a copy of an HTML parser.
weight: 30
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
  - internal/thirdparty/xhtml/
  - internal/provider/imapsmtp/fetch.go
  - internal/provider/imapsmtp/summary.go
  - internal/mcpserver/shape.go
---

This page explains how email-mcp cleans the text of mail before an agent reads
it, how HTML becomes text, which limits stop a hostile message from using all
the memory, and why the repository holds a copy of an HTML parser. Read it
before you change a rule for hidden text, a limit on HTML, or the copied parser.
The mail that an agent writes is on the page [Building
mail](/tech/mail/building-mail/).

A third party is a person who wrote text that email-mcp passes on and who is not
the user: the sender of a mail. Text from a third party is untrusted, and the
tool results mark it. It can hold prompt injection: words that try to give
instructions to the agent, often hidden from the person who reads the mail. The
package `mime` handles this. MIME (Multipurpose Internet Mail Extensions) is the
format that gives a mail its parts, such as the text, the HTML and the
attachments. HTML (HyperText Markup Language) is the language of web pages and
of many mails.

Cleaning does not make the text trusted. The marker is text that the agent
reads. It does not force the agent. An email can contain instructions, and an
agent may follow them. This is why a send waits for your yes by default (see
[Untrusted
content](/manual/daily-use/what-the-agent-can-do/#untrusted-content)).

## Two pipelines

{{< diagram "mail-pipelines" >}}

**Incoming mail**, from the server to the agent. `GetMessage` in
{{< src "internal/provider/imapsmtp/fetch.go" >}} does steps 1 to 5.

1. **Fetch.** The backend reads the structure of the message. It fetches the
   text parts that `mime.LayoutOf` picks (see "Which parts are the body") with
   `BODY.PEEK`, the way that IMAP (Internet Message Access Protocol, the
   protocol for reading mail) reads a message without marking it as read. An
   HTML part is fetched whole, because a style rule at the end of a document can
   hide an element at the start. A part over 4 MiB (`maxHTMLBytes`) is not
   fetched or converted. A MiB (mebibyte) is 1,048,576 bytes.
2. **Decode.** `mime.DecodeText` undoes the transfer encoding (the way that a
   part writes its bytes as plain text: base64 or quoted-printable) and the
   charset (the table that maps bytes to characters). A byte that is not valid
   UTF-8 (a way to write each character as one to four bytes) becomes U+FFFD,
   the replacement character. A damaged part gives the text up to the damage,
   and no error.
3. **Parse.** `ReadableText` (the `text` body format) and `ReadableHTML` (the
   `html` format) start with `parseHTML`, which calls the copied parser with a
   work budget. A document that needs more work gives `ErrHTMLTooComplex` and no
   text.
4. **Hidden-text rules.** A walk over the tree leaves out the text that the
   rules find hidden, and makes a report of what it left out.
5. **Clean.** `CleanUntrusted` removes hidden characters from the text. The
   backend cuts the body into pages.
6. **Wrap as untrusted.** `messageOut` in
   {{< src "internal/mcpserver/shape.go" >}} cleans each string again and puts
   the text that a sender wrote inside the `Untrusted` wrapper. The text copy of
   the result starts with a fixed banner line. [One tool call, step by
   step](/tech/start-here/tool-call/#where-text-from-the-mail-is-cleaned)
   explains the wrapper.

**Outgoing mail**, from the agent to the server. `Service.Send` and
`CreateDraft` call `mime.Build`. [Building mail](/tech/mail/building-mail/)
explains the steps.

1. **Compose.** The agent gives a `mail.Compose`: the recipients, a subject, a
   text or an HTML body, a reply or a forward, and attachments as bytes.
2. **Check.** `Build` refuses an address that cannot be sent and a value with a
   line break.
3. **Refuse hidden characters.** The subject, the text body and the display
   names are refused, not cleaned, when `CleanUntrusted` would remove a
   character.
4. **Clean the HTML, make the text.** `outgoingHTML` cleans the HTML, and
   `HTMLToText` makes the text part from the result.
5. **Write the message.** The text part, the HTML part and the attachments.
6. **Preview.** `service` shows the start of the text part to the person.

Where each part is:

| File | What it holds |
|---|---|
| {{< src "internal/mime/untrusted.go" >}} | `CleanUntrusted`, `HiddenRunes`, `CleanAndCount`: the removal of hidden characters |
| {{< src "internal/mime/html.go" >}} | `HTMLToText`, `ReadableText`, `HiddenReport`, the walk over the tree, the links, the work budget, and `SanitizeHTML` |
| {{< src "internal/mime/visible.go" >}} | `VisibleHTML`, `ReadableHTML` and `outgoingHTML`: the same rules, with HTML as the result |
| {{< src "internal/mime/css.go" >}} | A small reader of CSS (Cascading Style Sheets) that finds the elements a mail client hides |
| {{< src "internal/mime/parts.go" >}} | `LayoutOf`: which parts are the body, and which are other versions |
| {{< src "internal/mime/decode.go" >}}, {{< src "internal/mime/multipart.go" >}} and {{< src "internal/mime/params.go" >}} | The decoders of text and transfer encodings, `ParseMessage`, the readers of header blocks and multipart bodies, and the names and parameters in the forms of RFC 2231 and RFC 2047 (RFC: a numbered document that defines an internet standard) |
| {{< src "internal/mime/compare.go" >}} | `TextsDiffer`: do two versions of a body say the same? |
| {{< src "internal/provider/imapsmtp/summary.go" >}} | The summary and the snippet of each message in a list |
| {{< src "internal/thirdparty/xhtml/" >}} | The copied HTML parser |

The files that build mail (`build.go`, `body.go`, `recipients.go`, `draft.go`
and `address.go`) are explained on [Building
mail](/tech/mail/building-mail/).

## What CleanUntrusted removes

`CleanUntrusted` removes the characters of third-party text that a reader cannot
see. A character that nobody sees can carry data, so the function removes the
characters that draw nothing. It works in this order, and the result is always
valid UTF-8:

| Step | What it does |
|---|---|
| Bytes | Each run of bytes that is not valid UTF-8 becomes U+FFFD, which a reader sees. |
| Line breaks | `\r\n` and a lone `\r` become `\n`. |
| Invisible characters | Every character of the Unicode category Cf (format characters) goes, such as the zero width space, the direction marks and the byte order mark. So do other characters that draw nothing, such as the tag block, the variation selectors U+FE00 to U+FE0E, the private use areas and the Hangul fillers. The exceptions are the joiners below and the few format characters that a reader sees, such as the Arabic number signs. |
| Control characters | Category Cc goes, except `\n` and `\t`. |
| Joiners | U+200C (zero width non-joiner), U+200D (zero width joiner) and U+FE0F (variation selector 16, which asks for an emoji, a small picture that is a character) go unless they change what a reader sees. |

**Why the joiners stay in some places.** In Persian, Arabic and the Indic
scripts, a joiner changes the shape of the letters. In an emoji sequence, a
joiner makes one picture of several. So `keepsJoiner` keeps a joiner next to a
letter of a script that joins letters, and inside an emoji sequence. Between
Latin letters, for example, a joiner changes nothing on the screen, and text can
hide data in it, so it goes. A run of joiners is cut to what a word or an emoji
needs. Cleaning the result again changes nothing.

## Which parts are the body

`mime.LayoutOf` in {{< src "internal/mime/parts.go" >}} is the one rule for what
a mail client shows. The read path and the preview of a draft use it, so an
agent reads what a typical mail client shows. Only `text/plain` and `text/html`
parts can be the body. In a `multipart/alternative`, a client shows the last
part that it can show, and the parts before it are other versions. In a
`multipart/related`, it shows the root part. In another multipart, it shows
every part, in order. A part with the disposition `attachment` is never the
body.

The backend reads at most 16 parts for a body (`maxShownParts`). It compares the
body with up to 4 other versions (`maxOtherVersions`). With more than 4, the
result says that the versions differ. The result also says when it left text out
(`text_left_out`) and when another version of the body says something else
(`alternatives_differ`). `TextsDiffer` compares the words of the two versions,
not their text. It gives a hint to the reader. It is not a check that stops a
message.

## How HTML becomes text

`HTMLToText` turns HTML into plain text. It keeps what a person sees in a mail
client and leaves out the text that the rules find hidden, because a sender can
use the hidden parts to give instructions to an agent. The rules are in
{{< src "internal/mime/html.go" >}} and {{< src "internal/mime/css.go" >}}. This
page gives the groups. email-mcp cannot always tell what a reader sees. A mail
can hold hidden text even when `get_message` does not return
`hidden_content_suspected: true`. When it matters, open the mail yourself.

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
- **Comments.** HTML comments.

**The principle: a hiding rule on an element wins.** A style can come from the
`style` attribute and from the rules for the name, the classes and the id of the
element. If any of them hides the element, it is hidden. The order, the strength
of the source and `!important` do not matter. This is stricter than a browser,
which picks one source. Inside one source, `display`, `visibility`, `opacity`
and `font-size` work as in a browser: the last declaration counts.

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
the link can be seen. A part of an address that is too long is cut, and
`...[cut]` marks the place, because text can hide in an address and a person
rarely reads it. The manual has the numbers (see [Known
limitations](/manual/reference/known-limitations/)).

**HTML as the result.** The `html` body format uses `ReadableHTML`, and outgoing
mail uses `outgoingHTML`. Both are in {{< src "internal/mime/visible.go" >}} and
run `prunedHTML`. It applies the same rules (`prune` uses the same `visibility`
as the walk of `HTMLToText`), renders what is left, and cleans it with the
policy of `SanitizeHTML` (built on `bluemonday`, a library that cleans HTML).
The result has no `img`, `script` or `style` element and no `style` attribute.
The attributes that stay are `href` and the fixed `rel="nofollow"`, and a link
that cannot be seen loses its `href`.

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
| `maxParts`, `maxDepth` (`decode.go`) | 10,000, 32 | Parts and multipart nesting that `ParseMessage` follows |
| `maxHeaderBytes`, `maxHeaderFields` (`decode.go`) | 1 MiB, 10,000 | One header block |
| `maxBoundary`, `qpBufSize` (`decode.go`) | 200, 256 KiB | A multipart boundary, and a line of quoted-printable text |
| `multipartBufSize` (`multipart.go`) | 16 KiB | The read buffer of a multipart body |

An HTML part that is too large or too complex is left out of the body, and the
result says so (`text_left_out`). When no part can be read, the error wraps
`mail.ErrTooLarge` and advises the agent to ask for `body_format` "none", which
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
faster than the size of the document. The code after the parser reads and
writes the attributes of each copy too. A check on the tokens cannot follow all
the rules of the tree builder, so the parser counts its own work and stops.

**What the patch does.** `ParseWithLimit(r, maxCost, ...)` stops with
`ErrTooComplex` at the first step after which the cost is over `maxCost`. The
cost goes up by `NodeCost` for each node that the parser makes, copies included,
and by `AttrCost` plus the bytes for each attribute of such a node. A tag with
more than 1,000 different attributes stops the tokenizer (the first step of the
parser, which cuts the text into tags and words) (`maxTagAttrs`). `mime` wraps
the error in `ErrHTMLTooComplex`, and gives no text. Every changed line has a
comment that starts with `// email-mcp:`. A test checks the cost against the
tree (`TestCostIsTheCostOfTheTree`), and another checks that the trees are the
same as the trees of upstream (`TestTreesAreTheSameAsUpstream`).

**How to update it.** The {{< src "internal/thirdparty/xhtml/README.md" >}} has
the steps. In short:

1. Change the version of `golang.org/x/net` in `go.mod`.
2. Copy the files from the module cache (the folder where Go keeps downloaded
   modules; `go env GOMODCACHE` prints it) over the files here, and check the
   difference with `diff`.
3. Rename the package, and apply the patch to the copied `parse.go` and
   `token.go`. Search `parse.go` for every place that makes a node or adds
   attributes. A place that is not counted makes the test of step 4 fail.
4. Run `go test -race ./internal/thirdparty/... ./internal/mime/`.
5. Change the version in the README.

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
3. **Update the comment of `HTMLToText`** so that it matches the rules.
4. **Add a test case** to the hidden-text tests in `hidden_test.go`. Each case
   has a word in a place that a reader does not see. If the code works the case
   out, the word must be gone. If it does not, the report must say so.
5. **Report what you cannot work out.** A style that you cannot work out sets
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

- [Building mail](/tech/mail/building-mail/)
- [One tool call, step by step](/tech/start-here/tool-call/)
- [The mail backend](/tech/mail/mail-backend/)
- [What the agent can do](/manual/daily-use/what-the-agent-can-do/) in the user
  manual
- [Known limitations](/manual/reference/known-limitations/) in the user manual
