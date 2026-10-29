-- 0008_web: the sign-in of the local web interface.
--
-- The web interface is for the user, never for the agent. A browser signs in
-- once: the user runs "email-mcp setup" in a terminal, and it opens the browser
-- with a one-time setup token in the address. The server swaps the setup token
-- for a session, and the browser keeps the session id in a cookie. The sessions
-- are in the database, so every local process accepts them, on any port.
--
-- Neither table ever holds a token or a session id. hash is the SHA-256 of the
-- text of the setup token or of the session id, 32 bytes. Both have 256 random
-- bits, so nobody can find one from its hash, and a slow password hash is not
-- needed. The web package makes the values and the hashes. The hash is unique,
-- so one value belongs to exactly one row. A lookup finds the row by the hash
-- alone: the caller has only the value, and the row says which tenant it
-- belongs to.
--
-- Times are INTEGER Unix milliseconds in UTC. A row is valid while the time is
-- before expires_at, and expires_at is after created_at. Deleting a tenant
-- deletes its rows.

-- web_setup_tokens: a setup token lives 10 minutes and works once. Using it
-- deletes the row, in the same transaction that adds the session. An expired
-- row is deleted when it is used, and when the next setup token is made.
CREATE TABLE web_setup_tokens (
    hash       BLOB    NOT NULL UNIQUE CHECK (typeof(hash) = 'blob' AND length(hash) = 32),
    tenant_id  TEXT    NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK (expires_at > created_at)
);

-- web_sessions: a signed-in browser. A session lives 30 days. An expired row is
-- deleted when its cookie comes back, and when the next session is made.
CREATE TABLE web_sessions (
    hash       BLOB    NOT NULL UNIQUE CHECK (typeof(hash) = 'blob' AND length(hash) = 32),
    tenant_id  TEXT    NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    CHECK (expires_at > created_at)
);
