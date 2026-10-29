-- name: LockPlanTarget :one
SELECT v.status, v.created_by, v.content_sha256, a.content_sha256 AS parsed_sha256, d.mime, v.confirm_first_row_headers,
       k.embedding_space_id, k.config_revision,
       e.provider_id, e.model, e.dims, e.normalized, e.distance
FROM document_version v
JOIN document d ON d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id
JOIN knowledge_base k ON k.tenant_id = d.tenant_id AND k.id = d.kb_id
JOIN embedding_space e ON e.tenant_id = k.tenant_id AND e.id = k.embedding_space_id
JOIN parse_artifact a ON a.tenant_id = v.tenant_id AND a.kb_id = v.kb_id
  AND a.document_id = v.document_id AND a.version_id = v.id
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id) AND v.id = sqlc.arg(version_id)
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND k.status = 'active'
FOR UPDATE OF v FOR SHARE OF d, k, a, e;

-- name: LoadIngestPlan :one
SELECT embedding_space_id, config_revision, plan_sha256, section_count, chunk_count
FROM ingest_plan WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);

-- name: ReserveSectionIDs :many
SELECT nextval('section_id_seq')::bigint AS id FROM generate_series(1, sqlc.arg(item_count)::integer);

-- name: ReserveChunkIDs :many
SELECT nextval('chunk_id_seq')::bigint AS id FROM generate_series(1, sqlc.arg(item_count)::integer);

-- name: InsertPlanSections :execrows
INSERT INTO section (id, tenant_id, kb_id, document_id, version_id, parent_section_id,
                     ordinal, level, heading, heading_path, page_start, page_end, body_md, token_count)
OVERRIDING SYSTEM VALUE
SELECT item.id, sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(document_id), sqlc.arg(version_id), item.parent_id,
       item.ordinal, item.level, item.heading, item.heading_path, item.page_start, item.page_end, item.body_md, item.token_count
FROM jsonb_to_recordset(sqlc.arg(items)::jsonb) AS item(
  id bigint, parent_id bigint, ordinal integer, level smallint, heading text, heading_path text[],
  page_start integer, page_end integer, body_md text, token_count integer
)
ORDER BY item.ordinal;

-- name: InsertPlanChunks :execrows
INSERT INTO chunk (id, tenant_id, kb_id, document_id, version_id, section_id, ordinal, kind,
                   content, search_text, page_start, page_end, token_count,
                   is_current, disabled, access_tags, doc_tags, doc_mime, doc_updated_at)
OVERRIDING SYSTEM VALUE
SELECT item.id, v.tenant_id, v.kb_id, v.document_id, v.id, item.section_id, item.ordinal, item.kind,
       item.content, item.search_text, item.page_start, item.page_end, item.token_count,
       false, false, ARRAY['kb:' || v.kb_id::text || ':all'], d.tags, d.mime, v.created_at
FROM document_version v
JOIN document d ON d.tenant_id = v.tenant_id AND d.kb_id = v.kb_id AND d.id = v.document_id
CROSS JOIN jsonb_to_recordset(sqlc.arg(items)::jsonb) AS item(
  id bigint, section_id bigint, ordinal integer, kind text, content text, search_text text,
  page_start integer, page_end integer, token_count integer
)
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND v.document_id = sqlc.arg(document_id) AND v.id = sqlc.arg(version_id)
ORDER BY item.ordinal;

-- name: InsertIngestPlan :exec
INSERT INTO ingest_plan (tenant_id, kb_id, document_id, version_id, embedding_space_id,
                         config_revision, plan_sha256, section_count, chunk_count)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(document_id), sqlc.arg(version_id),
        sqlc.arg(embedding_space_id), sqlc.arg(config_revision), sqlc.arg(plan_sha256),
        sqlc.arg(section_count), sqlc.arg(chunk_count));

-- name: ListPlanChunkIDs :many
SELECT id, ordinal, is_current, disabled, access_tags FROM chunk
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id)
ORDER BY ordinal;

-- name: CountPlanSections :one
SELECT count(*)::bigint FROM section
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND document_id = sqlc.arg(document_id) AND version_id = sqlc.arg(version_id);
