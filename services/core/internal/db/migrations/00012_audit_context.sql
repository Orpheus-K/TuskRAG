-- +goose Up
ALTER TABLE audit_log ADD COLUMN details jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_details_object CHECK (jsonb_typeof(details) = 'object');
