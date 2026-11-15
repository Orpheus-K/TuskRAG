-- name: UserExists :one
SELECT EXISTS(SELECT 1 FROM app_user WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(user_id));

-- name: EmbeddingSpaceExists :one
SELECT EXISTS(SELECT 1 FROM embedding_space WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(space_id));

-- name: TenantEgressPolicy :one
SELECT egress_policy_max FROM tenant WHERE id = sqlc.arg(tenant_id);

-- name: ResolveEmbeddingScope :one
SELECT k.tenant_id, k.id AS kb_id, k.egress_policy, t.egress_policy_max
FROM knowledge_base k JOIN tenant t ON t.id = k.tenant_id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id)
  AND k.embedding_space_id = sqlc.arg(space_id) AND k.config_revision = sqlc.arg(config_revision)
  AND k.status = 'active';

-- name: AuditKnowledgeBaseAction :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, target_user_id, new_role, result)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(action), 'knowledge_base', sqlc.arg(kb_id),
        sqlc.narg(target_user_id), sqlc.narg(new_role), sqlc.arg(result));

-- name: CreateKnowledgeBase :one
INSERT INTO knowledge_base (tenant_id, name, embedding_space_id, egress_policy, status)
VALUES (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(embedding_space_id), sqlc.arg(egress_policy), 'active')
RETURNING id, tenant_id, name, embedding_space_id, config_revision, egress_policy, status, created_at;

-- name: AddKnowledgeBaseMember :exec
INSERT INTO kb_membership (tenant_id, kb_id, user_id, role)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(user_id), sqlc.arg(role));

-- name: ListVisibleKnowledgeBases :many
SELECT k.id, k.tenant_id, k.name, k.embedding_space_id, k.config_revision, k.egress_policy, k.status, k.created_at, m.role
FROM knowledge_base k JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND m.user_id = sqlc.arg(user_id)
ORDER BY k.id;

-- name: GetVisibleKnowledgeBase :one
SELECT k.id, k.tenant_id, k.name, k.embedding_space_id, k.config_revision, k.egress_policy, k.status, k.created_at, m.role
FROM knowledge_base k JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id) AND m.user_id = sqlc.arg(user_id);

-- name: LockVisibleKnowledgeBase :one
SELECT k.id FROM knowledge_base k JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id) AND m.user_id = sqlc.arg(user_id)
FOR UPDATE OF k;

-- name: LockMemberRole :one
SELECT role FROM kb_membership
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND user_id = sqlc.arg(user_id)
FOR SHARE;

-- name: GetMemberRole :one
SELECT role FROM kb_membership
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND user_id = sqlc.arg(user_id);

-- name: CountKnowledgeBaseOwners :one
SELECT count(*) FROM kb_membership WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND role = 'owner';

-- name: SetKnowledgeBaseMember :exec
INSERT INTO kb_membership (tenant_id, kb_id, user_id, role)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(user_id), sqlc.arg(role))
ON CONFLICT (tenant_id, kb_id, user_id) DO UPDATE SET role = EXCLUDED.role;

-- name: RemoveKnowledgeBaseMember :execrows
DELETE FROM kb_membership WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND user_id = sqlc.arg(user_id);

-- name: ArchiveKnowledgeBase :exec
UPDATE knowledge_base SET status = 'archived' WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(kb_id);

-- name: ListKnowledgeBaseMembers :many
SELECT m.user_id, u.display_name, m.role, m.created_at
FROM kb_membership m
JOIN app_user u ON u.tenant_id = m.tenant_id AND u.id = m.user_id
WHERE m.tenant_id = sqlc.arg(tenant_id) AND m.kb_id = sqlc.arg(kb_id)
ORDER BY m.user_id;

-- name: ListTenantUsers :many
SELECT id, display_name, created_at
FROM app_user
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY id;

-- name: SetKnowledgeBaseEgressPolicy :one
UPDATE knowledge_base
SET egress_policy = sqlc.arg(egress_policy)
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(kb_id) AND status = 'active'
RETURNING id, tenant_id, name, embedding_space_id, config_revision, egress_policy, status, created_at;

-- name: SetKnowledgeBaseTrustTier :execrows
UPDATE knowledge_base
SET trust_tier = sqlc.arg(trust_tier)
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(kb_id) AND status = 'active';

-- name: GetKnowledgeBaseTrustTier :one
SELECT k.trust_tier
FROM knowledge_base k
JOIN kb_membership m ON m.tenant_id = k.tenant_id AND m.kb_id = k.id
WHERE k.tenant_id = sqlc.arg(tenant_id) AND k.id = sqlc.arg(kb_id) AND m.user_id = sqlc.arg(user_id);
