---
title: Tables with signs that HTML escapes
weight: 2
---

The name of a table is plain text in an attribute. A sign such as & or < must
be escaped once. If it is escaped twice, a screen reader says "amp;".

| Fish & Chips | a < b |
| ------------ | ----- |
| one          | two   |

A code span with quotes and a less-than sign.

| `say "hi"` | `a<b` |
| ---------- | ----- |
| one        | two   |

Bold text, slanted text and a link. The name has the words and no tags.

| **Bold** and _slanted_ | [link](https://example.com) |
| ---------------------- | --------------------------- |
| one                    | two                         |
