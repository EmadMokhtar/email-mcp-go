# xhtml: the HTML parser of golang.org/x/net, with a limit on its work

This directory is a copy of the HTML parser of `golang.org/x/net/html`, with a
patch. It is the only third-party code that is copied into this repository.

- **Upstream:** module `golang.org/x/net`, directory `html`.
- **Version:** exactly `v0.62.0`, the version in `go.mod` (copied from the module
  cache: `env -u GOROOT go env GOMODCACHE`).
- **Licence:** `LICENSE` and `PATENTS` are the files at the root of the upstream
  module. The code is under the BSD licence of the Go authors.
- **Used by:** `internal/mime` (`HTMLToText` and `VisibleHTML`). Nothing else
  imports it. `bluemonday` (the HTML sanitizer) uses the upstream package
  `golang.org/x/net/html`, and it does not build a tree, so it is not changed.

## Why

The size of an HTML document does not bound the work of the parser, nor the
work of the code that reads the tree:

- The tree builder keeps a list of the formatting elements (a, b, i, font, ...)
  that are open. For each piece of text it copies every element of that list
  into the tree again ("reconstruct the active formatting elements"). A few
  tens of kilobytes that open 500 formatting elements with different attributes
  and then have many paragraphs make millions of nodes and gigabytes of memory.
  The same happens when the elements are opened inside a `<table>`: the parser
  puts them out of the table (foster parenting) and keeps them in its list.
- A copy of an element shares the strings of its attributes, so it is cheap
  for the parser. But the code after the parser reads the attributes of every
  copy (a link address, a style), and `Render` writes them for every copy. A
  link of 1 MiB copied 500 times is 500 MiB of output.
- A repeated `<html>` or `<body>` tag with attributes is merged into the
  element, and the merge goes over all the attributes that the element has
  (`copyAttributes`). 20,000 such tags (249 KB) are 200 million steps.
- The tokenizer keeps several copies of the attributes of one tag (about 300
  bytes for each) before the parser sees the tag.

An estimate from the tokens cannot follow all the rules of the tree builder, so
the parser itself counts its work and stops.

## What is here

Copied, with one change each (the package name, see below):

`const.go`, `doctype.go`, `entity.go`, `escape.go`, `foreign.go`, `node.go`,
`nodetype_string.go`, `parse.go`, `render.go`, `token.go`.

Not copied: `doc.go` (only the package comment), `iter.go` (iterators over
nodes, not used), the upstream tests and `testdata`, and the sub-packages. The
code still imports the upstream package `golang.org/x/net/html/atom`, which is
only data (the list of the known names), so `atom.Atom` is the same type here and
upstream.

Added (not from upstream): `limit_test.go`, `race_on_test.go`,
`race_off_test.go`, `staticcheck.conf` (turns the standalone staticcheck off for
this directory; `.golangci.yml` skips the directory too), and this file.

## What was changed

Every changed line has a comment that starts with `// email-mcp:`. A new
function, type, constant or variable has the comment above it, and that comment
covers its body. The package name line of every file is changed too.

1. **All files:** `package html` is now `package xhtml // email-mcp: ...`. This is
   the whole change in all files except `parse.go` and `token.go`.
2. **`parse.go`, the type `parser`:** new fields `cost`, `maxCost`, `limited`.
3. **`parse.go`, new code before `func (p *parser) top()`:**
   - `ErrTooComplex`, the error of the limit.
   - `NodeCost` and `AttrCost`, the units of the cost (exported, so that a
     caller can say what its limit means).
   - `maxTagAttrs` (1,000), the most attributes of one tag (see 11).
   - `costLimitReached`, a private type. The parser panics with a value of this
     type when the cost passes the limit.
   - `(*parser).charge` (adds work to the cost and checks the limit),
     `attrCost`, `nodeCost`, `(*parser).newNode` (counts a node with its
     attributes) and `(*parser).cloneNode` (`Node.clone()` that is counted before
     the copy is made).
4. **`parse.go`, every place that makes a node** (23 places; found with
   `grep -n "&Node{\|clone()" parse.go` and checked by the tests):
   - 18 literals `&Node{...}` are now `p.newNode(&Node{...})`: the text node of
     `addText` (outside a table), the element of `addElement`, the comments of the
     insertion modes (15 places, some added to `p.doc`, one to `p.oe[0]`), and the
     root element of `ParseFragmentWithOptions`.
   - The text node that `addText` makes in a table (foster parenting) is counted
     with `p.newNode(n)` after `fosterParent` has put it in the tree (see 5).
   - The doctype node (`parseDoctype`) is counted, with its attributes (the
     public and system identifiers), where `initialIM` adds it.
   - The three `clone()` calls are now `p.cloneNode(...)`: one in
     `reconstructActiveFormattingElements`, two in the adoption agency.
   - The document node is counted by the start value `cost: NodeCost` in the two
     `parser` literals (`ParseWithOptions`, `ParseFragmentWithOptions`).
