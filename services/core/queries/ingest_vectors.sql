-- name: LockStagedChunks :many
SELECT id, ordinal, is_current, disabled, access_tags FROM chunk
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id)
  AND id = ANY(sqlc.arg(chunk_ids)::bigint[])
ORDER BY ordinal FOR SHARE;

-- name: LoadEmbeddingBatch :many
SELECT c.id, c.ordinal, s.ordinal AS section_ordinal, c.kind, c.content, c.search_text, c.token_count,
  c.page_start, c.page_end, c.is_current, c.disabled, c.access_tags,
  (v.chunk_id IS NOT NULL)::boolean AS has_vector,
  COALESCE(v.is_current, false)::boolean AS vector_current,
  COALESCE(v.access_tags, '{}'::text[])::text[] AS vector_access_tags,
  COALESCE((NOT sqlc.arg(normalized)::boolean OR abs(l2_norm(v.embedding) - 1.0) <= 0.005), false)::boolean AS vector_norm_valid
FROM chunk c
JOIN section s ON s.tenant_id = c.tenant_id AND s.kb_id = c.kb_id AND s.document_id = c.document_id
  AND s.version_id = c.version_id AND s.id = c.section_id
LEFT JOIN chunk_vector v ON v.tenant_id = c.tenant_id AND v.kb_id = c.kb_id AND v.chunk_id = c.id
  AND v.space_id = sqlc.arg(space_id)
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id)
  AND c.id = ANY(sqlc.arg(chunk_ids)::bigint[])
ORDER BY c.ordinal FOR SHARE OF c, s;

-- name: LockPlanVectors :many
SELECT chunk_id, is_current, access_tags FROM chunk_vector
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND space_id = sqlc.arg(space_id) AND chunk_id = ANY(sqlc.arg(chunk_ids)::bigint[])
ORDER BY chunk_id FOR UPDATE;

-- name: UpsertPlanVectors :execrows
INSERT INTO chunk_vector (tenant_id, kb_id, chunk_id, space_id, is_current, access_tags, embedding)
SELECT c.tenant_id, c.kb_id, c.id, sqlc.arg(space_id), false, c.access_tags, item.embedding::halfvec
FROM chunk c
JOIN jsonb_to_recordset(sqlc.arg(items)::jsonb) AS item(id bigint, ordinal integer, embedding text)
  ON c.id = item.id AND c.ordinal = item.ordinal
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id)
  AND NOT c.is_current AND NOT c.disabled AND c.access_tags = ARRAY['kb:' || c.kb_id::text || ':all']
ON CONFLICT (tenant_id, chunk_id, space_id) DO UPDATE
SET embedding = EXCLUDED.embedding, access_tags = EXCLUDED.access_tags
WHERE chunk_vector.kb_id = EXCLUDED.kb_id AND NOT chunk_vector.is_current
  AND chunk_vector.access_tags = EXCLUDED.access_tags;

-- name: CountPlanVectors :one
-- Allow for half-precision rounding while rejecting materially non-unit vectors.
SELECT count(*)::bigint AS total,
       count(*) FILTER (WHERE NOT c.is_current AND NOT c.disabled
         AND c.access_tags = ARRAY['kb:' || c.kb_id::text || ':all']
         AND NOT v.is_current AND v.access_tags = c.access_tags
         AND (NOT sqlc.arg(normalized)::boolean OR abs(l2_norm(v.embedding) - 1.0) <= 0.005))::bigint AS valid
FROM chunk c
JOIN chunk_vector v ON v.tenant_id = c.tenant_id AND v.kb_id = c.kb_id AND v.chunk_id = c.id
  AND v.space_id = sqlc.arg(space_id)
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.document_id = sqlc.arg(document_id) AND c.version_id = sqlc.arg(version_id);

-- name: MarkPlanIndexing :execrows
UPDATE document_version SET status = 'indexing'
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND id = sqlc.arg(version_id)
  AND status IN ('embedding', 'indexing');
