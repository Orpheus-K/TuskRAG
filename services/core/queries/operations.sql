-- name: StorageVersionsPage :many
SELECT id, kb_id, document_id, file_key, content_sha256, byte_size
FROM document_version
WHERE tenant_id = sqlc.arg(tenant_id) AND id > sqlc.arg(after_id)
ORDER BY id LIMIT 100;
