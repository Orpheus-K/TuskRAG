-- name: AllocateDocumentIDs :one
SELECT nextval('document_id_seq')::bigint AS document_id,
       nextval('document_version_id_seq')::bigint AS version_id;

-- name: AllocateDocumentVersionID :one
SELECT nextval('document_version_id_seq')::bigint AS version_id;

-- name: GetVisibleVersionUploadTarget :one
SELECT d.title, d.mime, k.status, m.role
FROM document d
JOIN knowledge_base k ON k.tenant_id = d.tenant_id AND k.id = d.kb_id
JOIN kb_membership m ON m.tenant_id = d.tenant_id AND m.kb_id = d.kb_id
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id)
  AND d.id = sqlc.arg(document_id) AND m.user_id = sqlc.arg(user_id)
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb';

-- Lock the document before the knowledge base, matching publication order.
-- name: LockEditableDocument :one
SELECT d.title, d.mime
FROM document d JOIN kb_membership m ON m.tenant_id = d.tenant_id AND m.kb_id = d.kb_id
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id)
  AND d.id = sqlc.arg(document_id) AND m.user_id = sqlc.arg(user_id)
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb'
FOR UPDATE OF d;

-- name: DuplicateDocumentVersion :one
SELECT EXISTS (
  SELECT 1 FROM document_version v
  WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.document_id = sqlc.arg(document_id)
    AND v.content_sha256 = sqlc.arg(content_sha256)
    AND v.confirm_first_row_headers = sqlc.arg(confirm_first_row_headers)
);

-- name: DuplicateOtherDocumentContent :one
SELECT EXISTS (
  SELECT 1 FROM document_version v JOIN document d
    ON d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id
  WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
    AND v.document_id <> sqlc.arg(document_id) AND v.content_sha256 = sqlc.arg(content_sha256)
    AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb'
);

-- name: DuplicateKnowledgeContent :one
SELECT EXISTS (
  SELECT 1 FROM document_version v JOIN document d
    ON d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id
  WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
    AND v.content_sha256 = sqlc.arg(content_sha256) AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb'
);

-- name: CreateDocument :exec
INSERT INTO document (id, tenant_id, kb_id, title, mime)
OVERRIDING SYSTEM VALUE
VALUES (sqlc.arg(document_id), sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(title), sqlc.arg(mime));

-- name: CreateDocumentVersion :one
INSERT INTO document_version (id, tenant_id, kb_id, document_id, content_sha256, byte_size, file_key, status, created_by, confirm_first_row_headers)
OVERRIDING SYSTEM VALUE
VALUES (sqlc.arg(version_id), sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(document_id),
        sqlc.arg(content_sha256), sqlc.arg(byte_size), sqlc.arg(file_key), 'queued', sqlc.arg(user_id), sqlc.arg(confirm_first_row_headers))
RETURNING created_at;

-- name: GetVisibleDocumentVersion :one
SELECT d.id AS document_id, v.id AS version_id, d.title, d.mime, v.status, v.byte_size, v.created_at
FROM document d JOIN document_version v
  ON v.tenant_id = d.tenant_id AND v.kb_id = d.kb_id AND v.document_id = d.id
JOIN kb_membership m ON m.tenant_id = d.tenant_id AND m.kb_id = d.kb_id
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id)
  AND v.id = sqlc.arg(version_id) AND m.user_id = sqlc.arg(user_id)
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb';

-- name: ListVisibleDocuments :many
SELECT d.id, d.title, d.mime, d.current_version_id, COALESCE(current_version.status, '')::text AS current_status,
       v.id AS latest_version_id, v.status AS latest_status, v.byte_size,
       v.failure_code, v.created_at AS latest_created_at, COALESCE(latest_job.id, 0)::bigint AS latest_job_id,
       COALESCE(latest_job.status, '')::text AS latest_job_status, latest_job.last_error AS latest_job_error,
       COALESCE(latest_job.payload = jsonb_build_object('kb_id', d.kb_id, 'document_id', d.id, 'version_id', v.id), false)::boolean AS latest_job_matches,
       k.status AS kb_status
FROM document d
JOIN knowledge_base k ON k.tenant_id = d.tenant_id AND k.id = d.kb_id
LEFT JOIN document_version current_version
  ON current_version.tenant_id = d.tenant_id AND current_version.document_id = d.id AND current_version.id = d.current_version_id
LEFT JOIN LATERAL (
  SELECT id, status, byte_size, failure_code, created_at
  FROM document_version
  WHERE tenant_id = d.tenant_id AND kb_id = d.kb_id AND document_id = d.id
  ORDER BY id DESC LIMIT 1
) v ON true
LEFT JOIN LATERAL (
  SELECT id, status, last_error, payload FROM job
  WHERE tenant_id = d.tenant_id AND kind = 'ingest'
    AND dedupe_key = 'document-version:' || v.id::text
  ORDER BY id DESC LIMIT 1
) latest_job ON true
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id)
  AND d.id > sqlc.arg(after_id) AND d.deleted_at IS NULL
  AND (d.acl_mode = 'inherit_kb' OR EXISTS (
    SELECT 1 FROM document_acl a
    WHERE a.tenant_id = d.tenant_id AND a.kb_id = d.kb_id AND a.document_id = d.id
      AND (a.subject_type = 'user' AND a.subject_id = sqlc.arg(user_id)
        OR a.subject_type = 'group' AND a.subject_id IN (
          SELECT gm.group_id FROM user_group_member gm
          WHERE gm.tenant_id = d.tenant_id AND gm.user_id = sqlc.arg(user_id)
        ))
  ))
