-- +goose Up
-- A corrected table-header decision creates a distinct immutable version even
-- when the original bytes are unchanged.
ALTER TABLE public.document_version
  DROP CONSTRAINT document_version_tenant_id_document_id_content_sha256_key;
ALTER TABLE public.document_version
  ADD CONSTRAINT document_version_content_decision_key
  UNIQUE (tenant_id, document_id, content_sha256, confirm_first_row_headers);
