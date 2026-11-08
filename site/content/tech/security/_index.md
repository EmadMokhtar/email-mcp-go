---
title: Security
description: The rules that keep the agent inside its limits, where the code enforces them, and the tests that check them.
weight: 50
code_areas:
  - internal/e2e/
  - internal/signing/
---

This group explains the security design of email-mcp: the two channels into
the use cases, the main rules of the design, where the code enforces
each one, and which test fails when one breaks. Read it before you change code
that checks a caller, a permission, a confirmation, a secret or a connection.

- [Security design](/tech/security/security-design/): the model, the two
  channels, the table of rules and their tests, the signed values, and a
  checklist for a change to security code.
