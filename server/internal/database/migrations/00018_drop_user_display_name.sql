-- +goose Up
-- Display names never earned their keep: the UI, the API and the audit log all
-- present the account by username only, so the column was a second source of truth.
ALTER TABLE users DROP COLUMN display_name;

-- +goose Down
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
