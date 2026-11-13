-- name: CreateEvalSet :one
INSERT INTO eval_set (tenant_id, kb_id, revision)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), 1) RETURNING id;

-- name: AdvanceEvalSet :one
UPDATE eval_set SET revision = revision + 1
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
  AND id = sqlc.arg(set_id) AND revision = sqlc.arg(expected)::integer
RETURNING revision;

-- name: GetEvalSetRevision :one
SELECT r.* FROM eval_set s
JOIN eval_set_revision r ON r.tenant_id=s.tenant_id AND r.kb_id=s.kb_id AND r.set_id=s.id
  AND r.revision=CASE WHEN sqlc.arg(revision)::integer=0 THEN s.revision ELSE sqlc.arg(revision)::integer END
WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.kb_id=sqlc.arg(kb_id) AND s.id=sqlc.arg(set_id);

-- name: InsertEvalSetRevision :one
INSERT INTO eval_set_revision (tenant_id,kb_id,set_id,revision,definition,digest,created_by)
VALUES (sqlc.arg(tenant_id),sqlc.arg(kb_id),sqlc.arg(set_id),sqlc.arg(revision),sqlc.arg(definition),sqlc.arg(digest),sqlc.arg(user_id))
RETURNING *;

-- One current, visible witness per target is sufficient. Documents, KB and
-- membership are locked first; shared witness locks fence disabling/tag changes.
-- name: CountVisibleEvalSetLocations :one
SELECT count(*) FROM jsonb_to_recordset(sqlc.arg(locations)::jsonb)
  AS e(document_id bigint,section_id bigint,page integer)
JOIN LATERAL (
  SELECT c.id FROM chunk c
  JOIN document d ON d.tenant_id=c.tenant_id AND d.kb_id=c.kb_id AND d.id=c.document_id
  JOIN document_version v ON v.tenant_id=c.tenant_id AND v.kb_id=c.kb_id AND v.document_id=c.document_id AND v.id=c.version_id
  WHERE c.tenant_id=sqlc.arg(tenant_id) AND c.kb_id=sqlc.arg(kb_id) AND c.document_id=e.document_id
    AND (COALESCE(e.section_id,0)=0 OR c.section_id=e.section_id)
    AND (COALESCE(e.page,0)=0 OR e.page BETWEEN c.page_start AND c.page_end)
    AND c.is_current AND NOT c.disabled
    AND c.access_tags @> ARRAY['kb:'||sqlc.arg(kb_id)::bigint::text||':all']::text[]
    AND d.current_version_id=c.version_id AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb'
    AND v.status IN ('ready','partial')
  ORDER BY c.id LIMIT 1 FOR SHARE OF c,v
) witness ON true;

-- name: AuditEvalSetRevision :exec
INSERT INTO audit_log(tenant_id,actor_user_id,action,object_type,object_id,result,details)
VALUES(sqlc.arg(tenant_id),sqlc.arg(user_id),'eval.set.save','eval_set',sqlc.arg(set_id),'allow',
  jsonb_build_object('kb_id',sqlc.arg(kb_id)::bigint,'revision',sqlc.arg(revision)::integer,'sha256',sqlc.arg(digest)::text));

-- Filter the current revision's entire target set before LIMIT, including
-- targets outside any future case page. Hidden IDs never become cursors.
-- name: ListVisibleEvalSets :many
SELECT s.id, r.revision, r.digest, r.created_at,
       (r.definition->>'name')::text AS name,
       jsonb_array_length(r.definition->'cases')::integer AS case_count,
       bindings.items::jsonb AS locations
FROM eval_set s
JOIN eval_set_revision r ON r.tenant_id=s.tenant_id AND r.kb_id=s.kb_id
  AND r.set_id=s.id AND r.revision=s.revision
CROSS JOIN LATERAL (
  SELECT COALESCE(jsonb_agg(DISTINCT item),'[]'::jsonb) AS items
  FROM jsonb_array_elements(r.definition->'cases') question
  CROSS JOIN LATERAL jsonb_array_elements(COALESCE(NULLIF(question->'expected','null'::jsonb),'[]'::jsonb)) item
) bindings
WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.kb_id=sqlc.arg(kb_id)
  AND s.id>sqlc.arg(cursor)::bigint
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements(bindings.items) e
    WHERE NOT EXISTS (
      SELECT 1 FROM chunk c
      JOIN document d ON d.tenant_id=c.tenant_id AND d.kb_id=c.kb_id AND d.id=c.document_id
      JOIN document_version v ON v.tenant_id=c.tenant_id AND v.kb_id=c.kb_id AND v.document_id=c.document_id AND v.id=c.version_id
      WHERE c.tenant_id=s.tenant_id AND c.kb_id=s.kb_id AND c.document_id=(e->>'document_id')::bigint
        AND (COALESCE((e->>'section_id')::bigint,0)=0 OR c.section_id=(e->>'section_id')::bigint)
        AND (COALESCE((e->>'page')::integer,0)=0 OR (e->>'page')::integer BETWEEN c.page_start AND c.page_end)
        AND c.is_current AND NOT c.disabled
        AND c.access_tags @> ARRAY['kb:'||s.kb_id::text||':all']::text[]
        AND d.current_version_id=c.version_id AND d.deleted_at IS NULL AND d.acl_mode='inherit_kb'
        AND v.status IN ('ready','partial')
    )
  )
ORDER BY s.id LIMIT sqlc.arg(page_size);
