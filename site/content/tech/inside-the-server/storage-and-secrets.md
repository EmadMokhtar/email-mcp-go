---
title: Storage and secrets
description: Where email-mcp keeps its data, which fields are encrypted, hashed or plain, how the keys work, and how long data stays.
weight: 30
code_areas:
  - internal/store/store.go
  - internal/store/migrate.go
  - internal/store/migrations/
  - internal/store/keys.go
  - internal/store/rewrap.go
  - internal/secrets/vault.go
  - internal/secrets/kek.go
  - internal/secrets/aead.go
  - internal/secrets/secret.go
  - internal/app/app.go
  - internal/app/kek.go
  - cmd/email-mcp/key.go
---

This page shows where email-mcp keeps its data, which fields are encrypted,
hashed or plain, and how the keys protect them. Read it before you add a table,
a column or a secret. The user view is in [Where the data
lives](/manual/reference/data-folder/) and [The master
key](/manual/reference/master-key/).

## Where the data lives

All data is in one SQLite database (a database in one file). The package `store`
is the only package that talks to it. Its package comment states the rule of the
tenants: every query for one tenant has a `tenant_id = ?` condition. A tenant is
the owner of a set of accounts, and a local install has one, with the id
`local`.

- **The folder.** `defaultDataDir` in {{< src "internal/app/app.go" >}} chooses
  it: `os.UserConfigDir()` plus `email-mcp` on macOS and Windows, and
  `$XDG_DATA_HOME/email-mcp` or `~/.local/share/email-mcp` on Linux
  (XDG_DATA_HOME is a standard setting that says where programs keep their
  files). The flag `--data-dir` or the variable `EMAIL_MCP_DATA_DIR` chooses
  another one, and the flag wins.
- **The files.** The database is `email-mcp.db` (`store.DBFileName`). SQLite
  keeps its write-ahead log (WAL: a mode where readers and one writer work
  together) next to it, in `email-mcp.db-wal` and `email-mcp.db-shm`. The lock
  file for the first master key is `kek.lock`.
- **The modes.** A mode says who may read and write a file. `store.Open` creates
  a missing folder with mode 0700 (only the owner can use it), and makes the
  database file, when it is missing, with mode 0600 before SQLite opens it,
  because SQLite would use 0644. SQLite gives its other two files the mode of
  the database file. Keep the data folder private to your user account.
  `email-mcp doctor` checks the modes of the folder and of the database files,
  on systems that have file modes.

## How the database opens

`newDB` in {{< src "internal/store/store.go" >}} puts the settings in the DSN
(data source name, the connection string), so that every connection of the pool
(the set of open connections) gets them. Several programs share the file, and
each setting matters for that:

| Setting | What it does | Why it matters |
|---|---|---|
| `journal_mode(WAL)` | Readers do not block the writer. | Several programs read and write at once. |
| `busy_timeout(5000)` | A statement waits up to 5 seconds for a lock. | A short wait is not an error. |
| `foreign_keys(1)` | SQLite checks foreign keys (a column that must match a row of another table). | The rules that delete the rows of a deleted account (`ON DELETE CASCADE`) depend on it. |
| `secure_delete(1)` | SQLite overwrites deleted content with zeros. | Free space in the database file does not keep old rows. |
| `synchronous(FULL)` | The log is flushed to disk at each commit. | Sending a message once, also after a power loss, depends on it. |
| `_txlock=immediate` | A transaction takes the write lock when it starts. | A read followed by a write could fail at once with "database is locked", and the busy timeout would not help. |

The daily job also runs a checkpoint (a step that copies the write-ahead log
into the database file). The first connection to a file that was just
made switches it to WAL mode, and two programs that do this together can get
`SQLITE_BUSY` at once. `connect` tries again (`connectAttempts`,
`connectDelay`).

## Migrations

A migration is a numbered SQL (Structured Query Language) file that changes the
structure of the database. The files in `internal/store/migrations/` are
embedded in the program and named like `0001_name.sql`: four digits, a name of
lower-case letters, digits and underscores, and `.sql`. `migrate` in
{{< src "internal/store/migrate.go" >}} runs the files that have not run, in
order of version. Each file runs in its own transaction (a group of database
steps that all happen or none do) with the row that records its version. A
database that holds a version above the highest file was written by a version of
email-mcp that has more migrations, and `Open` refuses it with `ErrNewerSchema`.

To add a migration:

