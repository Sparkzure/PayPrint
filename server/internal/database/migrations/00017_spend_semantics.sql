-- +goose Up
-- Accounts do not hold money: they pay per order, so the only meaningful number is how
-- much an account has spent so far. Two columns kept the old "balance" wording alive.

-- 1. ledger_entries.balance_fen stored the running total AFTER the entry was appended,
--    which makes it the cumulative spend at that point, not a balance. Renamed so the
--    name matches what the writer actually computes.
ALTER TABLE ledger_entries RENAME COLUMN balance_fen TO spent_after_fen;

-- 2. users.balance_fen was never written after creation (every reader derives the
--    number from the ledger), so the column only served to mislead.
ALTER TABLE users DROP COLUMN balance_fen;

-- +goose Down
ALTER TABLE users ADD COLUMN balance_fen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ledger_entries RENAME COLUMN spent_after_fen TO balance_fen;
