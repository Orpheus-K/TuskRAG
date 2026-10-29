-- +goose Up
CREATE TABLE document (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
  mime text NOT NULL,
  current_version_id bigint,
  acl_mode text NOT NULL DEFAULT 'inherit_kb' CHECK (acl_mode IN ('inherit_kb', 'restricted')),
  acl_revision integer NOT NULL DEFAULT 1 CHECK (acl_revision > 0),
  tags text[] NOT NULL DEFAULT '{}',
  deleted_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, kb_id, id),
  FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_base(tenant_id, id)
);

CREATE TABLE document_version (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  document_id bigint NOT NULL,
  content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
  byte_size bigint NOT NULL CHECK (byte_size BETWEEN 1 AND 209715200),
  file_key text NOT NULL,
  status text NOT NULL CHECK (status IN ('queued', 'parsing', 'embedding', 'indexing', 'ready', 'partial', 'failed', 'superseded', 'purging')),
  failure_code text,
  failure_detail text,
  parser_profile text,
  page_count integer CHECK (page_count > 0),
  vlm_page_count integer NOT NULL DEFAULT 0 CHECK (vlm_page_count >= 0),
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, document_id, id),
  UNIQUE (tenant_id, document_id, content_sha256),
  UNIQUE (tenant_id, file_key),
  FOREIGN KEY (tenant_id, kb_id, document_id) REFERENCES document(tenant_id, kb_id, id),
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user(tenant_id, id)
);
ALTER TABLE document ADD CONSTRAINT document_current_version_fk
  FOREIGN KEY (tenant_id, id, current_version_id) REFERENCES document_version(tenant_id, document_id, id);
CREATE INDEX document_by_kb ON document (tenant_id, kb_id, id) WHERE deleted_at IS NULL;
CREATE INDEX document_version_by_content ON document_version (tenant_id, kb_id, content_sha256);

ALTER TABLE document ENABLE ROW LEVEL SECURITY;
ALTER TABLE document FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON document
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
ALTER TABLE document_version ENABLE ROW LEVEL SECURITY;
ALTER TABLE document_version FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON document_version
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON document, document_version TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE document_id_seq, document_version_id_seq TO tuskrag_app;
