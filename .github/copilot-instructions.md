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

- `cmd/email-mcp` — entry point and flag parsing.
- `internal/config` — configuration loaded from environment variables.
- `internal/imap` — IMAP client: search, fetch, flags, move, delete.
- `internal/smtp` — SMTP client: send, reply, forward.
- `internal/server` — MCP server, tool handlers, HTTP transport.
- `internal/tools` — MCP tool schema definitions.
- `pkg/models` — shared data types.

## Style

- Format with `gofmt`; the build treats formatting as non-negotiable.
- Return errors, do not panic. Wrap with `fmt.Errorf("...: %w", err)` so the
  cause survives. Never discard an error to make a signature simpler.
- Keep exported identifiers documented with a comment starting with the name.
- Comments explain *why*, not what. The code already shows what.
- Table-driven tests with `testify` (`assert` and `require`), matching the
  existing `_test.go` files.

## Project-specific rules

- Message IDs in the tool API are IMAP **UIDs**, never sequence numbers.
  Sequence numbers shift when a message is expunged.
- Use the `Uid*` client methods for every IMAP operation.
- Fetch with `BODY.PEEK` unless the intent really is to mark mail as read.
- Never log email content: no bodies, subjects, recipients or attachments.
  Log metadata such as folder, UID and counts.
- `imap.Client` holds one connection and serializes commands with its own
  mutex. Callers must not add their own locking.

## Commands

- `make check` — format, vet and test. Run before proposing changes.
- `make test-race` — tests under the race detector.
- `make lint` — golangci-lint, if installed.
- `make build` — build the binary into `bin/`.

## Commit messages

Follow the Conventional Commits specification, as described in
`.github/commit.instructions.md`.
