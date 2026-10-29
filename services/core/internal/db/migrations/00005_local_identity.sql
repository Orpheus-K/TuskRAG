-- +goose Up
CREATE TABLE local_account (
  tenant_id bigint PRIMARY KEY REFERENCES tenant(id),
  user_id bigint NOT NULL,
  login text NOT NULL,
  password_hash text NOT NULL,
  credential_version bigint NOT NULL DEFAULT 1 CHECK (credential_version > 0),
  failed_attempts integer NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
  locked_until timestamptz,
  disabled boolean NOT NULL DEFAULT false,
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id)
);

CREATE TABLE login_session (
  tenant_id bigint NOT NULL,
  token_hash bytea NOT NULL CHECK (octet_length(token_hash) = 32),
  user_id bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  absolute_expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  PRIMARY KEY (tenant_id, token_hash),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id)
);
CREATE INDEX login_session_expiry ON login_session (absolute_expires_at);

-- +goose StatementBegin
DO $$
DECLARE table_name text;
BEGIN
  FOREACH table_name IN ARRAY ARRAY['local_account', 'login_session']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', table_name);
    EXECUTE format('CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint) WITH CHECK (tenant_id = current_setting(''tuskrag.tenant_id'')::bigint)', table_name);
  END LOOP;
END $$;
-- +goose StatementEnd
GRANT SELECT, INSERT, UPDATE, DELETE ON local_account, login_session TO tuskrag_app;
