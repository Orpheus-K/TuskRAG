-- name: LockPublicationDocument :one
SELECT d.current_version_id
FROM document d JOIN knowledge_base k ON k.tenant_id = d.tenant_id AND k.id = d.kb_id
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id) AND d.id = sqlc.arg(document_id)
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND k.status = 'active'
FOR UPDATE OF d;

-- name: LockPublicationSections :many
SELECT s.id, s.ordinal, COALESCE(parent.ordinal, 0)::integer AS parent_ordinal,
       s.level, s.heading, s.heading_path, s.page_start, s.page_end, s.token_count,
       sha256(convert_to(s.body_md, 'UTF8'))::bytea AS body_hash
FROM section s LEFT JOIN section parent
  ON parent.tenant_id = s.tenant_id AND parent.kb_id = s.kb_id AND parent.document_id = s.document_id
  AND parent.version_id = s.version_id AND parent.id = s.parent_section_id
WHERE s.tenant_id = sqlc.arg(tenant_id) AND s.kb_id = sqlc.arg(kb_id)
  AND s.document_id = sqlc.arg(document_id) AND s.version_id = sqlc.arg(version_id)
ORDER BY s.ordinal FOR SHARE OF s;

-- name: LockPublicationChunks :many
SELECT c.id, c.ordinal, s.ordinal AS section_ordinal, c.kind, c.token_count, c.page_start, c.page_end,
       c.is_current, c.disabled, c.access_tags,
       sha256(convert_to(c.content, 'UTF8'))::bytea AS content_hash,
       sha256(convert_to(c.search_text, 'UTF8'))::bytea AS search_hash,
       (c.doc_tags = d.tags AND c.doc_mime = d.mime AND c.doc_updated_at = v.created_at)::boolean AS metadata_current
FROM chunk c
JOIN section s ON s.tenant_id = c.tenant_id AND s.kb_id = c.kb_id AND s.document_id = c.document_id
  AND s.version_id = c.version_id AND s.id = c.section_id
JOIN document d ON d.tenant_id = c.tenant_id AND d.kb_id = c.kb_id AND d.id = c.document_id
JOIN document_version v ON v.tenant_id = c.tenant_id AND v.kb_id = c.kb_id AND v.document_id = c.document_id AND v.id = c.version_id
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id)
ORDER BY c.ordinal FOR UPDATE OF c;

-- name: LockPublicationVectors :many
SELECT v.chunk_id, v.is_current, v.access_tags,
       (NOT sqlc.arg(normalized)::boolean OR abs(l2_norm(v.embedding) - 1.0) <= 0.005)::boolean AS norm_valid
FROM chunk_vector v JOIN chunk c ON c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id)
  AND v.space_id = sqlc.arg(space_id)
ORDER BY v.chunk_id FOR UPDATE OF v;

-- name: LockPreviousVersion :one
SELECT status FROM document_version
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(previous_version_id)
FOR UPDATE;

-- name: PublicationCurrentConsistent :one
SELECT NOT EXISTS (
  SELECT 1 FROM chunk c WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
    AND c.document_id = sqlc.arg(document_id) AND c.is_current
    AND (c.version_id <> sqlc.arg(previous_version_id)::bigint OR c.disabled
      OR c.access_tags <> ARRAY['kb:' || c.kb_id::text || ':all'])
) AND NOT EXISTS (
  SELECT 1 FROM chunk_vector v JOIN chunk c ON c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
  WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id) AND c.document_id = sqlc.arg(document_id)
    AND v.is_current AND (c.version_id <> sqlc.arg(previous_version_id)::bigint OR NOT c.is_current OR v.access_tags <> c.access_tags)
) AND NOT EXISTS (
  SELECT 1 FROM chunk c WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
    AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(previous_version_id)::bigint
    AND NOT c.disabled AND (NOT c.is_current OR NOT EXISTS (
      SELECT 1 FROM chunk_vector v WHERE v.tenant_id = c.tenant_id AND v.kb_id = c.kb_id AND v.chunk_id = c.id
        AND v.space_id = sqlc.arg(space_id) AND v.is_current AND v.access_tags = c.access_tags
    ))
) AND (sqlc.arg(previous_version_id)::bigint = 0 OR EXISTS (
  SELECT 1 FROM ingest_plan p WHERE p.tenant_id = sqlc.arg(tenant_id) AND p.kb_id = sqlc.arg(kb_id)
    AND p.document_id = sqlc.arg(document_id) AND p.version_id = sqlc.arg(previous_version_id)::bigint
    AND p.section_count = (SELECT count(*) FROM section s WHERE s.tenant_id = p.tenant_id AND s.kb_id = p.kb_id
      AND s.document_id = p.document_id AND s.version_id = p.version_id)
    AND p.chunk_count = (SELECT count(*) FROM chunk c WHERE c.tenant_id = p.tenant_id AND c.kb_id = p.kb_id
      AND c.document_id = p.document_id AND c.version_id = p.version_id)
)) AS consistent;

-- name: DeactivatePreviousVectors :execrows
UPDATE chunk_vector v SET is_current = false
FROM chunk c WHERE c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
  AND c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id) AND c.document_id = sqlc.arg(document_id)
  AND c.version_id = sqlc.arg(previous_version_id) AND v.is_current;

-- name: DeactivatePreviousChunks :execrows
UPDATE chunk SET is_current = false WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(previous_version_id) AND is_current;

-- name: SupersedePreviousVersion :execrows
UPDATE document_version SET status = 'superseded'
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND document_id = sqlc.arg(document_id)
  AND id = sqlc.arg(previous_version_id) AND status IN ('ready', 'partial');

-- name: SupersedePendingVersion :execrows
UPDATE document_version SET status = 'superseded'
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND document_id = sqlc.arg(document_id)
  AND id = sqlc.arg(version_id) AND status = 'indexing';

-- name: ActivatePublicationChunks :execrows
UPDATE chunk SET is_current = true WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id) AND NOT is_current AND NOT disabled;

-- name: ActivatePublicationVectors :execrows
UPDATE chunk_vector v SET is_current = true
FROM chunk c WHERE c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
  AND c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id) AND c.document_id = sqlc.arg(document_id)
  AND c.version_id = sqlc.arg(version_id) AND c.is_current AND v.space_id = sqlc.arg(space_id)
  AND NOT v.is_current AND v.access_tags = c.access_tags;

-- name: MarkPublicationReady :execrows
UPDATE document_version SET status = 'ready'
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND document_id = sqlc.arg(document_id)
  AND id = sqlc.arg(version_id) AND status = 'indexing';

-- name: SetPublicationCurrent :execrows
UPDATE document SET current_version_id = sqlc.arg(version_id)
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND id = sqlc.arg(document_id)
  AND COALESCE(current_version_id, 0) = sqlc.arg(previous_version_id)::bigint;
