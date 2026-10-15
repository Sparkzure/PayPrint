-- page_count_source records whether page_count was counted by the server
-- ('detected') or merely declared by the client ('declared'). The dispatcher only
-- trusts 'detected' for the "print the whole document" shortcut.
-- name: CreatePrintFile :one
INSERT INTO print_files (
    id, user_id, original_name, content_type, size_bytes, sha256,
    storage_path, page_count, page_count_source, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPrintFileForUser :one
SELECT * FROM print_files WHERE id = ? AND user_id = ?;

-- name: SetPrintFilePageCount :exec
UPDATE print_files SET page_count = ? WHERE id = ?;

-- name: ListPrintFilesByUser :many
SELECT * FROM print_files WHERE user_id = ? ORDER BY created_at DESC LIMIT ?;
