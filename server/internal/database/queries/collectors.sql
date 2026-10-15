-- Collection credentials. The payment listener is an API client, not a user
-- account, so there is no login, no password and no device binding here -- just a
-- token bound to one site. See migration 00007.

-- name: CreateCollectorCredential :one
INSERT INTO collector_credentials (
    id, printer_id, label, token_hash, created_at
) VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- Lookup path for every report: hash the presented token, find the row, check it is
-- not revoked and not expired by omission (credentials do not expire).
-- name: GetCollectorCredentialByTokenHash :one
SELECT * FROM collector_credentials WHERE token_hash = ?;

-- name: ListCollectorCredentials :many
SELECT * FROM collector_credentials ORDER BY created_at DESC LIMIT ?;

-- Revocation is a soft delete: "this credential existed and was revoked on X" is
-- exactly what an operator needs when a report is disputed later.
-- name: RevokeCollectorCredential :execrows
UPDATE collector_credentials
SET revoked_at = ?, revoke_reason = ?
WHERE id = ? AND revoked_at = 0;

-- name: TouchCollectorCredential :exec
UPDATE collector_credentials SET last_used_at = ? WHERE id = ?;

-- Hard delete. Revocation keeps the row for later dispute review; this removes it
-- entirely (e.g. a credential created by mistake for the wrong site). Audit log
-- still records who deleted it and why.
-- name: DeleteCollectorCredential :execrows
DELETE FROM collector_credentials WHERE id = ?;
