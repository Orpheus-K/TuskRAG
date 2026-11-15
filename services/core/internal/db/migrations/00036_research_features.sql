-- +goose Up
-- 5.1 MCP OAuth Resource Server helpers (allow SECURITY DEFINER owned by tuskrag_migrator to resolve across tenants while tuskrag_app remains RLS-enforced)
ALTER TABLE oidc_provider NO FORCE ROW LEVEL SECURITY;
ALTER TABLE oidc_auth_state NO FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION resolve_oidc_provider_tenant_by_issuer(p_issuer text, p_tenant_id bigint DEFAULT NULL)
RETURNS bigint
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
  SELECT CASE
    WHEN count(*) = 1 THEN min(p.tenant_id)
    ELSE NULL
  END
  FROM oidc_provider p
  WHERE p.enabled = true
    AND rtrim(p.issuer_url, '/') = rtrim(p_issuer, '/')
    AND (p_tenant_id IS NULL OR p.tenant_id = p_tenant_id);
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION list_public_oidc_issuers(p_tenant_id bigint DEFAULT NULL)
RETURNS text[]
LANGUAGE sql
SECURITY DEFINER
SET search_path = public, pg_temp
AS $$
  SELECT COALESCE(ARRAY(
    SELECT DISTINCT p.issuer_url
    FROM oidc_provider p
    WHERE p.enabled = true
      AND (p_tenant_id IS NULL OR p.tenant_id = p_tenant_id)
    ORDER BY p.issuer_url
  ), ARRAY[]::text[]);
$$;
-- +goose StatementEnd

GRANT EXECUTE ON FUNCTION resolve_oidc_provider_tenant_by_issuer(text, bigint) TO tuskrag_app;
GRANT EXECUTE ON FUNCTION list_public_oidc_issuers(bigint) TO tuskrag_app;

-- 5.2 Retrieval poisoning & prompt injection guard: trust tiers + ingestion findings
ALTER TABLE knowledge_base
  ADD COLUMN IF NOT EXISTS trust_tier text NOT NULL DEFAULT 'standard'
  CHECK (trust_tier IN ('trusted', 'standard', 'untrusted'));

ALTER TABLE document
  ADD COLUMN IF NOT EXISTS trust_tier text
  CHECK (trust_tier IS NULL OR trust_tier IN ('trusted', 'standard', 'untrusted'));

ALTER TABLE document_version
  ADD COLUMN IF NOT EXISTS injection_findings jsonb;

-- 5.3 Agentic exact-match tool: pg_trgm GIN index on chunk.content when pg_trgm is available
-- +goose StatementBegin
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    BEGIN
      CREATE EXTENSION IF NOT EXISTS pg_trgm;
    EXCEPTION WHEN insufficient_privilege OR undefined_file OR feature_not_supported THEN
      NULL;
    END;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    CREATE INDEX IF NOT EXISTS chunk_content_trgm_idx ON chunk USING gin (content gin_trgm_ops);
  END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX IF EXISTS chunk_content_trgm_idx;
ALTER TABLE document_version DROP COLUMN IF EXISTS injection_findings;
ALTER TABLE document DROP COLUMN IF EXISTS trust_tier;
ALTER TABLE knowledge_base DROP COLUMN IF EXISTS trust_tier;
DROP FUNCTION IF EXISTS list_public_oidc_issuers(bigint);
DROP FUNCTION IF EXISTS resolve_oidc_provider_tenant_by_issuer(text, bigint);
ALTER TABLE oidc_auth_state FORCE ROW LEVEL SECURITY;
ALTER TABLE oidc_provider FORCE ROW LEVEL SECURITY;
