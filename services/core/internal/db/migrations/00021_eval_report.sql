-- +goose Up
CREATE TABLE eval_report (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  source text NOT NULL CHECK (source = 'imported'),
  report jsonb NOT NULL CHECK (jsonb_typeof(report) = 'object' AND octet_length(report::text) <= 16777216),
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kb_id, digest),
  FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_base(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user(tenant_id, id)
);
ALTER TABLE eval_report ENABLE ROW LEVEL SECURITY;
ALTER TABLE eval_report FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON eval_report
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON eval_report TO tuskrag_app;
