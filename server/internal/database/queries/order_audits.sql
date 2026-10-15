-- The order journal: one immutable record per order, written in the same transaction
-- that creates the order (an order without its audit row must not exist). The console
-- previews the newest entries and exports the whole thing as CSV.
--
-- Every column is a snapshot taken at checkout, and the table has no foreign keys:
-- releasing a device, deleting a site (which cascades to its orders) or renaming an
-- account must never change what the audit says happened.
-- name: CreateOrderAudit :exec
INSERT INTO order_audits (
    id, order_id, order_no, user_id, username, device_label, device_fingerprint,
    amount_fen, file_name, color_mode, duplex, copies, total_pages, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- Newest first, for the console preview and for paged streaming of the CSV export.
-- id is the tiebreaker: several orders can share a timestamp second.
-- name: ListOrderAudits :many
SELECT * FROM order_audits
ORDER BY created_at DESC, id DESC
LIMIT ? OFFSET ?;

-- name: CountOrderAudits :one
SELECT COUNT(*) AS total FROM order_audits;