ORDER BY d.id
LIMIT sqlc.arg(fetch_limit)
FOR SHARE OF d;

-- name: ListVisibleDocumentsFiltered :many
SELECT d.id, d.title, d.mime, d.current_version_id, COALESCE(current_version.status, '')::text AS current_status,
       v.id AS latest_version_id, v.status AS latest_status, v.byte_size,
       v.failure_code, v.created_at AS latest_created_at, COALESCE(latest_job.id, 0)::bigint AS latest_job_id,
       COALESCE(latest_job.status, '')::text AS latest_job_status, latest_job.last_error AS latest_job_error,
       COALESCE(latest_job.payload = jsonb_build_object('kb_id', d.kb_id, 'document_id', d.id, 'version_id', v.id), false)::boolean AS latest_job_matches,
       k.status AS kb_status
FROM document d
JOIN knowledge_base k ON k.tenant_id = d.tenant_id AND k.id = d.kb_id
LEFT JOIN document_version current_version
  ON current_version.tenant_id = d.tenant_id AND current_version.document_id = d.id AND current_version.id = d.current_version_id
LEFT JOIN LATERAL (
  SELECT id, status, byte_size, failure_code, created_at
  FROM document_version
  WHERE tenant_id = d.tenant_id AND kb_id = d.kb_id AND document_id = d.id
  ORDER BY id DESC LIMIT 1
) v ON true
LEFT JOIN LATERAL (
  SELECT id, status, last_error, payload FROM job
  WHERE tenant_id = d.tenant_id AND kind = 'ingest'
    AND dedupe_key = 'document-version:' || v.id::text
  ORDER BY id DESC LIMIT 1
) latest_job ON true
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id)
  AND d.id > sqlc.arg(after_id) AND d.deleted_at IS NULL
  AND (d.acl_mode = 'inherit_kb' OR EXISTS (
    SELECT 1 FROM document_acl a
    WHERE a.tenant_id = d.tenant_id AND a.kb_id = d.kb_id AND a.document_id = d.id
      AND (a.subject_type = 'user' AND a.subject_id = sqlc.arg(user_id)
        OR a.subject_type = 'group' AND a.subject_id IN (
          SELECT gm.group_id FROM user_group_member gm
          WHERE gm.tenant_id = d.tenant_id AND gm.user_id = sqlc.arg(user_id)
        ))
  ))
  AND (sqlc.arg(query)::text = '' OR d.title ILIKE '%' || sqlc.arg(query)::text || '%')
  AND (cardinality(sqlc.arg(tags)::text[]) = 0 OR d.tags && sqlc.arg(tags)::text[])
  AND (sqlc.arg(status)::text = '' OR v.status = sqlc.arg(status)::text)
ORDER BY d.id
LIMIT sqlc.arg(fetch_limit)
FOR SHARE OF d;

-- name: LockDocumentVersionForRetry :one
SELECT v.status, v.failure_code,
  COALESCE(v.id = (SELECT max(other.id) FROM document_version other
    WHERE other.tenant_id = v.tenant_id AND other.kb_id = v.kb_id AND other.document_id = v.document_id), false)::boolean AS is_latest
FROM document_version v
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id) AND v.id = sqlc.arg(version_id)
FOR UPDATE OF v;

-- name: QueueFailedDocumentVersion :execrows
UPDATE document_version SET status = 'queued', failure_code = NULL, failure_detail = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
  AND status = 'failed' AND failure_code = sqlc.arg(failure_code);

-- name: AuditDocumentAction :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(action), 'document', sqlc.arg(document_id), sqlc.arg(result));

-- name: LockDocumentVersionForCancel :one
SELECT v.id FROM document_version v JOIN document d
  ON d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id) AND v.id = sqlc.arg(version_id)
  AND v.status IN ('queued', 'parsing', 'embedding', 'indexing')
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb'
FOR UPDATE OF v FOR SHARE OF d;

-- name: CancelDocumentVersion :execrows
UPDATE document_version SET status = 'failed', failure_code = 'cancelled', failure_detail = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
  AND status IN ('queued', 'parsing', 'embedding', 'indexing');

-- name: LockDocumentForPurgeControl :one
SELECT (d.deleted_at IS NOT NULL)::boolean AS deleted
FROM document d JOIN kb_membership m ON m.tenant_id = d.tenant_id AND m.kb_id = d.kb_id
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id)
  AND d.id = sqlc.arg(document_id) AND m.user_id = sqlc.arg(user_id)
  AND d.acl_mode = 'inherit_kb'
FOR UPDATE OF d;

-- name: LockDocumentVersionForPurgeRetry :one
SELECT v.status,
       (EXISTS (SELECT 1 FROM document d WHERE d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id AND d.current_version_id = v.id))::boolean AS is_current
FROM document_version v
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id) AND v.id = sqlc.arg(version_id)
FOR UPDATE OF v;
