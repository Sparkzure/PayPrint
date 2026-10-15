-- +goose Up
-- A ban needs its reason on the record itself.
--
-- The audit log already keeps who did what, but the reason has to be readable
-- where the decision matters: in the account list, and when someone later asks
-- "why was this account locked out".
ALTER TABLE users ADD COLUMN ban_reason TEXT    NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN banned_at  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN banned_by  TEXT    NOT NULL DEFAULT '';

-- The action is called "ban" everywhere now; rename the stored value so a
-- reader never has to know there used to be a second word for it.
UPDATE users SET status = 'banned' WHERE status = 'disabled';

-- +goose Down
UPDATE users SET status = 'disabled' WHERE status = 'banned';
ALTER TABLE users DROP COLUMN banned_by;
ALTER TABLE users DROP COLUMN banned_at;
ALTER TABLE users DROP COLUMN ban_reason;
