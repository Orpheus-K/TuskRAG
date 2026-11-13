-- name: LatestRetrievalConfig :one
SELECT * FROM kb_retrieval_config_revision
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id)
ORDER BY revision DESC LIMIT 1;

-- name: InsertRetrievalConfig :one
INSERT INTO kb_retrieval_config_revision
  (tenant_id, kb_id, revision, top_k, rerank_provider_id, rerank_model,
   rerank_top_n, rerank_fallback, rerank_min_score, created_by)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(revision), sqlc.arg(top_k),
  sqlc.narg(rerank_provider_id), sqlc.narg(rerank_model), sqlc.narg(rerank_top_n),
  sqlc.arg(rerank_fallback), sqlc.narg(rerank_min_score), sqlc.arg(created_by))
RETURNING *;

-- name: RetrievalConfigProvider :one
SELECT locality FROM model_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id)
FOR SHARE;

-- name: AuditRetrievalConfig :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), 'kb.config.update', 'knowledge_base', sqlc.arg(kb_id),
  'allow', jsonb_build_object('retrieval_revision', sqlc.arg(revision)::integer));
