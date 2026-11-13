-- +goose Up
CREATE TABLE user_group (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
  source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'oidc')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);

CREATE TABLE user_group_member (
  tenant_id bigint NOT NULL,
  group_id bigint NOT NULL,
  user_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, group_id, user_id),
  FOREIGN KEY (tenant_id, group_id) REFERENCES user_group(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX user_group_member_user_idx ON user_group_member (tenant_id, user_id, group_id);

CREATE TABLE document_acl (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  document_id bigint NOT NULL,
  subject_type text NOT NULL CHECK (subject_type IN ('user', 'group')),
  subject_id bigint NOT NULL CHECK (subject_id > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kb_id, document_id, subject_type, subject_id),
  FOREIGN KEY (tenant_id, kb_id, document_id) REFERENCES document(tenant_id, kb_id, id) ON DELETE CASCADE
);

CREATE INDEX document_acl_subject_idx ON document_acl (tenant_id, subject_type, subject_id, kb_id, document_id);

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['user_group', 'user_group_member', 'document_acl']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON user_group, user_group_member, document_acl TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE user_group_id_seq TO tuskrag_app;
