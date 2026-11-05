# Changelog

## [0.6.0](https://github.com/EmadMokhtar/email-mcp-go/compare/v0.5.0...v0.6.0) (2026-11-05)


### ⚠ BREAKING CHANGES

* **httpserve:** make in-band confirmation opt-in per token ([#33](https://github.com/EmadMokhtar/email-mcp-go/issues/33))

### Bug Fixes

* **httpserve:** make in-band confirmation opt-in per token ([#33](https://github.com/EmadMokhtar/email-mcp-go/issues/33)) ([1d0a7c1](https://github.com/EmadMokhtar/email-mcp-go/commit/1d0a7c1dc2457c478fc4eb71cb43ab1d95e2c053))
* **imapsmtp:** return the text a mail client shows, not the hidden alternative ([#36](https://github.com/EmadMokhtar/email-mcp-go/issues/36)) ([74a34a2](https://github.com/EmadMokhtar/email-mcp-go/commit/74a34a27c29c66355d4003b772f6c56dbde5d89c))
* **mime:** fail closed on css the hidden-text check cannot evaluate ([#38](https://github.com/EmadMokhtar/email-mcp-go/issues/38)) ([8a681c1](https://github.com/EmadMokhtar/email-mcp-go/commit/8a681c117e8c6399855323384371e5eafd2b781e))
* **mime:** sanitize outgoing html so the preview equals the message ([#37](https://github.com/EmadMokhtar/email-mcp-go/issues/37)) ([28ccf8a](https://github.com/EmadMokhtar/email-mcp-go/commit/28ccf8a467b604a865d04d053ccf6429722f0e9b))
* **outbox:** recheck the account level at delivery and let the user cancel ([#31](https://github.com/EmadMokhtar/email-mcp-go/issues/31)) ([f05a4b8](https://github.com/EmadMokhtar/email-mcp-go/commit/f05a4b881babea914564a986f01dcc57cd738e41))
* **send:** show punycode and treat non-ascii recipients as new ([#34](https://github.com/EmadMokhtar/email-mcp-go/issues/34)) ([d86172d](https://github.com/EmadMokhtar/email-mcp-go/commit/d86172d358490f79c421c80e978724665addb929))
* **settings:** require risk acceptance to turn off send confirmation ([#32](https://github.com/EmadMokhtar/email-mcp-go/issues/32)) ([238b3b0](https://github.com/EmadMokhtar/email-mcp-go/commit/238b3b02274d102e80c9a116a8e3a43928c3c50b))
* **web:** serve the portal on a dedicated host with short revocable sessions ([#35](https://github.com/EmadMokhtar/email-mcp-go/issues/35)) ([08bf266](https://github.com/EmadMokhtar/email-mcp-go/commit/08bf2663c7f6fa68ea3b7e9286f0427282c9f8ed))

## [0.5.0](https://github.com/EmadMokhtar/email-mcp-go/compare/v0.4.0...v0.5.0) (2026-11-05)


### Features

* **audit:** record every tool call of an agent ([#21](https://github.com/EmadMokhtar/email-mcp-go/issues/21)) ([d964001](https://github.com/EmadMokhtar/email-mcp-go/commit/d964001706e757248a74f1f5c32e4941a75896bb))

## [0.4.0](https://github.com/EmadMokhtar/email-mcp-go/compare/v0.3.0...v0.4.0) (2026-11-05)


### Features

* **web:** rebuild the portal as a small mail app ([#20](https://github.com/EmadMokhtar/email-mcp-go/issues/20)) ([18783b8](https://github.com/EmadMokhtar/email-mcp-go/commit/18783b8c85c6b40bb03452dfe7f62fe03fdcc357))


### Bug Fixes

* **mcpserver:** report an unreachable mail server instead of internal error ([#18](https://github.com/EmadMokhtar/email-mcp-go/issues/18)) ([68a5a9f](https://github.com/EmadMokhtar/email-mcp-go/commit/68a5a9f49fab36eb4497b239a8b3951735aa7547))

## [0.3.0](https://github.com/EmadMokhtar/email-mcp-go/compare/v0.2.0...v0.3.0) (2026-11-03)


### Features

* reconnect an account and restore the M0a follow-ups ([#15](https://github.com/EmadMokhtar/email-mcp-go/issues/15)) ([4763194](https://github.com/EmadMokhtar/email-mcp-go/commit/47631946d61167ce71dc0028787b755768d81d1d))

## [0.2.0](https://github.com/EmadMokhtar/email-mcp-go/compare/v0.1.0...v0.2.0) (2026-11-03)


### Features

* **web:** style the web pages with Bulma ([#13](https://github.com/EmadMokhtar/email-mcp-go/issues/13)) ([b98982c](https://github.com/EmadMokhtar/email-mcp-go/commit/b98982c4e9912f85f6b3a817bfb58c09316f09d4))

## [0.1.0](https://github.com/EmadMokhtar/email-mcp-go/releases/tag/v0.1.0) (2026-11-01)


### ⚠ BREAKING CHANGES

* rewrite the server in Go with local IMAP and SMTP (M0a) ([#3](https://github.com/EmadMokhtar/email-mcp-go/issues/3)) ([9e66ec0](https://github.com/EmadMokhtar/email-mcp-go/commit/9e66ec0))

### Features

* add email MCP server with IMAP and SMTP support ([#1](https://github.com/EmadMokhtar/email-mcp-go/issues/1)) ([3eb2327](https://github.com/EmadMokhtar/email-mcp-go/commit/3eb2327))
