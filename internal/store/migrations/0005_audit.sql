-- 0005_audit: what agents did.
--
-- The server writes one row for each tool call of an agent: the tool, the
-- account, the area, how many items, and the outcome. The Activity page of the
-- web interface reads the rows. Times are INTEGER Unix milliseconds in UTC.
--
-- The rows hold no mail content. detail is a short code, such as "rate_limited",
-- and never free text. The table keeps this rule itself: 0 to 64 characters from
-- a-z, 0-9 and "_". client is the name that the MCP client gave for itself, so it
-- is not trusted. The audit package checks it. The table limits tool and client
-- to 128 bytes.
--
-- recipients is a sealed (encrypted) list of addresses, because addresses are
-- personal data. key_version says which tenant key sealed it. Both are NULL when
-- the call had no recipients. The table makes sure that one is NULL only when the
-- other is.
--
-- outcome is "ok", "refused", "declined", "pending_approval" or "error". The
-- audit package owns the names.
--
-- account_id is NULL for an entry of the tenant that is not about one account.
-- Then the foreign key to accounts is not checked, and only the tenant must
-- exist. An entry for an account needs an area. Deleting an account deletes its
-- rows. AUTOINCREMENT makes sure that an id is never used twice, also after
-- old rows are deleted, so the id can order rows that have the same time.
CREATE TABLE audit_log (
    id                     INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
    tenant_id              TEXT    NOT NULL REFERENCES tenants (id),
    account_id             TEXT,
    recorded_at            INTEGER NOT NULL,
    area                   TEXT    NOT NULL DEFAULT '',
    client                 TEXT    NOT NULL DEFAULT '' CHECK (length(CAST(client AS BLOB)) <= 128),
    tool                   TEXT    NOT NULL CHECK (length(CAST(tool AS BLOB)) <= 128),
    item_count             INTEGER NOT NULL DEFAULT 0 CHECK (item_count >= 0),
    outcome                TEXT    NOT NULL CHECK (outcome IN ('ok', 'refused', 'declined', 'pending_approval', 'error')),
    detail                 TEXT    NOT NULL DEFAULT '' CHECK (length(detail) <= 64 AND detail NOT GLOB '*[^a-z0-9_]*'),
    recipients             BLOB,
    recipients_key_version INTEGER,
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id) ON DELETE CASCADE,
    CHECK ((recipients IS NULL) = (recipients_key_version IS NULL)),
    CHECK (account_id IS NULL OR area <> '')
);

-- The list of a tenant, newest first.
CREATE INDEX audit_log_by_tenant_time ON audit_log (tenant_id, recorded_at, id);

-- The daily clean-up deletes old rows of all tenants.
CREATE INDEX audit_log_by_time ON audit_log (recorded_at);
