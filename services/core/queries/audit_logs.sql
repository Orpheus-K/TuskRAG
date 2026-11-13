-- name: ListAuditLogs :many
SELECT a.id, a.tenant_id, a.actor_user_id, COALESCE(u.display_name, '') AS actor_name,
  a.action, a.object_type, a.object_id, a.result, a.target_user_id, a.new_role, a.details, a.created_at
FROM audit_log a
LEFT JOIN app_user u ON u.tenant_id = a.tenant_id AND u.id = a.actor_user_id
WHERE a.tenant_id = sqlc.arg(tenant_id)
  AND (sqlc.narg(action)::text IS NULL OR a.action = sqlc.narg(action)::text)
  AND (sqlc.narg(actor_user_id)::bigint IS NULL OR a.actor_user_id = sqlc.narg(actor_user_id)::bigint)
  AND (sqlc.narg(object_type)::text IS NULL OR a.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(result)::text IS NULL OR a.result = sqlc.narg(result)::text)
  AND (sqlc.narg(from_time)::timestamptz IS NULL OR a.created_at >= sqlc.narg(from_time)::timestamptz)
  AND (sqlc.narg(to_time)::timestamptz IS NULL OR a.created_at <= sqlc.narg(to_time)::timestamptz)
  AND (sqlc.narg(before_id)::bigint IS NULL OR a.id < sqlc.narg(before_id)::bigint)
ORDER BY a.id DESC
LIMIT sqlc.arg(limit_count);

-- name: InsertAuditEvent :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result, details)
VALUES (sqlc.arg(tenant_id), sqlc.arg(actor_user_id), sqlc.arg(action), sqlc.arg(object_type), sqlc.arg(object_id), sqlc.arg(result), sqlc.arg(details));

-- name: GetTenantEgressPolicy :one
SELECT egress_policy_max FROM tenant WHERE id = sqlc.arg(tenant_id);

-- name: UpdateTenantEgressPolicy :execrows
UPDATE tenant SET egress_policy_max = sqlc.arg(egress_policy_max) WHERE id = sqlc.arg(tenant_id);
