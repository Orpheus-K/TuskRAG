-- +goose Up
CREATE TABLE eval_set (
  id bigserial PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  UNIQUE (tenant_id, kb_id, id),
  FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_base(tenant_id, id) ON DELETE CASCADE
);
CREATE TABLE eval_set_revision (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  set_id bigint NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  definition jsonb NOT NULL CHECK (jsonb_typeof(definition) = 'object' AND octet_length(definition::text) <= 16777216),
  digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kb_id, set_id, revision),
  FOREIGN KEY (tenant_id, kb_id, set_id) REFERENCES eval_set(tenant_id, kb_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user(tenant_id, id)
);
ALTER TABLE eval_set ADD CONSTRAINT eval_set_head_revision
  FOREIGN KEY (tenant_id, kb_id, id, revision)
  REFERENCES eval_set_revision(tenant_id, kb_id, set_id, revision)
  DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE eval_set ENABLE ROW LEVEL SECURITY;
ALTER TABLE eval_set FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON eval_set
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
ALTER TABLE eval_set_revision ENABLE ROW LEVEL SECURITY;
ALTER TABLE eval_set_revision FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON eval_set_revision
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON eval_set, eval_set_revision TO tuskrag_app;
GRANT UPDATE (revision) ON eval_set TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE eval_set_id_seq TO tuskrag_app;
