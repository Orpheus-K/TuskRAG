-- name: ListDocumentVersionsForDeletion :many
SELECT v.id, EXISTS (
  SELECT 1
  FROM job j
  WHERE j.tenant_id = v.tenant_id AND j.kind = 'purge'
    AND j.dedupe_key = 'version:' || v.id::text AND j.status IN ('queued', 'running')
)::boolean AS has_active_purge, NOT EXISTS (
  SELECT 1 FROM job j
  WHERE j.tenant_id = v.tenant_id AND j.kind = 'purge'
    AND j.dedupe_key = 'version:' || v.id::text AND j.status IN ('queued', 'running')
    AND (j.payload IS DISTINCT FROM jsonb_build_object('kb_id', v.kb_id, 'document_id', v.document_id, 'version_id', v.id)
      OR j.max_attempts <> 3)
)::boolean AS active_purge_valid
FROM document_version v
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id)
ORDER BY v.id LIMIT 10001;

-- name: DeactivateDeletedDocumentVectors :execrows
UPDATE chunk_vector v SET is_current = false
FROM chunk c WHERE c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
  AND c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND v.is_current;

-- name: DeactivateDeletedDocumentChunks :execrows
UPDATE chunk SET is_current = false
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND is_current;

-- name: MarkDocumentDeleted :execrows
UPDATE document SET deleted_at = clock_timestamp(), current_version_id = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND id = sqlc.arg(document_id) AND deleted_at IS NULL AND acl_mode = 'inherit_kb';
