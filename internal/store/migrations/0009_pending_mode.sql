-- 0009_pending_mode: how the user answers a pending action.
--
-- mode is "in_band" or "approval". The confirm package owns the names.
--
--   - "in_band": the client asks the user in its own chat (an elicitation), and
--     the user answers there. Only that answer can confirm the action. The web
--     page never shows it and cannot approve or reject it.
--   - "approval": the client cannot ask the user, so the user approves or
--     rejects the action on the web page (the approval link).
--
-- One action has one mode, so the same message cannot be approved twice: once
-- in the chat and once on the web page.
--
-- An action that was stored before this column existed gets "in_band". Nobody
-- can say how the user was meant to answer it, so it fails closed: the web page
-- cannot approve it, and it expires.
ALTER TABLE pending_actions
    ADD COLUMN mode TEXT NOT NULL DEFAULT 'in_band' CHECK (mode IN ('in_band', 'approval'));
