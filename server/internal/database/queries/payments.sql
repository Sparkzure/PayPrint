-- A receipt is tied to the ACCOUNT (user_id), not to a device. The account is known
-- when the report comes from a signed-in user, and once an order is located (the
-- collector and callback paths only learn it at that point). Receipts that never
-- matched an order have no account and keep user_id NULL.
-- name: CreatePaymentEvent :one
INSERT INTO payment_events (
    id, event_id, source, channel, order_id, order_no, user_id, out_trade_no,
    amount_fen, status, reject_reason, reported_at, client_ts, nonce, raw_payload,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPaymentEventByEventID :one
SELECT * FROM payment_events WHERE event_id = ?;

-- name: ListPaymentEventsByOrder :many
SELECT * FROM payment_events WHERE order_id = ? ORDER BY created_at;

-- Operator view: the "money that did not turn into paper" queue.
--
-- Reports land here even when no order could be attributed to them (order_id is
-- NULL), which is exactly why they cannot be found through the order tables.
-- name: ListRecentPaymentEvents :many
SELECT * FROM payment_events
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: ListPaymentEventsByStatus :many
SELECT * FROM payment_events
WHERE status = ?
ORDER BY created_at DESC
LIMIT ? OFFSET ?;

-- name: CreateLedgerEntry :one
INSERT INTO ledger_entries (
    id, order_id, user_id, entry_type, amount_fen, spent_after_fen, channel, memo,
    created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- Net spend so far. Payments are positive and refunds negative, so the sum is exactly
-- "what this account has actually paid"; free prints are written as 0 and never count.
-- name: SumSpentForUser :one
SELECT CAST(COALESCE(SUM(amount_fen), 0) AS INTEGER) AS total_fen
FROM ledger_entries WHERE user_id = ?;

-- name: ListLedgerForUser :many
SELECT * FROM ledger_entries WHERE user_id = ? ORDER BY created_at DESC LIMIT ?;

-- name: GetLedgerEntryByOrderAndType :one
SELECT * FROM ledger_entries WHERE order_id = ? AND entry_type = ?;

-- name: CreateOrderVerification :one
INSERT INTO order_verifications (
    id, order_id, channel, out_trade_no, status, attempts, max_attempts,
    last_error, result, next_retry_at, created_at, updated_at
) VALUES (?, ?, ?, ?, 'pending', 0, ?, '', '', ?, ?, ?)
RETURNING *;

-- name: GetVerificationByOrder :one
SELECT * FROM order_verifications WHERE order_id = ?;

-- name: ListDueVerifications :many
SELECT * FROM order_verifications
WHERE status = 'pending' AND next_retry_at <= ?
ORDER BY next_retry_at
LIMIT ?;

-- name: ClaimVerification :execrows
UPDATE order_verifications
SET status = 'running', attempts = attempts + 1, updated_at = ?
WHERE id = ? AND status = 'pending';

-- name: FinishVerification :execrows
UPDATE order_verifications
SET status = ?, result = ?, last_error = ?, updated_at = ?
WHERE id = ?;

-- name: MarkVerificationRetry :execrows
UPDATE order_verifications
SET status = 'pending', last_error = ?, next_retry_at = ?, updated_at = ?
WHERE id = ?;
