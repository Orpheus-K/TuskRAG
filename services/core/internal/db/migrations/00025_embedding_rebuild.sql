-- +goose Up
CREATE TABLE embedding_rebuild (
 job_id bigint PRIMARY KEY,
 tenant_id bigint NOT NULL,
 kb_id bigint NOT NULL,
 source_space_id bigint NOT NULL,
 source_revision integer NOT NULL CHECK (source_revision > 0 AND source_revision < 2147483647),
 target_space_id bigint NOT NULL,
 target_provider_id bigint NOT NULL,
 target_model text NOT NULL CHECK (length(target_model)>0),
 target_dims integer NOT NULL CHECK (target_dims BETWEEN 64 AND 4000),
 target_normalized boolean NOT NULL,
 target_distance text NOT NULL CHECK (target_distance='cosine'),
 created_by bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK (source_space_id<>target_space_id),
 UNIQUE(tenant_id,kb_id,job_id),
 FOREIGN KEY(tenant_id,job_id) REFERENCES job(tenant_id,id),
 FOREIGN KEY(tenant_id,kb_id) REFERENCES knowledge_base(tenant_id,id),
 FOREIGN KEY(tenant_id,source_space_id) REFERENCES embedding_space(tenant_id,id),
 FOREIGN KEY(tenant_id,target_space_id) REFERENCES embedding_space(tenant_id,id),
 FOREIGN KEY(tenant_id,target_provider_id) REFERENCES model_provider(tenant_id,id),
 FOREIGN KEY(tenant_id,created_by) REFERENCES app_user(tenant_id,id)
);
ALTER TABLE embedding_rebuild ENABLE ROW LEVEL SECURITY;
ALTER TABLE embedding_rebuild FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON embedding_rebuild
 USING (tenant_id=current_setting('tuskrag.tenant_id')::bigint)
 WITH CHECK (tenant_id=current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT,INSERT ON embedding_rebuild TO tuskrag_app;
