-- name: SetTenant :one
SELECT set_config('tuskrag.tenant_id', sqlc.arg(tenant_id)::bigint::text, true)::text AS tenant_context;

-- name: CurrentTenant :one
SELECT current_setting('tuskrag.tenant_id')::bigint AS tenant_id;

-- name: EnqueueJob :one
INSERT INTO job (tenant_id, kind, status, dedupe_key, payload, max_attempts)
VALUES (sqlc.arg(tenant_id), sqlc.arg(kind), 'queued', sqlc.narg(dedupe_key), sqlc.arg(payload), sqlc.arg(max_attempts))
ON CONFLICT (tenant_id, kind, dedupe_key) WHERE dedupe_key IS NOT NULL AND status IN ('queued', 'running')
DO UPDATE SET dedupe_key = EXCLUDED.dedupe_key
WHERE job.payload = EXCLUDED.payload AND job.max_attempts = EXCLUDED.max_attempts
RETURNING id;

-- name: LockExpiredJob :one
SELECT id, tenant_id, kind, payload FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND status = 'running'
  AND kind = ANY(sqlc.arg(kinds)::text[])
  AND lease_until <= clock_timestamp() AND attempts >= max_attempts
ORDER BY lease_until, id FOR UPDATE SKIP LOCKED LIMIT 1;

-- name: DeadLetterExpiredJob :one
UPDATE job SET status = 'dead', lease_owner = NULL, lease_until = NULL,
  last_error = 'lease_expired', finished_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status = 'running'
  AND lease_until <= clock_timestamp() AND attempts >= max_attempts
RETURNING id;

-- name: ClaimJob :one
WITH candidate AS (
  SELECT queued.id FROM job AS queued
  WHERE queued.tenant_id = sqlc.arg(tenant_id) AND queued.attempts < queued.max_attempts
    AND queued.kind = ANY(sqlc.arg(kinds)::text[])
    AND ((queued.status = 'queued' AND queued.run_after <= clock_timestamp())
      OR (queued.status = 'running' AND queued.lease_until <= clock_timestamp()))
  ORDER BY queued.run_after, queued.id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE job AS j SET status = 'running', lease_owner = sqlc.arg(owner),
  lease_until = clock_timestamp() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  lease_generation = j.lease_generation + 1, attempts = j.attempts + 1
FROM candidate WHERE j.id = candidate.id
RETURNING j.id, j.tenant_id, j.kind, j.payload, j.lease_generation, j.lease_until, j.attempts;

-- name: RenewLease :one
UPDATE job SET lease_until = clock_timestamp() + make_interval(secs => sqlc.arg(lease_seconds)::double precision)
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status = 'running'
  AND lease_owner = sqlc.arg(owner) AND lease_generation = sqlc.arg(generation)
  AND attempts = sqlc.arg(attempt)
  AND lease_until > clock_timestamp()
RETURNING lease_until;

-- name: LockLease :one
SELECT id FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status = 'running'
  AND lease_owner = sqlc.arg(owner) AND lease_generation = sqlc.arg(generation)
  AND attempts = sqlc.arg(attempt)
  AND lease_until > clock_timestamp()
FOR UPDATE;

-- name: CompleteJob :one
UPDATE job SET status = 'succeeded', lease_owner = NULL, lease_until = NULL, finished_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status = 'running'
  AND lease_owner = sqlc.arg(owner) AND lease_generation = sqlc.arg(generation)
  AND attempts = sqlc.arg(attempt)
  AND lease_until > clock_timestamp()
RETURNING id;

-- name: FailJob :one
UPDATE job SET
  status = CASE WHEN sqlc.arg(retryable)::boolean AND attempts < max_attempts THEN 'queued'
    WHEN sqlc.arg(retryable)::boolean THEN 'dead' ELSE 'failed' END,
  run_after = CASE WHEN sqlc.arg(retryable)::boolean AND attempts < max_attempts
    THEN clock_timestamp() + make_interval(secs => sqlc.arg(delay_seconds)::double precision) ELSE run_after END,
  last_error = sqlc.arg(failure_code), lease_owner = NULL, lease_until = NULL,
  finished_at = CASE WHEN sqlc.arg(retryable)::boolean AND attempts < max_attempts THEN NULL ELSE clock_timestamp() END
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status = 'running'
  AND lease_owner = sqlc.arg(owner) AND lease_generation = sqlc.arg(generation)
  AND attempts = sqlc.arg(attempt)
  AND lease_until > clock_timestamp()
RETURNING status;

-- name: PlanJobFailure :one
SELECT CASE WHEN sqlc.arg(retryable)::boolean AND attempts < max_attempts THEN 'queued'
  WHEN sqlc.arg(retryable)::boolean THEN 'dead' ELSE 'failed' END::text AS next_status
FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status = 'running'
  AND lease_owner = sqlc.arg(owner) AND lease_generation = sqlc.arg(generation)
  AND attempts = sqlc.arg(attempt) AND lease_until > clock_timestamp()
FOR UPDATE;

-- name: LockActiveJob :one
SELECT id FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND kind = sqlc.arg(kind)
  AND dedupe_key = sqlc.arg(dedupe_key) AND payload = sqlc.arg(payload)::jsonb
  AND status IN ('queued', 'running')
FOR UPDATE;

-- name: LockJobForControl :one
SELECT id, kind, dedupe_key, payload, status, attempts, max_attempts, last_error, created_at, finished_at
FROM job WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id)
FOR UPDATE;

