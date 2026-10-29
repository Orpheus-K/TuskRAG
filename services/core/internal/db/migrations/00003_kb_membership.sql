-- +goose Up
ALTER TABLE tenant ADD COLUMN egress_policy_max text NOT NULL DEFAULT 'deny'
  CHECK (egress_policy_max IN ('deny', 'internal_only', 'allow'));

CREATE TABLE kb_membership (
  tenant_id bigint NOT NULL,
  kb_id bigint NOT NULL,
  user_id bigint NOT NULL,
  role text NOT NULL CHECK (role IN ('owner', 'admin', 'editor', 'viewer')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kb_id, user_id),
  FOREIGN KEY (tenant_id, kb_id) REFERENCES knowledge_base(tenant_id, id),
  FOREIGN KEY (tenant_id, user_id) REFERENCES app_user(tenant_id, id)
);
CREATE INDEX kb_membership_by_user ON kb_membership (tenant_id, user_id, kb_id);

ALTER TABLE kb_membership ENABLE ROW LEVEL SECURITY;
ALTER TABLE kb_membership FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON kb_membership
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON kb_membership TO tuskrag_app;

CREATE TABLE audit_log (
  id bigint GENERATED ALWAYS AS IDENTITY,
  tenant_id bigint NOT NULL REFERENCES tenant(id),
  actor_user_id bigint NOT NULL,
  action text NOT NULL,
  object_type text NOT NULL,
  object_id bigint NOT NULL,
  result text NOT NULL CHECK (result IN ('allow', 'deny', 'error')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, created_at)
) PARTITION BY RANGE (created_at);
CREATE TABLE audit_log_default PARTITION OF audit_log DEFAULT;
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_log FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON audit_log
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT ON audit_log TO tuskrag_app;
GRANT USAGE, SELECT ON SEQUENCE audit_log_id_seq TO tuskrag_app;
