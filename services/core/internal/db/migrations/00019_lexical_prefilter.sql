-- +goose Up
-- This generated value is the exact single-KB principal check used by retrieval.
-- It cannot drift when access_tags or kb_id changes.
ALTER TABLE chunk ADD COLUMN kb_access boolean GENERATED ALWAYS AS
  (access_tags @> ARRAY['kb:' || kb_id::text || ':all']::text[]) STORED;

-- Reprovision the v2 BM25 index with the migrator role after this migration.
-- Queries fail closed while the required replacement index is unavailable.
DROP INDEX IF EXISTS public.tuskrag_chunk_bm25_v1;
