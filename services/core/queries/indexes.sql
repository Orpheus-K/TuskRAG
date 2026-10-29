-- name: TryLockIndexProvisioning :one
SELECT pg_try_advisory_lock(841695642190183::bigint)::boolean;

-- name: TryLockIndexUse :one
SELECT pg_try_advisory_xact_lock_shared(841695642190183::bigint)::boolean;

-- name: SetIndexTenant :one
SELECT set_config('tuskrag.tenant_id', sqlc.arg(tenant_id)::text, false)::text;

-- name: GetIndexSpace :one
SELECT dims, distance FROM embedding_space
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(space_id);
