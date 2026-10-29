-- +goose Up
ALTER TABLE job RENAME COLUMN state TO status;
ALTER TABLE job DROP CONSTRAINT job_state_check;
ALTER TABLE job DROP CONSTRAINT job_tenant_id_dedupe_key_key;
ALTER TABLE job ALTER COLUMN dedupe_key DROP NOT NULL;
ALTER TABLE job
  ADD COLUMN max_attempts integer NOT NULL DEFAULT 3 CHECK (max_attempts > 0),
  ADD COLUMN run_after timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN lease_owner text,
  ADD COLUMN last_error text,
  ADD COLUMN finished_at timestamptz,
  ADD CONSTRAINT job_status_check CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'dead', 'cancelled'));

CREATE UNIQUE INDEX job_active_dedupe ON job (tenant_id, kind, dedupe_key)
  WHERE dedupe_key IS NOT NULL AND status IN ('queued', 'running');
CREATE INDEX job_queued_by_tenant ON job (tenant_id, run_after) WHERE status = 'queued';
