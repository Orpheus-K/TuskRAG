-- name: LockReadableDocument :one
SELECT d.id, d.kb_id, d.title, d.mime, d.current_version_id
FROM document d
WHERE d.tenant_id=sqlc.arg(tenant_id) AND d.id=sqlc.arg(document_id)
  AND d.deleted_at IS NULL
  AND (d.acl_mode='inherit_kb' OR EXISTS (
    SELECT 1 FROM document_acl a
    WHERE a.tenant_id = d.tenant_id AND a.kb_id = d.kb_id AND a.document_id = d.id
      AND (a.subject_type = 'user' AND a.subject_id = sqlc.arg(user_id)
        OR a.subject_type = 'group' AND a.subject_id IN (
          SELECT gm.group_id FROM user_group_member gm
          WHERE gm.tenant_id = d.tenant_id AND gm.user_id = sqlc.arg(user_id)
        ))
  ))
  AND EXISTS (SELECT 1 FROM kb_membership m WHERE m.tenant_id=d.tenant_id AND m.kb_id=d.kb_id AND m.user_id=sqlc.arg(user_id))
FOR SHARE OF d;

-- name: LockReadableKnowledge :one
SELECT id FROM knowledge_base
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(kb_id) AND status='active'
FOR SHARE;

-- name: LockReadableVersion :one
SELECT id FROM document_version
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id)
  AND document_id=sqlc.arg(document_id) AND id=sqlc.arg(version_id) AND status IN ('ready','partial')
FOR SHARE;

-- name: GetSectionDocument :one
SELECT document_id FROM section WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(section_id);

-- name: GetReadablePlan :one
SELECT section_count, chunk_count FROM ingest_plan
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id)
  AND document_id=sqlc.arg(document_id) AND version_id=sqlc.arg(version_id);

-- name: CountReadableSections :one
SELECT count(*)::bigint FROM section
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id)
  AND document_id=sqlc.arg(document_id) AND version_id=sqlc.arg(version_id);

-- name: LockReadableChunks :many
SELECT c.section_id, (c.is_current AND NOT c.disabled)::boolean AS allowed
FROM chunk c
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.kb_id=sqlc.arg(kb_id)
  AND c.document_id=sqlc.arg(document_id) AND c.version_id=sqlc.arg(version_id)
ORDER BY c.id LIMIT 10001 FOR SHARE OF c;

-- name: ListReadableSections :many
SELECT s.id, s.parent_section_id, s.ordinal, s.level, s.page_start, s.page_end, s.summary,
  octet_length(s.heading)::integer AS heading_bytes,
  octet_length(array_to_string(s.heading_path, ''))::integer AS path_bytes,
  (CASE WHEN octet_length(s.heading) <= 8192 THEN s.heading ELSE '' END)::text AS heading,
  (CASE WHEN octet_length(array_to_string(s.heading_path, '')) <= 16384 THEN s.heading_path ELSE ARRAY[]::text[] END)::text[] AS heading_path
FROM section s
WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.kb_id=sqlc.arg(kb_id)
  AND s.document_id=sqlc.arg(document_id) AND s.version_id=sqlc.arg(version_id)
  AND s.ordinal > sqlc.arg(after_ordinal)
ORDER BY s.ordinal LIMIT sqlc.arg(fetch_limit) FOR SHARE OF s;

-- name: LockReadableSection :one
SELECT s.id, s.parent_section_id, s.ordinal, s.level, s.page_start, s.page_end, s.token_count,
  octet_length(s.heading)::integer AS heading_bytes,
  octet_length(array_to_string(s.heading_path, ''))::integer AS path_bytes,
  octet_length(s.body_md)::integer AS body_bytes,
  (CASE WHEN octet_length(s.heading) <= 8192 THEN s.heading ELSE '' END)::text AS heading,
  (CASE WHEN octet_length(array_to_string(s.heading_path, '')) <= 16384 THEN s.heading_path ELSE ARRAY[]::text[] END)::text[] AS heading_path,
  (CASE WHEN octet_length(s.body_md) <= 1048576 THEN s.body_md ELSE '' END)::text AS body_md
FROM section s
WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.kb_id=sqlc.arg(kb_id)
  AND s.document_id=sqlc.arg(document_id) AND s.version_id=sqlc.arg(version_id) AND s.id=sqlc.arg(section_id)
FOR SHARE OF s;

-- name: AuditContentRead :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(action), sqlc.arg(object_type), sqlc.arg(object_id), sqlc.arg(result));

-- name: GetChunkLocation :one
SELECT document_id, section_id, ordinal, version_id
FROM chunk
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(chunk_id);

-- name: ListNeighborChunks :many
SELECT c.id, c.ordinal, c.kind, c.content, c.page_start, c.page_end, c.token_count
FROM chunk c
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id)
  AND c.ordinal BETWEEN sqlc.arg(min_ordinal) AND sqlc.arg(max_ordinal)
  AND c.is_current AND NOT c.disabled
ORDER BY c.ordinal
FOR SHARE OF c;
