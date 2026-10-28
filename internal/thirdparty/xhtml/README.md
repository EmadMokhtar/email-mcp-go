# xhtml: the HTML parser of golang.org/x/net, with a limit on the nodes

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

The size of an HTML document does not bound the work of the parser. The tree
builder keeps a list of the formatting elements (a, b, i, font, ...) that are
open. For each piece of text it copies every element of that list into the tree
again ("reconstruct the active formatting elements"). A few tens of kilobytes
that open 500 formatting elements with different attributes and then have many
paragraphs make millions of nodes and gigabytes of memory. The same happens when
the elements are opened inside a `<table>`: the parser puts them out of the
table (foster parenting) and keeps them in its list. An estimate from the
tokens cannot follow all the rules of the tree builder, so the parser itself
counts what it makes and stops.

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

Every changed line has a comment that starts with `// email-mcp:` (the lines of a
new function have it on the comment above the function, and on the lines that
call it). The package name line of every file is changed too.

1. **All files:** `package html` is now `package xhtml // email-mcp: ...`. This is
   the whole change in all files except `parse.go`.
2. **`parse.go`, the type `parser`:** new fields `nodes`, `copiedAttrs`,
   `maxNodes`, `limited`.
3. **`parse.go`, new code before `func (p *parser) top()`:**
   - `ErrTooManyNodes`, the error of the limit.
   - `nodeLimitReached`, a private type. The parser panics with a value of this
     type when it passes the limit.
   - `(*parser).newNode`: counts a node and checks the limit. `cloneNode`:
     `Node.clone()` that also counts the attributes that it copies. `checkLimit`.
     `subtreeSize`.
4. **`parse.go`, every place that makes a node** (25 places; found with
   `grep -n "&Node{\|clone()" parse.go` and checked by the tests):
   - 18 literals `&Node{...}` are now `p.newNode(&Node{...})`: the text node of
     `addText` (outside a table), the element of `addElement`, the comments of the
     insertion modes (15 places, some added to `p.doc`, one to `p.oe[0]`), and the
     root element of `ParseFragmentWithOptions`.
   - The text node that `addText` makes in a table (foster parenting) is counted
     with `p.newNode(n)` after `fosterParent` has put it in the tree (see 5).
   - The doctype node (`parseDoctype`) is counted where `initialIM` adds it.
   - The three `clone()` calls are now `p.cloneNode(...)`: one in
     `reconstructActiveFormattingElements`, two in the adoption agency.
   - The document node is counted by the start value `nodes: 1` in the two
     `parser` literals (`ParseWithOptions`, `ParseFragmentWithOptions`).
5. **`parse.go`, `(*parser).fosterParent`:** it returns `bool`, true when the node
   is in the tree. A text node that is merged into the text before it is not in
   the tree and is not counted. (The callers ignore the result, except
   `addText`.)
6. **`parse.go`, `inBodyIM`, the `frameset` start tag:** the parser cuts the
   `body` element out of the tree. Its nodes are taken off the count
   (`p.nodes -= subtreeSize(body)`), so the count stays the number of nodes of
   the tree.
7. **`parse.go`, `(*parser).parse`:** the function turns every panic into an
   error. A panic of type `nodeLimitReached` now gives `ErrTooManyNodes` as it is.
8. **`parse.go`, new `func ParseWithLimit`**, after `ParseWithOptions`.

`Node`, `Render`, the tokenizer and the rest are as upstream.

## What the limit counts

`ParseWithLimit(r, maxNodes, opts...)` stops with `ErrTooManyNodes` at the first
step after which `nodes + copiedAttrs > maxNodes`.

- `nodes` is exactly the number of nodes in the tree, the document node
  included. A test checks this against the tree for about 50 documents and
  thousands of random ones (`TestNodeCountIsTheNumberOfNodesOfTheTree`).
- `copiedAttrs` is the number of attributes that clones copied. A clone of an
  element with 2,000 attributes costs as much memory as 2,000 nodes, and the
  parser can make a clone for each paragraph. The attributes of the nodes that
  the parser makes from the input are not counted: the input bounds them.
- The parser keeps at most `maxNodes` nodes. It does not make more before it
  stops, so its memory is bounded by the limit, and not by the size of the input.
- A limit of 0 or less stops at the first node.

## How to update

1. Change the version of `golang.org/x/net` in `go.mod` (`env -u GOROOT go get
   golang.org/x/net@<version>`, and `go mod tidy`).
2. Copy the files in the list above from `$(go env GOMODCACHE)/golang.org/x/net@<version>/html`
   (and `LICENSE` and `PATENTS` from the module root) over the files here. Check
   with `diff` against the old copy which upstream lines changed.
3. Rename the package: `sed -i 's/^package html$/package xhtml/'`. Add the
   `// email-mcp:` comment to the package line of each file.
4. Apply the patch of "What was changed" to the new `parse.go`. Search it for
   every node that the parser makes: `grep -n "&Node{\|clone()\|new(Node)" parse.go`.
   If upstream has a new place, it must be counted, or the test in step 5 fails.
5. Run `env -u GOROOT go test -race ./internal/thirdparty/... ./internal/mime/`.
   `TestNodeCountIsTheNumberOfNodesOfTheTree` finds a place that is not counted.
   `TestTreesAreTheSameAsUpstream` checks that the trees are the same as the
   trees of upstream for the same documents.
6. Change the version in this file.
