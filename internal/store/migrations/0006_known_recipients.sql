-- 0006_known_recipients: the addresses that an account has sent mail to.
--
-- The new-recipient check of an account asks the user to confirm a send to an
-- address that the account has never sent to. This table remembers which
-- addresses are known. It never holds an address. hash is the HMAC-SHA-256 of
-- the lower-cased bare address, with a key that is made from the server key, 32
-- bytes. A plain SHA-256 would not be enough: anyone with a copy of the database
-- could hash a list of addresses and look them up. The service makes the hash.
-- The store does not know how.
--
-- created_at is the time of the first send to the address, INTEGER Unix
-- milliseconds in UTC. Deleting an account deletes its rows. The primary key
-- also serves the lookups and the delete of an account.
CREATE TABLE known_recipients (
    tenant_id  TEXT    NOT NULL,
    account_id TEXT    NOT NULL,
    hash       BLOB    NOT NULL CHECK (typeof(hash) = 'blob' AND length(hash) = 32),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (tenant_id, account_id, hash),
    FOREIGN KEY (tenant_id, account_id) REFERENCES accounts (tenant_id, id) ON DELETE CASCADE
);
