-- name: CreateUser :one
INSERT INTO users (
    id, username, username_ci, password_hash, phone, role, status,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByUsernameCI :one
SELECT * FROM users WHERE username_ci = ?;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?;

-- Banning records the reason on the row, not only in the audit log: the account
-- list has to show "why", and that question outlives whoever did the banning.
-- name: BanUser :exec
UPDATE users
SET status = 'banned', ban_reason = ?, banned_at = ?, banned_by = ?, updated_at = ?
WHERE id = ?;

-- name: UnbanUser :exec
UPDATE users
SET status = 'active', ban_reason = '', banned_at = 0, banned_by = '', updated_at = ?
WHERE id = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY created_at DESC LIMIT ?;

-- Free printing is a per-account switch an operator flips from the console: the
-- account's orders skip payment and go straight to awaiting_release.
-- name: SetUserFreePrint :exec
UPDATE users SET free_print = ?, updated_at = ? WHERE id = ?;

-- Spend per account, for the console's account list: one aggregate instead of one
-- query per row. Accounts without ledger entries simply do not appear.
-- name: ListSpentByUser :many
SELECT user_id,
       CAST(COALESCE(SUM(amount_fen), 0) AS INTEGER) AS spent_fen
FROM ledger_entries
GROUP BY user_id;

-- name: CountUsers :one
SELECT COUNT(*) AS total FROM users;

-- name: CountUsersByRole :one
SELECT COUNT(*) AS total FROM users WHERE role = ?;

-- Guards against disabling the last usable administrator: without it the
-- console could be locked out permanently (there is no out-of-band reset).
-- name: CountActiveUsersByRole :one
SELECT COUNT(*) AS total FROM users WHERE role = ? AND status = 'active';
