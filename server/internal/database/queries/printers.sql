-- name: ListPrinters :many
SELECT * FROM printers WHERE status != 'disabled' ORDER BY sort_order, code;

-- name: ListAllPrinters :many
SELECT * FROM printers ORDER BY sort_order, code;

-- name: GetPrinterByID :one
SELECT * FROM printers WHERE id = ?;

-- name: GetPrinterByCode :one
SELECT * FROM printers WHERE code = ?;

-- name: UpsertPrinter :one
INSERT INTO printers (
    id, code, name, cups_uri, location, status,
    price_single_bw_fen, price_single_color_fen, price_duplex_bw_fen, price_duplex_color_fen,
    supports_color, supports_duplex, paper_sizes, max_copies, sort_order,
    online_windows, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (code) DO UPDATE SET
    name                   = excluded.name,
    cups_uri               = excluded.cups_uri,
    location               = excluded.location,
    status                 = excluded.status,
    price_single_bw_fen    = excluded.price_single_bw_fen,
    price_single_color_fen = excluded.price_single_color_fen,
    price_duplex_bw_fen    = excluded.price_duplex_bw_fen,
    price_duplex_color_fen = excluded.price_duplex_color_fen,
    supports_color         = excluded.supports_color,
    supports_duplex        = excluded.supports_duplex,
    paper_sizes            = excluded.paper_sizes,
    max_copies             = excluded.max_copies,
    sort_order             = excluded.sort_order,
    online_windows         = excluded.online_windows,
    updated_at             = excluded.updated_at
RETURNING *;

-- name: SetPrinterStatus :exec
UPDATE printers SET status = ?, updated_at = ? WHERE id = ?;

-- Cascade hard delete of a site.
--
-- orders / print_jobs / collector_credentials all carry a foreign key to printers,
-- and ledger entries / payment events / verifications hang off orders, so the
-- children must be removed before the printer itself. The whole set runs inside
-- one transaction (see PrinterService.Delete).
--
-- WARNING: this destroys historical orders and accounting for the site. It is
-- deliberately explicit (operator types the site code in the console); the counts
-- are written to the audit log so the damage is traceable afterwards.
-- name: DeletePrintJobsByPrinter :execrows
DELETE FROM print_jobs WHERE printer_id = ?;

-- name: DeleteLedgerEntriesByPrinter :execrows
DELETE FROM ledger_entries WHERE order_id IN (SELECT id FROM orders WHERE printer_id = ?);

-- name: DeletePaymentEventsByPrinter :execrows
DELETE FROM payment_events WHERE order_id IN (SELECT id FROM orders WHERE printer_id = ?);

-- name: DeleteOrderVerificationsByPrinter :execrows
DELETE FROM order_verifications WHERE order_id IN (SELECT id FROM orders WHERE printer_id = ?);

-- name: DeleteOrdersByPrinter :execrows
DELETE FROM orders WHERE printer_id = ?;

-- name: DeleteCollectorCredentialsByPrinter :execrows
DELETE FROM collector_credentials WHERE printer_id = ?;

-- name: DeletePrinter :execrows
DELETE FROM printers WHERE id = ?;
