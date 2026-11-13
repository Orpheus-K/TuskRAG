-- +goose Up
CREATE INDEX IF NOT EXISTS audit_log_tenant_created_idx ON audit_log (tenant_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_log_tenant_action_idx ON audit_log (tenant_id, action, created_at DESC);

-- +goose Down
DROP INDEX IF EXISTS audit_log_tenant_action_idx;
DROP INDEX IF EXISTS audit_log_tenant_created_idx;
