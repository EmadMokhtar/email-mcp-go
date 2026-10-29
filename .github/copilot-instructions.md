# GitHub Copilot — Repository instruction file

## Purpose

Repository-specific guidance for GitHub Copilot and Copilot Chat, so
suggestions match this project's language, layout and tooling.

## About this project

This repository is **email-mcp-go**, a Model Context Protocol (MCP) server
written in **Go** (see `.go-version` and the `go` directive in `go.mod` for the
version in use). It gives MCP clients such as Claude Code access to one or more
email accounts over IMAP and SMTP. It runs on the user's own computer, and the
user sets per account what the agent may do.

Layout:

```
cmd/email-mcp/                  main and the subcommands (serve, setup, account, key, ...)
internal/app/                   wires the parts together: open, run, stop; master key loading
internal/mcpserver/             MCP tool registration (official go-sdk), error texts
internal/httpserve/             MCP over local (loopback) Streamable HTTP
internal/service/               use cases; enforces policy before any backend call
internal/policy/                permission levels, confirmation decisions, rate limits
internal/confirm/               actions that wait for the user's confirmation
internal/outbox/                queued sends with an undo window; sends at most once
internal/audit/                 the audit log
internal/domain/{account,mail}/ provider-neutral types and interfaces
internal/provider/imapsmtp/     IMAP + SMTP backend
internal/mime/                  MIME build/parse, HTML-to-text, untrusted-text cleaning
internal/discovery/             mail server autoconfig
internal/connect/               account connection texts shared by the web UI and the CLI
internal/store/                 SQLite; every per-tenant query takes a tenant ID
internal/secrets/               envelope encryption, master-key providers
internal/signing/, internal/tokens/  signed page tokens; HTTP access tokens
internal/web/                   local web UI (setup, settings, approvals, activity)
internal/inflight/              counts running work so a stop can wait for it
internal/errtype/               Go type names of an error chain, for logs
internal/version/               the version string
internal/e2e/                   end-to-end security tests of the whole program
internal/testutil/              fake IMAP and SMTP servers for tests
internal/thirdparty/xhtml/      the x/net HTML parser, patched with a work limit (see its README)
test/interop/                   interop tests against real Dovecot and Postfix (Docker)
```

Calendar and contacts (CalDAV and CardDAV), Google and Microsoft sign-in, and
hosted mode come in later milestones, with their own packages.

## Style

- Format with `gofmt`; the build treats formatting as non-negotiable.
- Return errors, do not panic. Wrap with `fmt.Errorf("...: %w", err)` so the
  cause survives. Never discard an error to make a signature simpler.
- Keep exported identifiers documented with a comment starting with the name.
- Comments explain *why*, not what. The code already shows what.
- Table-driven tests with `github.com/stretchr/testify/require`.

## Project-specific rules

- Message IDs in the tool API are IMAP **UIDs**, never sequence numbers.
  Sequence numbers shift when a message is expunged.
- Address messages with UID sets, never sequence sets, for every IMAP
  operation.
- Fetch with `BODY.PEEK` unless the intent really is to mark mail as read.
- Never log email content or secrets: no bodies, subjects, display names,
  email addresses, attachments or tokens. Log metadata such as folder, UID
  and counts.

## Commands

- `make test` — all tests under the race detector. Run before proposing
  changes.
- `make lint` — `go vet`, staticcheck and golangci-lint (needs golangci-lint
  v2 installed).
- `make vuln` — govulncheck.
- `make build` — build the binary into `bin/`.

## Commit messages

Follow the Conventional Commits specification, as described in
`.github/commit.instructions.md`.
