-- 0004_outbox: emails that wait to be sent.
--
-- A send waits here for its undo window. A worker then sends it. Times are
-- INTEGER Unix milliseconds in UTC.
--
-- status is "queued", "sending", "sent", "failed" or "cancelled". The outbox
-- package owns the meaning of the names, and the store needs them in its
-- queries.
--
-- send_after is the end of the undo window. next_attempt_at is the earliest time
-- of the next try. It starts equal to send_after, and a failed try moves it
-- later, so an item is due when both times have passed. attempts counts the
-- tries that failed.
--
-- claimed_at is the time of the last claim (the move from "queued" to "sending").
-- A program that stopped while it sent leaves an item in "sending". Other
-- programs that use the same database must not touch it while the send can still
-- run, so they find the item by this time. The table makes sure that an item in
-- "sending" has the time.
--
-- message is the whole email, sealed (encrypted) with the tenant key. key_version
-- says which tenant key sealed it. draft_ref is the opaque reference of the draft
-- that the message was made from, if any. It holds no mail content.
--
-- The email must not stay on disk after it is no longer needed. The table
-- keeps this rule itself: an item that waits or is being sent has its message,
-- and an item that has ended (sent, failed, cancelled) has none, and no draft
-- reference. Deleting an account deletes its items.
CREATE TABLE outbox (
    tenant_id       TEXT    NOT NULL,
    id              TEXT    NOT NULL,
    account_id      TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'cancelled')),
    send_after      INTEGER NOT NULL,
    next_attempt_at INTEGER NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    error_text      TEXT    NOT NULL DEFAULT '',
    message         BLOB,
    key_version     INTEGER NOT NULL,
    draft_ref       TEXT,
    created_at      INTEGER NOT NULL,
    sent_at         INTEGER,
    claimed_at      INTEGER,
    PRIMARY KEY (tenant_id, id),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id) ON DELETE CASCADE,
    CHECK ((status IN ('queued', 'sending')) = (message IS NOT NULL)),
    CHECK (status IN ('queued', 'sending') OR draft_ref IS NULL),
    CHECK (status <> 'sending' OR claimed_at IS NOT NULL)
);

-- The worker looks for queued items that are due.
CREATE INDEX outbox_by_status_send_after ON outbox (status, send_after);
