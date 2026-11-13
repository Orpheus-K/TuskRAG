-- +goose Up
CREATE TABLE eval_run_result (
  run_id bigint PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  report jsonb NOT NULL CHECK (jsonb_typeof(report)='object' AND octet_length(report::text)<=16777216),
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (tenant_id,kb_id,run_id) REFERENCES eval_run(tenant_id,kb_id,id) ON DELETE CASCADE
);
ALTER TABLE eval_run_result ENABLE ROW LEVEL SECURITY;
ALTER TABLE eval_run_result FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON eval_run_result
  USING (tenant_id=current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id=current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON eval_run_result TO tuskrag_app;
