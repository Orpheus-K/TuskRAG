-- name: GetTenantAuthPolicy :one
SELECT disable_local_login,
  EXISTS (
    SELECT 1 FROM oidc_provider p WHERE p.tenant_id = tenant.id AND p.enabled = true
  )::boolean AS has_enabled_oidc
FROM tenant
WHERE tenant.id = sqlc.arg(tenant_id);

-- name: UpdateTenantDisableLocalLogin :execrows
UPDATE tenant SET disable_local_login = sqlc.arg(disable_local_login)
WHERE id = sqlc.arg(tenant_id);

-- name: ListOIDCProviders :many
SELECT id, tenant_id, name, issuer_url, client_id,
  (client_secret_ciphertext IS NOT NULL)::boolean AS has_client_secret,
  scopes, group_claim, enabled, created_at
FROM oidc_provider
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY id;

-- name: ListPublicOIDCProviders :many
SELECT id, name
FROM oidc_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND enabled = true
ORDER BY id;

-- name: GetOIDCProvider :one
SELECT id, tenant_id, name, issuer_url, client_id,
  client_secret_id, client_secret_nonce, client_secret_ciphertext,
  scopes, group_claim, enabled, created_at
FROM oidc_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: CreateOIDCProvider :one
INSERT INTO oidc_provider (tenant_id, name, issuer_url, client_id, scopes, group_claim, enabled)
VALUES (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(issuer_url), sqlc.arg(client_id), sqlc.arg(scopes), sqlc.arg(group_claim), sqlc.arg(enabled))
RETURNING id;

-- name: SetOIDCProviderSecret :exec
UPDATE oidc_provider
SET client_secret_id = sqlc.arg(key_id),
    client_secret_nonce = sqlc.arg(nonce),
    client_secret_ciphertext = sqlc.arg(ciphertext)
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: DeleteOIDCProvider :execrows
DELETE FROM oidc_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(provider_id);

-- name: InsertOIDCAuthState :exec
INSERT INTO oidc_auth_state (state, tenant_id, provider_id, code_verifier, nonce, redirect_uri, expires_at)
VALUES (sqlc.arg(state), sqlc.arg(tenant_id), sqlc.arg(provider_id), sqlc.arg(code_verifier), sqlc.arg(nonce), sqlc.arg(redirect_uri), sqlc.arg(expires_at));

-- name: ResolveOIDCAuthStateTenant :one
SELECT COALESCE(resolve_oidc_auth_state_tenant(sqlc.arg(state)), 0)::bigint AS tenant_id;

-- name: ConsumeOIDCAuthState :one
DELETE FROM oidc_auth_state
WHERE state = sqlc.arg(state) AND tenant_id = sqlc.arg(tenant_id) AND expires_at > now()
RETURNING provider_id, code_verifier, nonce, redirect_uri;

-- name: GetOIDCIdentityUser :one
SELECT i.user_id, u.disabled
FROM oidc_identity i
JOIN app_user u ON u.tenant_id = i.tenant_id AND u.id = i.user_id
WHERE i.tenant_id = sqlc.arg(tenant_id) AND i.issuer = sqlc.arg(issuer) AND i.subject = sqlc.arg(subject);

-- name: TouchOIDCIdentity :exec
UPDATE oidc_identity
SET last_login_at = now()
WHERE tenant_id = sqlc.arg(tenant_id) AND issuer = sqlc.arg(issuer) AND subject = sqlc.arg(subject);

-- name: CreateOIDCUser :one
INSERT INTO app_user (tenant_id, display_name)
VALUES (sqlc.arg(tenant_id), sqlc.arg(display_name))
RETURNING id;

-- name: InsertOIDCIdentity :exec
INSERT INTO oidc_identity (tenant_id, provider_id, issuer, subject, user_id)
VALUES (sqlc.arg(tenant_id), sqlc.arg(provider_id), sqlc.arg(issuer), sqlc.arg(subject), sqlc.arg(user_id));

-- name: UpsertOIDCGroup :one
INSERT INTO user_group (tenant_id, name, source)
VALUES (sqlc.arg(tenant_id), sqlc.arg(name), 'oidc')
ON CONFLICT (tenant_id, name) DO UPDATE
SET source = CASE WHEN user_group.source = 'oidc' THEN 'oidc' ELSE user_group.source END
RETURNING id, source;

-- name: AddUserToOIDCGroup :exec
INSERT INTO user_group_member (tenant_id, group_id, user_id)
VALUES (sqlc.arg(tenant_id), sqlc.arg(group_id), sqlc.arg(user_id))
ON CONFLICT DO NOTHING;

-- name: RemoveStaleOIDCGroupMemberships :exec
DELETE FROM user_group_member m
USING user_group g
WHERE m.tenant_id = g.tenant_id
  AND m.group_id = g.id
  AND m.tenant_id = sqlc.arg(tenant_id)
  AND m.user_id = sqlc.arg(user_id)
  AND g.source = 'oidc'
  AND NOT (g.id = ANY(sqlc.arg(keep_group_ids)::bigint[]));

-- name: ResolveOIDCProviderTenantByIssuer :one
SELECT COALESCE(resolve_oidc_provider_tenant_by_issuer(sqlc.arg(issuer)::text, sqlc.narg(tenant_id)::bigint), 0)::bigint AS tenant_id;

-- name: GetEnabledOIDCProviderByIssuer :one
SELECT id, tenant_id, name, issuer_url, client_id, scopes, group_claim, enabled, created_at
FROM oidc_provider
WHERE tenant_id = sqlc.arg(tenant_id) AND enabled = true AND rtrim(issuer_url, '/') = rtrim(sqlc.arg(issuer)::text, '/');

-- name: ListPublicOIDCIssuers :one
SELECT list_public_oidc_issuers(sqlc.narg(tenant_id)::bigint)::text[] AS issuers;
