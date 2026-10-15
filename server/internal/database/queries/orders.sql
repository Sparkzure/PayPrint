-- An order belongs to the ACCOUNT (user_id). The device that placed it is not part of
-- the order any more: it is an attribute of the account, kept only as a snapshot in
-- order_audits (see migration 00014).
--
-- Two units live on this table and they are not interchangeable: sides_per_copy is
-- printed sides per copy (pages / n_up), while the amount is priced per SHEET and
-- derived from sheets_per_copy (duplex halves the sheet count). Reading the amount
-- off sides_per_copy double-charges every duplex job.
-- name: CreateOrder :one
INSERT INTO orders (
    id, order_no, user_id, printer_id, file_id, status, amount_fen,
    currency, title, params_json, cost_json, sides_per_copy, sheets_per_copy,
    is_test, created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOrderByID :one
SELECT * FROM orders WHERE id = ?;

-- name: GetOrderByNo :one
SELECT * FROM orders WHERE order_no = ?;

-- Ownership is part of the WHERE clause, so a foreign order is indistinguishable
-- from a missing one and cannot be probed.
-- name: GetOrderForUser :one
SELECT * FROM orders WHERE id = ? AND user_id = ?;

-- name: ListOrdersForUser :many
SELECT
    o.id              AS id,
    o.order_no        AS order_no,
    o.status          AS status,
    o.amount_fen      AS amount_fen,
    o.currency        AS currency,
    o.title           AS title,
    o.sides_per_copy  AS sides_per_copy,
    o.sheets_per_copy AS sheets_per_copy,
    o.created_at      AS created_at,
    o.expires_at      AS expires_at,
    o.paid_at         AS paid_at,
    o.completed_at    AS completed_at,
    o.close_reason    AS close_reason,
    p.code            AS printer_code,
    p.name            AS printer_name,
    p.location        AS printer_location,
    f.original_name   AS file_name,
    j.status          AS job_status,
    j.job_state       AS job_state,
    j.cups_job_id     AS cups_job_id,
    j.last_error      AS job_last_error,
    j.attempts        AS job_attempts
FROM orders AS o
JOIN printers AS p ON p.id = o.printer_id
JOIN print_files AS f ON f.id = o.file_id
LEFT JOIN print_jobs AS j ON j.order_id = o.id
WHERE o.user_id = ?
ORDER BY o.created_at DESC
LIMIT ? OFFSET ?;

-- name: GetOrderDetail :one
SELECT
    o.id              AS id,
    o.order_no        AS order_no,
    o.status          AS status,
    o.amount_fen      AS amount_fen,
    o.currency        AS currency,
    o.title           AS title,
    o.params_json     AS params_json,
    o.cost_json       AS cost_json,
    o.sides_per_copy  AS sides_per_copy,
    o.sheets_per_copy AS sheets_per_copy,
    o.created_at      AS created_at,
    o.expires_at      AS expires_at,
    o.paid_at         AS paid_at,
    o.completed_at    AS completed_at,
    o.close_reason    AS close_reason,
    p.id              AS printer_id,
    p.code            AS printer_code,
    p.name            AS printer_name,
    p.location        AS printer_location,
    f.id              AS file_id,
    f.original_name   AS file_name,
    f.page_count      AS file_page_count,
    j.status          AS job_status,
    j.job_state       AS job_state,
    j.cups_job_id     AS cups_job_id,
    j.last_error      AS job_last_error,
    j.attempts        AS job_attempts
FROM orders AS o
JOIN printers AS p ON p.id = o.printer_id
JOIN print_files AS f ON f.id = o.file_id
LEFT JOIN print_jobs AS j ON j.order_id = o.id
WHERE o.id = ? AND o.user_id = ?;

-- name: CountOrdersForUser :one
SELECT COUNT(*) AS total FROM orders WHERE user_id = ?;

-- Pending = still awaiting_payment. The per-user cap (runtime setting
-- max_pending_orders_per_user) counts exactly these: slots free up as orders
-- get paid, cancelled, or expired -- never retroactively.
-- ASCII-only comment: sqlc's SQLite parser rejects non-ASCII comments.
-- name: CountUserPendingOrders :one
SELECT COUNT(*) AS total FROM orders
WHERE user_id = ? AND status = 'awaiting_payment';

-- Two pending orders at the same site may never carry the same amount: a WeChat
-- receipt only carries an amount, so a duplicate would make the match ambiguous
-- and force a manual decision. Order creation checks this inside the write
-- transaction (immediate tx lock) so a racing pair cannot slip through.
-- name: CountPendingOrdersByAmount :one
SELECT COUNT(*) AS total FROM orders
WHERE printer_id = ? AND amount_fen = ? AND status = 'awaiting_payment';

-- Records that the payment report passed the whole validation chain.
-- The order moves to awaiting_release, NOT to a printable state: printing is only
-- opened after the user explicitly confirms (see MarkOrderQueued), so a validated
-- report alone can never produce paper.
-- Only effective while still awaiting_payment, which makes the operation
-- idempotent and guarantees a terminal state is never overwritten.
-- name: MarkOrderPaid :execrows
UPDATE orders
SET status = 'awaiting_release', paid_at = ?, updated_at = ?
WHERE id = ? AND status = 'awaiting_payment';

-- User confirmed printing. Only effective from awaiting_release, so a double tap
-- (or a replayed request) can never produce a second print job.
-- name: MarkOrderQueued :execrows
UPDATE orders
SET status = 'queued', updated_at = ?
WHERE id = ? AND status = 'awaiting_release';

-- Paid but never confirmed: swept by housekeeping so the money does not silently
-- stay on the books while nothing gets printed.
-- name: ListStaleReleaseOrders :many
SELECT * FROM orders
WHERE status = 'awaiting_release' AND paid_at <= ?
ORDER BY paid_at
LIMIT ?;

-- name: CloseAwaitingReleaseOrder :execrows
UPDATE orders
SET status = ?, closed_at = ?, close_reason = ?, updated_at = ?
WHERE id = ? AND status = 'awaiting_release';

-- name: UpdateOrderStatus :exec
UPDATE orders SET status = ?, updated_at = ? WHERE id = ?;

-- Printing finished successfully. Guarded on the in-flight statuses so a late
-- poll can never resurrect an order that has meanwhile been refunded/disputed.
-- name: MarkOrderCompleted :execrows
UPDATE orders
SET status = 'completed', completed_at = ?, updated_at = ?
WHERE id = ? AND status IN ('queued', 'printing');

-- name: CloseOrder :execrows
UPDATE orders
SET status = ?, closed_at = ?, close_reason = ?, updated_at = ?
WHERE id = ? AND status = 'awaiting_payment';

-- name: ListExpiredOrders :many
SELECT * FROM orders
WHERE status = 'awaiting_payment' AND expires_at <= ?
ORDER BY expires_at
LIMIT ?;

-- name: ListRecentOrders :many
SELECT * FROM orders ORDER BY created_at DESC LIMIT ?;

-- Amount matching for collector reports.
--
-- A WeChat receipt carries an amount and a time and usually nothing else, so the
-- target order is the one at that site with the SAME amount that can still absorb
-- the payment:
--   * awaiting_payment and not expired -> the ordinary case;
--   * awaiting_release -> this receipt was already matched, possibly under a
--     different event_id (the listener re-reads the notification list after a
--     restart). Returning it lets the idempotent branch answer, instead of
--     inventing a "no matching order" that reads like lost money.
-- created_before is paid_at + clock skew: an order created after the money arrived
-- cannot be the order that money paid for.
-- name: FindPaymentMatchCandidates :many
SELECT * FROM orders
WHERE printer_id = ?
  AND amount_fen = ?
  AND created_at <= ?
  AND (
        status = 'awaiting_release'
     OR (status = 'awaiting_payment' AND expires_at > ?)
      )
ORDER BY created_at
LIMIT 5;

-- Late receipts: the money arrived outside the order's payment window. No state
-- change happens here; naming the order only turns an opaque "unmatched" into
-- something the operator can refund against.
--
-- Two shapes are covered on purpose: already swept to 'expired', and still sitting
-- in 'awaiting_payment' but past expires_at (the housekeeper has not run yet, or is
-- disabled). Missing the second one would leave the worst case -- money taken,
-- nothing printed -- with no order number attached to it.
-- name: FindExpiredOrdersByAmount :many
SELECT * FROM orders
WHERE printer_id = ?
  AND amount_fen = ?
  AND (
        (status = 'expired' AND closed_at >= ?)
     OR (status = 'awaiting_payment' AND expires_at <= ?)
      )
ORDER BY created_at DESC
LIMIT 5;
