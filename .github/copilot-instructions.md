# GitHub Copilot — Repository instruction file

## Purpose

Repository-specific guidance for GitHub Copilot and Copilot Chat, so
suggestions match this project's language, layout and tooling.

## About this project

This repository is **email-mcp-go**, a Model Context Protocol (MCP) server
written in **Go** (see `.go-version` and the `go` directive in `go.mod` for the
version in use). It exposes an email account to MCP clients such as Claude
Desktop over IMAP and SMTP.

Layout:

```
cmd/email-mcp/                 main, subcommand wiring
internal/mcpserver/            MCP tool registration (official go-sdk)
internal/service/              use cases; enforces policy before any backend call
internal/policy/               permission levels, confirmation decisions, rate limits
internal/domain/{account,mail}/ provider-neutral types and interfaces
internal/provider/imapsmtp/    IMAP + SMTP backend
internal/mime/                 MIME build/parse, HTML-to-text, untrusted-text cleaning
internal/discovery/            mail server autoconfig
internal/store/                SQLite; every query takes a tenant ID
internal/secrets/              envelope encryption, master-key providers
internal/outbox/, internal/audit/, internal/confirm/, internal/signing/, internal/tokens/
internal/web/                  local web UI
```

Some of these packages do not exist yet. The rewrite adds them task by task.

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
