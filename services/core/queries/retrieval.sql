-- name: GetSearchBinding :one
SELECT k.embedding_space_id, k.config_revision, e.dims, e.distance, e.normalized
FROM knowledge_base k
JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
JOIN embedding_space e ON e.tenant_id = k.tenant_id AND e.id = k.embedding_space_id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id)
  AND m.user_id = sqlc.arg(user_id) AND k.status = 'active';

-- name: LockSearchBinding :one
SELECT k.embedding_space_id, k.config_revision, e.dims, e.distance, e.normalized
FROM knowledge_base k
JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
JOIN embedding_space e ON e.tenant_id = k.tenant_id AND e.id = k.embedding_space_id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id)
  AND m.user_id = sqlc.arg(user_id) AND k.status = 'active'
FOR SHARE OF k, m, e;

-- name: GetAuthorizedSearchScope :one
SELECT k.tenant_id, k.id AS kb_id, k.egress_policy, t.egress_policy_max
FROM knowledge_base k
JOIN tenant t ON t.id = k.tenant_id
JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id)
  AND m.user_id = sqlc.arg(user_id) AND k.status = 'active'
  AND k.embedding_space_id = sqlc.arg(space_id) AND k.config_revision = sqlc.arg(config_revision);

-- name: RecallHybrid :many
WITH lexical_candidates AS (
  SELECT c.id AS chunk_id, paradedb.score(c.id)::float8 AS raw_score
  FROM chunk c
  WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
    AND c.is_current AND c.kb_access
    AND c.search_text ||| sqlc.arg(query)::text
    -- Build the authorized current-version set once, before BM25 Top-K.
    -- Chunk foreign keys bind each version to its document and knowledge base.
    AND c.id @@@ paradedb.term_set('version_id', ARRAY(
      SELECT d.current_version_id
      FROM knowledge_base k
      JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
      JOIN document d ON d.tenant_id = k.tenant_id AND d.kb_id = k.id
      JOIN document_version dv ON dv.tenant_id = d.tenant_id AND dv.kb_id = d.kb_id
        AND dv.document_id = d.id AND dv.id = d.current_version_id
      WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id)
        AND m.user_id = sqlc.arg(user_id) AND k.status = 'active' AND k.embedding_space_id = sqlc.arg(space_id)
        AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND dv.status IN ('ready', 'partial')
        AND (cardinality(sqlc.arg(doc_tags)::text[]) = 0 OR d.tags && sqlc.arg(doc_tags)::text[])
        AND (cardinality(sqlc.arg(mimes)::text[]) = 0 OR d.mime = ANY(sqlc.arg(mimes)::text[]))
        AND (sqlc.narg(updated_after)::timestamptz IS NULL OR dv.created_at >= sqlc.narg(updated_after)::timestamptz)
    ))
  ORDER BY paradedb.score(c.id) DESC, c.id LIMIT 50
), semantic_candidates AS (
  SELECT v.chunk_id, (halfvec(v.embedding, sqlc.arg(dimensions)::int, true) <=> sqlc.arg(vector)::text::halfvec)::float8 AS distance
  FROM chunk_vector v
  WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id) AND v.space_id = sqlc.arg(space_id)
    AND v.is_current AND v.access_tags && sqlc.arg(principal_tags)::text[]
    -- Keep document filters on the vector scan so iterative HNSW can advance past rejected rows.
    AND EXISTS (
      SELECT 1 FROM chunk c
      JOIN document d ON d.tenant_id = c.tenant_id AND d.kb_id = c.kb_id AND d.id = c.document_id
      JOIN document_version dv ON dv.tenant_id = c.tenant_id AND dv.kb_id = c.kb_id
        AND dv.document_id = c.document_id AND dv.id = c.version_id
      JOIN knowledge_base k ON k.tenant_id = c.tenant_id AND k.id = c.kb_id
      JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
      WHERE c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
        AND m.user_id = sqlc.arg(user_id) AND k.status = 'active' AND k.embedding_space_id = v.space_id
        AND c.is_current AND NOT c.disabled AND c.access_tags && sqlc.arg(principal_tags)::text[]
        AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND d.current_version_id = c.version_id
        AND dv.status IN ('ready', 'partial')
        AND (cardinality(sqlc.arg(doc_tags)::text[]) = 0 OR c.doc_tags && sqlc.arg(doc_tags)::text[])
        AND (cardinality(sqlc.arg(mimes)::text[]) = 0 OR c.doc_mime = ANY(sqlc.arg(mimes)::text[]))
        AND (sqlc.narg(updated_after)::timestamptz IS NULL OR c.doc_updated_at >= sqlc.narg(updated_after)::timestamptz)
      OFFSET 0
    )
  ORDER BY distance LIMIT 50
), ranked AS (
  SELECT chunk_id, row_number() OVER (ORDER BY raw_score DESC, chunk_id) AS rank,
    'lexical'::text AS source, raw_score, NULL::float8 AS distance FROM lexical_candidates
  UNION ALL
  SELECT chunk_id, row_number() OVER (ORDER BY distance, chunk_id) AS rank,
    'semantic'::text AS source, NULL::float8 AS raw_score, distance FROM semantic_candidates
), fused AS (
  SELECT chunk_id, sum(1.0::float8 / (60.0::float8 + rank))::float8 AS score,
    min(rank) FILTER (WHERE source = 'lexical')::bigint AS lexical_rank,
    min(rank) FILTER (WHERE source = 'semantic')::bigint AS semantic_rank,
    max(raw_score) FILTER (WHERE source = 'lexical')::float8 AS lexical_score,
    min(distance) FILTER (WHERE source = 'semantic')::float8 AS semantic_distance
  FROM ranked GROUP BY chunk_id
)
SELECT chunk_id, score, COALESCE(lexical_rank, 0)::bigint AS lexical_rank,
  COALESCE(semantic_rank, 0)::bigint AS semantic_rank,
  COALESCE(lexical_score, 0)::float8 AS lexical_score,
  COALESCE(semantic_distance, 0)::float8 AS semantic_distance
