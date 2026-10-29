-- +goose Up
CREATE TABLE tenant (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app_user (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  display_name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

CREATE TABLE model_provider (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  name text NOT NULL,
  base_url text NOT NULL,
  locality text NOT NULL CHECK (locality IN ('internal', 'external')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);

CREATE TABLE embedding_space (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  provider_id bigint NOT NULL,
  model text NOT NULL,
  dims integer NOT NULL CHECK (dims BETWEEN 64 AND 4000),
  normalized boolean NOT NULL,
  distance text NOT NULL CHECK (distance IN ('cosine', 'ip')),
  index_name text,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, provider_id) REFERENCES model_provider(tenant_id, id)
);

CREATE TABLE knowledge_base (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  name text NOT NULL,
  embedding_space_id bigint NOT NULL,
  pending_space_id bigint,
  config_revision integer NOT NULL DEFAULT 1 CHECK (config_revision > 0),
  egress_policy text NOT NULL CHECK (egress_policy IN ('deny', 'internal_only', 'allow')),
  status text NOT NULL CHECK (status IN ('active', 'archived')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name),
  FOREIGN KEY (tenant_id, embedding_space_id) REFERENCES embedding_space(tenant_id, id),
  FOREIGN KEY (tenant_id, pending_space_id) REFERENCES embedding_space(tenant_id, id)
);

CREATE TABLE job (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  kind text NOT NULL,
  state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
  dedupe_key text NOT NULL,
  payload jsonb NOT NULL,
  lease_generation bigint NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
  lease_until timestamptz,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, dedupe_key)
);

ALTER TABLE tenant ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant
  USING (id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (id = current_setting('tuskrag.tenant_id')::bigint);

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['app_user', 'model_provider', 'embedding_space', 'knowledge_base', 'job']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd

GRANT USAGE ON SCHEMA public TO tuskrag_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON tenant, app_user, model_provider, embedding_space, knowledge_base, job TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE tenant_id_seq, app_user_id_seq, model_provider_id_seq, embedding_space_id_seq, knowledge_base_id_seq, job_id_seq TO tuskrag_app;
GRANT SELECT ON goose_db_version TO tuskrag_app;
