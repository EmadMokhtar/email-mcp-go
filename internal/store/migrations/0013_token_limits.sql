-- 0013_token_limits: limits of an access token, and which token made an audit
-- entry.
--
-- in_band_confirm says that the client of this token may answer a confirmation
-- in its own window (an in-band form). It is 0 by default, and every token from
-- before this migration gets 0. Without it, a send or a move to Trash that needs
-- a confirmation waits on the approval page of the web interface. So a program
-- that only holds a token cannot say the "yes" of the user.
--
-- expires_at is when the token stops working, in Unix milliseconds in UTC. A new
-- token always has one. It is NULL only on a token from before this migration:
-- such a token keeps working until the user revokes it.
--
-- last_used_at is the last time that the token passed a check. The tokens package
-- writes it at most about once a minute, so a check does not write to the database
-- each time. It is NULL for a token that was not used since this migration.
ALTER TABLE api_tokens ADD COLUMN in_band_confirm INTEGER NOT NULL DEFAULT 0 CHECK (in_band_confirm IN (0, 1));
ALTER TABLE api_tokens ADD COLUMN expires_at INTEGER CHECK (expires_at IS NULL OR expires_at > created_at);
ALTER TABLE api_tokens ADD COLUMN last_used_at INTEGER;

-- token_id is the id of the access token of the call that made the entry. It is
-- the id that "token list" shows, never the token. It is NULL when the caller had
-- no token: stdio, the web pages and the command line. It is not a foreign key: an
-- entry must stay when anything else about the token changes.
ALTER TABLE audit_log ADD COLUMN token_id TEXT CHECK (token_id IS NULL OR token_id <> '');
