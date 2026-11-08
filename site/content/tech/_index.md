---
title: Tech docs
description: How the code of email-mcp fits together and why, for the people who change it.
weight: 3
code_areas:
  - cmd/email-mcp/
  - internal/
---

These pages explain how the code of email-mcp works and why it is built this
way. They are for a developer who knows Go and does not know this code.
Each page names the files that it covers, and links to the code at the version
of this site.

email-mcp is an MCP server. MCP (Model Context Protocol) is the open standard
that lets an AI (artificial intelligence) app call the tools of another
program. The agent is the AI program that calls the tools. To learn how a
person installs and uses email-mcp, read the [user manual](/manual/).

Start with the [Architecture overview](/tech/start-here/overview/). It shows the
whole program on one page.

## If you want to know

The questions use a few terms. A tenant is the owner of a set of accounts.
IMAP (Internet Message Access Protocol) is the protocol for reading mail. Stdio
(standard input and output) and HTTP (HyperText Transfer Protocol) are the two
ways that a client talks to the server. Stdin is the standard input of a
program. A level is a number from 0 to 5 that says what the agent may do with an
account. A scope is a permission that an access token carries. The undo window
is the time that a send waits before it goes out, while it can still be
cancelled. The master key is the key that protects the other keys. A migration
is a numbered file that changes the structure of the database. Full autonomy
means that both confirmation switches of an account are off, so the agent sends
and moves mail to the Trash with no question. Ctrl-C is the keys that stop a
program in a terminal: hold the Ctrl key and press C. SMTP (Simple Mail
Transfer Protocol) is the protocol for sending mail. A provider is the service
behind a mail account. A backend is the part of the program that talks to the
mail server of one account. A certificate is the signed proof of identity that a
mail server shows when a connection starts. An authority is a body that signs
certificates, so that a client can trust them. A pin is the fingerprint of a
certificate that the person chose to trust. HTML (HyperText Markup Language) is
the language of web pages and of many mails.

