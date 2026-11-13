-- +goose Up
-- Retrieval-only revisions must not invalidate an ingest embedding binding.
CREATE TABLE kb_retrieval_config_revision (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  revision integer NOT NULL CHECK (revision > 0),
  top_k integer NOT NULL CHECK (top_k BETWEEN 1 AND 30),
  rerank_provider_id bigint,
  rerank_model text,
  rerank_top_n integer,
  rerank_fallback boolean NOT NULL,
  rerank_min_score double precision,
  created_by bigint NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kb_id, revision),
  FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_base(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, rerank_provider_id) REFERENCES model_provider(tenant_id, id),
  FOREIGN KEY (tenant_id, created_by) REFERENCES app_user(tenant_id, id),
  CHECK (
    (rerank_provider_id IS NULL AND rerank_model IS NULL AND rerank_top_n IS NULL
      AND NOT rerank_fallback AND rerank_min_score IS NULL)
    OR
    (rerank_provider_id IS NOT NULL AND rerank_model IS NOT NULL
      AND length(btrim(rerank_model)) BETWEEN 1 AND 200
      AND rerank_top_n IS NOT NULL AND rerank_top_n IN (20, 30, 50))
  ),
  CHECK (rerank_min_score IS NULL OR rerank_min_score NOT IN ('NaN'::float8, 'Infinity'::float8, '-Infinity'::float8))
);
ALTER TABLE kb_retrieval_config_revision ENABLE ROW LEVEL SECURITY;
ALTER TABLE kb_retrieval_config_revision FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kb_retrieval_config_revision
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON kb_retrieval_config_revision TO tuskrag_app;
