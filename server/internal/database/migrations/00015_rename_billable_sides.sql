-- +goose Up
-- orders.billable_sides is a leftover name from the era when the amount was
-- computed per printed side. Pricing moved to per sheet (migration 00010) and the
-- column has held sides-per-copy ever since -- migration 00014 reads it as
-- "sides * copies = total pages" when it backfills the audit trail. The old name
-- suggests it is the amount basis, which is now sheets_per_copy, so the two units
-- sit side by side under a name that invites the wrong arithmetic. Rename only:
-- the value and its meaning are unchanged.
ALTER TABLE orders RENAME COLUMN billable_sides TO sides_per_copy;

-- +goose Down
ALTER TABLE orders RENAME COLUMN sides_per_copy TO billable_sides;