1. Add the file with the next number. Two files with one version are refused.
2. Do not edit a file that a release has published. The database records only
   the version, so a database that ran the old text never runs the edited text.
3. Do not rebuild a table that another table references with `ON DELETE CASCADE`
   (create, copy, drop, rename). SQLite ignores `PRAGMA foreign_keys = OFF`
   inside a transaction, so the `DROP TABLE` would delete the rows that
   reference it, every stored password among them. Use
   `ALTER TABLE ... ADD COLUMN`. Migration 0012 dropped `web_sessions` on
   purpose, because no table references it.
4. Add the methods in a file of the package `store`, with the `tenant_id = ?`
   condition in each query for a tenant.

## The tables

| Purpose | Tables | What they hold |
|---|---|---|
| Keys | `tenants`, `tenant_keys`, `server_keys` | The tenants, the wrapped data keys, and the one wrapped server key |
| Accounts | `accounts`, `account_secrets` | Settings, levels and limits, and the sealed password |
| Pending actions | `pending_actions` | The sends and the Trash moves that wait for the person: the payload (the exact request) and the preview (what the person sees) |
| Outbox | `outbox` | The messages that wait to go out |
| Audit | `audit_log` | The activity log |
| Counters | `rate_counters`, `known_recipients` | The counts of the limits, and the hashes of known recipients |
| Tokens and sessions | `api_tokens`, `web_setup_tokens`, `web_sessions` | The hashes of access tokens (the secrets that programs send to the local HTTP, HyperText Transfer Protocol, server), setup tokens (the one-time sign-in links of the web portal, the local web site of the person) and session ids (the sign-ins of browsers) |
| Migrations | `schema_migrations` | The versions that have run |

A table keeps some rules itself, with `CHECK` conditions. A pending action that
waits has its payload and preview, and one that has ended has neither. An outbox
item that waits or sends has its message, and one that has ended has none. A bug
cannot keep the payload or the message of an ended item in its row, or lose one
that is still needed.

## Sealed, hashed or plain

To seal a value is to encrypt it. Each sealed value has its key version next to
it. SHA-256 (Secure Hash Algorithm with a 256-bit result) makes a fingerprint of
bytes. A token has 256 random bits, so nobody can find it from its hash, and a
slow password hash is not needed. An address has few bits, so the table keeps it
as an HMAC-SHA-256 (HMAC is a hash-based message authentication code: a hash
made with a secret key, here with SHA-256). Without the secret key, nobody can
test an address against the stored hashes.

| How | What | Where |
|---|---|---|
| Sealed | The password of an account | `account_secrets.sealed` |
| Sealed | The payload and the preview of a pending action | `pending_actions.payload`, `pending_actions.preview` |
| Sealed | The message of an outbox item | `outbox.message` |
| Sealed | The recipients and the arguments of an audit entry | `audit_log.recipients`, `audit_log.arguments` |
| Hashed (SHA-256) | Access tokens, setup tokens and session ids | `hash` of `api_tokens`, `web_setup_tokens`, `web_sessions` |
| Hashed (HMAC-SHA-256) | Known recipients | `known_recipients.hash` |
| Plain | Everything else, such as the address, the server settings, the levels, the limits, the times and the states | the other columns |

## Keys

The drawing uses three short names. KEK is the key encryption key (the master
key), DEK is a data encryption key, and AES-256-GCM is the cipher. The list
below explains each one.

{{< diagram "key-hierarchy" >}}

`secrets.Vault` in {{< src "internal/secrets/vault.go" >}} does envelope
encryption: a key wraps (encrypts) other keys, and those keys seal (encrypt) the
data.

- **The data key (DEK, data encryption key).** Each tenant has a random data
  key, with a version. `Encrypt` seals a value with AES-256-GCM (Advanced
  Encryption Standard with a 256-bit key, in Galois/Counter Mode: a cipher that
  also detects changes) under the latest version, and returns the version to
  store with the value. The first time that the vault sees a tenant, it makes
  version 1.
- **The associated data.** `secrets.AAD` ties a value to its tenant, account and
  field. It is not stored, and `Open` needs the same text. So a value that is
  sealed for one tenant, account and field cannot be opened as another tenant,
  account or field. An id must not contain `|`, because the format has no
  escaping.
- **The master key (KEK, key encryption key).** It wraps the data keys and the
  server key, so the database holds only wrapped keys.
