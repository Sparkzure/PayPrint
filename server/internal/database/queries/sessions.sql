-- Keyed by fingerprint rather than device id: the nonce must be consumed as soon
-- as the signature verifies, which happens before a new device has any row to
-- reference. See migration 00006.
-- name: CreateNonce :exec
INSERT INTO device_nonces (fingerprint, nonce, created_at, expires_at)
VALUES (?, ?, ?, ?);

-- name: DeleteExpiredNonces :execrows
DELETE FROM device_nonces WHERE expires_at < ?;

-- Unbinding clears the replay ledger of that fingerprint as well: the device is being
-- handed back as brand new, so it must not inherit the old nonces. What still bounds a
-- replay is the timestamp window plus the signature over the request.
-- name: DeleteNoncesByFingerprint :execrows
DELETE FROM device_nonces WHERE fingerprint = ?;

-- name: CreateSession :one
INSERT INTO sessions (
    id, user_id, device_id, token_hash, issued_at, expires_at, last_used_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = ? WHERE id = ?;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = ? WHERE id = ? AND revoked_at = 0;

-- name: RevokeSessionsByDevice :execrows
UPDATE sessions SET revoked_at = ? WHERE device_id = ? AND revoked_at = 0;

-- name: RevokeSessionsByUser :execrows
UPDATE sessions SET revoked_at = ? WHERE user_id = ? AND revoked_at = 0;

-- Unbinding removes the sessions instead of revoking them: the device is being released
-- back as brand new, so nothing about the old control has to survive -- and a token that
-- is not in the table at all is as dead as one that is revoked. Unlike BlockDevice,
-- which keeps the revoked rows because a suspension can be lifted.
-- name: DeleteSessionsByDevice :execrows
DELETE FROM sessions WHERE device_id = ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at < ?;
