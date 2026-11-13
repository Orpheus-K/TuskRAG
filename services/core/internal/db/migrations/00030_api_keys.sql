-- +goose Up
ALTER TABLE app_user ADD COLUMN disabled boolean NOT NULL DEFAULT false;

CREATE TABLE api_key (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  user_id bigint NOT NULL,
  name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 120),
  prefix text NOT NULL UNIQUE CHECK (length(prefix) = 8),
  secret_hash bytea NOT NULL CHECK (octet_length(secret_hash) = 32),
  kb_ids bigint[] NOT NULL DEFAULT '{}',
  actions text[] NOT NULL CHECK (
    cardinality(actions) > 0
    AND actions <@ ARRAY['search', 'read', 'answer', 'mcp', 'ingest']::text[]
  ),
  ip_allowlist text[] NOT NULL DEFAULT '{}',
  rate_limit_rpm integer NOT NULL DEFAULT 60 CHECK (rate_limit_rpm > 0),
  monthly_token_quota bigint NOT NULL DEFAULT 0 CHECK (monthly_token_quota >= 0),
  expires_at timestamptz,
  revoked_at timestamptz,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id)
);

CREATE INDEX api_key_user_idx ON api_key (tenant_id, user_id, id);

CREATE TABLE api_key_usage_monthly (
  tenant_id bigint NOT NULL,
  api_key_id bigint NOT NULL,
  month_key text NOT NULL CHECK (month_key ~ '^[0-9]{4}-[0-9]{2}$'),
  tokens_used bigint NOT NULL DEFAULT 0 CHECK (tokens_used >= 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, api_key_id, month_key),
  FOREIGN KEY (tenant_id, api_key_id) REFERENCES api_key(tenant_id, id) ON DELETE CASCADE
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION resolve_api_key_tenant(p_prefix text)
RETURNS bigint
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public, pg_catalog
AS $$
  SELECT tenant_id FROM api_key WHERE prefix = p_prefix LIMIT 1;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION resolve_api_key_tenant(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION resolve_api_key_tenant(text) TO tuskrag_app;

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['api_key', 'api_key_usage_monthly']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON api_key, api_key_usage_monthly TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE api_key_id_seq TO tuskrag_app;
