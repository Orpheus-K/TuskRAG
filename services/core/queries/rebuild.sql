-- name: RebuildTarget :one
SELECT s.id,s.provider_id,s.model,s.dims,s.normalized,s.distance,p.locality
FROM embedding_space s JOIN model_provider p ON p.tenant_id=s.tenant_id AND p.id=s.provider_id
WHERE s.tenant_id=sqlc.arg(tenant_id) AND s.id=sqlc.arg(space_id)
FOR SHARE OF s,p;

-- name: ReserveRebuildSpace :execrows
UPDATE knowledge_base SET pending_space_id=sqlc.arg(target_space_id)
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(kb_id) AND pending_space_id IS NULL
 AND embedding_space_id=sqlc.arg(source_space_id) AND config_revision=sqlc.arg(source_revision) AND status='active';

-- name: InsertEmbeddingRebuild :one
INSERT INTO embedding_rebuild(job_id,tenant_id,kb_id,source_space_id,source_revision,target_space_id,target_provider_id,target_model,target_dims,target_normalized,target_distance,created_by)
VALUES(sqlc.arg(job_id),sqlc.arg(tenant_id),sqlc.arg(kb_id),sqlc.arg(source_space_id),sqlc.arg(source_revision),sqlc.arg(target_space_id),sqlc.arg(target_provider_id),sqlc.arg(target_model),sqlc.arg(target_dims),sqlc.arg(target_normalized),sqlc.arg(target_distance),sqlc.arg(user_id)) RETURNING *;

-- name: GetEmbeddingRebuild :one
SELECT r.*,j.status,j.attempts,j.max_attempts,j.last_error,j.finished_at
FROM embedding_rebuild r JOIN job j ON j.tenant_id=r.tenant_id AND j.id=r.job_id AND j.kind='reembed'
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.kb_id=sqlc.arg(kb_id) AND r.job_id=sqlc.arg(job_id);

-- name: AuditEmbeddingRebuild :exec
INSERT INTO audit_log(tenant_id,actor_user_id,action,object_type,object_id,result,details)
VALUES(sqlc.arg(tenant_id),sqlc.arg(user_id),'kb.rebuild.create','knowledge_base',sqlc.arg(kb_id),'allow',
 jsonb_build_object('job_id',sqlc.arg(job_id)::bigint,'source_space_id',sqlc.arg(source_space_id)::bigint,'target_space_id',sqlc.arg(target_space_id)::bigint,'source_revision',sqlc.arg(source_revision)::integer));

-- name: ReleaseRebuildSpace :execrows
UPDATE knowledge_base k SET pending_space_id=NULL
FROM embedding_rebuild r
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.job_id=sqlc.arg(job_id)
 AND k.tenant_id=r.tenant_id AND k.id=r.kb_id AND k.pending_space_id=r.target_space_id;

-- name: AuditRebuildCancellation :exec
INSERT INTO audit_log(tenant_id,actor_user_id,action,object_type,object_id,result,details)
VALUES(sqlc.arg(tenant_id),sqlc.arg(user_id),'kb.rebuild.cancel','knowledge_base',sqlc.arg(kb_id),'allow',jsonb_build_object('job_id',sqlc.arg(job_id)::bigint));

-- name: RebuildJob :one
SELECT r.* FROM embedding_rebuild r JOIN job j ON j.tenant_id=r.tenant_id AND j.id=r.job_id AND j.kind='reembed'
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.job_id=sqlc.arg(job_id);

-- name: RebuildPendingSpace :one
SELECT pending_space_id FROM knowledge_base WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(kb_id);

-- name: SwitchRebuildSpace :execrows
UPDATE knowledge_base k SET embedding_space_id=r.target_space_id,pending_space_id=NULL,config_revision=k.config_revision+1
FROM embedding_rebuild r
WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.job_id=sqlc.arg(job_id)
 AND k.tenant_id=r.tenant_id AND k.id=r.kb_id AND k.status='active'
 AND k.embedding_space_id=r.source_space_id AND k.config_revision=r.source_revision AND k.pending_space_id=r.target_space_id;

-- name: AuditRebuildCompletion :exec
INSERT INTO audit_log(tenant_id,actor_user_id,action,object_type,object_id,result,details)
SELECT r.tenant_id,r.created_by,'kb.rebuild.complete','knowledge_base',r.kb_id,'allow',
 jsonb_build_object('job_id',r.job_id,'source_space_id',r.source_space_id,'target_space_id',r.target_space_id,'source_revision',r.source_revision)
FROM embedding_rebuild r WHERE r.tenant_id=sqlc.arg(tenant_id) AND r.job_id=sqlc.arg(job_id);

