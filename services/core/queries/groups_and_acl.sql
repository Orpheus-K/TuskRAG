-- name: CreateUserGroup :one
INSERT INTO user_group (tenant_id, name, source)
VALUES (sqlc.arg(tenant_id), sqlc.arg(name), sqlc.arg(source))
RETURNING id, tenant_id, name, source, created_at;

-- name: ListUserGroups :many
SELECT id, tenant_id, name, source, created_at
FROM user_group
WHERE tenant_id = sqlc.arg(tenant_id)
ORDER BY id;

-- name: GetUserGroup :one
SELECT id, tenant_id, name, source, created_at
FROM user_group
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(group_id);

-- name: GroupExists :one
SELECT EXISTS(
  SELECT 1 FROM user_group
  WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(group_id)
);

-- name: DeleteUserGroup :execrows
DELETE FROM user_group
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(group_id);

-- name: ListUserGroupMembers :many
SELECT gm.user_id, u.display_name, gm.created_at
FROM user_group_member gm
JOIN app_user u ON u.tenant_id = gm.tenant_id AND u.id = gm.user_id
WHERE gm.tenant_id = sqlc.arg(tenant_id) AND gm.group_id = sqlc.arg(group_id)
ORDER BY gm.user_id;

-- name: AddUserGroupMember :exec
INSERT INTO user_group_member (tenant_id, group_id, user_id)
VALUES (sqlc.arg(tenant_id), sqlc.arg(group_id), sqlc.arg(user_id))
ON CONFLICT (tenant_id, group_id, user_id) DO NOTHING;

-- name: RemoveUserGroupMember :execrows
DELETE FROM user_group_member
WHERE tenant_id = sqlc.arg(tenant_id) AND group_id = sqlc.arg(group_id) AND user_id = sqlc.arg(user_id);

-- name: ListUserGroupIDs :many
SELECT group_id
FROM user_group_member
WHERE tenant_id = sqlc.arg(tenant_id) AND user_id = sqlc.arg(user_id)
ORDER BY group_id;

-- name: GetDocumentACL :many
SELECT subject_type, subject_id, created_at
FROM document_acl
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND document_id = sqlc.arg(document_id)
ORDER BY subject_type, subject_id;

-- name: LockDocumentForACL :one
SELECT d.id, d.acl_mode, d.acl_revision
FROM document d
WHERE d.tenant_id = sqlc.arg(tenant_id) AND d.kb_id = sqlc.arg(kb_id) AND d.id = sqlc.arg(document_id)
  AND d.deleted_at IS NULL
FOR UPDATE OF d;

-- name: UpdateDocumentACLMode :one
UPDATE document
SET acl_mode = sqlc.arg(acl_mode), acl_revision = acl_revision + 1
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND id = sqlc.arg(document_id)
  AND deleted_at IS NULL
RETURNING acl_revision;

-- name: DeleteDocumentACL :exec
DELETE FROM document_acl
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND document_id = sqlc.arg(document_id);

-- name: InsertDocumentACL :exec
INSERT INTO document_acl (tenant_id, kb_id, document_id, subject_type, subject_id)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kb_id), sqlc.arg(document_id), sqlc.arg(subject_type), sqlc.arg(subject_id))
ON CONFLICT (tenant_id, kb_id, document_id, subject_type, subject_id) DO NOTHING;

-- name: EnqueueACLRefreshJob :one
INSERT INTO job (tenant_id, kind, status, dedupe_key, payload, max_attempts)
VALUES (sqlc.arg(tenant_id), 'acl_refresh', 'queued', sqlc.arg(dedupe_key), sqlc.arg(payload), 3)
ON CONFLICT (tenant_id, kind, dedupe_key) WHERE dedupe_key IS NOT NULL AND status IN ('queued', 'running')
DO UPDATE SET payload = EXCLUDED.payload
RETURNING id;

-- name: RefreshChunkAccessTags :exec
UPDATE chunk
SET access_tags = sqlc.arg(access_tags)::text[]
WHERE tenant_id = sqlc.arg(tenant_id) AND kb_id = sqlc.arg(kb_id) AND document_id = sqlc.arg(document_id);

-- name: RefreshChunkVectorAccessTags :exec
UPDATE chunk_vector v
SET access_tags = sqlc.arg(access_tags)::text[]
FROM chunk c
WHERE v.tenant_id = sqlc.arg(tenant_id) AND v.kb_id = sqlc.arg(kb_id)
  AND c.tenant_id = v.tenant_id AND c.kb_id = v.kb_id AND c.id = v.chunk_id
  AND c.document_id = sqlc.arg(document_id);
