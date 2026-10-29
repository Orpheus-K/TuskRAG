-- +goose Up
ALTER TABLE audit_log ADD COLUMN target_user_id bigint;
ALTER TABLE audit_log ADD COLUMN new_role text;
