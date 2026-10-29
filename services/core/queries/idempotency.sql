-- name: ReserveIdempotency :one
INSERT INTO idempotency_record (tenant_id, actor_user_id, operation, request_key, request_hash)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(operation), sqlc.arg(request_key), sqlc.arg(request_hash))
ON CONFLICT (tenant_id, actor_user_id, operation, request_key) DO NOTHING
RETURNING request_key;

-- name: FindIdempotency :one
SELECT request_hash, object_id FROM idempotency_record
WHERE tenant_id = sqlc.arg(tenant_id) AND actor_user_id = sqlc.arg(user_id)
  AND operation = sqlc.arg(operation) AND request_key = sqlc.arg(request_key);

-- name: CompleteIdempotency :execrows
UPDATE idempotency_record SET object_id = sqlc.arg(object_id)
WHERE tenant_id = sqlc.arg(tenant_id) AND actor_user_id = sqlc.arg(user_id)
  AND operation = sqlc.arg(operation) AND request_key = sqlc.arg(request_key) AND object_id IS NULL;