| If you want to know... | read... |
|---|---|
| What are the main parts, and what does each one do? | [Architecture overview](/tech/start-here/overview/#the-parts-from-the-outside-in) |
| Which code can an agent reach, and which code only a person can reach? | [Architecture overview](/tech/start-here/overview/#two-doors-into-the-use-cases) |
| Who is the caller, and what is a tenant in a local install? | [Architecture overview](/tech/start-here/overview/#the-caller-and-the-tenant) |
| What starts when I run `serve`, and what can run at the same time? | [Architecture overview](/tech/start-here/overview/#run-modes) |
| Why can several processes use one database? | [Architecture overview](/tech/start-here/overview/#run-modes) |
| Where does this code live? | [Map of packages](/tech/start-here/package-map/#all-packages) |
| What may a package import? | [Map of packages](/tech/start-here/package-map/#the-layer-rule) |
| Which package do I change for this feature? | [Map of packages](/tech/start-here/package-map/#where-to-add-code) |
| Which library does what, and why is `go-imap` pinned? | [Map of packages](/tech/start-here/package-map/#libraries) |
| What happens between the request of the client and the IMAP server? | [One tool call, step by step](/tech/start-here/tool-call/#the-steps) |
| Where is the permission checked? | [One tool call, step by step](/tech/start-here/tool-call/#where-the-permission-is-checked) |
| Where is the call recorded in the activity log? | [One tool call, step by step](/tech/start-here/tool-call/#where-the-call-is-recorded) |
| Where is text from the mail cleaned? | [One tool call, step by step](/tech/start-here/tool-call/#where-text-from-the-mail-is-cleaned) |
| Where do errors become the texts that the agent sees? | [One tool call, step by step](/tech/start-here/tool-call/#errors-become-fixed-texts) |
| What differs between stdio and HTTP? | [One tool call, step by step](/tech/start-here/tool-call/#what-differs-between-stdio-and-http) |
| What happens when an agent calls `send_message`? | [The send flow](/tech/inside-the-server/send-flow/#the-steps) |
| When is the person asked? | [The send flow](/tech/inside-the-server/send-flow/#when-the-person-is-asked) |
| How is the person asked? | [The send flow](/tech/inside-the-server/send-flow/#two-ways-to-ask) |
| How does the "yes" of the person bind to the exact message? | [The send flow](/tech/inside-the-server/send-flow/#how-the-yes-is-tied-to-the-message) |
| What is the undo window? | [The send flow](/tech/inside-the-server/send-flow/#delivery-and-the-undo-window) |
| Who can cancel a send, and until when? | [The send flow](/tech/inside-the-server/send-flow/#cancel) |
| What if the program stops, or the connection breaks, during a send? | [The send flow](/tech/inside-the-server/send-flow/#the-outbox) |
| How is a double send prevented? | [The send flow](/tech/inside-the-server/send-flow/#the-outbox) |
| What can an agent do at each level? | [Permissions](/tech/inside-the-server/permissions/#levels) |
| Who changes a level, and how? | [Permissions](/tech/inside-the-server/permissions/#who-changes-a-level) |
| What do token scopes change? | [Permissions](/tech/inside-the-server/permissions/#token-scopes) |
| What are the limits, and when do they apply? | [Permissions](/tech/inside-the-server/permissions/#limits) |
| What is full autonomy, and what does it need? | [Permissions](/tech/inside-the-server/permissions/#full-autonomy-and-the-risk-acceptance) |
| What can never be done, at any level? | [Permissions](/tech/inside-the-server/permissions/#what-no-level-allows) |
| Where are the files, and who may read them? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#where-the-data-lives) |
| Which fields are encrypted, hashed or plain? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#sealed-hashed-or-plain) |
| What is the master key, and where does it live? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#the-master-key) |
| How does the first start make the key, and what if two programs start at once? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#the-first-start) |
| How do I rotate the master key? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#rotation) |
| How long is data kept? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#how-long-data-is-kept) |
| How do I add a migration? | [Storage and secrets](/tech/inside-the-server/storage-and-secrets/#migrations) |
| What does the activity log record, and where do the entries come from? | [Activity log and program log](/tech/inside-the-server/audit-and-logging/#the-activity-log) |
| What is never recorded? | [Activity log and program log](/tech/inside-the-server/audit-and-logging/#what-is-kept-and-what-is-dropped) |
| How long are activity log entries kept? | [Activity log and program log](/tech/inside-the-server/audit-and-logging/#how-long-entries-stay-and-who-reads-them) |
| What goes in the program log, and where does it go? | [Activity log and program log](/tech/inside-the-server/audit-and-logging/#the-program-log) |
| How do I add a log line safely? | [Activity log and program log](/tech/inside-the-server/audit-and-logging/#add-a-log-line) |
| What does `app.Open` build, and in what order? | [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/#open-six-steps) |
| What background work runs? | [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/#run-clean-up-then-three-jobs) |
| What happens when I press Ctrl-C, or the client closes stdin? | [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/#the-signals-and-a-second-ctrl-c) |
| How long can a stop take, and why? | [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/#close-six-steps) |
| What happens at the next start after a crash? | [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/#after-a-crash) |
| Why does a short command not wait for the queue? | [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/#short-commands) |
| What must a mail provider implement? | [The mail backend](/tech/mail/mail-backend/#what-a-provider-implements) |
| How does the program keep one backend for each account, and see a change that another process made? | [The mail backend](/tech/mail/mail-backend/#one-backend-for-each-account) |
| How does the backend keep and reuse connections? | [The mail backend](/tech/mail/mail-backend/#the-connection-pool) |
| How does the backend secure a connection and check a certificate? | [The mail backend](/tech/mail/mail-backend/#the-secure-dialer) |
| When may a person trust a certificate that no authority signed, when an account is new? | [The mail backend](/tech/mail/mail-backend/#the-secure-dialer) |
| When may a person replace or remove a pin? | [The mail backend](/tech/mail/mail-backend/#the-secure-dialer) |
| How are messages, folders and tags addressed? | [The mail backend](/tech/mail/mail-backend/#refs-folders-and-tags) |
| How does a change to many messages stay safe, and what if it fails half way? | [The mail backend](/tech/mail/mail-backend/#changing-mail) |
| How does a send reach the SMTP server? | [The mail backend](/tech/mail/mail-backend/#sending-with-smtp) |
| How does the program find the server settings of an address? | [Connecting an account](/tech/mail/connecting-an-account/#finding-the-settings) |
| How does an account get connected and tested? | [Connecting an account](/tech/mail/connecting-an-account/#saving-an-account) |
| What do the web portal and the commands share when they connect an account? | [Connecting an account](/tech/mail/connecting-an-account/#the-shared-package) |
| What happens when a mail server refuses the stored login? | [Connecting an account](/tech/mail/connecting-an-account/#a-refused-login-and-the-reconnect) |
| Which limits and lifetimes does the mail backend have? | [The mail backend](/tech/mail/mail-backend/#limits-and-lifetimes) |
| What would I change to add a provider? | [The mail backend](/tech/mail/mail-backend/#adding-a-provider) |
| What steps does mail take on the way in and on the way out? | [Mail content](/tech/mail/mail-content/#two-pipelines) |
| What is removed from incoming text, and why? | [Mail content](/tech/mail/mail-content/#what-cleanuntrusted-removes) |
| Which parts of a message does an agent read? | [Mail content](/tech/mail/mail-content/#which-parts-are-the-body) |
| How is HTML turned into text? | [Mail content](/tech/mail/mail-content/#how-html-becomes-text) |
| What limits stop a hostile message from using all the memory? | [Mail content](/tech/mail/mail-content/#limits) |
| Why is there a copy of the HTML parser of `golang.org/x/net`? | [Mail content](/tech/mail/mail-content/#the-copied-parser) |
| How do I change the hidden-text rules safely? | [Mail content](/tech/mail/mail-content/#change-the-hidden-text-rules) |
| How is an outgoing message built, and what does it refuse? | [Building mail](/tech/mail/building-mail/#what-build-refuses) |
| How is a saved draft made ready to send? | [Building mail](/tech/mail/building-mail/#a-saved-draft) |
| Why does the preview start with the text that goes out? | [Building mail](/tech/mail/building-mail/#the-preview-and-the-text-part) |
| How are the commands registered and tested? | [The command line](/tech/ways-in/command-line/#how-commands-are-registered-and-tested) |
| Which commands open the parts of the program (the `App`), and how does each one close it? | [The command line](/tech/ways-in/command-line/#what-each-command-opens) |
| How do the commands handle passwords, prompts and exit codes (the number that a program gives back when it ends)? | [The command line](/tech/ways-in/command-line/#passwords-prompts-and-exit-codes) |
| What do the web portal and the commands share? | [The command line](/tech/ways-in/command-line/#what-the-commands-share-with-the-web-portal) |
| How do I add a command? | [The command line](/tech/ways-in/command-line/#add-a-command) |
| Which checks run before a request of `serve --http` reaches a tool, and in what order? | [Local HTTP mode](/tech/ways-in/local-http/#the-checks-in-order) |
| How do the access tokens of local HTTP work? | [Local HTTP mode](/tech/ways-in/local-http/#tokens) |
| Why is the HTTP server stateless (it keeps nothing between two requests), and what does that change? | [Local HTTP mode](/tech/ways-in/local-http/#stateless-mode) |
| Who gets the question in the chat, and who gets the link to the approval page? | [Local HTTP mode](/tech/ways-in/local-http/#who-gets-which-channel) |
| How are the size, the time and the number of HTTP requests bounded? | [Local HTTP mode](/tech/ways-in/local-http/#limits-and-timeouts) |
| How does the HTTP server stop? | [Local HTTP mode](/tech/ways-in/local-http/#how-the-server-stops) |
| When does the web portal start, and where does it listen? | [The web portal](/tech/ways-in/web-portal/#when-it-starts-and-where-it-listens) |
| How does a person sign in to the web portal, and why does it have its own host name? | [The web portal](/tech/ways-in/web-portal/#signing-in) |
| Which checks run on a request to the web portal, and in what order? | [The web portal](/tech/ways-in/web-portal/#the-checks-in-order) |
| How are the pages, the styles and the one script of the web portal served? | [The web portal](/tech/ways-in/web-portal/#pages-styles-and-the-script) |
| How does the web portal reach the service? | [The web portal](/tech/ways-in/web-portal/#how-the-portal-reaches-the-service) |
| How do I add a page to the web portal? | [The web portal](/tech/ways-in/web-portal/#add-a-page) |
| What protects the person from an agent that follows bad instructions? | [Security design](/tech/security/security-design/#the-model) |
| Where is each security rule enforced? | [Security design](/tech/security/security-design/#rules-and-their-tests) |
| Which test fails if a security rule breaks? | [Security design](/tech/security/security-design/#rules-and-their-tests) |
| Which values does the server sign, and how? | [Security design](/tech/security/security-design/#signed-values) |
| What must I check when I change security code? | [Security design](/tech/security/security-design/#when-you-change-security-code) |
| How do I build, test and lint the code? | [Local development](/tech/work-on-the-code/local-development/#the-make-targets) |
| Which Go version do I need? | [Local development](/tech/work-on-the-code/local-development/#the-go-version) |
| What does CI (continuous integration: the checks that GitHub runs on each push) run? | [Local development](/tech/work-on-the-code/local-development/#what-ci-runs) |
| How do I add a tool, a command, a migration or a package? | [Local development](/tech/work-on-the-code/local-development/#add-code) |
| Which conventions does the code follow? | [Local development](/tech/work-on-the-code/local-development/#rules-of-the-code) |
| Which page of the docs do I update when I change the code? | [Local development](/tech/work-on-the-code/local-development/#which-page-do-i-update) |
| Which kinds of test exist, and what does each one prove? | [Testing](/tech/work-on-the-code/testing/#the-kinds-of-test) |
| How do I test code that talks to IMAP, SMTP or the web portal (the pages that email-mcp shows in the browser)? | [Testing](/tech/work-on-the-code/testing/#test-servers) |
| What is in `internal/e2e`? | [Testing](/tech/work-on-the-code/testing/#the-end-to-end-tests) |
| What are the build tags (words that a Go file asks for before it is built), and what does the interop test (the test against real mail servers) need? | [Testing](/tech/work-on-the-code/testing/#build-tags) |
| Which tests run in CI, in the nightly interop run and in the release? | [Testing](/tech/work-on-the-code/testing/#what-ci-and-the-release-run) |
| What must be set up once, before the first release? | [Making a release](/tech/work-on-the-code/release-process/#once-before-the-first-release) |
| What do I do for each release? | [Making a release](/tech/work-on-the-code/release-process/#for-each-release) |
| How does the version go up? | [Making a release](/tech/work-on-the-code/release-process/#how-the-version-goes-up) |
| What do I do when a release fails? | [Making a release](/tech/work-on-the-code/release-process/#when-the-release-fails) |
| How can I try the build without publishing? | [Making a release](/tech/work-on-the-code/release-process/#try-the-build-on-your-computer) |
| How is the docs site published? | [Making a release](/tech/work-on-the-code/release-process/#how-the-site-is-published) |
