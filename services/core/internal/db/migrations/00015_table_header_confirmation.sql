-- +goose Up
ALTER TABLE public.document_version
  ADD COLUMN confirm_first_row_headers boolean NOT NULL DEFAULT false;

-- The choice belongs to the version being processed and cannot change after
-- the upload transaction has committed.
-- +goose StatementBegin
CREATE FUNCTION public.tuskrag_guard_header_confirmation() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF NEW.confirm_first_row_headers IS DISTINCT FROM OLD.confirm_first_row_headers THEN
    RAISE EXCEPTION USING ERRCODE = '23514', MESSAGE = 'table header confirmation is immutable for a document version';
  END IF;
  RETURN NEW;
END $$;
-- +goose StatementEnd

CREATE TRIGGER document_header_confirmation_guard
  BEFORE UPDATE OF confirm_first_row_headers ON public.document_version
  FOR EACH ROW EXECUTE FUNCTION public.tuskrag_guard_header_confirmation();
