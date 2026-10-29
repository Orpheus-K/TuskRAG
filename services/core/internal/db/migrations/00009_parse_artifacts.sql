-- +goose Up
CREATE TABLE parse_artifact (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  document_id bigint NOT NULL,
  version_id bigint NOT NULL,
  content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
  parsed_document bytea NOT NULL CHECK (octet_length(parsed_document) BETWEEN 1 AND 209715200),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, version_id),
  FOREIGN KEY (tenant_id, kb_id, document_id) REFERENCES document(tenant_id, kb_id, id),
  FOREIGN KEY (tenant_id, document_id, version_id) REFERENCES document_version(tenant_id, document_id, id)
);
ALTER TABLE parse_artifact ENABLE ROW LEVEL SECURITY;
ALTER TABLE parse_artifact FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON parse_artifact
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON parse_artifact TO tuskrag_app;
