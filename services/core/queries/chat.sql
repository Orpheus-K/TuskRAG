-- name: CreateChatModel :one
INSERT INTO chat_model (tenant_id, provider_id, model)
VALUES (sqlc.arg(tenant_id), sqlc.arg(provider_id), sqlc.arg(model)) RETURNING id;

-- name: ListChatModels :many
SELECT c.id, p.name, c.model FROM chat_model c
JOIN model_provider p ON p.tenant_id=c.tenant_id AND p.id=c.provider_id
WHERE c.tenant_id=sqlc.arg(tenant_id) ORDER BY c.id;

-- name: LoadChatModel :one
SELECT provider_id, model FROM chat_model
WHERE tenant_id=sqlc.arg(tenant_id) AND id=sqlc.arg(id);

-- name: AuditAnswer :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), 'answer.generate', 'knowledge_base', sqlc.arg(kb_id), 'allow',
  jsonb_build_object('model_id', sqlc.arg(model_id)::bigint, 'prompt_version', sqlc.arg(prompt_version)::text,
    'evidence_count', sqlc.arg(evidence_count)::integer, 'invalid_citations', sqlc.arg(invalid_citations)::integer, 'outcome', sqlc.arg(outcome)::text));
