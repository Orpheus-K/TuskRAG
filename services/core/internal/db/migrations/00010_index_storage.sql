-- +goose Up
ALTER TABLE document_version ADD CONSTRAINT document_version_scope_key
  UNIQUE (tenant_id, kb_id, document_id, id);
ALTER TABLE embedding_space ADD CONSTRAINT embedding_space_dimension_key
  UNIQUE (tenant_id, id, dims);

CREATE TABLE section (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  document_id bigint NOT NULL,
  version_id bigint NOT NULL,
  parent_section_id bigint,
  ordinal integer NOT NULL CHECK (ordinal > 0),
  level smallint NOT NULL CHECK (level BETWEEN 0 AND 9),
  heading text NOT NULL CHECK (length(heading) <= 1000000),
  heading_path text[] NOT NULL CHECK (array_position(heading_path, NULL) IS NULL),
  page_start integer,
  page_end integer,
  body_md text NOT NULL CHECK (octet_length(body_md) <= 67108864),
  token_count integer NOT NULL CHECK (token_count >= 0),
  summary text,
  UNIQUE (tenant_id, version_id, ordinal),
  UNIQUE (tenant_id, kb_id, document_id, version_id, id),
  FOREIGN KEY (tenant_id, kb_id, document_id, version_id)
    REFERENCES document_version(tenant_id, kb_id, document_id, id),
  FOREIGN KEY (tenant_id, kb_id, document_id, version_id, parent_section_id)
    REFERENCES section(tenant_id, kb_id, document_id, version_id, id),
  CHECK (parent_section_id IS DISTINCT FROM id),
  CHECK ((page_start IS NULL AND page_end IS NULL) OR
    (page_start IS NOT NULL AND page_end IS NOT NULL AND page_start > 0 AND page_end >= page_start)),
  CHECK (cardinality(heading_path) = 0 OR (array_ndims(heading_path) = 1 AND array_lower(heading_path, 1) = 1)),
  CHECK (
    (level = 0 AND heading = '' AND cardinality(heading_path) = 0 AND parent_section_id IS NULL)
    OR (level > 0 AND length(btrim(heading)) > 0 AND cardinality(heading_path) BETWEEN 1 AND 9
      AND heading_path[cardinality(heading_path)] IS NOT DISTINCT FROM heading)
  )
);

CREATE TABLE chunk (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  document_id bigint NOT NULL,
  version_id bigint NOT NULL,
  section_id bigint NOT NULL,
  ordinal integer NOT NULL CHECK (ordinal > 0),
  kind text NOT NULL CHECK (kind IN ('text', 'table', 'list', 'code', 'figure_caption')),
  content text NOT NULL CHECK (octet_length(content) BETWEEN 1 AND 4194304),
  search_text text NOT NULL CHECK (octet_length(search_text) BETWEEN 1 AND 4194304),
  lexical_tokens tsvector,
  page_start integer,
  page_end integer,
  token_count integer NOT NULL CHECK (token_count BETWEEN 1 AND 800),
  is_current boolean NOT NULL DEFAULT false,
  disabled boolean NOT NULL DEFAULT false,
  access_tags text[] NOT NULL CHECK (cardinality(access_tags) > 0 AND array_ndims(access_tags) = 1 AND array_lower(access_tags, 1) = 1 AND array_position(access_tags, NULL) IS NULL AND array_position(access_tags, '') IS NULL),
  doc_tags text[] NOT NULL DEFAULT '{}' CHECK (array_position(doc_tags, NULL) IS NULL AND (cardinality(doc_tags) = 0 OR (array_ndims(doc_tags) = 1 AND array_lower(doc_tags, 1) = 1))),
  doc_mime text NOT NULL CHECK (length(doc_mime) > 0),
  doc_updated_at timestamptz NOT NULL,
  UNIQUE (tenant_id, version_id, ordinal),
  UNIQUE (tenant_id, kb_id, id),
  FOREIGN KEY (tenant_id, kb_id, document_id, version_id, section_id)
    REFERENCES section(tenant_id, kb_id, document_id, version_id, id),
  CHECK (NOT (is_current AND disabled)),
  CHECK ((page_start IS NULL AND page_end IS NULL) OR
    (page_start IS NOT NULL AND page_end IS NOT NULL AND page_start > 0 AND page_end >= page_start))
);

CREATE TABLE chunk_vector (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  chunk_id bigint NOT NULL,
  space_id bigint NOT NULL,
  is_current boolean NOT NULL DEFAULT false,
  access_tags text[] NOT NULL CHECK (cardinality(access_tags) > 0 AND array_ndims(access_tags) = 1 AND array_lower(access_tags, 1) = 1 AND array_position(access_tags, NULL) IS NULL AND array_position(access_tags, '') IS NULL),
  embedding halfvec NOT NULL CHECK (l2_norm(embedding) > 0),
  dims integer GENERATED ALWAYS AS (vector_dims(embedding)) STORED,
  PRIMARY KEY (tenant_id, chunk_id, space_id),
  FOREIGN KEY (tenant_id, kb_id, chunk_id) REFERENCES chunk(tenant_id, kb_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, space_id, dims) REFERENCES embedding_space(tenant_id, id, dims)
);

CREATE INDEX chunk_by_section ON chunk (tenant_id, section_id);
CREATE INDEX chunk_access_tags ON chunk USING gin (access_tags) WHERE is_current;
CREATE INDEX chunk_vector_access_tags ON chunk_vector USING gin (access_tags) WHERE is_current;

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['section', 'chunk', 'chunk_vector']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON section, chunk, chunk_vector TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE section_id_seq, chunk_id_seq TO tuskrag_app;
