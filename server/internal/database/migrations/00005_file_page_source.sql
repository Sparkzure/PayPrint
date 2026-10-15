-- +goose Up
-- How print_files.page_count was obtained: 'detected' (the server counted the PDF
-- itself) or 'declared' (the client told us, because the server could not count it).
--
-- This distinction is load-bearing, not cosmetic. When the requested page range
-- covers the whole document the dispatcher omits page-ranges on purpose, letting
-- CUPS print the entire file (see decodeOrderPrintJob). Combined with a *declared*
-- -- i.e. unverified -- page count, that let a client pay for one page and receive
-- the whole document. Only a count the server produced itself may be trusted for
-- that optimisation.
--
-- Existing rows default to 'declared', which is the conservative side: ranges are
-- always sent, so the optimisation simply does not apply to them.
ALTER TABLE print_files ADD COLUMN page_count_source TEXT NOT NULL DEFAULT 'declared';

-- +goose Down
ALTER TABLE print_files DROP COLUMN page_count_source;
