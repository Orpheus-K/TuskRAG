-- name: StoreAnswerEvalReport :execrows
INSERT INTO answer_eval_report (tenant_id, kb_id, digest, source, report, created_by)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(digest), 'imported', sqlc.arg(report), sqlc.arg(user_id))
ON CONFLICT (tenant_id, kb_id, digest) DO NOTHING;

-- name: GetAnswerEvalReport :one
SELECT * FROM answer_eval_report
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND digest = sqlc.arg(digest);

-- name: AuditAnswerEvalReport :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), 'eval.answer_report.import', 'knowledge_base', sqlc.arg(kb_id),
  'allow', jsonb_build_object('report_sha256', sqlc.arg(digest)::text));

-- Prefilter every referenced document before pagination. Do not expose hidden
-- report IDs, counts or cursors. The service rechecks selected bindings under locks.
-- name: ListVisibleAnswerEvalReports :many
SELECT r.digest, r.source, r.created_at, (r.report - 'rows')::jsonb AS metadata,
       bindings.doc_ids::jsonb AS document_ids
FROM answer_eval_report r
CROSS JOIN LATERAL (
  SELECT COALESCE(jsonb_agg(DISTINCT doc_id), '[]'::jsonb) AS doc_ids
  FROM jsonb_array_elements(r.report->'rows') question
  CROSS JOIN LATERAL jsonb_array_elements(
    COALESCE(NULLIF(question->'evidence_documents', 'null'::jsonb), '[]'::jsonb)) doc_id
) bindings
WHERE r.tenant_id = sqlc.arg(tenant_id) AND r.kb_id = sqlc.arg(kb_id)
  AND r.digest > sqlc.arg(cursor)::text
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements_text(bindings.doc_ids) doc_id
    WHERE NOT EXISTS (
      SELECT 1 FROM document d
      WHERE d.tenant_id = r.tenant_id AND d.kb_id = r.kb_id
        AND d.id = doc_id::bigint
        AND d.deleted_at IS NULL AND d.acl_mode = 'inherit_kb'
    )
  )
ORDER BY r.digest LIMIT sqlc.arg(page_size);
