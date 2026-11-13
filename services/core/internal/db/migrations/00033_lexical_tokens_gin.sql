-- +goose Up
CREATE INDEX IF NOT EXISTS tuskrag_chunk_lexical_gin_v1
  ON public.chunk USING gin (lexical_tokens)
  WHERE is_current;
