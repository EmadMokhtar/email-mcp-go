---
title: Inside the server
description: How a send works, what each permission allows, where the data and the keys live, what the two logs hold, and how the program starts and stops.
weight: 20
code_areas:
  - internal/service/
  - internal/confirm/
  - internal/outbox/
  - internal/policy/
  - internal/store/
  - internal/secrets/
  - internal/audit/
  - internal/app/
---

These pages explain the parts of email-mcp that keep mail safe: the path of a
send, the permissions, the storage of data and secrets, the logs, and the order
in which the program starts and stops. Each page names the files that it covers.
Read them when you change one of these areas.

- [The send flow](/tech/inside-the-server/send-flow/): how a send goes from the
  agent to the mail server, with the question to the person, the outbox and the
  undo window.
- [Permissions](/tech/inside-the-server/permissions/): the levels, the token
  scopes and the limits, who changes them, and where the code checks them.
- [Storage and secrets](/tech/inside-the-server/storage-and-secrets/): where the
  data lives, which fields are encrypted, and how the keys work.
- [Activity log and program log](/tech/inside-the-server/audit-and-logging/):
  what the two logs hold and the rules for writing to them.
- [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/): the
  order of `Open`, `Run` and `Close`, and why a send is never lost and never
  sent twice.
