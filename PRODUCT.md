# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

One person, on their own computer, who lets an AI agent look after their email.
They use the portal for quick decisions: the agent gives them a link, they check
one action, decide, and leave. Setup (connecting accounts, setting permissions)
happens rarely. Hosted mode for many people is planned (M3), not built.

## Product Purpose

email-mcp is the bridge between an AI agent and a person's mail accounts. The web
portal is the part only the human uses: it connects accounts, sets how much the
agent may do, and answers the actions that wait for a person. Success means a
person can see exactly what the agent wants to do, decide with confidence, and
know for certain what happened.

## Positioning

The agent can get a link to a page but can never act on it. Only a browser that
the person signed in with `email-mcp setup` can approve a send or change a
permission. The portal is where a human decision is made and recorded.

## Operating Context

- The portal runs at `email-mcp.localhost`, on a random port. It is local to the
  computer. A sign-in lasts at most 24 hours, and 2 hours without use.
- A visit usually starts from a link in the chat with the agent.
- Approving a send can wait on the mail server: the default undo window is 0
  seconds, so the send happens inside the click.

## Capabilities and Constraints

- Pages: Accounts, Connect account, Account settings, Reconnect, Approvals, one
  Approval, Activity, Connections.
- Pages are server-rendered Go templates embedded in the binary.
- Everything stays inside the binary: no web fonts, no CDN, nothing from other
  sites.
- No inline script or inline style. The Content Security Policy allows only
  scripts and styles served by email-mcp itself.
- A page must work when the script does not run. Every action is a plain form
  post with a CSRF token.
- Text from a message is untrusted. It is shown as text and never as HTML.
- A page shows everything a person needs before they approve. A partial view
  cannot be approved.
- The user can change labels, but the exact wording of the safety warnings stays
  (full autonomy risk, certificate warnings, no encryption warning).
- The portal follows the light or dark setting of the computer.

## Brand Commitments

- The name is `email-mcp`. There is no logo or other brand asset.

## Evidence on Hand

- The user manual in `site/content/manual/` and
  `docs/superpowers/specs/2026-10-27-email-mcp-rewrite-design.md`.
- No customers, testimonials or benchmarks exist. Do not invent any.

## Product Principles

1. What waits for the person comes first. Setup comes second.
2. Show the real action, in full, before asking for a decision.
3. Every click answers at once, and the final result is the truth: a failed send
   never looks like a success.
4. Risk is clear and never hidden, but only real risk looks alarming.
5. The person stays in control: the agent asks, the person decides.