5. **`parse.go`, `(*parser).fosterParent`:** it returns `bool`, true when the node
   is in the tree. A text node that is merged into the text before it is not in
   the tree and is not counted. (The callers ignore the result, except
   `addText`.)
6. **`parse.go`, `copyAttributes`:** it is a method of `parser` now (the two
   callers in `inBodyIM` call `p.copyAttributes`). Before it builds its map, it
   counts `AttrCost` for each attribute that the element has. It counts each
   attribute that it adds like the attribute of a new node.
7. **`parse.go`, `inBodyIM`, the `frameset` start tag:** not changed. The cost
   does not go down when the parser takes the body out of the tree: the
   formatting elements of the body can still be copied after it.
8. **`parse.go`, `(*parser).parse`:** the function turns every panic into an
   error. A panic of type `costLimitReached` now gives `ErrTooComplex` as it is.
9. **`parse.go`, new `func ParseWithLimit`**, after `ParseWithOptions`. It sets
   the limit and the most attributes of one tag for the tokenizer.
10. **`token.go`, the type `Tokenizer`:** new field `maxAttrs`.
11. **`token.go`, `(*Tokenizer).readTag`:** when `maxAttrs` is above 0 and a tag
    has more different attributes than that, it sets `z.err` to `ErrTooComplex`
    and stops reading the tag. The token is an `ErrorToken`, and the parser
    returns the error. Only `ParseWithLimit` sets `maxAttrs`, so the tokenizer and
    `ParseWithOptions` work as upstream.

`Node`, `Render` and the rest are as upstream.

## What the limit counts

`ParseWithLimit(r, maxCost, opts...)` stops with `ErrTooComplex` at the first
step after which the cost is over `maxCost`, before it does that step. The cost
is the sum of:

- `NodeCost` (16) for each node that the parser makes: elements, text, comments,
  the doctype, the document, and each copy of an element;
- `AttrCost` (16) plus the bytes of the namespace, key and value for each
  attribute of such a node: of the nodes made from the input, of the copies
  (a copy shares the strings, but the code after the parser reads them for each
  copy), and of the attributes that a repeated `<html>` or `<body>` tag adds;
- `AttrCost` for each attribute that the `html` or `body` element already has,
  each time a repeated `<html>` or `<body>` tag with attributes is merged into it.

The cost only goes up. The text in the nodes is not counted: the input bounds it.

A node takes 112 bytes of memory and an attribute 48 bytes plus its strings, so
a unit is a few bytes. With `internal/mime`'s limit of 16,000,000 the parser makes at
most one million nodes (about 110 MiB), and the attributes of the tree have at
most 16 MB. A marketing mail of 4 MiB costs about 6.9 million.

A tag with more than 1,000 different attributes stops the tokenizer with
`ErrTooComplex`, whatever the limit. The tokenizer keeps every attribute of a
tag, in several copies, before the parser can count the tag: a tag of 4 MiB with
half a million attributes would use about 160 MiB first. Real tags have fewer
than a hundred attributes. The same attribute repeated is kept once, and the
attributes of end tags are not kept, so they are not counted for this.

The tests check the cost against the tree: for 50 documents and
thousands of random ones, the cost is exactly the sum over the nodes and
attributes of the tree (plus the merges of repeated `<html>` and `<body>` tags,
and a frameset that took the body out, where the cost is higher).

## How to update

1. Change the version of `golang.org/x/net` in `go.mod` (`env -u GOROOT go get
   golang.org/x/net@<version>`, and `go mod tidy`).
2. Copy the files in the list above from `$(go env GOMODCACHE)/golang.org/x/net@<version>/html`
   (and `LICENSE` and `PATENTS` from the module root) over the files here. Check
   with `diff` against the old copy which upstream lines changed.
3. Rename the package: `sed -i 's/^package html$/package xhtml/'`. Add the
   `// email-mcp:` comment to the package line of each file.
4. Apply the patch of "What was changed" to the new `parse.go` and `token.go`.
   Search `parse.go` for every node that the parser makes, and for every place
   that adds attributes to a node:
   `grep -n "&Node{\|clone()\|new(Node)\|\.Attr = \|Attr = append" parse.go`.
   If upstream has a new place, it must be counted, or the test in step 5 fails.
5. Run `env -u GOROOT go test -race ./internal/thirdparty/... ./internal/mime/`.
   `TestCostIsTheCostOfTheTree` finds a place that is not counted.
   `TestTreesAreTheSameAsUpstream` checks that the trees are the same as the
   trees of upstream for the same documents.
6. Change the version in this file.
