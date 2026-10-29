-- +goose Up
-- List and retry the newest ingest job without scanning unrelated job history.
CREATE INDEX job_ingest_history ON public.job (tenant_id, dedupe_key, id DESC)
  WHERE kind = 'ingest' AND dedupe_key IS NOT NULL;
