-- 0012_web_session_limits: a shorter, revocable session of the web interface.
--
-- A session now has two limits: expires_at, 24 hours after it was made, and
-- an idle limit that the web package counts from last_seen_at. id is a number
-- that the Connections page may show and post back to sign one browser out:
-- it is not the secret, and it says nothing about the hash. Every use of the
-- id has a tenant_id condition.
--
-- This migration drops the old table, and so it signs every browser out. That
-- is on purpose. An old session lived 30 days with no idle limit, and its cookie
-- was set on 127.0.0.1, where a browser sends it to every local port. So an old
-- cookie may be known to another local program, and it must stop working now.
-- Nothing else refers to this table, so dropping it deletes nothing else (see
-- the warning for new migrations in store.go).
DROP TABLE web_sessions;

CREATE TABLE web_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    hash         BLOB    NOT NULL UNIQUE CHECK (typeof(hash) = 'blob' AND length(hash) = 32),
    tenant_id    TEXT    NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    CHECK (expires_at > created_at),
    CHECK (last_seen_at >= created_at)
);

CREATE INDEX web_sessions_tenant ON web_sessions (tenant_id);
