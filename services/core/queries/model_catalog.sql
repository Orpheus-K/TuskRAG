-- name: LockLocalAdministrator :one
SELECT user_id FROM local_account WHERE tenant_id = sqlc.arg(tenant_id) AND user_id = sqlc.arg(user_id) AND NOT disabled FOR SHARE;

-- name: CreateModelProvider :one
INSERT INTO model_provider (tenant_id, name, base_url, locality)
VALUES (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(base_url), sqlc.arg(locality)) RETURNING id;

-- name: SetModelProviderCredential :exec
UPDATE model_provider SET api_key_id = sqlc.arg(key_id), api_key_nonce = sqlc.arg(nonce), api_key_ciphertext = sqlc.arg(ciphertext)
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: CreateEmbeddingSpace :one
INSERT INTO embedding_space (tenant_id, provider_id, model, dims, normalized, distance)
VALUES (sqlc.arg(tenant_id), sqlc.arg(provider_id), sqlc.arg(model), sqlc.arg(dims), sqlc.arg(normalized), sqlc.arg(distance))
RETURNING id;

-- name: ListEmbeddingSpaces :many
SELECT s.id, s.provider_id, p.name AS provider_name, p.base_url, p.locality, s.model, s.dims, s.normalized, s.distance,
  (p.api_key_ciphertext IS NOT NULL)::boolean AS has_credential
FROM embedding_space s JOIN model_provider p ON p.tenant_id = s.tenant_id AND p.id = s.provider_id
WHERE s.tenant_id = sqlc.arg(tenant_id) ORDER BY s.id;

-- name: LoadEmbeddingRuntime :one
SELECT s.id, s.provider_id, p.name AS provider_name, p.base_url, p.locality, s.model, s.dims, s.normalized, s.distance,
  p.api_key_id, p.api_key_nonce, p.api_key_ciphertext
FROM embedding_space s JOIN model_provider p ON p.tenant_id = s.tenant_id AND p.id = s.provider_id
WHERE s.tenant_id = sqlc.arg(tenant_id) AND s.id = sqlc.arg(space_id);

-- name: AuditModelAction :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(action), 'model_provider', sqlc.arg(provider_id), sqlc.arg(result));

-- name: LoadRerankProvider :one
SELECT id, base_url, locality, api_key_id, api_key_nonce, api_key_ciphertext
FROM model_provider WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: AuditDeniedModelCall :execrows
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
SELECT u.tenant_id, u.id, 'model.call', 'model_provider', p.id, 'deny',
  jsonb_build_object('knowledge_base_id', k.id, 'operation', sqlc.arg(operation)::text,
    'tenant_policy', sqlc.arg(tenant_policy)::text, 'knowledge_base_policy', sqlc.arg(kb_policy)::text,
    'locality', sqlc.arg(locality)::text)
FROM app_user u
JOIN knowledge_base k ON k.tenant_id = u.tenant_id AND k.id = sqlc.arg(kb_id)
JOIN model_provider p ON p.tenant_id = u.tenant_id AND p.id = sqlc.arg(provider_id)
WHERE u.tenant_id = sqlc.arg(tenant_id) AND u.id = sqlc.arg(user_id);

-- name: ListModelProviders :many
SELECT id, name, base_url, locality, (api_key_ciphertext IS NOT NULL)::boolean AS has_credential, created_at
FROM model_provider
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY id;

-- name: GetModelProvider :one
SELECT id, name, base_url, locality, api_key_id, api_key_nonce, api_key_ciphertext, created_at
FROM model_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: UpdateModelProvider :execrows
UPDATE model_provider
SET name = sqlc.arg(name), base_url = sqlc.arg(base_url), locality = sqlc.arg(locality)
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: DeleteModelProvider :execrows
DELETE FROM model_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: CountModelProviderReferences :one
SELECT (
  (SELECT count(*) FROM embedding_space e WHERE e.tenant_id = sqlc.arg(tenant_id) AND e.provider_id = sqlc.arg(provider_id)) +
  (SELECT count(*) FROM chat_model c WHERE c.tenant_id = sqlc.arg(tenant_id) AND c.provider_id = sqlc.arg(provider_id))
)::bigint AS ref_count;
