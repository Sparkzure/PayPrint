-- Runtime-editable settings. Only overridden keys exist as rows.

-- name: ListSettings :many
SELECT * FROM app_settings ORDER BY key;

-- name: GetSetting :one
SELECT * FROM app_settings WHERE key = ?;

-- name: UpsertSetting :exec
INSERT INTO app_settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: DeleteSetting :exec
DELETE FROM app_settings WHERE key = ?;
