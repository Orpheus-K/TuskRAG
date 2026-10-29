-- name: ClaimedPurgeJobMatches :one
SELECT EXISTS (SELECT 1 FROM job WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id)
  AND kind = 'purge' AND payload = sqlc.arg(payload)::jsonb);

-- name: LockPurgeDocument :one
SELECT current_version_id, COALESCE(deleted_at IS NOT NULL, false)::boolean AS deleted FROM document
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND id = sqlc.arg(document_id)
FOR UPDATE;

-- name: ReadPurgeDocumentDeleted :one
SELECT COALESCE(deleted_at IS NOT NULL, false)::boolean AS deleted FROM document
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND id = sqlc.arg(document_id);

-- name: LockPurgeVersion :one
SELECT status, file_key FROM document_version
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
FOR UPDATE;

-- name: PurgeHasCurrentRows :one
SELECT EXISTS (SELECT 1 FROM chunk c WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id) AND c.is_current)
OR EXISTS (SELECT 1 FROM chunk_vector v JOIN chunk c ON c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
  WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
    AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id) AND v.is_current) AS has_current;

-- name: MarkVersionPurging :execrows
UPDATE document_version AS v SET status = 'purging'
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id) AND v.document_id = sqlc.arg(document_id)
  AND v.id = sqlc.arg(version_id) AND (v.status IN ('superseded', 'purging') OR EXISTS (
    SELECT 1 FROM document d WHERE d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id
      AND d.id = v.document_id AND d.deleted_at IS NOT NULL
  ));

-- name: DeletePurgePlan :exec
DELETE FROM ingest_plan WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);

-- name: DeletePurgeChunks :exec
DELETE FROM chunk WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);

-- name: DeletePurgeSections :exec
DELETE FROM section WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);

-- name: DeletePurgeArtifact :exec
DELETE FROM parse_artifact WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);

-- name: DeletePurgeVersion :execrows
DELETE FROM document_version WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id) AND status = 'purging';

-- name: AuditPurgedVersion :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result)
VALUES (sqlc.arg(tenant_id), 0, 'document.purge', 'document_version', sqlc.arg(version_id), 'allow');
