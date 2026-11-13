-- name: ResolveAPIKeyTenant :one
SELECT COALESCE(resolve_api_key_tenant(sqlc.arg(prefix)), 0)::bigint AS tenant_id;

-- name: CreateAPIKey :one
INSERT INTO api_key (
  tenant_id, user_id, name, prefix, secret_hash, kb_ids, actions,
  ip_allowlist, rate_limit_rpm, monthly_token_quota, expires_at
)
VALUES (
  sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(name), sqlc.arg(prefix),
  sqlc.arg(secret_hash), sqlc.arg(kb_ids)::bigint[], sqlc.arg(actions)::text[],
  sqlc.arg(ip_allowlist)::text[], sqlc.arg(rate_limit_rpm), sqlc.arg(monthly_token_quota),
  sqlc.narg(expires_at)
)
RETURNING id, tenant_id, user_id, name, prefix, kb_ids, actions, ip_allowlist,
  rate_limit_rpm, monthly_token_quota, expires_at, revoked_at, last_used_at, created_at;

-- name: ListAPIKeys :many
SELECT k.id, k.tenant_id, k.user_id, k.name, k.prefix, k.kb_ids, k.actions, k.ip_allowlist,
  k.rate_limit_rpm, k.monthly_token_quota, k.expires_at, k.revoked_at, k.last_used_at, k.created_at,
  COALESCE(u.tokens_used, 0)::bigint AS month_tokens_used
FROM api_key k
LEFT JOIN api_key_usage_monthly u
  ON u.tenant_id = k.tenant_id AND u.api_key_id = k.id AND u.month_key = sqlc.arg(month_key)
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.user_id = sqlc.arg(user_id)
ORDER BY k.id DESC;

-- name: RevokeAPIKey :execrows
UPDATE api_key
SET revoked_at = now()
WHERE tenant_id = sqlc.arg(tenant_id) AND user_id = sqlc.arg(user_id)
  AND id = sqlc.arg(key_id) AND revoked_at IS NULL;

-- name: GetAPIKeyByPrefix :one
SELECT k.id, k.tenant_id, k.user_id, k.secret_hash, k.kb_ids, k.actions,
  k.ip_allowlist, k.rate_limit_rpm, k.monthly_token_quota, k.expires_at, k.revoked_at,
  au.disabled AS user_disabled,
  COALESCE(u.tokens_used, 0)::bigint AS month_tokens_used
FROM api_key k
JOIN app_user au ON au.tenant_id = k.tenant_id AND au.id = k.user_id
LEFT JOIN api_key_usage_monthly u
  ON u.tenant_id = k.tenant_id AND u.api_key_id = k.id AND u.month_key = sqlc.arg(month_key)
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.prefix = sqlc.arg(prefix);

-- name: TouchAPIKeyLastUsed :exec
UPDATE api_key SET last_used_at = now()
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(key_id);

-- name: AddAPIKeyTokenUsage :exec
INSERT INTO api_key_usage_monthly (tenant_id, api_key_id, month_key, tokens_used)
VALUES (sqlc.arg(tenant_id), sqlc.arg(api_key_id), sqlc.arg(month_key), sqlc.arg(tokens))
ON CONFLICT (tenant_id, api_key_id, month_key)
DO UPDATE SET tokens_used = api_key_usage_monthly.tokens_used + EXCLUDED.tokens_used,
              updated_at = now();
