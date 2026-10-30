-- 0007_api_tokens: the access tokens of the local HTTP server.
--
-- An MCP client that connects over HTTP sends a bearer token. This table never
-- holds a token. hash is the SHA-256 of the whole token text, 32 bytes. A token
-- has 256 random bits, so nobody can find a token from its hash, and a slow
-- password hash is not needed. The tokens package makes the token and the hash.
-- The hash is unique, so one token belongs to exactly one row. The index of the
-- hash serves the check of a token, which must find the row without knowing the
-- tenant.
--
-- id is a random text id. The user names a token by its id to revoke it, so the
-- user does not need the token itself. name is a label that the user chose: 1 to
-- 128 bytes. scopes is a JSON array of scope names, such as ["email:read"], and
-- is never empty: a token always has an explicit list. The tokens package checks
-- that each name is known.
--
-- Times are INTEGER Unix milliseconds in UTC. revoked_at is NULL while the token
-- is valid. A revoked row stays, so the list can show when it was revoked.
CREATE TABLE api_tokens (
    tenant_id  TEXT    NOT NULL REFERENCES tenants (id),
    id         TEXT    NOT NULL CHECK (id <> ''),
    name       TEXT    NOT NULL CHECK (name <> '' AND length(CAST(name AS BLOB)) <= 128),
    hash       BLOB    NOT NULL CHECK (typeof(hash) = 'blob' AND length(hash) = 32),
    scopes     TEXT    NOT NULL CHECK (json_valid(scopes) AND json_type(scopes) = 'array'
                                       AND json_array_length(scopes) > 0),
    created_at INTEGER NOT NULL,
    revoked_at INTEGER,
    PRIMARY KEY (tenant_id, id),
    CHECK (revoked_at IS NULL OR revoked_at >= created_at)
);

-- The check of a token finds its row by the hash.
CREATE UNIQUE INDEX api_tokens_by_hash ON api_tokens (hash);
