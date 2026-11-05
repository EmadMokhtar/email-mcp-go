-- 0011_audit_calls: a row for every tool call of an agent, with its arguments.
--
-- Until now the table had a row only for what the use cases of the service did:
-- the writes, and the actions of the user. Those rows are "action" rows, and
-- they do not change. A "call" row is one tools/call request of an agent, for
-- every tool and every outcome, also for a read and for a call that never
-- reached the service. So a call that changes something has one call row and
-- one or more action rows.
--
-- kind says which of the two a row is. Every row from before this migration is
-- an action.
--
-- arguments is the sealed (encrypted) JSON of the arguments of the call, and
-- arguments_key_version says which tenant key sealed it. Both are NULL when the
-- call had no arguments. The table makes sure that one is NULL only when the
-- other is, that a sealed value is not empty, and that a key version is at least
-- 1. The arguments can hold personal data, such as search text and a subject.
-- They never hold a message body or attachment data: the MCP layer takes those
-- out before the row is written.
ALTER TABLE audit_log ADD COLUMN kind TEXT NOT NULL DEFAULT 'action' CHECK (kind IN ('action', 'call'));
ALTER TABLE audit_log ADD COLUMN arguments BLOB CHECK (arguments IS NULL OR length(arguments) > 0);
ALTER TABLE audit_log ADD COLUMN arguments_key_version INTEGER CHECK (
    (arguments IS NULL) = (arguments_key_version IS NULL)
    AND (arguments_key_version IS NULL OR arguments_key_version >= 1)
);
