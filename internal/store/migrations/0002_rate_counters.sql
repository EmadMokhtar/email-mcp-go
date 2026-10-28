-- 0002_rate_counters: counters for the send and trash rate limits.
--
-- One row counts the uses of one kind in one fixed window of one account.
-- window_start is INTEGER Unix seconds in UTC (not milliseconds like the other
-- tables, because windows start on a whole hour). Deleting an account deletes
-- its counters.
CREATE TABLE rate_counters (
    tenant_id    TEXT    NOT NULL,
    account_id   TEXT    NOT NULL,
    kind         TEXT    NOT NULL,
    window_start INTEGER NOT NULL,
    count        INTEGER NOT NULL CHECK (count >= 0),
    PRIMARY KEY (tenant_id, account_id, kind, window_start),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id) ON DELETE CASCADE
);
