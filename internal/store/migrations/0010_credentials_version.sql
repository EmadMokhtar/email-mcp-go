-- 0010_credentials_version: a counter of the changes of the password and the
-- pins of an account.
--
-- A reconnect (store.ReconnectAccount) adds one each time it saves. A call that
-- failed because the mail server refused the stored login marks the account as
-- needing a reconnect only while the counter is still the value that the call
-- read before it got its mail backend (store.MarkNeedsReauth). So a call that
-- started on the old password or pins, before a reconnect, cannot undo that
-- reconnect. An account that was stored before this column existed starts at 0.
ALTER TABLE accounts
    ADD COLUMN credentials_version INTEGER NOT NULL DEFAULT 0 CHECK (credentials_version >= 0);
