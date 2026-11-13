-- +goose Up
ALTER TABLE api_key NO FORCE ROW LEVEL SECURITY;

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
