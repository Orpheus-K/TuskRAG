-- +goose Up
CREATE TABLE ingest_plan (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  document_id bigint NOT NULL,
  version_id bigint NOT NULL,
  embedding_space_id bigint NOT NULL,
  config_revision integer NOT NULL CHECK (config_revision > 0),
  plan_sha256 bytea NOT NULL CHECK (octet_length(plan_sha256) = 32),
  section_count integer NOT NULL CHECK (section_count BETWEEN 1 AND 10000),
  chunk_count integer NOT NULL CHECK (chunk_count BETWEEN 1 AND 10000),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, version_id),
  FOREIGN KEY (tenant_id, kb_id, document_id, version_id)
    REFERENCES document_version(tenant_id, kb_id, document_id, id),
  FOREIGN KEY (tenant_id, embedding_space_id) REFERENCES embedding_space(tenant_id, id)
);

ALTER TABLE ingest_plan ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingest_plan FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ingest_plan
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT, DELETE ON ingest_plan TO tuskrag_app;
