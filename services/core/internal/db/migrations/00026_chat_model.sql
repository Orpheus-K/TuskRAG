-- +goose Up
CREATE TABLE chat_model (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  provider_id bigint NOT NULL,
  model text NOT NULL CHECK (length(btrim(model)) BETWEEN 1 AND 200),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, provider_id) REFERENCES model_provider(tenant_id, id)
);
ALTER TABLE chat_model ENABLE ROW LEVEL SECURITY;
ALTER TABLE chat_model FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON chat_model
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON chat_model TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE chat_model_id_seq TO tuskrag_app;