FROM fused ORDER BY score DESC, chunk_id LIMIT 100;

-- name: RecheckSearchEvidence :many
SELECT c.id, c.document_id, c.version_id, c.section_id, c.content, c.page_start, c.page_end,
       d.title, d.mime, s.heading_path
FROM chunk c
JOIN document d ON d.tenant_id = c.tenant_id AND d.kb_id = c.kb_id AND d.id = c.document_id
JOIN document_version dv ON dv.tenant_id = c.tenant_id AND dv.kb_id = c.kb_id
  AND dv.document_id = c.document_id AND dv.id = c.version_id
JOIN section s ON s.tenant_id = c.tenant_id AND s.kb_id = c.kb_id AND s.document_id = c.document_id
  AND s.version_id = c.version_id AND s.id = c.section_id
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.id = ANY(sqlc.arg(chunk_ids)::bigint[])
  AND c.is_current AND NOT c.disabled AND c.access_tags && sqlc.arg(principal_tags)::text[]
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND d.current_version_id = c.version_id
  AND dv.status IN ('ready', 'partial')
  AND (cardinality(sqlc.arg(doc_tags)::text[]) = 0 OR d.tags && sqlc.arg(doc_tags)::text[])
  AND (cardinality(sqlc.arg(mimes)::text[]) = 0 OR d.mime = ANY(sqlc.arg(mimes)::text[]))
  AND (sqlc.narg(updated_after)::timestamptz IS NULL OR dv.created_at >= sqlc.narg(updated_after)::timestamptz)
ORDER BY c.id FOR SHARE OF c, d, dv, s;

-- name: AuditSearch :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), 'retrieval.search', 'knowledge_base', sqlc.arg(kb_id), 'allow',
  jsonb_build_object('query_sha256', sqlc.arg(query_hash)::text, 'query_bytes', sqlc.arg(query_bytes)::integer,
    'evidence_count', sqlc.arg(evidence_count)::integer));
