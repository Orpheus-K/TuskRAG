-- name: NextEvalRunID :one
SELECT nextval('eval_run_id_seq')::bigint AS id;

-- name: LockEvalRunScope :many
SELECT d.id FROM document d
WHERE d.tenant_id=sqlc.arg(tenant_id) AND d.kb_id=sqlc.arg(kb_id)
  AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb'
  AND EXISTS(SELECT 1 FROM document_version v WHERE v.tenant_id=d.tenant_id AND v.kb_id=d.kb_id AND v.document_id=d.id AND v.id=d.current_version_id AND v.status IN ('ready','partial'))
ORDER BY d.id LIMIT 10001 FOR SHARE OF d;

-- name: InsertEvalRun :one
INSERT INTO eval_run(id,tenant_id,kb_id,job_id,set_id,set_revision,set_digest,mode,configuration,config_digest,created_by)
VALUES(sqlc.arg(id),sqlc.arg(tenant_id),sqlc.arg(kb_id),sqlc.arg(job_id),sqlc.arg(set_id),sqlc.arg(set_revision),sqlc.arg(set_digest),sqlc.arg(mode),sqlc.arg(configuration),sqlc.arg(config_digest),sqlc.arg(user_id))
RETURNING *;

-- name: GetEvalRun :one
SELECT * FROM eval_run
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id) AND id=sqlc.arg(id);

-- name: GetClaimedEvalRun :one
SELECT * FROM eval_run
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id) AND job_id=sqlc.arg(job_id);

-- name: InsertEvalRunResult :exec
INSERT INTO eval_run_result(run_id,tenant_id,kb_id,digest,report)
VALUES(sqlc.arg(run_id),sqlc.arg(tenant_id),sqlc.arg(kb_id),sqlc.arg(digest),sqlc.arg(report));

-- name: GetEvalRunResult :one
SELECT * FROM eval_run_result
WHERE tenant_id=sqlc.arg(tenant_id) AND kb_id=sqlc.arg(kb_id) AND run_id=sqlc.arg(run_id);

-- name: GetEvalRunState :one
SELECT j.status,j.attempts,j.max_attempts,j.last_error,j.finished_at,
  EXISTS(SELECT 1 FROM eval_run_result result WHERE result.tenant_id=r.tenant_id AND result.kb_id=r.kb_id AND result.run_id=r.id) AS has_result
FROM eval_run r JOIN job j ON j.tenant_id=r.tenant_id AND j.id=r.job_id
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.kb_id=sqlc.arg(kb_id) AND r.id=sqlc.arg(id) AND j.kind='eval_run';

-- name: AuditEvalRunResult :exec
INSERT INTO audit_log(tenant_id,actor_user_id,action,object_type,object_id,result,details)
VALUES(sqlc.arg(tenant_id),sqlc.arg(user_id),'eval.run.complete','eval_run',sqlc.arg(run_id),'allow',
  jsonb_build_object('kb_id',sqlc.arg(kb_id)::bigint,'sha256',sqlc.arg(digest)::text));

-- name: AuditEvalRun :exec
INSERT INTO audit_log(tenant_id,actor_user_id,action,object_type,object_id,result,details)
VALUES(sqlc.arg(tenant_id),sqlc.arg(user_id),'eval.run.create','eval_run',sqlc.arg(id),'allow',
  jsonb_build_object('kb_id',sqlc.arg(kb_id)::bigint,'job_id',sqlc.arg(job_id)::bigint,'config_sha256',sqlc.arg(config_digest)::text));

-- name: ListVisibleEvalRunIDs :many
SELECT r.id FROM eval_run r
JOIN eval_set_revision revision ON revision.tenant_id=r.tenant_id AND revision.kb_id=r.kb_id AND revision.set_id=r.set_id AND revision.revision=r.set_revision
LEFT JOIN eval_run_result result ON result.tenant_id=r.tenant_id AND result.kb_id=r.kb_id AND result.run_id=r.id
CROSS JOIN LATERAL (
 SELECT COALESCE(jsonb_agg(DISTINCT item),'[]'::jsonb) AS items
 FROM jsonb_array_elements(revision.definition->'cases') question
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(question->'expected','null'::jsonb),'[]'::jsonb)) item
) targets
CROSS JOIN LATERAL (
 SELECT COALESCE(jsonb_agg(DISTINCT item),'[]'::jsonb) AS items
 FROM jsonb_array_elements(result.report->'rows') question
 CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(question->'fused','null'::jsonb),'[]'::jsonb) || COALESCE(NULLIF(question->'ranked','null'::jsonb),'[]'::jsonb)) item
) bindings
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.kb_id=sqlc.arg(kb_id) AND r.id>sqlc.arg(cursor)::bigint
 AND NOT EXISTS (
   SELECT 1 FROM jsonb_array_elements_text(COALESCE(NULLIF(r.configuration->'allowed_document_ids','null'::jsonb),'[]'::jsonb)) allowed
   WHERE NOT EXISTS (
     SELECT 1 FROM document d WHERE d.tenant_id=r.tenant_id AND d.kb_id=r.kb_id AND d.id=allowed::bigint
       AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb'
   )
 )
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(targets.items) e
    WHERE NOT EXISTS (
      SELECT 1 FROM chunk c
      JOIN document d ON d.tenant_id=c.tenant_id AND d.kb_id=c.kb_id AND d.id=c.document_id
      JOIN document_version v ON v.tenant_id=c.tenant_id AND v.kb_id=c.kb_id AND v.document_id=c.document_id AND v.id=c.version_id
      WHERE c.tenant_id=r.tenant_id AND c.kb_id=r.kb_id AND c.document_id=(e->>'document_id')::bigint
        AND (COALESCE((e->>'section_id')::bigint,0)=0 OR c.section_id=(e->>'section_id')::bigint)
        AND (COALESCE((e->>'page')::integer,0)=0 OR (e->>'page')::integer BETWEEN c.page_start AND c.page_end)
        AND c.is_current AND NOT c.disabled
        AND c.access_tags @> ARRAY['kb:'||r.kb_id::text||':all']::text[]
        AND d.current_version_id=c.version_id AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb'
        AND v.status IN ('ready','partial')
    )
  )
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
ORDER BY r.id LIMIT sqlc.arg(page_size);

