-- name: LockRebuildDocument :one
SELECT d.current_version_id FROM document d JOIN document_version v ON v.tenant_id=d.tenant_id AND v.kb_id=d.kb_id AND v.document_id=d.id AND v.id=d.current_version_id
WHERE d.tenant_id=sqlc.arg(tenant_id) AND d.kb_id=sqlc.arg(kb_id) AND d.id=sqlc.arg(document_id)
 AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb' AND v.status='ready'
FOR UPDATE OF d;

-- name: ReadRebuildChunks :many
SELECT c.id,c.ordinal,c.search_text,c.token_count,c.is_current,c.disabled,c.access_tags
FROM chunk c JOIN document_version v ON v.tenant_id=c.tenant_id AND v.kb_id=c.kb_id AND v.document_id=c.document_id AND v.id=c.version_id
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.kb_id=sqlc.arg(kb_id) AND c.document_id=sqlc.arg(document_id)
 AND c.version_id=sqlc.arg(version_id) AND v.status='ready' AND c.id>sqlc.arg(after_id)
ORDER BY c.id LIMIT sqlc.arg(page_size) FOR SHARE OF c,v;

-- name: UpsertRebuildVectors :execrows
INSERT INTO chunk_vector(tenant_id,kb_id,chunk_id,space_id,is_current,access_tags,embedding)
SELECT c.tenant_id,c.kb_id,c.id,sqlc.arg(space_id),true,c.access_tags,item.embedding::halfvec
FROM chunk c JOIN jsonb_to_recordset(sqlc.arg(items)::jsonb) AS item(id bigint,ordinal integer,embedding text) ON c.id=item.id AND c.ordinal=item.ordinal
WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.kb_id=sqlc.arg(kb_id) AND c.document_id=sqlc.arg(document_id)
 AND c.version_id=sqlc.arg(version_id) AND c.is_current AND NOT c.disabled AND c.access_tags=ARRAY['kb:'||c.kb_id::text||':all']
ON CONFLICT(tenant_id,chunk_id,space_id) DO UPDATE SET embedding=EXCLUDED.embedding,is_current=EXCLUDED.is_current,access_tags=EXCLUDED.access_tags
WHERE chunk_vector.kb_id=EXCLUDED.kb_id;


-- name: ListRebuildDocuments :many
SELECT id,acl_mode FROM document
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id) AND deleted_at IS NULL
 AND current_version_id IS NOT NULL AND id>sqlc.arg(after_id)
ORDER BY id LIMIT 64;

-- name: RebuildCoverageValid :one
WITH docs AS (
 SELECT d.id,d.current_version_id,d.acl_mode,dv.status
 FROM document d LEFT JOIN document_version dv ON dv.tenant_id=d.tenant_id AND dv.kb_id=d.kb_id AND dv.document_id=d.id AND dv.id=d.current_version_id
 WHERE d.tenant_id=sqlc.arg(tenant_id) AND d.kb_id=sqlc.arg(kb_id) AND d.deleted_at IS NULL AND d.current_version_id IS NOT NULL
), chunks AS (
 SELECT c.id,c.tenant_id,c.kb_id,c.document_id,c.is_current,c.disabled,c.access_tags FROM chunk c JOIN docs d ON d.id=c.document_id AND d.current_version_id=c.version_id
 WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.kb_id=sqlc.arg(kb_id)
)
SELECT (
 NOT EXISTS(SELECT 1 FROM docs d WHERE d.acl_mode<>'inherit_kb' OR d.status IS DISTINCT FROM 'ready' OR NOT EXISTS(SELECT 1 FROM chunks c WHERE c.document_id=d.id))
 AND NOT EXISTS(SELECT 1 FROM chunks c LEFT JOIN chunk_vector v ON v.tenant_id=c.tenant_id AND v.kb_id=c.kb_id AND v.chunk_id=c.id AND v.space_id=sqlc.arg(space_id)
  WHERE NOT c.is_current OR c.disabled OR c.access_tags<>ARRAY['kb:'||c.kb_id::text||':all']
   OR v.chunk_id IS NULL OR NOT v.is_current OR v.access_tags<>c.access_tags OR v.dims<>sqlc.arg(dimensions)
   OR (sqlc.arg(normalized)::boolean AND abs(l2_norm(v.embedding)-1.0)>0.005))
 AND NOT EXISTS(SELECT 1 FROM chunk c WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.kb_id=sqlc.arg(kb_id) AND c.is_current AND NOT EXISTS(SELECT 1 FROM chunks valid WHERE valid.id=c.id))
 AND NOT EXISTS(SELECT 1 FROM chunk_vector v WHERE v.tenant_id=sqlc.arg(tenant_id) AND v.kb_id=sqlc.arg(kb_id) AND v.space_id=sqlc.arg(space_id) AND v.is_current AND NOT EXISTS(SELECT 1 FROM chunks c WHERE c.id=v.chunk_id))
)::boolean AS valid;


