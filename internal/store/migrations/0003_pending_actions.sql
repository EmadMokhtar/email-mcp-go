-- 0003_pending_actions: actions that wait for the user to confirm them.
--
-- An agent asks to send an email or to move mail to Trash. The server keeps the
-- request here until the user confirms it. Times are INTEGER Unix milliseconds
-- in UTC.
--
-- hash is the SHA-256 of the kind and the payload, 32 bytes. It is stored as
-- plain bytes, because it does not show the payload.
--
-- payload and preview are sealed (encrypted) with the tenant key. The key
-- version says which tenant key sealed each one. The payload is the exact
-- request, for example the whole message. The preview holds recipients, a
-- subject and the start of the body, so it is secret too.
--
-- state is "pending", "approved", "rejected", "expired" or "done". The confirm
-- package owns these names. result_id is empty until the action has a result.
-- Deleting an account deletes its pending actions.
CREATE TABLE pending_actions (
    tenant_id           TEXT    NOT NULL,
    id                  TEXT    NOT NULL,
    account_id          TEXT    NOT NULL,
    kind                TEXT    NOT NULL,
    hash                BLOB    NOT NULL CHECK (length(hash) = 32),
    state               TEXT    NOT NULL,
    created_at          INTEGER NOT NULL,
    expires_at          INTEGER NOT NULL,
    result_id           TEXT    NOT NULL DEFAULT '',
    payload             BLOB    NOT NULL,
    payload_key_version INTEGER NOT NULL,
    preview             BLOB    NOT NULL,
    preview_key_version INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id) ON DELETE CASCADE
);

-- The list of waiting actions of a tenant, newest first.
CREATE INDEX pending_actions_by_state ON pending_actions (tenant_id, state, created_at);
