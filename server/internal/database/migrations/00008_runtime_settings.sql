-- +goose Up
-- Runtime-editable settings.
--
-- Only the keys an operator actually overrode are stored here; everything else
-- keeps following the environment variable / built-in default. That makes the
-- source of the effective value unambiguous ("did I set this here, or is it
-- still the .env value?") and means adding a new setting needs no data
-- migration.
--
-- Deliberately NOT a place for secrets or for values that must stay pinned for
-- the life of a process: ADMIN_TOKEN stays in the environment (it is the
-- bootstrap channel), and the signing window / nonce retention stay there too
-- (loosening them at runtime would weaken replay protection, and the two are
-- coupled by a startup-time invariant).
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT    PRIMARY KEY,
    value      TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS app_settings;
