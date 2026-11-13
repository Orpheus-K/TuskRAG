-- name: StoreEvalReport :execrows
INSERT INTO eval_report (tenant_id, kb_id, digest, source, report, created_by)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(digest), 'imported', sqlc.arg(report), sqlc.arg(user_id))
ON CONFLICT (tenant_id, kb_id, digest) DO NOTHING;

-- name: GetEvalReport :one
SELECT * FROM eval_report
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND digest = sqlc.arg(digest);

-- Lock documents before the KB, matching publication and deletion.
-- name: LockEvalDocuments :many
SELECT id FROM document
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND id = ANY(sqlc.arg(document_ids)::bigint[])
  AND deleted_at IS NULL AND acl_mode = 'inherit_kb'
ORDER BY id FOR SHARE;

-- name: LockEvalKnowledgeBase :one
SELECT status FROM knowledge_base
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(kb_id)
FOR SHARE;

-- name: EvalReportEvidence :many
SELECT c.id, c.document_id, c.section_id, c.page_start, c.page_end
FROM chunk c
JOIN document d ON d.tenant_id = c.tenant_id AND d.kb_id = c.kb_id AND d.id = c.document_id
JOIN document_version v ON v.tenant_id = c.tenant_id AND v.kb_id = c.kb_id
  AND v.document_id = c.document_id AND v.id = c.version_id
WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.kb_id = sqlc.arg(kb_id)
  AND c.id = ANY(sqlc.arg(chunk_ids)::bigint[]) AND c.is_current AND NOT c.disabled
  AND c.access_tags @> ARRAY['kb:' || sqlc.arg(kb_id)::bigint::text || ':all']::text[]
  AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb' AND d.current_version_id = c.version_id
  AND v.status IN ('ready', 'partial')
ORDER BY c.id FOR SHARE OF c, v;

-- name: AuditEvalReport :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), 'eval.report.import', 'knowledge_base', sqlc.arg(kb_id),
  'allow', jsonb_build_object('report_sha256', sqlc.arg(digest)::text));

-- Prefilter every evidence reference before pagination. Do not expose hidden
-- report IDs, counts or cursors. The service rechecks selected bindings under locks.
-- name: ListVisibleEvalReports :many
SELECT r.digest, r.source, r.created_at, (r.report - 'rows')::jsonb AS metadata,
       bindings.items::jsonb AS evidence
FROM eval_report r
CROSS JOIN LATERAL (
  SELECT COALESCE(jsonb_agg(DISTINCT item), '[]'::jsonb) AS items
  FROM jsonb_array_elements(r.report->'rows') question
  CROSS JOIN LATERAL jsonb_array_elements(
    COALESCE(NULLIF(question->'fused','null'::jsonb),'[]'::jsonb) ||
    COALESCE(NULLIF(question->'ranked','null'::jsonb),'[]'::jsonb)) item
) bindings
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.kb_id = sqlc.arg(kb_id)
  AND r.digest > sqlc.arg(cursor)::text
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(bindings.items) e
    WHERE NOT EXISTS (
      SELECT 1 FROM chunk c
      JOIN document d ON d.tenant_id=c.tenant_id AND d.kb_id=c.kb_id AND d.id=c.document_id
      JOIN document_version v ON v.tenant_id=c.tenant_id AND v.kb_id=c.kb_id
        AND v.document_id=c.document_id AND v.id=c.version_id
      WHERE c.tenant_id=r.tenant_id AND c.kb_id=r.kb_id AND c.id=(e->>'ChunkID')::bigint
        AND c.document_id=(e->>'DocumentID')::bigint AND c.section_id=(e->>'SectionID')::bigint
        AND COALESCE(c.page_start,0)=(e->>'PageStart')::integer
        AND COALESCE(c.page_end,0)=(e->>'PageEnd')::integer
        AND c.is_current AND NOT c.disabled
        AND c.access_tags @> ARRAY['kb:'||r.kb_id::text||':all']::text[]
        AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb' AND d.current_version_id=c.version_id
        AND v.status IN ('ready','partial')
    )
  )
ORDER BY r.digest LIMIT sqlc.arg(page_size);
