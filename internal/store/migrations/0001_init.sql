-- 0001_init: tenants, keys, accounts and account secrets.
--
-- All times are INTEGER Unix milliseconds in UTC. Ciphertext is stored as BLOB.
--
-- Warning for every new migration file: never rebuild (create, copy, drop,
-- rename) a table that another table references with ON DELETE CASCADE. A file
-- runs in a transaction, where SQLite ignores "PRAGMA foreign_keys = OFF", so the
-- drop would delete the rows that reference it, for example every stored
-- password in account_secrets. See the package comment in store.go.

-- schema_migrations records which migration files have run. The migration code
-- also creates this table before the first file, because it must read the table
-- before it can know whether this file has run.
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY
);

CREATE TABLE tenants (
    id         TEXT    NOT NULL PRIMARY KEY,
    created_at INTEGER NOT NULL
);

-- tenant_keys holds the data keys of a tenant. Each key is wrapped (encrypted)
-- with the master key. A tenant can have several versions of its key. New
-- secrets use the latest version.
CREATE TABLE tenant_keys (
    tenant_id  TEXT    NOT NULL REFERENCES tenants (id),
    version    INTEGER NOT NULL CHECK (version >= 1),
    wrapped    BLOB    NOT NULL,
    kek_id     TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, version)
);

-- server_keys holds the one server key. The CHECK allows only one row.
CREATE TABLE server_keys (
    id         INTEGER NOT NULL PRIMARY KEY CHECK (id = 1),
    wrapped    BLOB    NOT NULL,
    kek_id     TEXT    NOT NULL,
    created_at INTEGER NOT NULL
);

-- accounts: mail, levels and limits are JSON text. mail is NULL for a provider
-- that has no mail server settings. autonomy_accepted_at is NULL until the user
-- accepts full autonomy.
CREATE TABLE accounts (
    tenant_id            TEXT    NOT NULL REFERENCES tenants (id),
    id                   TEXT    NOT NULL,
    provider             TEXT    NOT NULL,
    address              TEXT    NOT NULL,
    display_name         TEXT    NOT NULL DEFAULT '',
    mail                 TEXT,
    levels               TEXT    NOT NULL,
    confirm_sends        INTEGER NOT NULL CHECK (confirm_sends IN (0, 1)),
    confirm_deletes      INTEGER NOT NULL CHECK (confirm_deletes IN (0, 1)),
    autonomy_accepted_at INTEGER,
    limits               TEXT    NOT NULL,
    status               TEXT    NOT NULL,
    created_at           INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, id)
);

-- account_secrets holds sealed (encrypted) values, such as a password. Deleting
-- an account deletes its secrets.
CREATE TABLE account_secrets (
    tenant_id   TEXT    NOT NULL,
    account_id  TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    key_version INTEGER NOT NULL,
    sealed      BLOB    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, kind),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id) ON DELETE CASCADE
);
