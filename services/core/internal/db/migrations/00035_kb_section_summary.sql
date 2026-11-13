-- +goose Up
ALTER TABLE knowledge_base
  ADD COLUMN IF NOT EXISTS section_summary_enabled boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE knowledge_base
  DROP COLUMN IF EXISTS section_summary_enabled;
