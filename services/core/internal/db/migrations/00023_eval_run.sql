-- +goose Up
ALTER TABLE job ADD CONSTRAINT job_tenant_id_id_key UNIQUE (tenant_id, id);
CREATE TABLE eval_run (
  id bigserial PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  job_id bigint NOT NULL UNIQUE,
  set_id bigint NOT NULL,
  set_revision integer NOT NULL CHECK (set_revision > 0),
  set_digest text NOT NULL CHECK (set_digest ~ '^[0-9a-f]{64}$'),
  mode text NOT NULL CHECK (mode IN ('lexical','vector','rrf','rerank')),
  configuration jsonb NOT NULL CHECK (jsonb_typeof(configuration) = 'object' AND octet_length(configuration::text) <= 2097152),
  config_digest text NOT NULL CHECK (config_digest ~ '^[0-9a-f]{64}$'),
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, kb_id, id),
  FOREIGN KEY (tenant_id, job_id) REFERENCES job(tenant_id, id),
  FOREIGN KEY (tenant_id, kb_id, set_id, set_revision) REFERENCES eval_set_revision(tenant_id, kb_id, set_id, revision),
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user(tenant_id, id)
);
ALTER TABLE eval_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE eval_run FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON eval_run
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON eval_run TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE eval_run_id_seq TO tuskrag_app;
