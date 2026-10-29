-- +goose Up
CREATE TABLE idempotency_record (
  tenant_id bigint NOT NULL,
  actor_user_id bigint NOT NULL,
  operation text NOT NULL,
  request_key text NOT NULL,
  request_hash bytea NOT NULL CHECK (octet_length(request_hash) = 32),
  object_id bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, actor_user_id, operation, request_key),
  FOREIGN KEY (tenant_id, actor_user_id) REFERENCES app_user(tenant_id, id)
);
CREATE INDEX idempotency_record_created_at ON idempotency_record (created_at);
ALTER TABLE idempotency_record ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_record FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON idempotency_record
  USING (tenant_id = current_setting('tuskrag.tenant_id')::bigint)
  WITH CHECK (tenant_id = current_setting('tuskrag.tenant_id')::bigint);
GRANT SELECT, INSERT, UPDATE, DELETE ON idempotency_record TO tuskrag_app;