- **The server key.** One random key, wrapped by the master key. The raw server
  key is never used as a key itself. `DerivedServerKey` gives the HMAC-SHA-256
  of a label under it. The label `signing/v1` gives the key of the signer, which
  signs request states (they name a waiting action), page tokens (they name the
  next page of a search), cursors (they mark where the last check for arrived
  messages stopped) and the tokens of the web forms. Other labels give the keys
  of other values of the server. A change of a label makes the old values
  useless.

## The master key

`EMAIL_MCP_KEK` chooses the source. It is `keychain` (the password store of the
operating system, and the default) or `file:` and an absolute path.
`CheckKEKSource` refuses a relative path, because it would depend on the folder
where the AI (artificial intelligence) app starts the server. It refuses a path
with a `..` part too. The keychain entry has the service `email-mcp` and the
account `master-key`. A key file holds the key as standard base64 text (32
bytes, 44 characters). Base64 is text made of letters, digits and a few signs,
that stands for bytes. `NewFileKEK` refuses a file that the group or other users
can access, on systems that have file modes. On Windows, keep the key file in a
folder that only your user account can read.

### The first start

`loadKEK` in {{< src "internal/app/kek.go" >}} takes a lock file while it looks
for the key and makes it. Two AI apps can start two programs at once on a first
install. Without the lock, each would make its own key. The lock of a key file
is in the data folder. The lock of the keychain is in
`os.UserConfigDir()/email-mcp`, because two data folders can share one keychain
entry. The operating system releases the lock when the program ends.

The program makes a key only when the database holds no wrapped key
(`databaseHasKeys`). If the database has keys and the key is missing, the start
is refused with a message. Another random key could never open the stored keys,
and a replacement keychain entry would block a later restore of the real one.
Two programs can try to make the same key at the same moment. The store inserts
a data key or the server key only if none exists, and the program that finds a
key already there uses the stored key.

### Rotation

`key rotate` calls `App.RotateKEK`. It refuses the key in use under any spelling
of its path. It takes the locks of the old and the target source, makes the key
file of the target (never over an existing file) and puts it on disk first. Then
`Vault.RewrapAll` wraps all stored keys again with the target key in one
transaction. The store checks in that transaction that the stored keys are
exactly the keys that were listed, so a key that another program added makes it
fail with `ErrConflict`. It is all or nothing. The sealed values do not change.
If the old master key may have leaked, rotation is not enough: also change the
mail passwords at your mail provider. The old key file or keychain entry is
never deleted. The other programs must be stopped first, because they hold the
old key in memory.

### Secrets in output

`secrets.Secret` is a string type that prints `[redacted]` in `fmt`, in
`log/slog` and in JSON (JavaScript Object Notation) in the normal cases. The doc
comment of `Secret` lists the cases that it does not cover. Only `Reveal` gives
the value, so each place that uses a value is easy to find in a review. The same
holds for the `Vault`.

## How long data is kept

| Data | Kept | Removed by |
|---|---|---|
| Ended pending actions | 30 days after they were made (`pendingRetention`). The payload and preview go when the action ends. | `Manager.Prune` |
| Audit entries | 90 days (`auditRetention`) | `Log.Prune` |
| Outbox message | Until the item ends. The row stays, with no message, until the account is removed. | The statement that ends the item |
| Limit counters | 48 hours (`retention`) | `ReserveCounters` |
| Setup token | 10 minutes (`setupTokenTTL`), and it works once | Its use, or the next setup token |
| Web session | 24 hours (`sessionTTL`), and 2 hours without use (`sessionIdleTTL`) | Its next use, or the next session |
| Access token | A new token works for 90 days by default (`DefaultLifetime`). The person can choose from 1 minute to 3,650 days. | Nothing. The row stays after the token ends or is revoked, so the list of tokens can show it. |
| Everything of an account | Until the account is removed | `ON DELETE CASCADE` |

`App.dailyJobs` runs the prune of the first two rows and a checkpoint of the
write-ahead log (`store.CheckpointWAL`). It runs when `Run` starts, and then
every 24 hours (`jobEvery`). The page [Startup and
shutdown](/tech/inside-the-server/startup-and-shutdown/) explains `Run`.

## Related pages

- [Activity log and program log](/tech/inside-the-server/audit-and-logging/)
- [Startup and shutdown](/tech/inside-the-server/startup-and-shutdown/)
- [Architecture overview](/tech/start-here/overview/)
- [Where the data lives](/manual/reference/data-folder/) in the user manual
- [The master key](/manual/reference/master-key/) in the user manual