-- name: LatestIngestJob :one
SELECT id FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND kind = 'ingest' AND dedupe_key = sqlc.arg(dedupe_key)
ORDER BY id DESC LIMIT 1;

-- name: AuditJobRetry :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, object_type, object_id, result)
VALUES (sqlc.arg(tenant_id), sqlc.arg(user_id), 'job.retry', 'job', sqlc.arg(job_id), 'allow');

-- name: LockNamedActiveIngest :one
SELECT id, (payload = sqlc.arg(payload)::jsonb)::boolean AS matches
FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND kind = 'ingest'
  AND dedupe_key = sqlc.arg(dedupe_key) AND status IN ('queued', 'running')
FOR UPDATE;

-- name: CancelJob :one
UPDATE job SET status = 'cancelled', lease_generation = lease_generation + 1,
  lease_owner = NULL, lease_until = NULL, finished_at = clock_timestamp()
WHERE tenant_id = sqlc.arg(tenant_id) AND id = sqlc.arg(job_id) AND status IN ('queued', 'running')
RETURNING kind;

-- name: NotifyJobReady :exec
SELECT pg_notify('tuskrag_job_ready', sqlc.arg(tenant_id)::bigint::text);

-- name: NotifyJobCancelled :exec
SELECT pg_notify('tuskrag_job_cancelled', sqlc.arg(job_id)::bigint::text);

-- name: ListenJobReady :exec
LISTEN tuskrag_job_ready;

-- name: ListenJobCancelled :exec
LISTEN tuskrag_job_cancelled;

-- name: LatestPurgeJob :one
SELECT id FROM job
WHERE tenant_id = sqlc.arg(tenant_id) AND kind = 'purge' AND dedupe_key = sqlc.arg(dedupe_key)
ORDER BY id DESC LIMIT 1;

-- name: ListKBJobsForPurge :many
SELECT j.id, j.dedupe_key, j.payload, j.status, j.attempts, j.max_attempts, j.last_error, j.created_at, j.finished_at,
       d.title AS document_title,
       (d.id IS NOT NULL AND d.deleted_at IS NOT NULL)::boolean AS document_deleted,
       v.id AS version_row_id,
       COALESCE(v.status, '')::text AS version_status,
       (d.current_version_id IS NOT NULL AND v.id IS NOT NULL AND d.current_version_id = v.id)::boolean AS version_is_current,
       NOT EXISTS (
         SELECT 1 FROM job newer
         WHERE newer.tenant_id = j.tenant_id AND newer.kind = 'purge'
           AND newer.dedupe_key = j.dedupe_key AND newer.id > j.id
       )::boolean AS is_latest
FROM job j
LEFT JOIN document d
  ON d.tenant_id = j.tenant_id AND d.kb_id = sqlc.arg(kb_id) AND d.id::text = j.payload->>'document_id'
LEFT JOIN document_version v
  ON v.tenant_id = j.tenant_id AND v.kb_id = sqlc.arg(kb_id) AND v.document_id = d.id AND v.id::text = j.payload->>'version_id'
WHERE j.tenant_id = sqlc.arg(tenant_id)
  AND j.kind = 'purge'
  AND j.payload @> jsonb_build_object('kb_id', sqlc.arg(kb_id)::bigint)
  AND (d.id IS NULL OR d.acl_mode = 'inherit_kb')
  AND (sqlc.arg(before_id)::bigint = 0 OR j.id < sqlc.arg(before_id)::bigint)
ORDER BY j.id DESC
LIMIT sqlc.arg(fetch_limit);
