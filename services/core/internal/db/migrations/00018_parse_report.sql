-- +goose Up
ALTER TABLE public.parse_artifact ADD COLUMN parse_report jsonb;
ALTER TABLE public.parse_artifact ADD CONSTRAINT parse_report_size
  CHECK (parse_report IS NULL OR (jsonb_typeof(parse_report) = 'object' AND octet_length(parse_report::text) <= 1048576));
