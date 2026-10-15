-- +goose Up
-- Per-sheet pricing: single/duplex x bw/color. Replaces the old per-side pair.
--
-- The old model charged per SIDE (price_bw_fen / price_color_fen). The new model
-- charges per SHEET, which makes duplex pricing explicit instead of deriving it
-- from the side count. Four columns, one for each (duplex, color) combination.
ALTER TABLE printers ADD COLUMN price_single_bw_fen    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE printers ADD COLUMN price_single_color_fen INTEGER NOT NULL DEFAULT 0;
ALTER TABLE printers ADD COLUMN price_duplex_bw_fen    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE printers ADD COLUMN price_duplex_color_fen INTEGER NOT NULL DEFAULT 0;

-- Migration keeps the effective per-side cost identical:
--   * a one-sided sheet is one side  -> per-sheet price == old per-side price;
--   * a duplex sheet is two sides     -> per-sheet price == 2 x old per-side price.
UPDATE printers SET
    price_single_bw_fen    = price_bw_fen,
    price_single_color_fen = price_color_fen,
    price_duplex_bw_fen    = price_bw_fen * 2,
    price_duplex_color_fen = price_color_fen * 2;

ALTER TABLE printers DROP COLUMN price_bw_fen;
ALTER TABLE printers DROP COLUMN price_color_fen;

-- Per-account free printing. When set, this account's orders skip payment
-- entirely and go straight to awaiting_release (the user still confirms printing;
-- the "user confirms before paper comes out" rule is not weakened).
ALTER TABLE users ADD COLUMN free_print INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN free_print;

ALTER TABLE printers ADD COLUMN price_bw_fen    INTEGER NOT NULL DEFAULT 0;
ALTER TABLE printers ADD COLUMN price_color_fen INTEGER NOT NULL DEFAULT 0;

-- Best-effort round trip: the one-sided price maps back to the old per-side price.
UPDATE printers SET
    price_bw_fen    = price_single_bw_fen,
    price_color_fen = price_single_color_fen;

ALTER TABLE printers DROP COLUMN price_single_bw_fen;
ALTER TABLE printers DROP COLUMN price_single_color_fen;
ALTER TABLE printers DROP COLUMN price_duplex_bw_fen;
ALTER TABLE printers DROP COLUMN price_duplex_color_fen;
