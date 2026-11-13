-- +goose Up
ALTER TABLE tenant ADD COLUMN disable_local_login boolean NOT NULL DEFAULT false;

CREATE TABLE oidc_provider (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  name text NOT NULL,
  issuer_url text NOT NULL,
  client_id text NOT NULL,
  client_secret_id text,
  client_secret_nonce bytea,
  client_secret_ciphertext bytea,
  scopes text NOT NULL DEFAULT 'openid profile email',
  group_claim text NOT NULL DEFAULT 'groups',
  enabled boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name),
  UNIQUE (tenant_id, issuer_url)
);

CREATE TABLE oidc_identity (
  tenant_id bigint NOT NULL,
  provider_id bigint NOT NULL REFERENCES oidc_provider(id) ON DELETE CASCADE,
  issuer text NOT NULL,
  subject text NOT NULL,
  user_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_login_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, issuer, subject),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id)
);

CREATE TABLE oidc_auth_state (
  state text PRIMARY KEY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  provider_id bigint NOT NULL REFERENCES oidc_provider(id) ON DELETE CASCADE,
  code_verifier text NOT NULL,
  nonce text NOT NULL,
  redirect_uri text NOT NULL,
  expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION resolve_oidc_auth_state_tenant(p_state text)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
  SELECT s.tenant_id
  FROM oidc_auth_state s
  WHERE s.state = p_state AND s.expires_at > now()
  LIMIT 1;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['oidc_provider', 'oidc_identity', 'oidc_auth_state']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd

GRANT SELECT, INSERT, UPDATE, DELETE ON oidc_provider, oidc_identity, oidc_auth_state TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE oidc_provider_id_seq TO tuskrag_app;
GRANT EXECUTE ON FUNCTION resolve_oidc_auth_state_tenant(text) TO tuskrag_app;

-- +goose Down
DROP FUNCTION IF EXISTS resolve_oidc_auth_state_tenant(text);
DROP TABLE IF EXISTS oidc_auth_state;
DROP TABLE IF EXISTS oidc_identity;
DROP TABLE IF EXISTS oidc_provider;
ALTER TABLE tenant DROP COLUMN IF EXISTS disable_local_login;
