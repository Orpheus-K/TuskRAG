-- name: NextTenantID :one
SELECT nextval('tenant_id_seq')::bigint;

-- name: IdentityTenantExists :one
SELECT EXISTS(SELECT 1 FROM tenant WHERE id = sqlc.arg(tenant_id));

-- name: BootstrapTenant :exec
INSERT INTO tenant (id, name) OVERRIDING SYSTEM VALUE VALUES (sqlc.arg(tenant_id), sqlc.arg(name));

-- name: BootstrapUser :one
INSERT INTO app_user (tenant_id, display_name) VALUES (sqlc.arg(tenant_id), sqlc.arg(display_name)) RETURNING id;

-- name: BootstrapLocalAccount :exec
INSERT INTO local_account (tenant_id, user_id, login, password_hash)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(login), sqlc.arg(password_hash));

-- name: LookupLocalAccount :one
SELECT user_id, password_hash, credential_version, disabled, COALESCE(locked_until > clock_timestamp(), false)::boolean AS locked
FROM local_account WHERE tenant_id = sqlc.arg(tenant_id) AND login = sqlc.arg(login);

-- name: LockLocalAccount :one
SELECT user_id, password_hash, credential_version, disabled, COALESCE(locked_until > clock_timestamp(), false)::boolean AS locked
FROM local_account WHERE tenant_id = sqlc.arg(tenant_id) AND login = sqlc.arg(login)
FOR UPDATE;

-- name: LocalLoginFailed :exec
UPDATE local_account SET failed_attempts = failed_attempts + 1,
  locked_until = CASE WHEN failed_attempts + 1 >= 5 THEN clock_timestamp() + interval '15 minutes' ELSE locked_until END
WHERE tenant_id = sqlc.arg(tenant_id) AND user_id = sqlc.arg(user_id);

-- name: LocalLoginSucceeded :exec
UPDATE local_account SET failed_attempts = 0, locked_until = NULL
WHERE tenant_id = sqlc.arg(tenant_id) AND user_id = sqlc.arg(user_id);

-- name: CreateSession :exec
INSERT INTO login_session (tenant_id, user_id, token_hash, absolute_expires_at)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(token_hash),
        clock_timestamp() + make_interval(secs => sqlc.arg(absolute_seconds)::double precision));

-- name: AuthenticateSession :one
UPDATE login_session AS s SET last_seen_at = clock_timestamp()
FROM local_account AS a
WHERE s.tenant_id = sqlc.arg(tenant_id) AND s.token_hash = sqlc.arg(token_hash)
  AND a.tenant_id = s.tenant_id AND a.user_id = s.user_id AND NOT a.disabled
  AND s.revoked_at IS NULL AND s.absolute_expires_at > clock_timestamp()
  AND s.last_seen_at > clock_timestamp() - make_interval(secs => sqlc.arg(idle_seconds)::double precision)
RETURNING s.user_id;

-- name: RevokeSession :one
UPDATE login_session SET revoked_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id) AND token_hash = sqlc.arg(token_hash) AND revoked_at IS NULL
RETURNING user_id;

-- name: AuditIdentityAction :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), sqlc.arg(action), 'identity', sqlc.arg(object_id), sqlc.arg(result));
